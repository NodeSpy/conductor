package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubGate struct {
	v     BotVerdict
	err   error
	calls int
}

func (g *stubGate) CheckVerb(context.Context, SkillIdentity, string, map[string]any) (BotVerdict, error) {
	g.calls++
	return g.v, g.err
}

// The bot gate sits on the one skill chokepoint: a bot-bound grant's
// call runs only when the gate allows it; a proposal comes back to the agent
// without invoking the connector; a refusal is an error; and with no gate
// wired a bot-bound grant fails closed. A grant with no bot binding
// never consults the gate.
func TestRunSkillVerbBotGate(t *testing.T) {
	r := capRunner(t)
	st := newFakeState(t, "svc")
	id := SkillIdentity{Agent: "bot:sre", Verbs: []string{"svc.post"}, Bot: "sre", BotRole: "home",
		Scopes: map[string]map[string][]string{"svc.post": {"channel": {"*"}}}}
	opts := func() map[string]any { return map[string]any{"text": "hi", "channel": "#ops"} }

	if _, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts()); err == nil || !strings.Contains(err.Error(), "no bot gate") {
		t.Fatalf("bot-bound grant without a gate must fail closed: %v", err)
	}
	g := &stubGate{v: BotVerdict{Output: map[string]any{"executed": false, "proposal": "p1"}}}
	r.Bots = g
	out, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts())
	if err != nil || out["proposal"] != "p1" {
		t.Fatalf("proposal verdict: %v %v", out, err)
	}
	if n := len(st.snapshot()); n != 0 {
		t.Fatalf("a proposed call reached the connector (%d calls)", n)
	}
	g.v, g.err = BotVerdict{}, errors.New("never tier")
	if _, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts()); err == nil {
		t.Fatal("gate refusal ignored")
	}
	g.v, g.err = BotVerdict{Allow: true}, nil
	if _, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts()); err != nil {
		t.Fatal(err)
	}
	if n := len(st.snapshot()); n != 1 {
		t.Fatalf("allowed call did not run (%d calls)", n)
	}
	// The gate only narrows: a verb the grant does not admit never reaches it.
	before := g.calls
	if _, err := r.RunSkillVerb(context.Background(), id, "svc.notify", map[string]any{"text": "x"}); err == nil {
		t.Fatal("ungranted verb ran")
	}
	if g.calls != before {
		t.Fatal("the gate was consulted for a verb the grant refuses")
	}
	// No bot binding: the gate is not consulted at all.
	plain := id
	plain.Bot, plain.BotRole = "", ""
	before = g.calls
	g.v = BotVerdict{Output: map[string]any{"proposal": "x"}}
	if _, err := r.RunSkillVerb(context.Background(), plain, "svc.post", opts()); err != nil {
		t.Fatal(err)
	}
	if g.calls != before {
		t.Fatal("a non-bot grant consulted the bot gate")
	}
}
