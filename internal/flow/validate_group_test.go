package flow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// TestValidateRejections is the load-time gate's rejection table: every kind
// of dangling reference, unknown key, or out-of-scope read fails BEFORE the
// daemon runs.
func TestValidateRejections(t *testing.T) {
	base := `
connectors:
  svc: { type: fake, options: { text: "default" } }
agents:
  fixer: { provider: claude }
workflows:
  wf:
    inputs:
      x: { type: string, required: true }
    outputs:
      out: "{{.s1.id}}"
    steps:
      - { id: s1, uses: svc.post, options: { text: "{{.inputs.x}}" } }
`
	cases := []struct {
		name, trigger, wantErr string
	}{
		{
			"unknown event",
			"- on: svc.nope\n  steps: [{uses: svc.post, options: {text: t}}]",
			`no event "nope"`,
		},
		{
			"unknown filter key",
			"- on: svc.ping\n  filters: {bogus: 1}\n  steps: [{uses: svc.post, options: {text: t}}]",
			`unknown key "bogus"`,
		},
		{
			"filter type mismatch",
			"- on: svc.ping\n  filters: {only: [a]}\n  steps: [{uses: svc.post, options: {text: t}}]",
			"want string",
		},
		{
			"unknown trigger option",
			"- on: svc.ping\n  options: {nope: 1}\n  steps: [{uses: svc.post, options: {text: t}}]",
			`unknown key "nope"`,
		},
		{
			"unknown verb",
			"- on: svc.ping\n  steps: [{uses: svc.zap, options: {}}]",
			`no verb "zap"`,
		},
		{
			"unknown verb option",
			"- on: svc.ping\n  steps: [{uses: svc.post, options: {text: t, extra: 1}}]",
			`unknown key "extra"`,
		},
		{
			"missing required option (no default covers it)",
			"- on: svc.ping\n  steps: [{uses: svc.ask, options: {}}]",
			`missing required key "prompt"`,
		},
		{
			"dangling template ref",
			"- on: svc.ping\n  steps: [{id: a, type: agent, agent: fixer, prompt: 'do {{.nope}}'}]",
			"{{.nope}} is not available",
		},
		{
			"step output read before it exists",
			"- on: svc.ping\n  steps: [{id: a, uses: svc.post, options: {text: '{{.b.id}}'}}, {id: b, uses: svc.post, options: {text: t}}]",
			"{{.b.id}} is not available",
		},
		{
			"start hook reading a step output",
			"- on: svc.ping\n  hooks: [{at: start, uses: svc.post, options: {text: '{{.a.id}}'}}]\n  steps: [{id: a, uses: svc.post, options: {text: t}}]",
			"{{.a.id}} is not available",
		},
		{
			"verb output field typo",
			"- on: svc.ping\n  steps: [{id: a, uses: svc.post, options: {text: t}}, {id: b, uses: svc.post, options: {text: '{{.a.bogus}}'}}]",
			`no output "bogus"`,
		},
		{
			"unknown agent profile",
			"- on: svc.ping\n  steps: [{type: agent, agent: ghost, prompt: p}]",
			`unknown agent profile "ghost"`,
		},
		{
			"handoff on a non-ask connector",
			"- on: svc.ping\n  steps: [{type: agent, agent: fixer, prompt: p, background: true, handoff: svc}]",
			"", // svc HAS an ask verb (Ask true) — this case asserts the positive; see below
		},
		{
			"unknown workflow input",
			"- on: svc.ping\n  steps: [{id: c, use: wf, with: {x: v, y: 2}}]",
			`no input "y"`,
		},
		{
			"missing required workflow input",
			"- on: svc.ping\n  steps: [{id: c, use: wf, with: {}}]",
			`requires input "x"`,
		},
		{
			"group key out-of-scope ref",
			"- on: svc.ping\n  group: {key: '{{.zilch}}'}\n  steps: [{uses: svc.post, options: {text: t}}]",
			"{{.zilch}} is not available",
		},
		{
			"fail hook error ref OK at fail but not at done",
			"- on: svc.ping\n  hooks: [{at: done, uses: svc.post, options: {text: '{{.error}}'}}]\n  steps: [{uses: svc.post, options: {text: t}}]",
			"{{.error}} is not available",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, base+"triggers:\n"+indent(tc.trigger, "  ")+"\n")
			reg := buildRegistry(t, cfg)
			err := Validate(cfg, reg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestValidateScopedPositives: the mirror-image accepts — the same refs in
// the positions where they ARE legal.
func TestValidateScopedPositives(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake, options: { text: "covers-required" } }
agents:
  fixer: { provider: claude }
triggers:
  - on: svc.ping
    group: { key: "{{.repo}}#{{.number}}", window: 5s }
    hooks:
      - { at: start, uses: svc.post, options: { text: "ctx only: {{.msg}}" } }
      - { at: done,  uses: svc.post, options: { text: "sees {{.a.id}}" } }
      - { at: fail,  uses: svc.post, options: { text: "{{.failed_step}}: {{.error}}" } }
    steps:
      - id: a
        uses: svc.post
        options: { text: "{{.msg}}" }
        hooks:
          - { at: done, uses: svc.post, options: { text: "own output {{.a.id}}" } }
      - id: each
        for_each: "{{.group.events}}"
        uses: svc.post
        options: { text: "item {{.index}}: {{.item.msg}}" }
      - id: b
        if: "{{.a.id}} > 0"
        uses: svc.post
        options: { text: "{{.a.id}}" }
  # A verb-only options map satisfied ENTIRELY by connector defaults.
  - on: svc.ping
    name: defaults-only
    steps: [{ id: d, uses: svc.post }]
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = pre + l
		}
	}
	return strings.Join(lines, "\n")
}

// --- grouper with a fake clock ---

type fakeTimer struct {
	c        *fakeClock
	deadline time.Time
	fn       func()
	stopped  bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	t.deadline = t.c.now.Add(d)
	t.stopped = false
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) GroupTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, deadline: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

// advance moves time forward, firing due timers (outside the lock).
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.deadline.After(c.now) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
	// Let the fire goroutines run.
	time.Sleep(10 * time.Millisecond)
}

type batchRec struct {
	mu      sync.Mutex
	batches [][]core.Trigger
	block   chan struct{} // non-nil: fire blocks until closed
}

func (b *batchRec) fire(key string, events []core.Trigger) {
	if b.block != nil {
		<-b.block
	}
	b.mu.Lock()
	b.batches = append(b.batches, events)
	b.mu.Unlock()
}

func (b *batchRec) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

func trigN(n string) core.Trigger {
	return core.Trigger{Kind: "ping", Dedup: n, Title: n}
}

func TestGrouperDebounce(t *testing.T) {
	clock := newFakeClock()
	rec := &batchRec{}
	g := NewGrouper(clock, rec.fire)

	// Events at t=0, +5s, +10s with a 15s window: each event resets the
	// debounce, so nothing fires until 15s of quiet.
	g.Add("k", trigN("e1"), 15*time.Second, time.Minute)
	clock.advance(5 * time.Second)
	g.Add("k", trigN("e2"), 15*time.Second, time.Minute)
	clock.advance(5 * time.Second)
	g.Add("k", trigN("e3"), 15*time.Second, time.Minute)
	clock.advance(14 * time.Second)
	if rec.count() != 0 {
		t.Fatal("fired before the window went quiet")
	}
	clock.advance(2 * time.Second)
	if rec.count() != 1 {
		t.Fatalf("batches: %d, want 1", rec.count())
	}
	rec.mu.Lock()
	got := len(rec.batches[0])
	rec.mu.Unlock()
	if got != 3 {
		t.Fatalf("batch size: %d, want 3", got)
	}
}

func TestGrouperMaxWait(t *testing.T) {
	clock := newFakeClock()
	rec := &batchRec{}
	g := NewGrouper(clock, rec.fire)

	// A never-quiet stream (every 10s, window 15s) is capped by max_wait 40s.
	window, maxWait := 15*time.Second, 40*time.Second
	g.Add("k", trigN("e0"), window, maxWait)
	for i := 0; i < 5; i++ {
		clock.advance(10 * time.Second)
		g.Add("k", trigN("e"), window, maxWait)
	}
	if rec.count() == 0 {
		t.Fatal("max_wait did not cap a never-quiet group")
	}
}

func TestGrouperOneRunPerKey(t *testing.T) {
	clock := newFakeClock()
	rec := &batchRec{block: make(chan struct{})}
	g := NewGrouper(clock, rec.fire)

	g.Add("k", trigN("a"), time.Second, time.Minute)
	clock.advance(2 * time.Second) // fires; fire() blocks
	// Events during the in-flight run buffer into the next batch.
	g.Add("k", trigN("b"), time.Second, time.Minute)
	g.Add("k", trigN("c"), time.Second, time.Minute)
	clock.advance(5 * time.Second)
	if rec.count() != 0 {
		t.Fatal("second batch fired while the first was in flight")
	}
	// A closed channel unblocks every current and future fire immediately;
	// never reassign block after goroutines exist (data race).
	close(rec.block)
	time.Sleep(20 * time.Millisecond) // first fire records + done() reschedules
	clock.advance(2 * time.Second)
	deadline := time.Now().Add(time.Second)
	for rec.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count() != 2 {
		t.Fatalf("batches: %d, want 2", rec.count())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.batches[0]) != 1 || len(rec.batches[1]) != 2 {
		t.Fatalf("batch sizes: %d, %d — want 1, 2", len(rec.batches[0]), len(rec.batches[1]))
	}
}

func TestGrouperDistinctKeysIndependent(t *testing.T) {
	clock := newFakeClock()
	rec := &batchRec{}
	g := NewGrouper(clock, rec.fire)
	g.Add("k1", trigN("a"), time.Second, time.Minute)
	g.Add("k2", trigN("b"), time.Second, time.Minute)
	clock.advance(2 * time.Second)
	if rec.count() != 2 {
		t.Fatalf("batches: %d, want 2 (independent keys)", rec.count())
	}
}

func TestGroupKeyDefault(t *testing.T) {
	tr := trigN("dedup-sig")
	key, err := GroupKey("", tr, nil)
	if err != nil || key != "dedup-sig" {
		t.Fatalf("default key: %q, %v", key, err)
	}
	key, err = GroupKey("{{.repo}}#{{.number}}", tr, map[string]any{"repo": "a/b", "number": 7})
	if err != nil || key != "a/b#7" {
		t.Fatalf("templated key: %q, %v", key, err)
	}
}

// --- quiet hours + SpecFor ---

func TestInQuietWindow(t *testing.T) {
	q := func(from, to string) *config.QuietHours {
		return &config.QuietHours{TZ: "UTC", From: from, To: to}
	}
	at := func(h, m int) time.Time {
		return time.Date(2026, 3, 3, h, m, 0, 0, time.UTC)
	}
	// Same-day window.
	if in, _ := InQuietWindow(q("09:00", "17:00"), at(12, 0)); !in {
		t.Error("12:00 should be inside 09:00–17:00")
	}
	if in, _ := InQuietWindow(q("09:00", "17:00"), at(8, 59)); in {
		t.Error("08:59 should be outside")
	}
	if in, until := InQuietWindow(q("09:00", "17:00"), at(16, 59)); !in || until.Hour() != 17 {
		t.Errorf("16:59 in-window end: %v %v", in, until)
	}
	// Overnight window.
	if in, until := InQuietWindow(q("22:00", "07:00"), at(23, 30)); !in || until.Day() != 4 {
		t.Errorf("23:30 overnight: in=%v until=%v", in, until)
	}
	if in, _ := InQuietWindow(q("22:00", "07:00"), at(6, 59)); !in {
		t.Error("06:59 should be inside the overnight window")
	}
	if in, _ := InQuietWindow(q("22:00", "07:00"), at(12, 0)); in {
		t.Error("noon should be outside the overnight window")
	}
	// Fail-open on bad input.
	if in, _ := InQuietWindow(&config.QuietHours{From: "nope", To: "07:00"}, at(3, 0)); in {
		t.Error("unparsable clock must fail open")
	}
	if in, _ := InQuietWindow(nil, at(3, 0)); in {
		t.Error("nil quiet hours must fail open")
	}
}

func TestSpecFor(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps: [{uses: svc.post, options: {text: t}}]
`)
	r := New(Runner{Cfg: cfg})
	if _, ok := r.SpecFor("0:svc.ping"); !ok {
		t.Error("valid ref should resolve")
	}
	for _, bad := range []string{"1:svc.ping", "0:svc.other", "garbage", "x:y", ""} {
		if _, ok := r.SpecFor(bad); ok {
			t.Errorf("ref %q should not resolve", bad)
		}
	}
}

// TestRunWithBatchContext: {{.group.*}} is addressable in steps of a batched
// run, including from js code.
func TestRunWithBatchContext(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps:
      - { id: n, run: js, code: "return {total: ctx.group.events.length}" }
      - { id: p, uses: svc.post, options: { text: "{{.group.key}} has {{.n.total}} (first {{.group.first.msg}}, last {{.group.last.msg}})" } }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	spec := cfg.Triggers[0]

	t1 := newTrigger("ping", map[string]any{"msg": "first-msg"})
	t2 := newTrigger("ping", map[string]any{"msg": "last-msg"})
	batch := &Batch{Key: "K", Events: []core.Trigger{t1, t2}}
	rig.Runner.Run(context.Background(), emptyRun(), t2, spec, batch, false)

	calls := st.snapshot()
	if len(calls) == 0 {
		t.Fatal("no verb calls")
	}
	got, _ := calls[len(calls)-1].Opts["text"].(string)
	if got != "K has 2 (first first-msg, last last-msg)" {
		t.Fatalf("batch context text: %q", got)
	}
}
