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
	body := "connectors: { gh: { type: github } }\n" + packsBlock
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
agents:
  a:
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
	for _, field := range []string{"host: prod", "runtime: gpu", "controller: acp1"} {
		dir := t.TempDir()
		writePackSource(t, dir, "src/p", `
pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }
agents:
  a:
    workspace: local
    `+field+`
`)
		path := writeConsumer(t, dir, "packs:\n  p:\n    source: ./src/p\n")
		_, err := resolveAndLoad(t, path)
		if err == nil || !strings.Contains(err.Error(), "runtime/host/controller") {
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
	}
	for _, s := range bad {
		if _, err := parseSource(s, "/cfg"); err == nil {
			t.Errorf("parseSource(%q) should reject the remote-helper transport", s)
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
        agent: a
        prompt: "${settings.greet}"
agents:
  a: { workspace: local }
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
