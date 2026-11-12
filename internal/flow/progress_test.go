package flow

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// progressClock is a hand-driven clock for the progress report.
type progressClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *progressClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *progressClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// driveProgress swaps in a hand-driven clock and a hook loop that wakes only
// when the report changes (tick: an hour), so every post a test sees is one
// it caused.
func driveProgress(t *testing.T) *progressClock {
	t.Helper()
	clk := &progressClock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	oldNow, oldTick := progressNow, progressTick
	progressNow, progressTick = clk.Now, time.Hour
	t.Cleanup(func() { progressNow, progressTick = oldNow, oldTick })
	return clk
}

// waitPosts waits until l has recorded n posts and returns them.
func waitPosts(t *testing.T, l *headLog, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p := l.posts(); len(p) >= n {
			return p
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waited for %d posts, have %v", n, l.posts())
	return nil
}

// settle gives the hook loop time to act on anything pending, then reports
// the posts so far: for asserting that NOTHING was posted.
func settle(l *headLog) []string {
	time.Sleep(60 * time.Millisecond)
	return l.posts()
}

const progressSpec = `
on: %[1]s.ping
hooks:
  - { at: progress, uses: %[1]s.post, options: { text: "{{.progress.state}}|{{.progress.text}}|{{.progress.elapsed}}|{{.run.start_sha}}" } }
steps:
  - { id: fix, type: agent, prompt: p }
`

// A queued run reports at once — why it waits and its place — and later
// changes only as often as the hook's pacing allows; a state change
// (queued → running) goes out at once; an unchanged report never re-posts,
// and elapsed time moves in whole minutes.
func TestProgressQueuedThrottledAndDeduped(t *testing.T) {
	clk := driveProgress(t)
	rig, l := headRig(t, "pq", "aaaaaaa1111")
	trig := headTrigger("pq")
	p := rig.Runner.NewProgress(context.Background(), trig, mustSpec(t, fmt.Sprintf(progressSpec, "pq")))
	if p == nil {
		t.Fatal("a trigger with a progress hook got no report")
	}
	defer p.Close()

	p.Queued("an agent slot", 2)
	if got := waitPosts(t, l, 1)[0]; got != "queued|queued — waiting for an agent slot (2 ahead)|<1m|aaaaaaa1111" {
		t.Fatalf("first queued post = %q", got)
	}
	// The queue moves 20s later: inside the 30s pacing, so not yet.
	clk.Advance(20 * time.Second)
	p.Queued("an agent slot", 1)
	if got := settle(l); len(got) != 1 {
		t.Fatalf("posted inside the pacing window: %v", got)
	}
	// Past the window, the next change goes out with the current place.
	clk.Advance(15 * time.Second)
	p.Queued("an agent slot", 0)
	if got := waitPosts(t, l, 2)[1]; got != "queued|queued — waiting for an agent slot (next)|<1m|aaaaaaa1111" {
		t.Fatalf("second queued post = %q", got)
	}
	// Nothing changed but the clock, within the minute: nothing re-posts.
	clk.Advance(20 * time.Second)
	p.Queued("an agent slot", 0)
	if got := settle(l); len(got) != 2 {
		t.Fatalf("an unchanged report re-posted: %v", got)
	}
	// A whole minute later the elapsed time changed: one post.
	clk.Advance(30 * time.Second)
	p.Queued("an agent slot", 0)
	if got := waitPosts(t, l, 3)[2]; got != "queued|queued — waiting for an agent slot (next)|1m|aaaaaaa1111" {
		t.Fatalf("elapsed post = %q", got)
	}
	// The run starts 1s later: a state change ignores the pacing.
	clk.Advance(time.Second)
	p.begin(context.Background(), trig, withRun(rig.Runner.progressBase(trig, nil), runFacts{startSHA: "bbbbbbb2222"}), "")
	if got := waitPosts(t, l, 4)[3]; got != "running|starting|1m|bbbbbbb2222" {
		t.Fatalf("start post = %q", got)
	}
	// Past the pacing window, a report that says the same thing (same text,
	// same whole minute) still never re-posts.
	clk.Advance(30 * time.Second)
	p.lifecycle("starting")
	if got := settle(l); len(got) != 4 {
		t.Fatalf("an unchanged report re-posted past the window: %v", got)
	}
}

// The agent's own note outranks the tool calls its runtime sees, for a
// while; after that the latest tool call shows. A fan-out's count wins over
// both while it runs.
func TestProgressNoteActivityAndFanOut(t *testing.T) {
	clk := driveProgress(t)
	rig, l := headRig(t, "pn", "aaaaaaa1111")
	trig := headTrigger("pn")
	p := rig.Runner.NewProgress(context.Background(), trig, mustSpec(t, fmt.Sprintf(progressSpec, "pn")))
	defer p.Close()
	p.begin(context.Background(), trig, withRun(rig.Runner.progressBase(trig, nil), runFacts{startSHA: "aaaaaaa1111"}), "")
	waitPosts(t, l, 1)

	step := func(d time.Duration, fn func(), want string) {
		t.Helper()
		clk.Advance(d)
		n := len(l.posts())
		fn()
		if got := waitPosts(t, l, n+1)[n]; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	step(31*time.Second, func() { p.agentNote("reading 2 comments") }, "running|reading 2 comments|<1m|aaaaaaa1111")
	// A tool call while the note is fresh: the note stays; only the minute moved.
	step(31*time.Second, func() { p.agentActivity("editing adapter.go") }, "running|reading 2 comments|1m|aaaaaaa1111")
	// The note has aged: the latest tool call shows.
	step(2*time.Minute, func() { p.agentActivity("running go test ./adapters/...") }, "running|running go test ./adapters/...|3m|aaaaaaa1111")
	step(31*time.Second, func() { p.fanOut("review", "reviewing", 18) }, "running|reviewing: 0/18 done|3m|aaaaaaa1111")
	for i := 0; i < 9; i++ {
		p.fanItemDone("review")
	}
	step(31*time.Second, func() { p.agentNote("checking one finding") }, "running|reviewing: 9/18 done|4m|aaaaaaa1111")
	step(31*time.Second, func() { p.fanEnd("review") }, "running|checking one finding|4m|aaaaaaa1111")
	step(31*time.Second, func() { p.retrying("fix", 2, 3) }, "retrying|retrying fix (attempt 2 of 3)|5m|aaaaaaa1111")
}

// Close stops the report: an update after it posts nothing. Final still
// posts its one last word, whatever the pacing.
func TestProgressCloseAndFinal(t *testing.T) {
	clk := driveProgress(t)
	rig, l := headRig(t, "pc", "aaaaaaa1111")
	trig := headTrigger("pc")
	p := rig.Runner.NewProgress(context.Background(), trig, mustSpec(t, fmt.Sprintf(progressSpec, "pc")))
	p.begin(context.Background(), trig, withRun(rig.Runner.progressBase(trig, nil), runFacts{startSHA: "aaaaaaa1111"}), "")
	waitPosts(t, l, 1)
	p.Close()
	clk.Advance(time.Minute)
	p.agentNote("after the end")
	if got := settle(l); len(got) != 1 {
		t.Fatalf("posted after Close: %v", got)
	}
	p.Final(ProgressWaiting, "waiting — the agent runtime is not answering")
	if got := l.posts(); len(got) != 2 || got[1] != "waiting|waiting — the agent runtime is not answering|1m|aaaaaaa1111" {
		t.Fatalf("final post: %v", got)
	}
}

// The report ends before the run's outcome hooks fire: a change the pacing
// held back, falling due while the done hook posts, never lands after (and
// over) what the run's end says.
func TestProgressNothingAfterTheOutcome(t *testing.T) {
	clk := driveProgress(t)
	progressTick = time.Millisecond // the loop wakes on its own, often
	rig, l := headRig(t, "pz", "aaaaaaa1111")
	spec := mustSpec(t, `
on: pz.ping
hooks:
  - { at: progress, uses: pz.post, options: { text: "progress {{.progress.text}}" } }
  - { at: done, uses: pz.post, options: { text: "done" } }
steps:
  - { id: fix, type: agent, prompt: p, progress: "working" }
`)
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		waitPosts(t, l, 1)
		// Inside the pacing window: held back, not posted.
		req.Activity("editing adapter.go")
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	l.mu.Lock()
	l.onPost = func(text string) {
		if text == "done" { // the held change falls due now
			clk.Advance(time.Minute)
			time.Sleep(30 * time.Millisecond)
		}
	}
	l.mu.Unlock()
	runTrigger(rig, headTrigger("pz"), spec)
	got := settle(l)
	if len(got) == 0 || got[len(got)-1] != "done" {
		t.Fatalf("a progress post landed after the outcome: %v", got)
	}
}

// A for_each step counts its items done on the run's report as they finish.
func TestProgressForEachCountsItems(t *testing.T) {
	clk := driveProgress(t)
	rig, l := headRig(t, "pf", "aaaaaaa1111")
	spec := mustSpec(t, `
on: pf.ping
hooks:
  - { at: progress, uses: pf.post, options: { text: "progress {{.progress.text}}" } }
steps:
  - { id: each, for_each: "a,b,c", uses: pf.post, options: { text: "item {{.item}}" }, progress: "counting" }
`)
	l.mu.Lock()
	counted := regexp.MustCompile(`progress counting: [1-3]/3 done`)
	l.onPost = func(text string) {
		if !strings.HasPrefix(text, "item ") {
			return
		}
		clk.Advance(31 * time.Second) // each item takes a while
		if text == "item c" {
			// Two items are done: give the report a moment to say so
			// before the last one ends the fan-out.
			for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
				if counted.MatchString(strings.Join(l.posts(), "\n")) {
					return
				}
			}
		}
	}
	l.mu.Unlock()
	runTrigger(rig, headTrigger("pf"), spec)
	got := strings.Join(settle(l), "\n")
	// The loop posts at its own pace, so which counts show varies; one past
	// zero always does.
	if !counted.MatchString(got) {
		t.Fatalf("no count of the items done:\n%s", got)
	}
}

// A run's hooks — start, progress and done alike — see where its dashboard
// page is.
func TestProgressRunURL(t *testing.T) {
	driveProgress(t)
	rig, l := headRig(t, "pu", "aaaaaaa1111")
	rig.Runner.RunURL = func(id string) string { return "https://dash.example/#/runs/" + id }
	spec := mustSpec(t, `
on: pu.ping
hooks:
  - { at: start, uses: pu.post, options: { text: "start url={{.run.url}}" } }
  - { at: progress, uses: pu.post, options: { text: "{{.progress.text}} url={{.run.url}}" } }
  - { at: done, uses: pu.post, options: { text: "done url={{.run.url}}" } }
steps:
  - { id: fix, type: agent, prompt: p }
`)
	trig := headTrigger("pu")
	trig.HistoryID = "r-link1"
	run := emptyRun()
	run.ID = "run-link"
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		waitPosts(t, l, 2)
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	runTriggerWithRun(rig, run, trig, spec)
	if posts := l.posts(); len(posts) < 3 || !strings.HasPrefix(posts[0], "start ") || !strings.HasPrefix(posts[len(posts)-1], "done ") {
		t.Fatalf("posts = %v", posts)
	}
	for _, s := range l.posts() {
		if !strings.HasSuffix(s, " url=https://dash.example/#/runs/r-link1") {
			t.Fatalf("a progress post without the run's link: %q", s)
		}
	}
	// No dashboard: the link is empty, never a guess.
	rig.Runner.RunURL = nil
	p := rig.Runner.NewProgress(context.Background(), trig, spec)
	defer p.Close()
	p.mu.Lock()
	p.base, p.historyID = map[string]any{}, "r-link1"
	u := p.scopeLocked()["run"].(map[string]any)["url"]
	p.mu.Unlock()
	if u != "" {
		t.Fatalf("run.url with no dashboard = %v", u)
	}
}

// A whole run: what the agent says (step.progress, by its dispatch id) and
// what its runtime sees it do reach the progress hook, the start hook goes
// out before any progress, and nothing of the progress lands after the
// run's outcome.
func TestProgressRunSequence(t *testing.T) {
	clk := driveProgress(t)
	rig, l := headRig(t, "ps", "aaaaaaa1111")
	spec := mustSpec(t, `
on: ps.ping
hooks:
  - { at: start, uses: ps.post, options: { text: "start" } }
  - { at: progress, uses: ps.post, options: { text: "progress {{.progress.text}} ({{.progress.elapsed}})" } }
  - { at: done, uses: ps.post, options: { text: "done" } }
steps:
  - { id: fix, type: agent, prompt: p, progress: "working on {{.msg}}" }
`)
	var tapped bool
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Activity == nil {
			t.Error("the dispatch carries no Activity: the runtime cannot report what the agent does")
			return dispatch.RunRef{Output: "{}"}, nil
		}
		// What the run reported before the agent started has gone out, and
		// the hook loop has caught up (no wake-up still pending from it).
		waitPosts(t, l, 2)
		settle(l)
		say := func(d time.Duration, fn func()) {
			clk.Advance(d)
			n := len(l.posts())
			fn()
			waitPosts(t, l, n+1)
		}
		say(31*time.Second, func() { tapped = rig.Runner.ReportProgress(req.DispatchID, "reading 2\ncomments") })
		say(31*time.Second, func() { req.Activity("editing adapter.go") })
		say(2*time.Minute, func() { req.Activity("running go test ./adapters/...") })
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	runTrigger(rig, headTrigger("ps"), spec)
	if !tapped {
		t.Fatal("step.progress found no run for the agent's dispatch")
	}
	got := settle(l)
	// The run's first report is "starting" or, when its first step began
	// before the hook loop woke, that step's phrase: one post either way.
	if len(got) > 1 && got[1] == "progress starting (<1m)" {
		got[1] = "progress working on x (<1m)"
	}
	want := []string{
		"start",
		"progress working on x (<1m)",
		"progress reading 2 comments (<1m)",
		"progress reading 2 comments (1m)",
		"progress running go test ./adapters/... (3m)",
		"done",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("posts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The dispatch is untapped once its step ends.
	if rig.Runner.ReportProgress("anything", "late") {
		t.Fatal("a note for no running dispatch was taken")
	}
}

// A note is one clean line: control and invisible characters go, whitespace
// collapses, and a long one is clipped.
func TestProgressNoteIsCleaned(t *testing.T) {
	r := New(Runner{})
	if got := r.cleanPhrase("  running\ttests​\x1b[31m  now\n"); got != "running tests [31m now" {
		t.Fatalf("cleaned = %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := []rune(r.cleanPhrase(long)); len(got) != noteMax || got[noteMax-1] != '…' {
		t.Fatalf("clipped to %d runes", len(got))
	}
}

// A trigger with no progress hook has no report, and costs nothing.
func TestProgressNoneWithoutHooks(t *testing.T) {
	rig, _ := headRig(t, "p0", "aaaaaaa1111")
	if p := rig.Runner.NewProgress(context.Background(), headTrigger("p0"), mustSpec(t, "on: p0.ping\nsteps:\n  - { id: fix, type: agent, prompt: p }\n")); p != nil {
		t.Fatal("a report without progress hooks")
	}
	var p *Progress // every method is nil-safe
	p.Queued("x", 1)
	p.agentNote("x")
	p.Final(ProgressWaiting, "x")
	p.Close()
}

// A step's progress: phrase renders over its scope; none, or one that does
// not render, leaves conductor's neutral words.
func TestProgressStepLabel(t *testing.T) {
	r := New(Runner{})
	data := map[string]any{"msg": "the flaky test"}
	if got := r.stepLabel(mustSpec(t, "on: x.y\nsteps:\n  - { id: a, type: agent, prompt: p, progress: \"fixing {{.msg}}\" }\n").Steps[0], data); got != "fixing the flaky test" {
		t.Fatalf("label = %q", got)
	}
	if got := r.stepLabel(mustSpec(t, "on: x.y\nsteps:\n  - { id: a, type: agent, prompt: p }\n").Steps[0], data); got != "" {
		t.Fatalf("unlabelled step = %q", got)
	}
	rig, _ := headRig(t, "pl", "aaaaaaa1111")
	p := rig.Runner.NewProgress(context.Background(), headTrigger("pl"), mustSpec(t, fmt.Sprintf(progressSpec, "pl")))
	defer p.Close()
	p.stepStart("fix", "", true)
	p.mu.Lock()
	text := p.reportLocked()["text"]
	p.mu.Unlock()
	if text != "agent working (step fix)" {
		t.Fatalf("neutral agent text = %v", text)
	}
}
