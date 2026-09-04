package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestAccessorsAndTranslate: the seams main and replay use — Name, Translate,
// RetryPolicy/SweepSettings/IdentityTokens, Actions enumeration.
func TestAccessorsAndTranslate(t *testing.T) {
	cfg := baseConfig()
	cfg.Retry = config.Retry{Max: 2}
	cfg.Identity = Identity{ReadToken: "app", WriteToken: "w", CommitAuthor: "self"}
	g := newTestIntegration(t, cfg)
	if g.Name() != "test" && g.Name() == "" {
		t.Fatalf("Name = %q", g.Name())
	}
	// Translate is the replay surface over triggersFor.
	trs := g.Translate(context.Background(), "issue_comment", []byte(`{
		"action":"created",
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":3,"pull_request":{},"user":{"login":"me"}},
		"comment":{"id":51,"user":{"login":"reviewer"},"body":"please fix"}}`))
	if len(trs) != 1 || trs[0].Kind != "new_comment" {
		t.Fatalf("Translate: %+v", trs)
	}
	if g.RetryPolicy().Max != 2 {
		t.Fatal("RetryPolicy passthrough")
	}
	if g.SweepSettings().Enabled {
		t.Fatal("SweepSettings passthrough")
	}
	r, w, a := g.IdentityTokens()
	if r != "app" || w != "w" || a != "self" {
		t.Fatalf("IdentityTokens: %q %q %q", r, w, a)
	}
	refs := g.Actions()
	if len(refs) == 0 || !strings.Contains(refs[0].Where, "github[") {
		t.Fatalf("Actions refs: %+v", refs)
	}
}

// TestEnsureClientsCredentialChain: token → static auth; App-less token-less →
// the gh CLI fallback (stubbed on PATH); a failing gh surfaces the guidance.
func TestEnsureClientsCredentialChain(t *testing.T) {
	// token: static auth.
	cfg := baseConfig()
	cfg.App = AppConfig{}
	cfg.Token = "pat-123"
	g := newTestIntegration(t, cfg)
	if err := g.ensureClients(); err != nil {
		t.Fatalf("token chain: %v", err)
	}
	tok, err := g.app.installationToken(context.Background(), 0)
	if err != nil || tok != "pat-123" {
		t.Fatalf("static auth token: %q %v", tok, err)
	}

	// gh CLI fallback.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/usr/bin/env bash\necho gh-tok\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg.Token = ""
	g2 := newTestIntegration(t, cfg)
	if err := g2.ensureClients(); err != nil {
		t.Fatalf("gh fallback: %v", err)
	}
	tok, _ = g2.app.installationToken(context.Background(), 0)
	if tok != "gh-tok" {
		t.Fatalf("gh token: %q", tok)
	}

	// Empty gh output errors with the configure guidance.
	os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/usr/bin/env bash\necho\n"), 0o755)
	g3 := newTestIntegration(t, cfg)
	if err := g3.ensureClients(); err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("empty gh token: %v", err)
	}
}
