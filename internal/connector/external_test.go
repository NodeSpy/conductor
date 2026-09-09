package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	"gopkg.in/yaml.v3"
)

// refWith builds a config.ConnectorRef from a YAML mapping (exercises the same
// UnmarshalYAML path config load uses).
func refWith(t *testing.T, m map[string]any) config.ConnectorRef {
	t.Helper()
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ConnectorRef
	if err := yaml.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

type fakeInvoker struct {
	lastReq plugin.InvokeRequest
	out     map[string]any
	err     error
}

func (f *fakeInvoker) Invoke(_ context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	f.lastReq = req
	return f.out, f.err
}

func TestRegisterExternalTypeRefusesBundledOverride(t *testing.T) {
	// github is a bundled type registered via init().
	err := RegisterExternalType(&TypeDecl{Type: "github"}, nil)
	if err == nil || !strings.Contains(err.Error(), "bundled") {
		t.Fatalf("want bundled-override refusal, got %v", err)
	}
	// A fresh external type registers, then unregisters cleanly. Cleanup runs
	// even if an assertion below fails, so the global registry never leaks this
	// type into other tests in the package.
	t.Cleanup(func() { UnregisterExternalType("acme-ext-test") })
	if err := RegisterExternalType(&TypeDecl{Type: "acme-ext-test"}, nil); err != nil {
		t.Fatalf("register external: %v", err)
	}
	if !IsExternalType("acme-ext-test") {
		t.Fatal("expected external tag")
	}
	UnregisterExternalType("acme-ext-test")
	if _, ok := TypeDeclFor("acme-ext-test"); ok {
		t.Fatal("expected type removed")
	}
	// Unregister must never drop a bundled type.
	UnregisterExternalType("github")
	if _, ok := TypeDeclFor("github"); !ok {
		t.Fatal("bundled github must survive UnregisterExternalType")
	}
}

func TestResolveConnectionRedactsAndAudits(t *testing.T) {
	sec := secrets.New()
	sec.LookupEnv = func(k string) (string, bool) {
		if k == "JIRA_TOKEN" {
			return "s3cr3t-value", true
		}
		return "", false
	}
	decl := &plugin.Decl{
		ProtocolVersion: plugin.ProtocolVersion, Type: "jira",
		Verbs: []plugin.Verb{{Name: "search", Outputs: plugin.Schema{
			"count": {Type: "integer", Required: true},
		}}},
	}
	fi := &fakeInvoker{out: map[string]any{"count": 3}}
	var audits []map[string]any
	td := mapDecl(decl)

	conn, refs, err := resolveConnection(refWith(t, map[string]any{
		"type": "jira", "base_url": "https://acme.example", "token": "env:JIRA_TOKEN",
	}), sec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conn["token"] != "s3cr3t-value" || conn["base_url"] != "https://acme.example" {
		t.Fatalf("bad conn: %+v", conn)
	}
	if len(refs) != 1 || refs[0] != "env:JIRA_TOKEN" {
		t.Fatalf("bad refs: %v", refs)
	}
	// resolved secret is Tracked → redacted from logs
	if got := sec.Redact("leak s3cr3t-value here"); strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("secret not redacted: %q", got)
	}
	// base_url (not a secret) is NOT redacted
	if got := sec.Redact("https://acme.example"); !strings.Contains(got, "acme.example") {
		t.Fatalf("non-secret wrongly redacted: %q", got)
	}

	e := &externalImpl{
		client: fi, instance: "myjira", decl: td, conn: conn,
		secretRefs: refs, pluginRef: "jira@1.0", pluginType: "jira",
		audit: func(m map[string]any) { audits = append(audits, m) },
	}
	out, err := e.Invoke(context.Background(), "search", map[string]any{"q": "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != 3 {
		t.Fatalf("bad output: %+v", out)
	}
	// credential handed to the plugin, and audited (name, not value)
	if fi.lastReq.Connection["token"] != "s3cr3t-value" {
		t.Fatal("plugin did not receive credential")
	}
	if len(audits) != 1 || audits[0]["event"] != "plugin_credential" {
		t.Fatalf("missing audit: %+v", audits)
	}
	if _, hasVal := audits[0]["token"]; hasVal {
		t.Fatal("audit must not contain the credential value")
	}
	for _, v := range flatten(audits[0]) {
		if strings.Contains(v, "s3cr3t-value") {
			t.Fatalf("audit leaked the secret value: %+v", audits[0])
		}
	}
}

func TestResolveConnectionAllowSecretsGate(t *testing.T) {
	sec := secrets.New()
	sec.LookupEnv = func(k string) (string, bool) { return "v", true }

	// allow_secrets set but the referenced secret is NOT on it → refused.
	allow := map[string]bool{"env:PERMITTED": true}
	_, _, err := resolveConnection(refWith(t, map[string]any{
		"type": "jira", "token": "env:FORBIDDEN",
	}), sec, allow)
	if err == nil || !strings.Contains(err.Error(), "allow_secrets") {
		t.Fatalf("want allow_secrets refusal, got %v", err)
	}

	// The permitted ref passes.
	if _, refs, err := resolveConnection(refWith(t, map[string]any{
		"type": "jira", "token": "env:PERMITTED",
	}), sec, allow); err != nil || len(refs) != 1 {
		t.Fatalf("permitted ref should pass: refs=%v err=%v", refs, err)
	}
}

func TestExternalInvokeRejectsBadOutput(t *testing.T) {
	decl := &plugin.Decl{ProtocolVersion: plugin.ProtocolVersion, Type: "jira",
		Verbs: []plugin.Verb{{Name: "search", Outputs: plugin.Schema{"count": {Type: "integer", Required: true}}}}}
	td := mapDecl(decl)
	// plugin returns an undeclared key → schema validation rejects
	fi := &fakeInvoker{out: map[string]any{"evil": "payload"}}
	e := &externalImpl{client: fi, instance: "j", decl: td, pluginRef: "jira@1"}
	if _, err := e.Invoke(context.Background(), "search", nil); err == nil {
		t.Fatal("expected schema-validation rejection of malformed output")
	}
}

func flatten(m map[string]any) []string {
	var out []string
	for _, v := range m {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []string:
			out = append(out, t...)
		}
	}
	return out
}
