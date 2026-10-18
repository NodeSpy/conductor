package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// sweepID is a sweep worker's identity: a worker bound to a sweep, not a
// case.
func sweepID(sweep string) flow.SkillIdentity {
	return flow.SkillIdentity{Bot: "sre", BotRole: config.BotRoleWorker, Agent: "bot:sre:worker", Dispatch: "d-x", CaseID: sweep, Verbs: []string{"*"}}
}

func (f *gateFixture) addSweep(t *testing.T) string {
	t.Helper()
	var id string
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error {
		id = r.NextID("x")
		if r.Sweeps == nil {
			r.Sweeps = map[string]*store.Sweep{}
		}
		r.Sweeps[id] = &store.Sweep{ID: id, Status: "running", Started: f.now}
		if r.Workers == nil {
			r.Workers = map[string]*store.Worker{}
		}
		r.Workers["w-"+id] = &store.Worker{ID: "w-" + id, Sweep: id, Dispatch: "d-x", Status: "running", Parent: "conductor",
			Requester: "sweep " + id, RequesterClass: ReqProactive, ReadOnly: true}
		return nil
	})
	return id
}

// A sweep is read-only by the gate, not by its prompt: with the bot
// sandboxed, out of shadow mode and the verb promoted, only observe runs.
func TestSweepIsReadOnlyAtTheGate(t *testing.T) {
	f := newGateFixture(t, testBot(), true)
	x := f.addSweep(t)
	if v, err := check(t, f, sweepID(x), "pd.get_incident", nil); err != nil || !v.Allow {
		t.Fatalf("observe refused for a sweep: %+v %v", v, err)
	}
	// The same promoted verb runs for the bot itself …
	if v, _ := check(t, f, homeID(), "pd.ack", nil); !v.Allow {
		t.Fatal("fixture: promoted verb should run for the home session")
	}
	// … but a sweep's call becomes a proposal, attributed to the sweep.
	v, err := check(t, f, sweepID(x), "pd.ack", nil)
	if err != nil || v.Allow {
		t.Fatalf("a sweep ran a mutating verb: %+v %v", v, err)
	}
	p := f.st.Bot("sre").Proposals[v.Output["proposal"].(string)]
	if p.RequesterClass != ReqProactive || p.Requester != "sweep "+x || p.CaseID != "" || p.Status != "pending" {
		t.Fatalf("sweep proposal attribution: %+v", p)
	}
	if v, _ := check(t, f, sweepID(x), "gh.comment", map[string]any{"body": "x"}); v.Allow {
		t.Fatal("a sweep ran a propose-tier verb")
	}
	if _, err := check(t, f, sweepID(x), "aws.iam_attach", nil); err == nil {
		t.Fatal("never tier ran for a sweep")
	}
	// Each lock holds alone: the proactive class without the read-only flag.
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error { r.Workers["w-"+x].ReadOnly = false; return nil })
	if v, _ := check(t, f, sweepID(x), "pd.ack", nil); v.Allow {
		t.Fatal("the proactive class alone did not hold a sweep to observe")
	}
	// In shadow mode the sweep's write is a shadow proposal.
	m := testBot()
	m.Autonomy.Shadow = boolp(true)
	f.cfg.Bots["sre"] = m
	if v, _ := check(t, f, sweepID(x), "pd.ack", nil); v.Allow || v.Output["status"] != "shadow" {
		t.Fatalf("shadow sweep: %+v", v)
	}
}

// While a case a finding (or a peer) opened is open, the home session runs on
// that class's trust: nothing above observe runs unapproved.
func TestFindingAndPeerCasesCapTheHome(t *testing.T) {
	for _, class := range []string{ReqProactive, ReqPeer, "bot:other", "unknown-class"} {
		f := newGateFixture(t, testBot(), true)
		c := f.openCase(t, class)
		if v, _ := check(t, f, homeID(), "pd.ack", nil); v.Allow {
			t.Fatalf("%s: home ran a promoted verb while that case was open", class)
		}
		if v, _ := check(t, f, f.worker(t, c), "pd.ack", nil); v.Allow {
			t.Fatalf("%s: a worker on that case ran a promoted verb", class)
		}
		if v, _ := check(t, f, f.worker(t, c), "pd.get_incident", nil); !v.Allow {
			t.Fatalf("%s: observe refused", class)
		}
	}
}

// ---- the service side ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type sweepFixture struct {
	*svcFixture
	clk     *clock
	notices []Notice
	audits  []map[string]any
	logs    []string
	cost    float64
	usage   RunUsage // tokens / turns per finished sweep run (cost comes from cost)
	mu      sync.Mutex
}

func newSweepFixture(t *testing.T, p config.BotProactive) *sweepFixture {
	t.Helper()
	f := &sweepFixture{svcFixture: newSvc(t), clk: &clock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}}
	m := f.cfg.Bots["sre"]
	m.Charter = "Keep acme healthy.\n\n## Proactive checks\n\n- cost anomalies in the bill\n- alarms in ALARM\n\n## Never\n\n- nothing"
	if p.Checklist == (config.ProactiveChecklist{}) {
		p.Checklist.Section = "Proactive checks"
	}
	m.Proactive = &p
	f.cfg.Bots["sre"] = m
	f.svc.Now = f.clk.now
	f.svc.Notify = func(_ context.Context, n Notice) { f.mu.Lock(); f.notices = append(f.notices, n); f.mu.Unlock() }
	f.svc.Audit = func(e map[string]any) { f.mu.Lock(); f.audits = append(f.audits, e); f.mu.Unlock() }
	f.svc.Log = func(s string, a ...any) { f.mu.Lock(); f.logs = append(f.logs, fmt.Sprintf(s, a...)); f.mu.Unlock() }
	f.svc.RunUsage = func(string) (RunUsage, bool) {
		u := f.usage
		u.CostUSD = f.cost
		return u, true
	}
	return f
}

// sweepFired returns the sweep triggers fired so far.
func (f *sweepFixture) sweepFired() []fired {
	f.workers.mu.Lock()
	defer f.workers.mu.Unlock()
	var out []fired
	for _, x := range f.workers.fired {
		if x.trigger == config.BotSweepTrigger("sre") {
			out = append(out, x)
		}
	}
	return out
}

// finish reports a sweep's step.done and its run's end.
func (f *sweepFixture) finish(t *testing.T, x fired, output any) {
	t.Helper()
	_, w, ok := f.svc.findWorker("", x.history)
	if !ok {
		t.Fatalf("no worker for %s", x.history)
	}
	if !f.svc.WorkerDone(context.Background(), w.Dispatch, "", output) {
		t.Fatal("sweep worker done not handled")
	}
	f.svc.OnRunEvent(context.Background(), "run_done", x.history, "completed", "")
}

func (f *sweepFixture) unreadInbox() []string {
	var out []string
	for _, m := range f.st.Bot("sre").Inbox {
		if m.Delivered.IsZero() {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestSweepScheduleQuietSweepAndRunBudget(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), Budget: config.ProactiveBudget{MaxRunsPerDay: 2}})
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	fs := f.sweepFired()
	if len(fs) != 1 {
		t.Fatalf("first tick fired %d sweeps", len(fs))
	}
	x := fs[0]
	sid, _ := x.inputs[dispatch.BotContextCase].(string)
	sw := f.st.Bot("sre").Sweeps[sid]
	if sw == nil || sw.Status != "running" {
		t.Fatalf("the sweep binding is not a running sweep: %q %+v", sid, sw)
	}
	var sweepWorker *store.Worker
	for _, w := range f.st.Bot("sre").Workers {
		if w.Sweep == sid {
			sweepWorker = w
		}
	}
	if sweepWorker == nil || sweepWorker.RequesterClass != ReqProactive || !sweepWorker.ReadOnly || sweepWorker.Parent != "conductor" || sweepWorker.CaseID != "" {
		t.Fatalf("sweep worker record: %+v", sweepWorker)
	}
	f.svc.Gate.Sandbox = fixedJail(true)
	if v, _ := f.svc.Gate.CheckVerb(ctx, childID("sre", *sweepWorker), "pd.ack", nil); v.Allow {
		t.Fatal("a real sweep worker ran a promoted write")
	}
	if _, err := f.svc.Tell(ctx, homeCaller(f.svcFixture), sweepWorker.ID, "fix it"); err == nil {
		t.Fatal("the home instructed a sweep")
	}
	prompt, _ := x.inputs["prompt"].(string)
	for _, want := range []string{"cost anomalies in the bill", "READ-ONLY", "Notify threshold: high", "step.done"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("sweep prompt lacks %q", want)
		}
	}
	if !strings.Contains(prompt, "Your checklist (set by your operator)\n\n- cost anomalies in the bill\n- alarms in ALARM\n\n## Already tracked") {
		t.Error("the checklist is not exactly the charter section")
	}
	// Running: no overlap, even when due.
	f.clk.add(2 * time.Hour)
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 1 {
		t.Fatalf("a second sweep started while one ran (%d)", n)
	}
	if _, err := f.svc.StartSweep(ctx, "sre", true); !errors.Is(err, ErrSweepSkipped) {
		t.Fatalf("operator sweep overlapped a running one: %v", err)
	}
	// A quiet sweep: one ledger line, nothing reaches the operator.
	before := len(f.unreadInbox())
	f.finish(t, x, map[string]any{"summary": "all clear", "findings": []any{}})
	rec := f.st.Bot("sre")
	if sw := rec.Sweeps[sid]; sw.Status != "quiet" || sw.Summary != "all clear" {
		t.Fatalf("quiet sweep record: %+v", sw)
	}
	if n := len(f.unreadInbox()); n != before || len(f.notices) != 0 {
		t.Fatalf("a quiet sweep notified (inbox %d→%d, notices %d)", before, n, len(f.notices))
	}
	for _, c := range rec.Cases {
		if c.Source == "proactive" {
			t.Fatal("a quiet sweep opened a case")
		}
	}
	// Second sweep once due; then the run budget (2/24h) stops the third.
	f.clk.add(time.Hour)
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 2 {
		t.Fatalf("due sweep not started (%d)", n)
	}
	f.finish(t, f.sweepFired()[1], map[string]any{"summary": "fine"})
	f.clk.add(time.Hour)
	for i := 0; i < 3; i++ {
		f.svc.Tick(ctx, "sre")
	}
	if n := len(f.sweepFired()); n != 2 {
		t.Fatalf("budget did not stop the third sweep (%d)", n)
	}
	if _, err := f.svc.StartSweep(ctx, "sre", true); err == nil || !strings.Contains(err.Error(), "max_runs_per_day") {
		t.Fatalf("operator sweep past the budget: %v", err)
	}
	skips := 0
	for _, l := range f.logs {
		if strings.Contains(l, "sweep not started") && strings.Contains(l, "max_runs_per_day") {
			skips++
		}
	}
	if skips != 1 {
		t.Fatalf("a budget skip should be logged once, got %d", skips)
	}
	// The window rolls: a day later sweeps run again.
	f.clk.add(24 * time.Hour)
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 3 {
		t.Fatalf("sweeps did not resume after the window (%d)", n)
	}
}

func TestSweepCostBudget(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), Budget: config.ProactiveBudget{MaxCostPerDay: 2}})
	ctx := context.Background()
	f.cost = 1.0 // two sweeps reach the $2 cap exactly
	f.svc.Tick(ctx, "sre")
	f.finish(t, f.sweepFired()[0], map[string]any{"summary": "ok"})
	if c := f.svc.Sweeps("sre", 1)[0].CostUSD; c != 1.0 {
		t.Fatalf("sweep cost not recorded: %v", c)
	}
	// Finished, but not due: nothing starts before `every` has passed.
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 1 {
		t.Fatalf("a sweep started before it was due (%d)", n)
	}
	f.clk.add(time.Hour)
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 2 {
		t.Fatalf("under the cost cap, a sweep should run (%d)", n)
	}
	f.finish(t, f.sweepFired()[1], map[string]any{"summary": "ok"})
	f.clk.add(time.Hour)
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 2 {
		t.Fatalf("a sweep ran past max_cost_per_day (%d)", n)
	}
	if _, err := f.svc.StartSweep(ctx, "sre", true); err == nil || !strings.Contains(err.Error(), "max_cost_per_day") {
		t.Fatalf("cost budget: %v", err)
	}
}

func TestSweepQuietHours(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), QuietHours: "11:00-13:00", Timezone: "UTC",
		Budget: config.ProactiveBudget{MaxRunsPerDay: 5}})
	ctx := context.Background()
	f.svc.Tick(ctx, "sre") // 12:00 UTC: quiet
	if n := len(f.sweepFired()); n != 0 {
		t.Fatal("a sweep started in quiet hours")
	}
	// An operator's explicit request is not held by quiet hours.
	if _, err := f.svc.StartSweep(ctx, "sre", true); err != nil {
		t.Fatalf("operator sweep in quiet hours: %v", err)
	}
	f.finish(t, f.sweepFired()[0], map[string]any{"summary": "ok"})
	f.clk.add(90 * time.Minute) // 13:30
	f.svc.Tick(ctx, "sre")
	if n := len(f.sweepFired()); n != 2 {
		t.Fatalf("no sweep after quiet hours (%d)", n)
	}
}

func TestSweepFindingsThresholdDedupeAndEscalation(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), NotifyAt: "medium", Budget: config.ProactiveBudget{MaxRunsPerDay: 10}})
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	f.finish(t, f.sweepFired()[0], map[string]any{"summary": "two things", "findings": []any{
		map[string]any{"key": "cost:ec2", "title": "EC2 spend up 40% day over day", "severity": "high", "detail": "the bill shows it"},
		map[string]any{"key": "disk:db1", "title": "db1 disk 71%", "severity": "low"},
	}})
	rec := f.st.Bot("sre")
	var fc *store.Case
	for _, c := range rec.Cases {
		if c.Key == "cost:ec2" {
			fc = c
		}
		if c.Key == "disk:db1" {
			t.Fatal("a below-threshold finding opened a case")
		}
	}
	if fc == nil || fc.RequesterClass != ReqProactive || fc.Source != "proactive" || fc.Severity != "high" {
		t.Fatalf("finding case: %+v", fc)
	}
	if len(f.notices) != 1 || f.notices[0].CaseID != fc.ID || f.notices[0].Severity != "high" {
		t.Fatalf("notices: %+v", f.notices)
	}
	inbox := strings.Join(f.unreadInbox(), "\n")
	if !strings.Contains(inbox, "EC2 spend up 40%") || !strings.Contains(inbox, "untrusted DATA") || strings.Contains(inbox, "db1 disk") {
		t.Fatalf("home inbox: %s", inbox)
	}
	sw := f.svc.Sweeps("sre", 1)[0]
	if sw.Status != "findings" || len(sw.Findings) != 2 || sw.Findings[1].CaseID != "" || sw.Findings[1].Notified {
		t.Fatalf("sweep findings: %+v", sw)
	}
	day := f.clk.now()
	mem, err := os.ReadFile(filepath.Join(f.cfg.BotRepoDir("sre"), "log", "proactive", day.Format("2006/01"), day.Format("2006-01-02")+".md"))
	if err != nil || !strings.Contains(string(mem), "db1 disk 71%") || !strings.Contains(string(mem), "cost:ec2") {
		t.Fatalf("findings not in memory: %v %s", err, mem)
	}
	// The same key again: noted on the open case, no new case, no notice.
	f.clk.add(time.Hour)
	f.svc.Tick(ctx, "sre")
	f.finish(t, f.sweepFired()[1], map[string]any{"findings": []any{
		map[string]any{"key": "cost:ec2", "title": "EC2 spend still up", "severity": "high"},
	}})
	rec = f.st.Bot("sre")
	n := 0
	for _, c := range rec.Cases {
		if c.Key == "cost:ec2" {
			n++
		}
	}
	if n != 1 || len(f.notices) != 1 || !strings.Contains(rec.Cases[fc.ID].Summary, "saw it again") {
		t.Fatalf("dedupe: %d cases, %d notices, summary %q", n, len(f.notices), rec.Cases[fc.ID].Summary)
	}
	// Worse: an escalation past the threshold notifies once more.
	f.clk.add(time.Hour)
	f.svc.Tick(ctx, "sre")
	f.finish(t, f.sweepFired()[2], map[string]any{"findings": []any{
		map[string]any{"key": "cost:ec2", "title": "EC2 spend tripled", "severity": "critical"},
	}})
	if len(f.notices) != 2 || f.notices[1].Severity != "critical" || f.st.Bot("sre").Cases[fc.ID].Severity != "critical" {
		t.Fatalf("escalation: %+v", f.notices)
	}
}

func TestParseFindingsIsDefensive(t *testing.T) {
	var many []any
	for i := 0; i < 50; i++ {
		many = append(many, map[string]any{"title": "t", "severity": "BOGUS"})
	}
	raw, _ := json.Marshal(map[string]any{"summary": "s", "findings": append(many, map[string]any{})})
	sum, fs := parseFindings(raw)
	if sum != "s" || len(fs) != maxFindings || fs[0].Severity != "medium" || fs[0].Key == "" {
		t.Fatalf("parse: %q %d %+v", sum, len(fs), fs[0])
	}
	if _, fs := parseFindings(json.RawMessage(`"not an object"`)); fs != nil {
		t.Fatal("non-object output produced findings")
	}
}

// A restart marks a running sweep interrupted (and so frees the schedule).
func TestSweepInterruptedByRestart(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), Budget: config.ProactiveBudget{MaxRunsPerDay: 5}})
	f.svc.Tick(context.Background(), "sre")
	f.svc.recoverWorkers("sre")
	if sw := f.svc.Sweeps("sre", 1)[0]; sw.Status != "interrupted" {
		t.Fatalf("sweep after restart: %+v", sw)
	}
	for _, m := range f.unreadInbox() {
		if strings.Contains(m, "interrupted") {
			t.Fatal("an interrupted sweep reached the operator")
		}
	}
}
