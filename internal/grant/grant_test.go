package grant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func applySpec() GrantSpec {
	return GrantSpec{
		AgentID:     "agent-1",
		Tool:        "terraform",
		Argv:        Argv{Words: []string{"apply", "plan.tfplan"}, Flags: map[string][]string{"--input": {"false"}}},
		FileDigests: []FileDigest{{Path: "plan.tfplan", SHA256: digestA}},
		Expiry:      t0.Add(15 * time.Minute),
		SingleUse:   true,
		ApprovedBy:  "operator",
		CaseID:      "c-1",
	}
}

func applyPresented() Presented {
	return Presented{AgentID: "agent-1", Tool: "terraform",
		Argv: Argv{Words: []string{"apply", "plan.tfplan"}, Flags: map[string][]string{"--input": {"false"}}}}
}

func clock(at *time.Time) func() time.Time { return func() time.Time { return *at } }

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*GrantSpec){
		"no identity":       func(g *GrantSpec) { g.AgentID, g.Dispatch = "", "" },
		"no tool":           func(g *GrantSpec) { g.Tool = "" },
		"tool path":         func(g *GrantSpec) { g.Tool = "/usr/bin/terraform" },
		"empty argv":        func(g *GrantSpec) { g.Argv = Argv{} },
		"no approver":       func(g *GrantSpec) { g.ApprovedBy = " " },
		"no expiry":         func(g *GrantSpec) { g.Expiry = time.Time{} },
		"past expiry":       func(g *GrantSpec) { g.Expiry = t0 },
		"too long":          func(g *GrantSpec) { g.Expiry = t0.Add(MaxTTL + time.Second) },
		"bad digest":        func(g *GrantSpec) { g.FileDigests[0].SHA256 = "abc" },
		"upper digest":      func(g *GrantSpec) { g.FileDigests[0].SHA256 = strings.ToUpper(digestA) },
		"empty digest path": func(g *GrantSpec) { g.FileDigests[0].Path = "" },
		"dup digest": func(g *GrantSpec) {
			g.FileDigests = append(g.FileDigests, FileDigest{Path: "plan.tfplan", SHA256: digestA})
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			g := applySpec()
			mut(&g)
			if err := g.Validate(t0); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want ErrInvalid", err)
			}
		})
	}
	if err := applySpec().Validate(t0); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	g := applySpec()
	g.AgentID, g.Dispatch = "", "d-1"
	if err := g.Validate(t0); err != nil {
		t.Fatalf("dispatch-only identity rejected: %v", err)
	}
}

func TestMatches(t *testing.T) {
	g := applySpec()
	if !g.Matches(applyPresented()) {
		t.Fatal("exact command did not match")
	}
	mut := map[string]func(*Presented){
		"other agent":   func(p *Presented) { p.AgentID = "agent-2" },
		"other tool":    func(p *Presented) { p.Tool = "tofu" },
		"other word":    func(p *Presented) { p.Argv.Words = []string{"apply", "other.tfplan"} },
		"extra word":    func(p *Presented) { p.Argv.Words = append(p.Argv.Words, "x") },
		"word order":    func(p *Presented) { p.Argv.Words = []string{"plan.tfplan", "apply"} },
		"flag value":    func(p *Presented) { p.Argv.Flags = map[string][]string{"--input": {"true"}} },
		"extra flag":    func(p *Presented) { p.Argv.Flags["--auto-approve"] = []string{""} },
		"missing flag":  func(p *Presented) { p.Argv.Flags = nil },
		"renamed flag":  func(p *Presented) { p.Argv.Flags = map[string][]string{"--inputs": {"false"}} },
		"repeated flag": func(p *Presented) { p.Argv.Flags["--input"] = []string{"false", "false"} },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			p := applyPresented()
			p.Argv.Flags = map[string][]string{"--input": {"false"}}
			m(&p)
			if g.Matches(p) {
				t.Fatalf("matched a different command: %+v", p)
			}
		})
	}
	// Dispatch binding: when set it must match too.
	g.Dispatch = "d-1"
	p := applyPresented()
	if g.Matches(p) {
		t.Fatal("dispatch-bound grant matched a presenter with no dispatch")
	}
	p.Dispatch = "d-1"
	if !g.Matches(p) {
		t.Fatal("dispatch-bound grant did not match its dispatch")
	}
	// Dispatch-only grant ignores the agent id the broker presents.
	g.AgentID = ""
	p.AgentID = "whoever"
	if !g.Matches(p) {
		t.Fatal("dispatch-only grant should not constrain agent id")
	}
}

func TestArgvFlagOrderIrrelevant(t *testing.T) {
	a := Argv{Words: []string{"apply"}, Flags: map[string][]string{"--a": {"1"}, "--b": {""}}}
	b := Argv{Words: []string{"apply"}, Flags: map[string][]string{"--b": {""}, "--a": {"1"}}}
	if !a.Equal(b) {
		t.Fatal("flag map order should not matter")
	}
	if got := a.String(); got != "apply --a=1 --b" {
		t.Fatalf("String = %q", got)
	}
	if !(Argv{Words: []string{"x"}}).Equal(Argv{Words: []string{"x"}, Flags: map[string][]string{}}) {
		t.Fatal("nil and empty flags should be equal")
	}
}

func TestCheckDigests(t *testing.T) {
	g := applySpec()
	if err := g.CheckDigests(map[string]string{"plan.tfplan": digestA}); err != nil {
		t.Fatalf("matching digest refused: %v", err)
	}
	if err := g.CheckDigests(map[string]string{"plan.tfplan": strings.Repeat("b", 64)}); !errors.Is(err, ErrDigest) {
		t.Fatalf("changed file accepted: %v", err)
	}
	if err := g.CheckDigests(map[string]string{}); !errors.Is(err, ErrDigest) {
		t.Fatalf("unchecked file accepted: %v", err)
	}
}

func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha = %s", got)
	}
	if !validHexSHA256(got) {
		t.Fatal("FileSHA256 output must satisfy Validate")
	}
}

func TestMemStoreLifecycle(t *testing.T) {
	now := t0
	s := NewMemStoreClock(clock(&now))
	ctx := context.Background()

	if _, err := s.Mint(ctx, GrantSpec{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid spec minted: %v", err)
	}
	g, err := s.Mint(ctx, applySpec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.ID, "g_") || !g.Issued.Equal(t0) {
		t.Fatalf("mint did not assign id/issued: %+v", g)
	}
	// Mutating what Mint returned must not reach the store.
	g.Argv.Words[0] = "destroy"
	got, _ := s.Get(ctx, g.ID)
	if got.Argv.Words[0] != "apply" {
		t.Fatal("store aliased caller memory")
	}

	found, _ := s.Find(ctx, applyPresented())
	if len(found) != 1 || found[0].ID != g.ID {
		t.Fatalf("Find = %+v", found)
	}
	other := applyPresented()
	other.AgentID = "agent-2"
	if f, _ := s.Find(ctx, other); len(f) != 0 {
		t.Fatal("Find returned another agent's grant")
	}
	if _, err := s.Consume(ctx, g.ID, other); !errors.Is(err, ErrMismatch) {
		t.Fatalf("consume by other agent: %v", err)
	}
	if _, err := s.Consume(ctx, g.ID, applyPresented()); err != nil {
		t.Fatalf("first use refused: %v", err)
	}
	if _, err := s.Consume(ctx, g.ID, applyPresented()); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second use of single-use grant: %v", err)
	}
	if f, _ := s.Find(ctx, applyPresented()); len(f) != 0 {
		t.Fatal("used grant still found")
	}
	if _, err := s.Consume(ctx, "g_nope", applyPresented()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestMemStoreExpiryAndRevoke(t *testing.T) {
	now := t0
	s := NewMemStoreClock(clock(&now))
	ctx := context.Background()
	multi := applySpec()
	multi.SingleUse = false
	g, err := s.Mint(ctx, multi)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Consume(ctx, g.ID, applyPresented()); err != nil {
			t.Fatalf("multi-use use %d: %v", i, err)
		}
	}
	now = multi.Expiry // expiry is exclusive
	if _, err := s.Consume(ctx, g.ID, applyPresented()); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired use: %v", err)
	}
	now = t0
	g2, _ := s.Mint(ctx, applySpec())
	if err := s.Revoke(ctx, g2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, g2.ID, applyPresented()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked use: %v", err)
	}
	if err := s.Revoke(ctx, "g_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	byCase, _ := s.ByCase(ctx, "c-1")
	if len(byCase) != 2 {
		t.Fatalf("ByCase = %d grants, want 2", len(byCase))
	}
	if none, _ := s.ByCase(ctx, ""); len(none) != 0 {
		t.Fatal("ByCase(\"\") must not list caseless grants")
	}
	// A day past expiry, the next mint sweeps it.
	now = multi.Expiry.Add(MaxTTL + time.Minute)
	fresh := applySpec()
	fresh.Expiry = now.Add(time.Minute)
	if _, err := s.Mint(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale grant not swept: %v", err)
	}
}

// Two brokers racing one single-use grant: exactly one wins.
func TestMemStoreConsumeIsAtomic(t *testing.T) {
	s := NewMemStoreClock(func() time.Time { return t0 })
	g, err := s.Mint(context.Background(), applySpec())
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(context.Background(), g.ID, applyPresented()); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d concurrent uses succeeded, want 1", wins.Load())
	}
}

func TestParseArgv(t *testing.T) {
	a := ParseArgv([]string{"apply", "-input=false", "--var=x=1", "plan.tfplan"})
	if !a.Equal(Argv{Words: []string{"apply", "plan.tfplan"}, Flags: map[string][]string{"-input": {"false"}, "--var": {"x=1"}}}) {
		t.Fatalf("parse = %+v", a)
	}
	// A bare flag and its value stay positional, so reordering them is a
	// different command.
	x := ParseArgv([]string{"apply", "-var-file", "a.tfvars", "plan"})
	y := ParseArgv([]string{"apply", "a.tfvars", "-var-file", "plan"})
	if x.Equal(y) {
		t.Fatal("reordered bare flag parsed equal")
	}
	if z := ParseArgv([]string{"-", "--", "-="}); len(z.Flags) != 0 || len(z.Words) != 3 {
		t.Fatalf("edge tokens: %+v", z)
	}
}

// Near returns this agent's and tool's grants whatever their argv or state,
// so the broker can say WHY none applied.
func TestNearAndExplain(t *testing.T) {
	now := t0
	s := NewMemStoreClock(clock(&now))
	ctx := context.Background()
	mint := func(mut func(*GrantSpec)) GrantSpec {
		g := applySpec()
		if mut != nil {
			mut(&g)
		}
		out, err := s.Mint(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != StateLive {
			t.Fatalf("minted state %q", out.State)
		}
		return out
	}
	live := mint(nil)
	other := mint(func(g *GrantSpec) { g.Argv = Argv{Words: []string{"apply", "other.tfplan"}} })
	revoked := mint(nil)
	_ = s.Revoke(ctx, revoked.ID)
	used := mint(nil)
	if _, err := s.Consume(ctx, used.ID, applyPresented()); err != nil {
		t.Fatal(err)
	}
	expiring := mint(func(g *GrantSpec) { g.Expiry = t0.Add(time.Minute) })
	mint(func(g *GrantSpec) { g.AgentID = "agent-2" })  // another agent: never near
	mint(func(g *GrantSpec) { g.Tool = "tofu" })        // another tool: never near
	mint(func(g *GrantSpec) { g.Dispatch = "d-other" }) // a dispatch binding that does not match
	now = t0.Add(2 * time.Minute)

	near, err := s.Near(ctx, applyPresented())
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]error{}
	for _, g := range near {
		why[g.ID] = Explain(g, applyPresented(), now)
	}
	want := map[string]error{live.ID: nil, other.ID: ErrMismatch, revoked.ID: ErrRevoked, used.ID: ErrConsumed, expiring.ID: ErrExpired}
	if len(near) != len(want) {
		t.Fatalf("Near returned %d grants, want %d: %+v", len(near), len(want), near)
	}
	for id, w := range want {
		if got, ok := why[id]; !ok || got != w {
			t.Errorf("Explain(%s) = %v, want %v", id, got, w)
		}
	}
	if near[0].ID != expiring.ID {
		t.Fatalf("Near not soonest-expiry first: %s", near[0].ID)
	}
	// Find agrees with Explain == nil.
	found, _ := s.Find(ctx, applyPresented())
	if len(found) != 1 || found[0].ID != live.ID || found[0].State != StateLive {
		t.Fatalf("Find: %+v", found)
	}
	if g, _ := s.Get(ctx, revoked.ID); g.State != StateRevoked {
		t.Fatalf("Get state %q", g.State)
	}
}

// The argv comparison exactly as the package doc states it.
func TestArgvSemanticsAsDocumented(t *testing.T) {
	eq := func(a, b []string) bool { return ParseArgv(a).Equal(ParseArgv(b)) }
	same := [][2][]string{
		{{"apply", "-a=1", "-b=2", "p"}, {"apply", "-b=2", "-a=1", "p"}}, // -k=v flags: a set
		{{"-a=1", "apply", "p"}, {"apply", "p", "-a=1"}},                 // …wherever they sit
	}
	differ := [][2][]string{
		{{"apply", "p", "q"}, {"apply", "q", "p"}},                           // word order
		{{"apply", "-var-file", "a", "p"}, {"apply", "p", "-var-file", "a"}}, // a bare -flag value pair is words
		{{"apply", "-a=1", "-a=2"}, {"apply", "-a=2", "-a=1"}},               // repeated-flag value order
		{{"apply", "p"}, {"apply", "p", "-auto-approve"}},                    // an extra token
		{{"apply", "-a=1"}, {"apply", "-a=2"}},                               // a changed value
		{{"apply", "-a=1"}, {"apply", "-b=1"}},                               // a changed name
	}
	for _, c := range same {
		if !eq(c[0], c[1]) {
			t.Errorf("%v and %v should be the same command", c[0], c[1])
		}
	}
	for _, c := range differ {
		if eq(c[0], c[1]) {
			t.Errorf("%v and %v should differ", c[0], c[1])
		}
	}
}
