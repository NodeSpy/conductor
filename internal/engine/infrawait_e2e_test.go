package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// fakeBrokenModelTool is a scripted agent CLI whose model is unreachable for
// its first FAILS launches: each prints the runtime's own error line in place
// of an answer — exactly what a broken TLS path to the API looks like — and
// exits 0. From then on it answers. Every launch's prompt is logged.
const fakeBrokenModelTool = `#!/bin/sh
sid=""
while [ $# -gt 0 ]; do
  case "$1" in --session-id|--resume) sid="$2"; shift ;; esac
  shift
done
prompt=$(cat)
n=$(cat "STATE.n" 2>/dev/null || echo 0)
n=$((n+1)); echo "$n" > "STATE.n"
printf '=== launch %s\n%s\n' "$n" "$prompt" >> "STATE.prompts"
fails=$(cat "STATE.fails" 2>/dev/null || echo 0)
if [ "$n" -le "$fails" ]; then
  printf '{"session_id":"%s","result":"API Error: Unable to connect to API: SSL certificate verification failed"}\n' "$sid"
  exit 0
fi
case "$prompt" in *"connectivity check"*)
  printf '{"session_id":"%s","result":"OK"}\n' "$sid"; exit 0 ;;
esac
printf '{"session_id":"%s","result":"{\\"verdict\\":\\"looks good\\"}"}\n' "$sid"
`

// infraKind runs a pinned verb step, then a schema-carrying review step on the
// cli runtime — the shape of a review workflow.
var infraKind = resumeKind{
	name: "cli", runtime: "cc",
	step: `{ id: ack, uses: eg.post, options: { text: "started" } }
      - { id: review, type: agent, name: reviewer, prompt: "review the change", runtime: cc, checkout: none,
          output_schema: { type: object, required: [verdict], properties: { verdict: { type: string } } } }`,
}

type infraRig struct {
	rig   resumeRig
	state string // the fake's state prefix
}

func newInfraRig(t *testing.T, fails int) infraRig {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	root := t.TempDir()
	t.Cleanup(func() { settle(root) })
	rig := resumeRig{kind: infraKind, root: root, stateDir: filepath.Join(root, "state"), origin: filepath.Join(root, "origin")}
	t.Setenv("HOME", filepath.Join(root, "home"))
	state := filepath.Join(root, "STATE")
	rig.cli = filepath.Join(root, "claude")
	if err := os.WriteFile(rig.cli, []byte(strings.ReplaceAll(fakeBrokenModelTool, `"STATE`, `"`+state)), 0o700); err != nil {
		t.Fatal(err)
	}
	ir := infraRig{rig: rig, state: state}
	ir.setFails(t, fails)
	return ir
}

func (ir infraRig) setFails(t *testing.T, n int) {
	t.Helper()
	if err := os.WriteFile(ir.state+".fails", []byte(strconv.Itoa(n)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (ir infraRig) prompts() string { b, _ := os.ReadFile(ir.state + ".prompts"); return string(b) }

// fastInfra shrinks the recovery's clocks for a test.
func fastInfra(t *testing.T, escalateAfter time.Duration) {
	t.Helper()
	pb, ea := infraProbeBackoff, infraEscalateAfter
	infraProbeBackoff, infraEscalateAfter = []time.Duration{40 * time.Millisecond}, escalateAfter
	t.Cleanup(func() { infraProbeBackoff, infraEscalateAfter = pb, ea })
}

func infraTrigger() core.Trigger {
	trig := flowTrigger("evt-infra")
	trig.TargetTrusted = true
	trig.Target = core.Target{Repo: "acme/web", Number: 7, BaseRef: "main", HeadSHA: "abc123"}
	return trig
}

func waitingRun(st *store.Store) (store.WorkflowRun, bool) {
	for _, r := range st.PendingRuns() {
		if r.InfraWait != nil {
			return r, true
		}
	}
	return store.WorkflowRun{}, false
}

func histories(st *store.Store) []store.RunHistory {
	return st.ListHistory(20)
}

func escalations(n *fakeNotif) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, e := range n.events {
		if strings.HasPrefix(e, "escalate:") || strings.HasPrefix(e, "failed:") {
			out = append(out, e)
		}
	}
	return out
}

// The incident, end to end: the runtime prints an API error instead of an
// answer. The run is not failed — it waits at the review step, keeping the
// runtime's reply in its record; the corrective turn is not spent on it; the
// runtime is probed; once it answers the run resumes from the review step
// (the step before it is NOT re-run) and completes. The outage is reported
// exactly once.
func TestInfraFailureWaitsThenResumesAndCompletes(t *testing.T) {
	fastInfra(t, 0) // report at the first failed probe — and only then
	ir := newInfraRig(t, 3)
	d := startDaemon(t, ir.rig)
	defer d.st.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d.eng.baseCtx = ctx
	notif := d.eng.notif.(*fakeNotif)

	before := gateCalls()
	d.eng.process(ctx, infraTrigger())
	waitLong(t, "the run to finish", 60*time.Second, func() bool {
		return d.logs.has("resuming the run that waited") && len(d.st.PendingRuns()) == 0
	})
	waitLong(t, "the resumed execution to record", 10*time.Second, func() bool {
		for _, h := range histories(d.st) {
			if h.Status == "ok" {
				return true
			}
		}
		return false
	})

	if got := gateCalls() - before; got != 1 {
		t.Fatalf("the pinned step before the failed one ran %d times, want 1", got)
	}
	if strings.Contains(ir.prompts(), "Your previous reply was not valid JSON") {
		t.Fatalf("the corrective turn was spent on the runtime's error:\n%s", ir.prompts())
	}
	if d.logs.has("workflow failed") {
		t.Fatalf("the run failed instead of waiting: %v", d.logs.lines)
	}
	var waited, ok store.RunHistory
	for _, h := range histories(d.st) {
		switch h.Status {
		case "waiting":
			waited = h
		case "ok":
			ok = h
		}
	}
	if waited.ID == "" || waited.FailedStep != "review" ||
		!strings.Contains(waited.Error, "SSL certificate verification failed") {
		t.Fatalf("the waiting record does not say why: %+v", waited)
	}
	if ok.RetryOf != waited.ID {
		t.Fatalf("the resumed execution does not link back to the wait: ok=%+v waiting=%s", ok, waited.ID)
	}
	if s, found := ok.Step("review"); !found || s.Status != "ok" || s.Outputs["verdict"] != "looks good" {
		t.Fatalf("resumed review step = %+v", s)
	}
	if es := escalations(notif); len(es) != 1 || !strings.Contains(es[0], "agents can't reach the model") ||
		!strings.Contains(es[0], "will resume automatically") {
		t.Fatalf("want exactly one outage report, got %q", es)
	}
	// The failed attempt's agent was released: the target is not held live
	// by a dead session, so later level events are not skipped forever.
	act := config.Action{FlowRef: "0:eg.ping"}
	waitLong(t, "the target to be free after the run completed", 10*time.Second, func() bool {
		return !d.eng.liveFor(ctx, infraTrigger(), act, infraTrigger().Key())
	})
}

// A restart in the middle of the wait: the waiting run is a record, so the
// next daemon life probes and resumes it. An outage reported before the
// restart is not reported again.
func TestInfraWaitSurvivesRestart(t *testing.T) {
	fastInfra(t, 0)
	ir := newInfraRig(t, 1000)
	d1 := startDaemon(t, ir.rig)
	ctx1, stop1 := context.WithCancel(context.Background())
	d1.eng.baseCtx = ctx1
	d1.eng.process(ctx1, infraTrigger())
	waitLong(t, "the outage report", 60*time.Second, func() bool { return len(escalations(d1.eng.notif.(*fakeNotif))) == 1 })
	w, found := waitingRun(d1.st)
	if !found {
		t.Fatal("no waiting run recorded")
	}
	stop1()
	time.Sleep(100 * time.Millisecond)
	d1.st.Close()

	// The runtime is fixed while the daemon is down.
	ir.setFails(t, 0)
	d2 := startDaemon(t, ir.rig)
	defer d2.st.Close()
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	d2.eng.baseCtx = ctx2
	d2.eng.ResumeWorkflows(ctx2)
	if !d2.eng.infraWaitingFor(infraTrigger(), config.Action{FlowRef: "0:eg.ping"}) {
		t.Fatal("the restarted daemon does not hold the waiting run's target live")
	}
	waitLong(t, "the run to resume and finish", 60*time.Second, func() bool {
		return d2.logs.has("resuming the run that waited") && len(d2.st.PendingRuns()) == 0
	})
	waitLong(t, "the resumed execution to record", 10*time.Second, func() bool {
		for _, h := range histories(d2.st) {
			if h.Status == "ok" && h.RunID == w.ID {
				return true
			}
		}
		return false
	})
	if es := escalations(d2.eng.notif.(*fakeNotif)); len(es) != 0 {
		t.Fatalf("the restart reported the outage again / failed: %q", es)
	}
}

// A waiting run whose target closes (merged) is dropped, not resumed: no
// further agent turn runs for it, its record is released, its execution is
// recorded stopped, and the target is no longer held live.
func TestInfraWaitDroppedWhenTargetCloses(t *testing.T) {
	fastInfra(t, time.Hour)
	ir := newInfraRig(t, 1000)
	d := startDaemon(t, ir.rig)
	defer d.st.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d.eng.baseCtx = ctx
	trig := infraTrigger()
	act := config.Action{FlowRef: "0:eg.ping"}
	d.eng.process(ctx, trig)
	waitLong(t, "the run to wait", 60*time.Second, func() bool { _, ok := waitingRun(d.st); return ok })
	if !d.eng.liveFor(ctx, trig, act, trig.Key()) {
		t.Fatal("a waiting run does not hold its target live: a sweep would start a duplicate")
	}
	if d.eng.hasLiveAgentFor(ctx, act, trig.Key(), trig.Kind) {
		t.Fatal("the failed attempt's dead session still holds the target live")
	}

	d.eng.markClosed(trig.Key())
	waitLong(t, "the run to be dropped", 60*time.Second, func() bool { return len(d.st.PendingRuns()) == 0 })
	if !d.logs.has("dropping the run waiting for its runtime — its target closed") {
		t.Fatalf("no drop log: %v", d.logs.lines)
	}
	if d.logs.has("resuming the run that waited") {
		t.Fatal("a run on a closed target was resumed")
	}
	var stopped bool
	for _, h := range histories(d.st) {
		stopped = stopped || h.Status == "stopped"
		if h.Status == "waiting" {
			t.Fatalf("history still says waiting: %+v", h)
		}
	}
	if !stopped {
		t.Fatalf("the dropped execution is not recorded stopped: %+v", histories(d.st))
	}
	waitLong(t, "the dropped run to free its target", 10*time.Second, func() bool {
		return !d.eng.liveFor(ctx, trig, act, trig.Key())
	})
	if es := escalations(d.eng.notif.(*fakeNotif)); len(es) != 0 {
		t.Fatalf("a dropped run was reported: %q", es)
	}
}

// A failure that only looks like the runtime's — the probe answers, the step
// keeps failing the same way — is bounded: after MaxInfraResumes resumes the
// run fails like any other.
func TestInfraWaitIsBounded(t *testing.T) {
	fastInfra(t, time.Hour)
	ir := newInfraRig(t, 0)
	// Only the review step's prompt fails; the probe answers.
	script := strings.ReplaceAll(fakeBrokenModelTool, `if [ "$n" -le "$fails" ]; then`,
		`case "$prompt" in *"connectivity check"*) fails=0 ;; esac
if [ "$n" -le "$fails" ] || [ "$fails" = "-1" ]; then`)
	if err := os.WriteFile(ir.rig.cli, []byte(strings.ReplaceAll(script, `"STATE`, `"`+ir.state)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ir.state+".fails", []byte("-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, ir.rig)
	defer d.st.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d.eng.baseCtx = ctx
	d.eng.process(ctx, infraTrigger())
	waitLong(t, "the run to fail for good", 120*time.Second, func() bool { return d.logs.has("workflow failed") })
	waitLong(t, "the record to go", 10*time.Second, func() bool { return len(d.st.PendingRuns()) == 0 })
	if n := strings.Count(strings.Join(d.logs.lines, "\n"), "resuming the run that waited"); n != 5 {
		t.Fatalf("resumed %d times, want 5", n)
	}
}

// An operator can stop a waiting run (`conductor runs stop` on its waiting
// record): it is dropped like one whose target went away.
func TestStopDropsAWaitingRun(t *testing.T) {
	fastInfra(t, time.Hour)
	infraProbeBackoff = []time.Duration{time.Hour} // nothing resumes it meanwhile
	ir := newInfraRig(t, 1000)
	d := startDaemon(t, ir.rig)
	defer d.st.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d.eng.baseCtx = ctx
	d.eng.process(ctx, infraTrigger())
	waitLong(t, "the run to wait", 60*time.Second, func() bool {
		return d.eng.infraWaitingFor(infraTrigger(), config.Action{FlowRef: "0:eg.ping"})
	})
	w, _ := waitingRun(d.st)
	if err := d.eng.StopRunByID(w.InfraWait.History); err != nil {
		t.Fatalf("stop a waiting run: %v", err)
	}
	if len(d.st.PendingRuns()) != 0 {
		t.Fatalf("the stopped run is still recorded: %+v", d.st.PendingRuns())
	}
	if rec, ok := d.st.GetHistory(w.InfraWait.History); !ok || rec.Status != "stopped" {
		t.Fatalf("history = %+v", rec)
	}
	if err := d.eng.StopRunByID(w.InfraWait.History); err == nil {
		t.Fatal("a second stop found the run again")
	}
}
