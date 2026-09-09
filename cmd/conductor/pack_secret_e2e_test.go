package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// writeSecKitPack writes a minimal pack whose one hand-off agent may draw a
// single abstract secret through the broker. brokerVia controls whether the
// pack correctly declares secrets_via: broker (the happy path) or omits it
// (the footgun the guard must reject). No connectors or verbs — the secret
// path is the entire subject.
func writeSecKitPack(t *testing.T, dir string, brokerVia bool) {
	t.Helper()
	via := ""
	if brokerVia {
		via = "      secrets_via: broker\n"
	}
	manifest := `
pack:
  name: sec-kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    secrets:
      review_token: { desc: token the poster uses }
    roles:
      handoff: {}
agents:
  handoff:
    workspace: local
    skill:
` + via + `      allow_secrets: [review_token]
`
	pd := filepath.Join(dir, "src")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, config.PackManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPackBoundSecretDeliveredThroughBroker is the end-to-end proof that a
// pack's abstract secret, once bound by the consumer, is delivered to the
// pack's hand-off agent at runtime through the broker — and ONLY under the
// consumer's bound name, never the pack's internal one.
//
// The whole chain runs with nothing hardcoded to match:
//
//	pack requires.secrets.review_token             (abstract, shipped)
//	  -> consumer packs.review.secrets: {review_token: house/review}   (bind)
//	    -> pack_refs rebind rewrites skill.allow_secrets -> [house/review]
//	      -> config.Load validates it against the consumer's vaults: house
//	        -> broker.Issue/Redeem hands the file-vault value to the agent
//
// The negative cases prove the rebind is load-bearing: the pack's own internal
// name is NOT a live grant, and a secret outside allow_secrets is refused.
func TestPackBoundSecretDeliveredThroughBroker(t *testing.T) {
	const sentinel = "s3cr3t-review-token-value"

	dir := t.TempDir()
	// A real file vault: the consumer's `house` vault, with `review` holding
	// the secret. The broker resolves house/review through a genuine file
	// read, exactly as the daemon's lookup does.
	vaultDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "review"), []byte(sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeSecKitPack(t, dir, true)

	cfgBody := `
connectors:
  gh:
    type: github
vaults:
  house:
    type: file
    dir: ` + vaultDir + `
packs:
  review:
    source: ./src
    version: 1.0.0
    secrets: { review_token: house/review }
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := config.ResolvePacks(cfgPath); err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The rebind must have rewritten the pack's abstract name to the
	// consumer's bound vault ref, under the namespaced agent.
	prof, ok := cfg.Agents["review/handoff"]
	if !ok {
		var have []string
		for n := range cfg.Agents {
			have = append(have, n)
		}
		t.Fatalf("namespaced agent review/handoff missing; have %v", have)
	}
	if prof.Skill == nil {
		t.Fatal("review/handoff has no skill block")
	}
	if got := prof.Skill.AllowSecrets; len(got) != 1 || got[0] != "house/review" {
		t.Fatalf("allow_secrets not rebound to consumer ref: got %v, want [house/review]", got)
	}
	if prof.Skill.SecretsVia != "broker" {
		t.Fatalf("secrets_via = %q, want broker", prof.Skill.SecretsVia)
	}

	// The broker lookup mirrors the daemon's vault path: cut "<vault>/<key>"
	// and read the backend. A real file read — nothing hardcoded to the name.
	fv := &vaults.FileVault{Dir: vaultDir}
	lookup := func(name string) (string, bool) {
		vn, key, ok := strings.Cut(name, "/")
		if !ok || vn != "house" {
			return "", false
		}
		v, err := fv.Read(context.Background(), key)
		return v, err == nil && v != ""
	}
	b := skill.NewBroker(lookup, nil)
	tok, err := b.MintSession(skill.Identity{Agent: "review/handoff", Policy: *prof.Skill}, 0)
	if err != nil {
		t.Fatalf("MintSession: %v", err)
	}

	// Positive: the agent draws the secret under the CONSUMER's bound ref and
	// redeems the real file-vault value.
	gid, _, err := b.Issue(tok, prof.Skill.AllowSecrets[0], skill.Peer{})
	if err != nil {
		t.Fatalf("Issue(%s): %v", prof.Skill.AllowSecrets[0], err)
	}
	got, err := b.Redeem(tok, gid, skill.Peer{})
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got != sentinel {
		t.Fatalf("delivered secret = %q, want %q", got, sentinel)
	}

	// Negative 1: the pack's INTERNAL name is not a live grant — the rebind is
	// load-bearing, so the consumer's binding (not the author's label) is what
	// the broker honors.
	if _, _, err := b.Issue(tok, "review_token", skill.Peer{}); err == nil {
		t.Fatal("Issue(review_token) succeeded; the pack-internal name must not resolve after rebind")
	}
	// Negative 2: a secret outside allow_secrets is refused.
	if _, _, err := b.Issue(tok, "house/other", skill.Peer{}); err == nil {
		t.Fatal("Issue of a non-allowed secret succeeded; allow_secrets must gate")
	}
}

// TestPackAllowSecretsWithoutBrokerRejected proves the footgun guard: an agent
// that lists allow_secrets but does not set secrets_via: broker is rejected at
// Load — the broker refuses every issue for such a session, so the grants
// could never resolve. This is the exact defect review-kit shipped.
func TestPackAllowSecretsWithoutBrokerRejected(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "review"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeSecKitPack(t, dir, false) // omits secrets_via: broker

	cfgBody := `
connectors:
  gh:
    type: github
vaults:
  house:
    type: file
    dir: ` + vaultDir + `
packs:
  review:
    source: ./src
    version: 1.0.0
    secrets: { review_token: house/review }
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(cfgPath); err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("Load accepted allow_secrets without secrets_via: broker; the footgun guard must reject it")
	}
	if !strings.Contains(err.Error(), "secrets_via: broker") {
		t.Fatalf("error did not name the fix: %v", err)
	}
}
