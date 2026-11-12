package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/pkg/expr"
)

// RUN PROGRESS.
//
// A run reports what it is doing as it goes, under {{.progress.*}}, to its
// trigger's `at: progress` hooks. Core knows nothing about where a report
// goes (a commit status, a chat message): it keeps one report per run,
// updates it from what it sees, and fires the progress hooks with it —
// throttled, and only when what a hook would send changed.
//
// What updates the report:
//   - the wait for a slot before the run starts: queued, and why (engine);
//   - the run starting, each step starting (its `progress:` phrase, else a
//     neutral one), a fan-out's items finishing ("3/8 done"), a step
//     retrying, the end-of-step push;
//   - the agent itself, through the step.progress verb (a note: its own
//     words for what it is doing);
//   - the runtime, from the agent's stream (its tool calls), when the
//     runtime can see it (dispatch.Request.Activity).
//
// A progress hook fires at most once per its `every:` (default 30s), at
// once when the run's state changes (queued → running → waiting), and never
// when its rendered options are what it last sent; elapsed time is reported
// in whole minutes, so an unchanged report re-posts at most once a minute.
// The report stops before the run's done/fail/stop hooks fire, so a late
// progress update can never overwrite the run's outcome.
//
// The report's fields ({{.progress.<field>}}):
//
//	state     queued | running | retrying | waiting
//	text      the one-line summary: the latest of the fields below that
//	          applies (a fan-out's count wins while it runs; an agent's
//	          own note wins over its tool calls for noteFresh)
//	step      the step running (a nested workflow's step id, when in one)
//	source    lifecycle | agent | activity — where text came from
//	note      the agent's last step.progress note
//	activity  the last activity the runtime saw ("editing main.go")
//	elapsed   time since the run was queued or started: "<1m", "12m", "1h05m"
//	since     when that was (RFC 3339)
//	ahead     queued: runs ahead of this one for an agent slot (0 = next,
//	          -1 = not counted)
//	done, total   a fan-out step's finished and total items (0 when none)
//	attempt, attempts   a retrying step's attempt and its limit
//
// and {{.run.url}}: the run's page on the dashboard, when one is reachable.

// Progress states.
const (
	ProgressQueued   = "queued"
	ProgressRunning  = "running"
	ProgressRetrying = "retrying"
	ProgressWaiting  = "waiting"
)

// noteFresh is how long an agent's own note outranks the tool calls the
// runtime sees: the agent's words say more than "running go test".
const noteFresh = 2 * time.Minute

// progressTick is how often a run's report is re-checked for a due hook;
// a var so tests can shorten it.
var progressTick = 5 * time.Second

// progressNow is the report's clock; a var so tests can drive it.
var progressNow = time.Now

// progressHookTimeout bounds one progress hook call.
const progressHookTimeout = 30 * time.Second

// noteMax bounds an agent note or activity phrase, in runes.
const noteMax = 80

type progressKey struct{}

// WithProgress carries p into a run (Runner.Run picks it up), so a report
// begun while the run waited for a slot continues as it runs.
func WithProgress(ctx context.Context, p *Progress) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, p)
}

func progressFrom(ctx context.Context) *Progress {
	p, _ := ctx.Value(progressKey{}).(*Progress)
	return p
}

// Progress is one run's live report. Every method is safe on a nil
// *Progress (a run with no progress hooks has none).
type Progress struct {
	r     *Runner
	t     core.Trigger
	hooks []config.Hook
	now   func() time.Time

	mu        sync.Mutex
	ctx       context.Context // what hooks fire under (the run's, once it starts)
	base      map[string]any  // the hooks' scope, minus progress; nil = not yet known
	reading   bool            // Queued is reading the head for base
	historyID string
	state     string
	text      string
	source    string
	step      string
	note      string
	noteAt    time.Time
	activity  string
	ahead     int
	fanStep   string
	fanLabel  string
	fanDone   int
	fanTotal  int
	attempt   int
	attempts  int
	since     time.Time

	fireMu    sync.Mutex // held while hooks fire, and around the run's start hooks
	last      []string   // per hook: what it last sent
	lastAt    []time.Time
	lastState []string

	poke   chan struct{}
	stop   chan struct{}
	exited chan struct{}
	closed sync.Once
}

// NewProgress begins the progress report of a run of spec on t, or returns
// nil when spec has no progress hooks. Its hooks start firing once the
// report has a scope (Queued or the run's start); Close ends it.
func (r *Runner) NewProgress(ctx context.Context, t core.Trigger, spec config.TriggerSpec) *Progress {
	var hooks []config.Hook
	for _, h := range spec.Hooks {
		if h.At == "progress" {
			hooks = append(hooks, h)
		}
	}
	if len(hooks) == 0 {
		return nil
	}
	p := &Progress{
		r: r, t: t, hooks: hooks, now: progressNow,
		ctx:       context.WithoutCancel(ctx),
		last:      make([]string, len(hooks)),
		lastAt:    make([]time.Time, len(hooks)),
		lastState: make([]string, len(hooks)),
		poke:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		exited:    make(chan struct{}),
	}
	p.since = p.now()
	go p.loop(ctx)
	return p
}

// Queued reports the run waiting before it starts: reason says for what
// ("an agent slot"), ahead how many runs will be served first (-1 unknown).
// The first call reads the target's head (in the background: it never
// blocks the caller), so the hooks have a commit to report on before the
// run's own start reads it again.
func (p *Progress) Queued(reason string, ahead int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	needBase, ctx, t := p.base == nil && !p.reading, p.ctx, p.t
	p.reading = p.reading || needBase
	p.mu.Unlock()
	if needBase {
		// Off the caller's path: Queued is called as the slot queue moves.
		go func() {
			var f runFacts
			f.startSHA, f.state, f.stopWords = p.r.readHead(ctx, t)
			f.headSHA = f.startSHA
			base := withRun(p.r.progressBase(t, nil), f)
			p.update(func() {
				if p.base == nil {
					p.base = base
				}
			})
		}()
	}
	text := "queued — waiting for " + reason
	switch {
	case ahead == 0:
		text += " (next)"
	case ahead > 0:
		text += fmt.Sprintf(" (%d ahead)", ahead)
	}
	p.update(func() {
		p.state, p.text, p.source, p.ahead = ProgressQueued, text, "lifecycle", ahead
	})
}

// begin is the run starting: from here the hooks see the run's own scope and
// fire under its context.
func (p *Progress) begin(ctx context.Context, t core.Trigger, base map[string]any, historyID string) {
	if p == nil {
		return
	}
	p.update(func() {
		// t: the trigger that runs, which may be a newer one that coalesced
		// into the wait.
		p.t, p.ctx, p.base, p.historyID = t, context.WithoutCancel(ctx), base, historyID
		p.state, p.text, p.source, p.ahead = ProgressRunning, "starting", "lifecycle", 0
	})
}

// hold runs fn with no progress hook firing meanwhile (the run's start hooks,
// so a queued report never lands after them).
func (p *Progress) hold(fn func()) {
	if p == nil {
		fn()
		return
	}
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	fn()
}

// lifecycle reports what the run itself is doing ("pushing").
func (p *Progress) lifecycle(text string) {
	if p == nil {
		return
	}
	p.update(func() {
		p.state, p.text, p.source = ProgressRunning, text, "lifecycle"
		p.attempt, p.attempts = 0, 0
	})
}

// stepStart reports step id starting, as label ("" = a neutral phrase).
func (p *Progress) stepStart(id, label string, agent bool) {
	if p == nil {
		return
	}
	if label == "" {
		label = "running step " + id
		if agent {
			label = "agent working (step " + id + ")"
		}
	}
	p.update(func() {
		p.state, p.text, p.source, p.step = ProgressRunning, label, "lifecycle", id
		p.attempt, p.attempts = 0, 0
	})
}

// fanOut reports step id fanning out to total items; label names them.
func (p *Progress) fanOut(id, label string, total int) {
	if p == nil || total == 0 {
		return
	}
	if label == "" {
		label = id
	}
	p.update(func() { p.fanStep, p.fanLabel, p.fanDone, p.fanTotal = id, label, 0, total })
}

// fanItemDone counts one of step id's items finished.
func (p *Progress) fanItemDone(id string) {
	if p == nil {
		return
	}
	p.update(func() {
		if p.fanStep == id && p.fanDone < p.fanTotal {
			p.fanDone++
		}
	})
}

// fanEnd ends step id's fan-out.
func (p *Progress) fanEnd(id string) {
	if p == nil {
		return
	}
	p.update(func() {
		if p.fanStep == id {
			p.fanStep, p.fanLabel, p.fanDone, p.fanTotal = "", "", 0, 0
		}
	})
}

// retrying reports step id's next attempt (1-based) of attempts.
func (p *Progress) retrying(id string, attempt, attempts int) {
	if p == nil {
		return
	}
	p.update(func() {
		p.state, p.step, p.source = ProgressRetrying, id, "lifecycle"
		p.attempt, p.attempts = attempt, attempts
		p.text = fmt.Sprintf("retrying %s (attempt %d of %d)", id, attempt, attempts)
	})
}

// agentNote is the agent's own word on what it is doing (step.progress).
func (p *Progress) agentNote(text string) {
	if p == nil || text == "" {
		return
	}
	p.update(func() {
		p.note, p.noteAt = text, p.now()
		if p.state == ProgressRetrying {
			p.state = ProgressRunning
		}
		p.text, p.source = text, "agent"
	})
}

// agentActivity is what the runtime saw the agent do (a tool call).
func (p *Progress) agentActivity(text string) {
	if p == nil || text == "" {
		return
	}
	p.update(func() {
		p.activity = text
		if p.source == "agent" && p.now().Sub(p.noteAt) < noteFresh {
			return
		}
		if p.state == ProgressRetrying {
			p.state = ProgressRunning
		}
		p.text, p.source = text, "activity"
	})
}

// Final posts one last report — state and text — after the report was
// closed (or closes it first), at once and whatever the pacing: a run that
// ends without an outcome hook to say so (it waits on its runtime, say)
// leaves this as what its progress hooks last said.
func (p *Progress) Final(state, text string) {
	if p == nil {
		return
	}
	p.Close()
	p.mu.Lock()
	p.state, p.text, p.source = state, text, "lifecycle"
	p.fanStep, p.fanTotal, p.fanDone = "", 0, 0
	p.mu.Unlock()
	p.flush(true)
}

// Close ends the report: no progress hook fires after it returns (except
// through Final). Idempotent.
func (p *Progress) Close() {
	if p == nil {
		return
	}
	p.closed.Do(func() { close(p.stop) })
	<-p.exited
}

// update applies fn under the lock and wakes the hook loop.
func (p *Progress) update(fn func()) {
	p.mu.Lock()
	fn()
	p.mu.Unlock()
	select {
	case p.poke <- struct{}{}:
	default:
	}
}

func (p *Progress) loop(ctx context.Context) {
	defer close(p.exited)
	tick := time.NewTicker(progressTick)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ctx.Done():
			return
		case <-p.poke:
		case <-tick.C:
		}
		select {
		case <-p.stop:
			return
		default:
		}
		p.flush(false)
	}
}

// flush fires every progress hook that is due and would send something new.
// force ignores the pacing (Final).
func (p *Progress) flush(force bool) {
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	p.mu.Lock()
	if p.base == nil {
		p.mu.Unlock()
		return
	}
	data := p.scopeLocked()
	state, ctx, t := p.state, p.ctx, p.t
	p.mu.Unlock()
	now := p.now()
	for i, h := range p.hooks {
		due := force || p.lastAt[i].IsZero() || p.lastState[i] != state || now.Sub(p.lastAt[i]) >= h.ProgressEvery()
		if !due {
			continue
		}
		key, ok := p.rendered(h, data)
		if !ok || key == p.last[i] {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, progressHookTimeout)
		p.r.runHooks(hctx, t, []config.Hook{h}, "progress", data, "workflow")
		cancel()
		p.last[i], p.lastAt[i], p.lastState[i] = key, now, state
	}
}

// rendered is what h would send now, as a comparable string; false when its
// condition is off (or does not evaluate — runHooks logs that when it fires).
func (p *Progress) rendered(h config.Hook, data map[string]any) (string, bool) {
	if h.If != "" {
		if ok, err := expr.Eval(h.If, data); err != nil || !ok {
			return "", false
		}
	}
	opts := h.Options
	if p.r.Conns != nil {
		conn, _, _ := strings.Cut(h.Uses, ".")
		if in, ok := p.r.Conns.Get(conn); ok {
			opts = connector.MergeOptions(in.DefaultOptions, h.Options)
		}
	}
	out, err := renderOptions(opts, data)
	if err != nil {
		return "render error: " + err.Error(), true
	}
	b, _ := json.Marshal(out)
	return h.Uses + " " + string(b), true
}

// scopeLocked is the progress hooks' scope: the run's, {{.run.url}}, and the
// report. p.mu held.
func (p *Progress) scopeLocked() map[string]any {
	d := cloneData(p.base)
	run := map[string]any{}
	if m, ok := d["run"].(map[string]any); ok {
		for k, v := range m {
			run[k] = v
		}
	}
	run["url"] = p.r.runURL(p.historyID)
	d["run"] = run
	d["progress"] = p.reportLocked()
	return hookData(d, "progress", p.state, "", "", nil)
}

// reportLocked is {{.progress}}. p.mu held.
func (p *Progress) reportLocked() map[string]any {
	text := p.text
	if p.fanTotal > 0 && p.state != ProgressQueued && p.state != ProgressWaiting {
		text = fmt.Sprintf("%s: %d/%d done", p.fanLabel, p.fanDone, p.fanTotal)
	}
	return map[string]any{
		"state":    p.state,
		"text":     text,
		"step":     p.step,
		"source":   p.source,
		"note":     p.note,
		"activity": p.activity,
		"elapsed":  fmtElapsed(p.now().Sub(p.since)),
		"since":    p.since.UTC().Format(time.RFC3339),
		"ahead":    p.ahead,
		"done":     p.fanDone,
		"total":    p.fanTotal,
		"attempt":  p.attempt,
		"attempts": p.attempts,
	}
}

// runURL is a recorded run's dashboard page by its history id; "" when the
// run is not recorded or no dashboard is linkable.
func (r *Runner) runURL(historyID string) string {
	if r.RunURL == nil || historyID == "" {
		return ""
	}
	return r.RunURL(historyID)
}

// fmtElapsed is a duration in whole minutes: "<1m", "12m", "1h05m".
func fmtElapsed(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

// progressBase is the progress hooks' scope before the run facts: what a
// start hook sees (the trigger's context, secrets, vaults, the batch). Built
// afresh, never the run's own scope, which its steps write as they go.
func (r *Runner) progressBase(t core.Trigger, batch *Batch) map[string]any {
	data := baseData(t, r.SecretVals)
	addVaultData(data, r.VaultVals)
	if batch != nil {
		data["group"] = groupData(batch, r.SecretVals)
	}
	return data
}

// stepLabel renders step's `progress:` phrase over its scope; "" when it has
// none or it does not render.
func (r *Runner) stepLabel(step config.Step, data map[string]any) string {
	if step.Progress == "" {
		return ""
	}
	s, err := render(step.Progress, data)
	if err != nil {
		return ""
	}
	return r.cleanPhrase(s)
}

// cleanPhrase makes text fit a one-line report: control and invisible
// format characters dropped, whitespace collapsed, tracked secrets
// redacted, at most noteMax runes.
func (r *Runner) cleanPhrase(text string) string {
	text = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return ' '
		}
		return c
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if r.Secrets != nil {
		text = r.Secrets.Redact(text)
	}
	if rs := []rune(text); len(rs) > noteMax {
		text = string(rs[:noteMax-1]) + "…"
	}
	return text
}

// tapProgress routes the step.progress notes of dispatch id to p until the
// returned func is called.
func (r *Runner) tapProgress(id string, p *Progress) func() {
	if p == nil || id == "" || r.progressTap == nil {
		return func() {}
	}
	r.progressTap.Store(id, p)
	return func() { r.progressTap.CompareAndDelete(id, p) }
}

// ReportProgress hands an agent's step.progress note to the run its dispatch
// belongs to (connector.StepOps.Progress). false: no run is reporting that
// dispatch's progress.
func (r *Runner) ReportProgress(dispatchID, text string) bool {
	if r.progressTap == nil {
		return false
	}
	v, ok := r.progressTap.Load(dispatchID)
	if !ok {
		return false
	}
	text = r.cleanPhrase(text)
	if text == "" {
		return false
	}
	v.(*Progress).agentNote(text)
	return true
}
