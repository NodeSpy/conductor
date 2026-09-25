package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// Observe mode: a decide step with `observe: <sql store>` records every
// answer it produced — the initial one and each escalation — one row per
// question, so thresholds can be set from data. The run id joins a row to the
// run's history and to the outcome loop (merged / reverted / approved), which
// is the ground truth a calibration needs.
//
// Recording is best-effort by design: a store that is down, full, or
// misconfigured is logged and audited, and never fails the step — the
// decision was made; losing its telemetry must not unmake it.

// decisionsTable is the table observe mode writes.
const decisionsTable = "conductor_decisions"

// decisionsDDL creates it. The column types are the common subset sqlite,
// postgres and mysql all accept.
const decisionsDDL = `CREATE TABLE IF NOT EXISTS ` + decisionsTable + ` (
  recorded_at VARCHAR(40) NOT NULL,
  run_id      VARCHAR(128),
  repo        VARCHAR(255),
  number      INTEGER,
  step        VARCHAR(255) NOT NULL,
  step_id     VARCHAR(255) NOT NULL,
  question    VARCHAR(255) NOT NULL,
  hop         INTEGER NOT NULL,
  final       INTEGER NOT NULL,
  answered_by VARCHAR(255) NOT NULL,
  answer      TEXT NOT NULL
)`

// decisionsReady remembers the stores whose table exists, so the DDL runs
// once per store per process.
var decisionsReady sync.Map

// observeDecisions writes a decide step's answer history to its observe
// store. history is in order: the initial answer, then each escalation; the
// last is the one the workflow used.
func (r *Runner) observeDecisions(ctx context.Context, t core.Trigger, step config.Step, id string, history []decideAnswer) {
	store := step.Decide.Observe
	if store == "" || len(history) == 0 {
		return
	}
	if err := r.writeDecisions(ctx, t, step, id, store, history); err != nil {
		r.Log("%s decide %s: observe store %q: %v (the decision stands; only its record was lost)", flowTag(t), id, store, err)
		r.audit(map[string]any{"event": "decide_observe_failed", "repo": t.Target.Repo, "number": t.Target.Number,
			"step": id, "store": store, "error": r.redactErr(err)})
	}
}

func (r *Runner) writeDecisions(ctx context.Context, t core.Trigger, step config.Step, id, store string, history []decideAnswer) error {
	st, err := sqlstore.Use(store)
	if err != nil {
		return err
	}
	if _, ready := decisionsReady.Load(store); !ready {
		if _, _, err := st.Exec(ctx, decisionsDDL, nil); err != nil {
			return fmt.Errorf("create %s: %w", decisionsTable, err)
		}
		decisionsReady.Store(store, true)
	}
	insert := decisionsInsert(st.Driver())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runID := memory.SourceFrom(ctx).Run
	identity := stepIdentity(ctx, step, id)
	for hop, a := range history {
		final := 0
		if hop == len(history)-1 {
			final = 1
		}
		for _, q := range step.Decide.Questions {
			raw, err := json.Marshal(a.answers[q.Name])
			if err != nil {
				return err
			}
			args := []any{now, runID, t.Target.Repo, t.Target.Number, identity, id, q.Name, hop, final, a.by, string(raw)}
			if _, _, err := st.Exec(ctx, insert, args); err != nil {
				return fmt.Errorf("insert into %s: %w", decisionsTable, err)
			}
		}
	}
	return nil
}

// decisionsInsert is the INSERT in the driver's placeholder dialect.
func decisionsInsert(driver string) string {
	cols := []string{"recorded_at", "run_id", "repo", "number", "step", "step_id", "question", "hop", "final", "answered_by", "answer"}
	ph := make([]string, len(cols))
	for i := range cols {
		if driver == "postgres" {
			ph[i] = fmt.Sprintf("$%d", i+1)
		} else {
			ph[i] = "?"
		}
	}
	return "INSERT INTO " + decisionsTable + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")"
}
