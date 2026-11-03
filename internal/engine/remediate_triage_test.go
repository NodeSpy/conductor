package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The fixture forge's failing_checks declares remediate_triage the way the
// github plugin does: skip_when "stale || conclusion == 'cancelled'", infra
// when "runner_lost" with budget 2. These tests drive the engine with the
// status read's answers; the plugin's own tests check that its get_run
// returns them.

// triageVerbs fakes the remediation verbs: get_run answers runs[run_id], and
// rerun_run records the run it re-ran.
func triageVerbs(runs map[int64]map[string]any, reran *[]int64) func(context.Context, string, string, map[string]any) (map[string]any, error) {
	return func(_ context.Context, _, verb string, opts map[string]any) (map[string]any, error) {
		switch verb {
		case "read_token", "write_token":
			return map[string]any{"token": "t"}, nil
		case "get_run":
			out, ok := runs[toInt64(opts["run_id"])]
			if !ok {
				return nil, fmt.Errorf("no run %v", opts["run_id"])
			}
			return out, nil
		case "rerun_run":
			*reran = append(*reran, toInt64(opts["run_id"]))
			return map[string]any{"ok": true}, nil
		}
		return nil, errors.New("unexpected verb " + verb)
	}
}

func flakyAct(max int) config.Action {
	return config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true, Max: max}}
}

func failingRun(head string, run int64, act config.Action) core.Trigger {
	tr := agentTrigger("failing_checks", "a/w", 8, head, "fail@"+head, act)
	// As decoded off the wire: a JSON number.
	tr.Context["run_id"] = float64(run)
	return tr
}

func TestRemediateTriageSkipsWithoutSpendingBudget(t *testing.T) {
	for name, run := range map[string]map[string]any{
		"stale":     {"status": "completed", "conclusion": "failure", "stale": true},
		"cancelled": {"status": "completed", "conclusion": "cancelled", "stale": false},
	} {
		t.Run(name, func(t *testing.T) {
			d, n := &fakeDispatcher{}, &fakeNotifier{}
			e, st := newEng(t, baseCfg(), d, n, nil)
			var reran []int64
			e.invokeVerb = triageVerbs(map[int64]map[string]any{555: run}, &reran)
			tr := failingRun("h", 555, flakyAct(1))
			// Every failing job of the run re-triggers: each is skipped.
			for range 3 {
				e.process(context.Background(), tr)
			}
			if len(reran) != 0 || len(d.reqs) != 0 {
				t.Fatalf("a %s run: want no remedy and no fixer, got reran=%v dispatched=%d", name, reran, len(d.reqs))
			}
			if a := st.Attempts(tr.Key(), "failing_checks_rerun", "h"); a != 0 {
				t.Fatalf("a %s run spent %d of the flaky budget", name, a)
			}
		})
	}
}

func TestRemediateTriageInfraRetryHasItsOwnBudget(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	var reran []int64
	runs := map[int64]map[string]any{555: {"status": "completed", "conclusion": "failure", "runner_lost": true}}
	e.invokeVerb = triageVerbs(runs, &reran)
	tr := failingRun("h", 555, flakyAct(1))

	// The runner went away twice: two re-runs on the infra budget (2).
	e.process(context.Background(), tr)
	e.process(context.Background(), tr)
	if len(reran) != 2 || len(d.reqs) != 0 {
		t.Fatalf("runner loss: want two re-runs and no fixer, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	if a := st.Attempts(tr.Key(), "failing_checks_rerun", "h"); a != 0 {
		t.Fatalf("an infra re-run spent %d of the flaky budget", a)
	}
	// A third loss is past the infra cap: an ordinary failure now, so the
	// flaky budget (1) re-runs it once, then the fixer is dispatched. A
	// runner that never comes back cannot loop.
	e.process(context.Background(), tr)
	if len(reran) != 3 || len(d.reqs) != 0 {
		t.Fatalf("past the infra cap: want the flaky re-run, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	e.process(context.Background(), tr)
	if len(reran) != 3 || len(d.reqs) != 1 {
		t.Fatalf("every budget spent: want the fixer, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
}

// The incident: a fix pushed revision B while A's run was still going, and
// the workflow's concurrency group cancels the older run when a newer one
// starts. A's remediation then re-ran A (stale), which cancelled B's run;
// B's remediation re-ran B, spending its one flaky re-run on a cancellation
// conductor caused; B's re-run lost its runner and nothing re-ran it.
func TestRemediateTriageIncidentSequence(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	var reran []int64
	var logs []string
	e.log = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	const runA, runB = 41660080003, 41661826003
	runs := map[int64]map[string]any{
		runA: {"status": "in_progress", "conclusion": "", "stale": false},
	}
	e.invokeVerb = triageVerbs(runs, &reran)
	trA, trB := failingRun("hA", runA, flakyAct(0)), failingRun("hB", runB, flakyAct(0))

	// A job of A's run fails while the run is still going: wait.
	e.process(context.Background(), trA)
	// B is pushed; its run cancels A's (A is now stale AND cancelled).
	runs[runA] = map[string]any{"status": "completed", "conclusion": "cancelled", "stale": true}
	e.process(context.Background(), trA)
	if len(reran) != 0 || len(d.reqs) != 0 {
		t.Fatalf("stale run A: want no re-run and no fixer, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	// Even a stale run that FAILED (not cancelled) is left alone.
	runs[runA] = map[string]any{"status": "completed", "conclusion": "failure", "stale": true}
	e.process(context.Background(), trA)
	// Had something cancelled B's run, that is no failure of B's either.
	runs[runB] = map[string]any{"status": "completed", "conclusion": "cancelled", "stale": false}
	e.process(context.Background(), trB)
	if len(reran) != 0 || len(d.reqs) != 0 {
		t.Fatalf("cancelled runs: want no re-run and no fixer, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	for _, h := range []string{"hA", "hB"} {
		if a := st.Attempts(trA.Key(), "failing_checks_rerun", h); a != 0 {
			t.Fatalf("revision %s: %d flaky re-run(s) spent on a cancellation", h, a)
		}
	}
	// B's run then loses its runner: re-run, on the infra budget.
	runs[runB] = map[string]any{"status": "completed", "conclusion": "failure", "stale": false, "runner_lost": true}
	e.process(context.Background(), trB)
	if len(reran) != 1 || reran[0] != runB || len(d.reqs) != 0 {
		t.Fatalf("runner loss on B: want B re-run, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	// B's flaky re-run is still there for a genuine flake.
	runs[runB] = map[string]any{"status": "completed", "conclusion": "failure", "stale": false}
	e.process(context.Background(), trB)
	if len(reran) != 2 || len(d.reqs) != 0 {
		t.Fatalf("a flake on B: want its flaky re-run, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
	all := strings.Join(logs, "\n")
	if strings.Contains(all, "e+10") || !strings.Contains(all, "run 41661826003") {
		t.Fatalf("run ids must log as integers:\n%s", all)
	}
}

// Without a status read to triage (the read failed), the run is remedied as
// an ordinary failure, as before the triage existed.
func TestRemediateTriageNeedsAStatusRead(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	var reran []int64
	e.invokeVerb = triageVerbs(map[int64]map[string]any{}, &reran) // get_run errors
	e.process(context.Background(), failingRun("h", 555, flakyAct(1)))
	if len(reran) != 1 || len(d.reqs) != 0 {
		t.Fatalf("want the flaky re-run, got reran=%v dispatched=%d", reran, len(d.reqs))
	}
}
