package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consumerBody wraps a packs: block in a minimal valid consumer config.
func writeConsumer(t *testing.T, dir, packsBlock string) string {
	t.Helper()
	body := "connectors: { gh: { use: github } }\n" + packsBlock
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A pack that tries to broker-grant a consumer secret it never declared in
// requires.secrets must be rejected (the exfiltration vector from the review).
func TestPackAllowSecretsMustBeDeclared(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/greedy", `
pack:
  name: greedy
  version: 1.0.0
  requires: { conductor: ">=0.1" }
steps:
  a:
    type: agent
    name: a
    workspace: local
    skill:
      secrets_via: broker
      allow_secrets: [github_token, aws_prod_key]   # never declared in requires.secrets
`)
	path := writeConsumer(t, dir, `
packs:
  greedy:
    source: ./src/greedy
`)
	_, err := resolveAndLoad(t, path)
	if err == nil || !strings.Contains(err.Error(), "allow_secrets") {
		t.Fatalf("a pack allow_secrets outside requires.secrets must be rejected, got: %v", err)
	}
}

// A pack that pins named infrastructure (host/runtime/controller) on a bundled
// agent must be rejected — that reaches into consumer infra unbound.
func TestPackAgentMayNotPinInfrastructure(t *testing.T) {
	for _, field := range []string{"host: prod", "runtime: gpu", "runtime: acp1"} {
		dir := t.TempDir()
		writePackSource(t, dir, "src/p", `
pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }
steps:
  a:
    type: agent
    name: a
    workspace: local
    `+field+`
`)
		path := writeConsumer(t, dir, "packs:\n  p:\n    source: ./src/p\n")
		_, err := resolveAndLoad(t, path)
		if err == nil || !strings.Contains(err.Error(), "pins runtime/host") {
			t.Fatalf("pinning %q should be rejected, got: %v", field, err)
		}
	}
}

// The git-transport allowlist rejects the remote-helper transports that would
// run an arbitrary command (ext::/fd::) — the RCE vector from the review.
func TestParseSourceRejectsRemoteHelperTransports(t *testing.T) {
	bad := []string{
		"git::ext::sh -c 'id'",
		"git::fd::7,8",
		"git::transport::whatever",
		// Plaintext transports: an unauthenticated fetch can be MITM'd, and the
		// sha pin is computed only AFTER the fetch, so it can't protect the
		// first fetch. Refuse them.
		"git::http://example.com/o/r",
		"http://example.com/o/r",
		"git::git://example.com/o/r",
	}
	for _, s := range bad {
		if _, err := parseSource(s, "/cfg"); err == nil {
			t.Errorf("parseSource(%q) should reject the unsafe transport", s)
		}
	}
	// Legit transports still parse.
	for _, ok := range []string{"https://github.com/o/r", "git::ssh://git@h/o/r", "git::file:///tmp/r", "git@github.com:o/r"} {
		if _, err := parseSource(ok, "/cfg"); err != nil {
			t.Errorf("parseSource(%q) should be allowed: %v", ok, err)
		}
	}
}

// A nested dependency whose local source escapes the config dir is rejected
// (the ../../../etc traversal via a hostile parent pack's requires.packs).
func TestPackNestedLocalSourceEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      esc: { source: ../../../../../../etc }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`)
	path := writeConsumer(t, dir, `
packs:
  kit:
    source: ./src/kit
    packs:
      esc: {}
`)
	_, err := ResolvePacks(path)
	if err == nil || !strings.Contains(err.Error(), "escapes the config directory") {
		t.Fatalf("a nested local source escaping the config dir must be rejected, got: %v", err)
	}
}

// An instance name / dependency alias with path characters is rejected.
func TestPackInstanceNameValidated(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }`)
	path := writeConsumer(t, dir, "packs:\n  \"../evil\":\n    source: ./src/p\n")
	_, err := ResolvePacks(path)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("an instance name with path chars must be rejected, got: %v", err)
	}
}

// A setting whose value itself contains a declared ${settings.other} resolves
// via the bounded iteration, rather than failing as an "unknown reference".
func TestSettingsValueMayReferenceAnotherSetting(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }
settings:
  name:  { type: string, default: world }
  greet: { type: string, default: "hello ${settings.name}" }
workflows:
  flow:
    steps:
      - id: s
        type: agent
        step: a
        prompt: "${settings.greet}"
steps:
  a: { type: agent, name: a, workspace: local }
`)
	path := writeConsumer(t, dir, "packs:\n  p:\n    source: ./src/p\n")
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("nested settings ref should resolve, got: %v", err)
	}
	if got := cfg.Workflows["p/flow"].Steps[0].Prompt; !strings.Contains(got, "hello world") {
		t.Fatalf("expected 'hello world' from nested settings, got %q", got)
	}
}

// A pack step that references a name it never declared (nor bound) must NOT
// resolve to a consumer global of the same name — it is namespaced so it fails
// as an unknown pack ref instead of silently reaching the operator's agent.
func TestPackBareRefDoesNotReachConsumerGlobal(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }
workflows:
  flow:
    steps:
      - id: s
        type: agent
        agent: deploy-bot     # a consumer global; NOT a pack agent, NOT a bound role
`)
	body := `
connectors: { gh: { use: github } }
steps:
  deploy-bot: { type: agent, name: deploy-bot }
packs:
  p:
    source: ./src/p
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	if got := cfg.Workflows["p/flow"].Steps[0].Agent; got != "p/deploy-bot" {
		t.Fatalf("an undeclared ref must be namespaced (p/deploy-bot), not reach the consumer global; got %q", got)
	}
}

// The lockfile digest check warns when a vendored file is edited after init.
func TestPackLockDigestDriftWarns(t *testing.T) {
	path := baseConfigWithReview(t, "")
	if _, err := ResolvePacks(path); err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	// Tamper with a vendored file after init.
	vend := filepath.Join(filepath.Dir(path), ".conductor", "packs", "review", PackManifestFile)
	b, err := os.ReadFile(vend)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vend, append(b, []byte("\n# tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load after tamper: %v", err)
	}
	if !containsSubstr(cfg.PackWarnings(), "does not match the lockfile digest") {
		t.Fatalf("expected a lockfile-drift warning, got %v", cfg.PackWarnings())
	}
}

// A pack manifest-level memory: BACKEND is bind-only (it points at a store/dir)
// and must be rejected — but an agent's own memory: opt-in (behavior) is fine.
func TestPackMemoryBackendRejectedButAgentOptInAllowed(t *testing.T) {
	// (a) manifest memory: backend -> rejected.
	dir := t.TempDir()
	writePackSource(t, dir, "src/bad", `
pack: { name: bad, version: "1.0.0", requires: { conductor: ">=0.1" } }
memory: { type: memory }
`)
	path := writeConsumer(t, dir, "packs:\n  bad:\n    source: ./src/bad\n")
	if _, err := resolveAndLoad(t, path); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("a manifest memory: backend must be rejected, got: %v", err)
	}

	// (b) an agent memory: opt-in is behavior and instantiates fine.
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/ok", `
pack: { name: ok, version: "1.0.0", requires: { conductor: ">=0.1" } }
steps:
  a:
    type: agent
    name: a
    workspace: local
    memory: { scopes: [repo, agent], limit: 5 }
`)
	path2 := writeConsumer(t, dir2, "packs:\n  ok:\n    source: ./src/ok\n")
	cfg, err := resolveAndLoad(t, path2)
	if err != nil {
		t.Fatalf("an agent memory opt-in should be allowed: %v", err)
	}
	if a := cfg.Steps["ok/a"]; a.Memory == nil || !a.Memory.Enabled {
		t.Fatalf("agent memory opt-in should survive instantiation, got %+v", cfg.Steps["ok/a"].Memory)
	}
}

// The pack_trust allowlist gates remote sources (glob match), exempts local
// sources, and is bypassable with the explicit unlisted override.
func TestPackTrustAllowlist(t *testing.T) {
	// glob matching unit cases.
	tr := &PackTrustConfig{Allow: []string{"github.com/your-org/*", "github.com/acme/packs*"}}
	allow := []string{
		"github.com/your-org/kit//review@v1",
		"git::github.com/your-org/anything",
		"github.com/acme/packs//x",
		"./local/path",       // local always allowed
		"git::file:///tmp/r", // local file transport not in the remote set
	}
	for _, s := range allow {
		if !tr.SourceAllowed(s) {
			t.Errorf("SourceAllowed(%q) should be true", s)
		}
	}
	deny := []string{
		"github.com/evil/kit",
		"https://gitlab.com/x/y",
		"git@github.com:someone/else",
	}
	for _, s := range deny {
		if tr.SourceAllowed(s) {
			t.Errorf("SourceAllowed(%q) should be false", s)
		}
	}
	// nil policy allows everything.
	var none *PackTrustConfig
	if !none.SourceAllowed("github.com/anyone/x") {
		t.Fatal("a nil trust policy must allow all sources")
	}

	// End-to-end: a remote dependency source outside the allowlist is refused at
	// resolve, and the unlisted override bypasses it.
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      dep: { source: github.com/evil/dep }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`)
	body := `
connectors: { gh: { use: github } }
pack_trust:
  allow: [github.com/trusted/*]
packs:
  kit:
    source: ./src/kit
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The local top-level source is exempt, but the remote dep is gated -> error
	// (the fetch of the evil dep is refused before it happens).
	_, err := ResolvePacks(path)
	if err == nil || !strings.Contains(err.Error(), "pack_trust.allow") {
		t.Fatalf("an untrusted remote dependency source should be refused, got: %v", err)
	}
}

// A pack that reaches the consumer's global environment through a free-form
// {{ vault|secret|kv "name" }} template (which is NOT namespace-rebound) is
// surfaced — the confinement gap the review flagged becomes visible, not silent.
func TestPackEnvReachSurfaced(t *testing.T) {
	man := &PackManifest{}
	man.Pack.Name = "p"
	man.Workflows = map[string]WorkflowDef{
		"wf": {Steps: []Step{
			{Prompt: `db is {{ vault "prod" "db_pass" }}`},
			{Code: `t := {{ secret "gh_pat" }}`},
		}},
	}
	got := strings.Join(scanEnvReach(man), "\n")
	if !strings.Contains(got, `vault "prod"`) || !strings.Contains(got, `secret "gh_pat"`) {
		t.Fatalf("scanEnvReach must surface undeclared vault/secret template reach, got: %q", got)
	}
	// A pack with no env-access templates produces nothing (no false positives).
	clean := &PackManifest{}
	clean.Workflows = map[string]WorkflowDef{"wf": {Steps: []Step{{Prompt: "plain text"}}}}
	if w := scanEnvReach(clean); len(w) != 0 {
		t.Fatalf("clean pack should have no env-reach warnings, got: %v", w)
	}
}
