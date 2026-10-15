package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubGate struct {
	v     MemberVerdict
	err   error
	calls int
}

func (g *stubGate) CheckVerb(context.Context, SkillIdentity, string, map[string]any) (MemberVerdict, error) {
	g.calls++
	return g.v, g.err
}

// The member gate sits on the one skill chokepoint: a member-bound grant's
// call runs only when the gate allows it; a proposal comes back to the agent
// without invoking the connector; a refusal is an error; and with no gate
// wired a member-bound grant fails closed. A grant with no member binding
// never consults the gate.
func TestRunSkillVerbMemberGate(t *testing.T) {
	r := capRunner(t)
	st := newFakeState(t, "svc")
	id := SkillIdentity{Agent: "member:sre", Verbs: []string{"svc.post"}, Member: "sre", MemberRole: "home",
		Scopes: map[string]map[string][]string{"svc.post": {"channel": {"*"}}}}
	opts := func() map[string]any { return map[string]any{"text": "hi", "channel": "#ops"} }

	if _, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts()); err == nil || !strings.Contains(err.Error(), "no member gate") {
		t.Fatalf("member-bound grant without a gate must fail closed: %v", err)
	}
	g := &stubGate{v: MemberVerdict{Output: map[string]any{"executed": false, "proposal": "p1"}}}
	r.Members = g
	out, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts())
	if err != nil || out["proposal"] != "p1" {
		t.Fatalf("proposal verdict: %v %v", out, err)
	}
	if n := len(st.snapshot()); n != 0 {
		t.Fatalf("a proposed call reached the connector (%d calls)", n)
	}
	g.v, g.err = MemberVerdict{}, errors.New("never tier")
	if _, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts()); err == nil {
		t.Fatal("gate refusal ignored")
	}
	g.v, g.err = MemberVerdict{Allow: true}, nil
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
	// No member binding: the gate is not consulted at all.
	plain := id
	plain.Member, plain.MemberRole = "", ""
	before = g.calls
	g.v = MemberVerdict{Output: map[string]any{"proposal": "x"}}
	if _, err := r.RunSkillVerb(context.Background(), plain, "svc.post", opts()); err != nil {
		t.Fatal(err)
	}
	if g.calls != before {
		t.Fatal("a non-member grant consulted the member gate")
	}
}
