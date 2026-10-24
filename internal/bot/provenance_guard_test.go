package bot

import (
	"context"
	"testing"
)

// M2: the `event` trust tier is for REAL events — an alert, a schedule, a
// webhook — which always carry provenance (a source), even when no human
// requester is named. A truly provenance-less intake (no requester AND no
// source) must NOT inherit event-tier autonomy: it drops to the least
// trusted class, so nothing it asks for runs above observe/propose unapproved.
func TestEmptyIntakeWithoutProvenanceIsLeastTrust(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()

	// A provenance-less intake: no requester, no source.
	id, err := f.svc.Deliver(ctx, Event{Bot: "a", Title: "where did this come from?"})
	if err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "a", id); c.RequesterClass != ReqUser {
		t.Fatalf("provenance-less intake classified %q, want %q (least trust) — an empty intake must not be event-autonomous", c.RequesterClass, ReqUser)
	}

	// A real event: no human requester, but it carries a source. Still event.
	id2, err := f.svc.Deliver(ctx, Event{Bot: "a", Source: "pd.incident", Title: "db is down"})
	if err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "a", id2); c.RequesterClass != ReqEvent {
		t.Fatalf("real event (source %q) classified %q, want %q — a sourced event must stay event-autonomous", c.Source, c.RequesterClass, ReqEvent)
	}
}

// sourcedClass is the chokepoint: an empty requester is event only when the
// intake has a source to vouch for it. The pure classifier (classifyAgainst)
// keeps its documented ""→event contract; the guard is the source-aware layer.
func TestSourcedClassProvenanceGuard(t *testing.T) {
	cfg := newTeamFixture(t, nil).cfg
	ops := cfg.TeamOperators("platform")
	cases := []struct {
		name, requester, source, wantClass string
	}{
		{"empty, no source", "", "", ReqUser},
		{"empty, with source", "", "pd.incident", ReqEvent},
		{"operator, no source", "op", "", ReqOperator},
		{"third party, no source", "chat:U9", "", ReqUser},
	}
	for _, tc := range cases {
		_, class := sourcedClass(cfg, ops, tc.requester, tc.source)
		if class != tc.wantClass {
			t.Errorf("%s: class = %q, want %q", tc.name, class, tc.wantClass)
		}
	}
	// The pure classifier is unchanged: a bare empty requester is still event.
	if _, class := classifyAgainst(cfg, ops, ""); class != ReqEvent {
		t.Fatalf("classifyAgainst('') = %q, want %q (its documented contract)", class, ReqEvent)
	}
}

// The delivery write-boundary backstops the classify-time guard: even a caller
// that hands deliverCase an autonomy class for a requester-less AND source-less
// event cannot write a trusted case — it drops to least trust. This is the
// structural chokepoint, so a path that reaches the sink without sourcedClass
// (a ready ticket, a future caller) cannot produce a provenance-less-but-trusted
// intake.
func TestDeliverCaseDropsProvenanceLessAutonomy(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	for _, class := range []string{ReqEvent, ReqOperator, ReqBot} {
		id, err := f.svc.deliverCase(ctx, Event{Bot: "a"}, "", class, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if c := f.caseOf(t, "a", id); c.RequesterClass != ReqUser {
			t.Fatalf("deliverCase wrote %q for a provenance-less %s intake, want %q (least trust)", c.RequesterClass, class, ReqUser)
		}
	}
	// A source vouches for the event: an autonomy class with a source is kept.
	id, err := f.svc.deliverCase(ctx, Event{Bot: "a", Source: "pd.incident"}, "", ReqEvent, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "a", id); c.RequesterClass != ReqEvent {
		t.Fatalf("a sourced event was downgraded at the sink: %q", c.RequesterClass)
	}
	// A named requester vouches too: an operator with no source is kept.
	id, err = f.svc.deliverCase(ctx, Event{Bot: "a"}, "op", ReqOperator, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "a", id); c.RequesterClass != ReqOperator {
		t.Fatalf("a named operator was downgraded at the sink: %q", c.RequesterClass)
	}
}
