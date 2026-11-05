package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// staleEngine is a flow engine over a real store at dir — a "restart" is a
// second call on the same dir.
func staleEngine(t *testing.T, st *store.Store) *Engine {
	t.Helper()
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(retryCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	return New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif, Flow: runner, Connectors: reg})
}

// A history record a dead process left "running" (it was killed mid-run)
// is recorded interrupted when the next daemon starts, and no longer blocks
// `conductor runs retry`. A run this process started is never touched.
func TestStaleRunningHistoryIsInterruptedAndRetryable(t *testing.T) {
	st := tempStore(t)
	stale := recordedRun(t)
	stale.ID, stale.Status, stale.Error, stale.FailedStep = "rstale", "running", "", ""
	stale.Steps = stale.Steps[:1]
	_ = st.PutHistory(stale)

	eng := staleEngine(t, st) // the restarted daemon
	if _, err := eng.RetryRunByID(context.Background(), "rstale", "second", false); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("before the boot sweep the record reads running: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	live := recordedRun(t)
	live.ID, live.Status, live.Started = "rlive", "running", time.Now()
	_ = st.PutHistory(live)

	eng.ResumeWorkflows(context.Background())

	got, _ := st.GetHistory("rstale")
	if got.Status != "interrupted" || got.Finished.IsZero() {
		t.Fatalf("stale record: status %q finished %v", got.Status, got.Finished)
	}
	if _, err := st.GetHistoryVerified("rstale"); err != nil {
		t.Fatalf("the rewritten record still verifies: %v", err)
	}
	if l, _ := st.GetHistory("rlive"); l.Status != "running" {
		t.Fatalf("a run this process started is left alone: %q", l.Status)
	}
	resetGateCalls()
	// No --from: it continues at the first step the record does not show
	// done ("second", never reached), with "first"'s output pinned.
	if _, err := eng.RetryRunByID(context.Background(), "rstale", "", false); err != nil {
		t.Fatalf("an interrupted run is retryable: %v", err)
	}
	if calls := waitGateCalls(t, 1); len(calls) != 1 || calls[0]["text"] != "got pinned-42" {
		t.Fatalf("continued at the unreached step, inputs pinned: %+v", calls)
	}
}

// While a run on the same target and event is in flight (the interrupted
// run's own resume, say), a retry would put a second agent on it: refused,
// saying why.
func TestRetryRefusedWhileTheSameRunIsInFlight(t *testing.T) {
	st := tempStore(t)
	rec := recordedRun(t)
	rec.ID, rec.Status = "rint", "interrupted"
	_ = st.PutHistory(rec)
	_ = st.PutRun(store.WorkflowRun{ID: rec.RunID, Kind: rec.Kind, Repo: rec.Repo, Number: rec.Number})
	eng := staleEngine(t, st)
	_, err := eng.RetryRunByID(context.Background(), "rint", "second", false)
	if err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("retry over an in-flight run: %v", err)
	}
	_ = st.DeleteRun(rec.RunID)
	resetGateCalls()
	if _, err := eng.RetryRunByID(context.Background(), "rint", "second", false); err != nil {
		t.Fatalf("once it ended: %v", err)
	}
	waitGateCalls(t, 1)
}
