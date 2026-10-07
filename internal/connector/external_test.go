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
	// cron is a bundled type registered via init().
	err := RegisterExternalType(&TypeDecl{Type: "cron"}, nil)
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
	UnregisterExternalType("cron")
	if _, ok := TypeDeclFor("cron"); !ok {
		t.Fatal("bundled cron must survive UnregisterExternalType")
	}

	// Two plugins providing the same type must not silently clobber each other
	// (that would redirect credentials to whichever registered last).
	t.Cleanup(func() { UnregisterExternalType("acme-collide") })
	if err := RegisterExternalType(&TypeDecl{Type: "acme-collide"}, nil); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := RegisterExternalType(&TypeDecl{Type: "acme-collide"}, nil); err == nil || !strings.Contains(err.Error(), "another plugin") {
		t.Fatalf("want external-collision refusal, got %v", err)
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

// Host-owned header keys never cross to the plugin as connection fields.
func TestResolveConnectionStripsHostOwnedKeys(t *testing.T) {
	conn, _, err := resolveConnection(refWith(t, map[string]any{
		"use": "./conductor-jira", "type": "jira", "enabled": true, "network": []any{"x:443"},
		"isolation": map[string]any{"mode": "none"}, "allow_secrets": []any{"env:A"},
		"policy": map[string]any{}, "options": map[string]any{}, "auth": map[string]any{},
		"base_url": "https://acme.example",
	}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn) != 1 || conn["base_url"] != "https://acme.example" {
		t.Fatalf("host-owned keys leaked to the plugin: %+v", conn)
	}
}

// Secret references resolve at any depth in a connection — tracked for
// redaction and checked against allow_secrets like a top-level one.
func TestResolveConnectionResolvesNestedSecrets(t *testing.T) {
	sec := secrets.New()
	sec.LookupEnv = func(k string) (string, bool) { return "secret-value-" + k, true }
	conn, refs, err := resolveConnection(refWith(t, map[string]any{
		"sources": map[string]any{"a": map[string]any{"sign": map[string]any{"secret": "env:A"}}},
		"keys":    []any{"env:B", "plain"},
	}), sec, nil)
	if err != nil {
		t.Fatal(err)
	}
	sign := conn["sources"].(map[string]any)["a"].(map[string]any)["sign"].(map[string]any)
	if sign["secret"] != "secret-value-A" || conn["keys"].([]any)[0] != "secret-value-B" || conn["keys"].([]any)[1] != "plain" || len(refs) != 2 {
		t.Fatalf("conn=%v refs=%v", conn, refs)
	}
	if got := sec.Redact("leak secret-value-A"); strings.Contains(got, "secret-value-A") {
		t.Fatal("a nested secret was not tracked for redaction")
	}
	if _, _, err := resolveConnection(refWith(t, map[string]any{"x": map[string]any{"y": "env:C"}}), sec, map[string]bool{"env:A": true}); err == nil {
		t.Fatal("a nested secret outside allow_secrets must be refused")
	}
}

// TestTypeDeclsForReturnsEveryGroupSideBySide is finding 5: a bare
// type-name caller with no specific instance in mind (`conductor schema
// <type>`, credentialKeys()'s type sweep) must see EVERY resolved-version
// group's declaration, not silently just the first one registered
// (TypeDeclFor's "representative"). DeclFor, by contrast, resolves exactly
// the ONE group a specific bound instance belongs to.
func TestTypeDeclsForReturnsEveryGroupSideBySide(t *testing.T) {
	const typ = "zz-multi-version"
	t.Cleanup(func() {
		UnregisterExternalType(typ)
		ResetInstanceGroups()
	})
	gk1, gk2 := "connectors/"+typ+"@v1.0.0", "connectors/"+typ+"@v2.0.0"
	d1 := &TypeDecl{Type: typ, Desc: "v1 declaration"}
	d2 := &TypeDecl{Type: typ, Desc: "v2 declaration"}
	if err := RegisterExternalTypeGroup(d1, nil, gk1, "connectors/"+typ); err != nil {
		t.Fatalf("register group 1: %v", err)
	}
	if err := RegisterExternalTypeGroup(d2, nil, gk2, "connectors/"+typ); err != nil {
		t.Fatalf("register group 2: %v", err)
	}
	BindInstanceGroup("a", gk1)
	BindInstanceGroup("b", gk2)

	decls := TypeDeclsFor(typ)
	if len(decls) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(decls), decls)
	}
	if decls[gk1] != d1 || decls[gk2] != d2 {
		t.Fatalf("TypeDeclsFor did not return both groups' own decls: %+v", decls)
	}

	// DeclFor resolves each bound instance to its OWN group, never the
	// other's.
	da, ok := DeclFor(typ, "a")
	if !ok || da != d1 {
		t.Fatalf("DeclFor(a) = %+v, want d1", da)
	}
	db, ok := DeclFor(typ, "b")
	if !ok || db != d2 {
		t.Fatalf("DeclFor(b) = %+v, want d2", db)
	}
	// An instance with no binding at all falls back to the representative
	// (the first-registered group) — unchanged, single-version behavior.
	rep, ok := DeclFor(typ, "unbound")
	if !ok || rep != d1 {
		t.Fatalf("DeclFor(unbound) = %+v, want the representative d1", rep)
	}

	// A single-group (the overwhelmingly common) type still returns exactly
	// one entry, keyed by the type name itself.
	const single = "zz-single-version"
	t.Cleanup(func() { UnregisterExternalType(single) })
	ds := &TypeDecl{Type: single}
	if err := RegisterExternalType(ds, nil); err != nil {
		t.Fatalf("register single: %v", err)
	}
	only := TypeDeclsFor(single)
	if len(only) != 1 || only[single] != ds {
		t.Fatalf("single-group TypeDeclsFor = %+v, want exactly one entry keyed by the type name", only)
	}
}
