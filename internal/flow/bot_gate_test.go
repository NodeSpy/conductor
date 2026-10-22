package flow

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
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

// A BOT SPEAKS AS ITSELF. A verb whose connector declares the identity
// semantic gets its identity options filled from the calling bot's config,
// overwriting what the bot passed (it can't post as another bot), and an
// attribute the bot doesn't set removes the option. The name falls back to
// the config id. A grant with no bot is untouched.
func TestRunSkillVerbBotIdentity(t *testing.T) {
	r := capRunner(t)
	st := newFakeState(t, "svc")
	r.Bots = &stubGate{v: BotVerdict{Allow: true}}
	r.Cfg.Bots = map[string]config.BotConfig{
		"sre": {Identity: config.Identity{DisplayName: "Night Owl", Avatar: &config.BotAvatar{Shape: "circle", Eyes: "wide"}}},
		"ops": {},
	}
	id := func(bot string) SkillIdentity {
		return SkillIdentity{Agent: "bot:" + bot, Verbs: []string{"svc.say"}, Bot: bot, BotRole: "home"}
	}
	spoof := func() map[string]any {
		return map[string]any{"text": "hi", "username": "Release Manager", "icon": "lighthouse", "color": "teal"}
	}
	last := func() map[string]any { c := st.snapshot(); return c[len(c)-1].Opts }

	if _, err := r.RunSkillVerb(context.Background(), id("sre"), "svc.say", spoof()); err != nil {
		t.Fatal(err)
	}
	if o := last(); o["username"] != "Night Owl" || o["icon"] != "circle" || o["text"] != "hi" {
		t.Fatalf("sre's post: %v", o)
	}
	if _, has := last()["color"]; has {
		t.Fatalf("an unset attribute kept the bot's own value: %v", last())
	}
	if _, err := r.RunSkillVerb(context.Background(), id("ops"), "svc.say", spoof()); err != nil {
		t.Fatal(err)
	}
	if o := last(); o["username"] != "ops" || o["icon"] != nil {
		t.Fatalf("a bot with no identity posts as its id, with no icon: %v", o)
	}
	// A bot that isn't in the config (a stale grant) gets every identity
	// option removed, never kept.
	if _, err := r.RunSkillVerb(context.Background(), id("gone"), "svc.say", spoof()); err != nil {
		t.Fatal(err)
	}
	if o := last(); o["username"] != nil || o["icon"] != nil {
		t.Fatalf("an unknown bot kept identity options: %v", o)
	}
	// Not a bot: nothing is filled.
	plain := id("sre")
	plain.Bot, plain.BotRole = "", ""
	if _, err := r.RunSkillVerb(context.Background(), plain, "svc.say", spoof()); err != nil {
		t.Fatal(err)
	}
	if o := last(); o["username"] != "Release Manager" {
		t.Fatalf("a non-bot call was rewritten: %v", o)
	}
}

// The engine fills exactly the attributes the plugin contract names: a
// plugin may map any of them, and the engine has a value for each.
func TestBotIdentityAttributesMatchTheContract(t *testing.T) {
	full := config.Identity{DisplayName: "N", Tagline: "t", Color: "plum", Avatar: &config.BotAvatar{Icon: "radar", Color: "teal"}}
	var got []string
	for k := range full.Attributes("sre") {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), sdk.IdentityAttributes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("engine attributes %v, contract %v", got, want)
	}
}
