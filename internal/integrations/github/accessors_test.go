package github

import (
	"context"
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
	// Passthrough: baseConfig sets no sweep, so the raw pointer stays nil (the
	// accessor injects no default — IsEnabled() applies the default-on later).
	if g.SweepSettings().Enabled != nil {
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
