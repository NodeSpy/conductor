package main

import (
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
)

// H1: with conductor's agent sandbox default-on, a bot's HOME session runs in a
// pinned paseo workspace conductor never registers as a worktree. `conductor
// sandbox exec` resolves that cwd through the daemon's sandbox resolver and
// mints the session's token from the answer — so the resolver MUST map a live
// bot session's cwd to the bot's own identity and its granted skill verbs. If
// it falls to a default (dir:) workspace the bot silently loses its identity
// and a verb-less token: it can no longer act through any verb.
func TestBotHomeSessionKeepsIdentityUnderSandbox(t *testing.T) {
	homeCwd := t.TempDir()
	cfg := &config.Config{
		Bots: map[string]config.BotConfig{
			"shopper": {Skill: &config.SkillPolicy{Verbs: []string{"browser.purchase", "chat.post"}}},
		},
	}
	cfg.Store.StateFile = filepath.Join(t.TempDir(), "state.json")

	st, err := store.Open(store.Options{StatePath: cfg.Store.StateFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateBot("shopper", func(r *store.BotRecord) error {
		r.Home = &store.HomeSession{ID: "h1", Dispatch: "d1", Cwd: homeCwd}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	svc := &bot.Service{Cfg: func() *config.Config { return cfg }, Ledger: st}
	// buildBots publishes the cwd→session lookup before wireAgentSandbox (which
	// runs later at boot) consults it.
	registerSandboxSessions(svc)
	t.Cleanup(func() { botSessionLookup = nil })

	// A broker so the resolver can mint the session's token.
	b := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
	oldB := skill.Active()
	skill.SetActive(b)
	t.Cleanup(func() { skill.SetActive(oldB) })

	stop, err := wireAgentSandbox(cfg, cfg.Store.StateFile, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	r := sandboxResolver.Load()
	if r == nil {
		t.Fatal("agent sandbox not wired (it is on by default)")
	}
	// A skill socket must be published for the policy to carry a minted token;
	// its path need not be live for this test.
	r.Socket = filepath.Join(t.TempDir(), "memory.sock")

	pol, err := r.PolicyFor(homeCwd, "claude", nil, false)
	if err != nil {
		t.Fatalf("policy for the bot's home cwd: %v", err)
	}
	// Its identity is the bot session's, not a verb-less dir: workspace.
	if want := bot.SessionIdentity("shopper", "h1"); pol.Identity != want {
		t.Fatalf("home session identity = %q, want %q — the sandbox stripped the bot's identity", pol.Identity, want)
	}
	// Its token carries the bot binding and the granted verbs, so the bot can
	// still act through them under the sandbox.
	if pol.Token == "" {
		t.Fatal("no session token minted for the bot's home session")
	}
	id, err := b.Authorize(pol.Token, skill.Peer{})
	if err != nil {
		t.Fatalf("authorize the home session token: %v", err)
	}
	if id.Bot != "shopper" || id.BotRole != config.BotRoleHome {
		t.Fatalf("token binding = bot %q role %q, want shopper/home", id.Bot, id.BotRole)
	}
	for _, v := range []string{"browser.purchase", "chat.post"} {
		if !hasVerb(id.Policy.Verbs, v) {
			t.Fatalf("home session token lost granted verb %q (verbs: %v)", v, id.Policy.Verbs)
		}
	}
}

func hasVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}
