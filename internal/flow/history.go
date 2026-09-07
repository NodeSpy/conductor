package flow

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/store"
)

// Execution history (#36 §20): every non-shadow run persists a full record —
// per-step inputs / outputs / status / timing / cost — extending the
// crash-resume checkpoint machinery into an inspectable, retryable trail.
// Step I/O is scrubbed exactly like checkpoints (scrubOutputs for outputs,
// the secrets resolver for rendered inputs) before it reaches disk. The
// recorder rides the context so plan sub-steps and hooks stay out (top-level
// steps only — checkpoint parity) while execVerb/execAgent can attach the
// step's rendered inputs from where they exist.

// histKey carries the run's recorder on the context.
type histKey struct{}

// histRec accumulates one run's history and persists it incrementally.
type histRec struct {
	r *Runner

	mu    sync.Mutex
	rec   store.RunHistory
	byID  map[string]int // step id → index into rec.Steps
	saved bool           // at least one persist succeeded (best-effort trail)
}

// histFrom reads the recorder off the context (nil outside a recorded run).
func histFrom(ctx context.Context) *histRec {
	h, _ := ctx.Value(histKey{}).(*histRec)
	return h
}

// beginHistory starts the run's record. Shadow/dry runs and runs the engine
// didn't persist (no ID) are not recorded.
func (r *Runner) beginHistory(ctx context.Context, run store.WorkflowRun, t core.Trigger, spec config.TriggerSpec, shadow bool) (context.Context, *histRec) {
	if shadow || run.ID == "" || r.Store == nil {
		return ctx, nil
	}
	h := &histRec{
		r: r,
		rec: store.RunHistory{
			ID:      "r" + strconv.FormatInt(time.Now().UnixNano(), 36),
			RunID:   run.ID,
			Kind:    t.Kind,
			Variant: spec.Name,
			On:      spec.On,
			Repo:    t.Target.Repo,
			Number:  t.Target.Number,
			Started: time.Now(),
			Status:  "running",
			Trigger: run.Trigger,
			Action:  run.Action,
		},
		byID: map[string]int{},
	}
	if retryOf, _ := ctx.Value(retryOfKey{}).(string); retryOf != "" {
		h.rec.RetryOf = retryOf
	}
	h.persist()
	h.emit("run_started", "", "running", "", 0)
	return context.WithValue(ctx, histKey{}, h), h
}

// step returns (creating if needed) the record for one step id.
func (h *histRec) step(id string, idx int) *store.StepRecord {
	if i, ok := h.byID[id]; ok {
		return &h.rec.Steps[i]
	}
	h.rec.Steps = append(h.rec.Steps, store.StepRecord{ID: id, Index: idx})
	h.byID[id] = len(h.rec.Steps) - 1
	return &h.rec.Steps[len(h.rec.Steps)-1]
}

// stepStart stamps a step's start time.
func (h *histRec) stepStart(id string, idx int) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.step(id, idx).Started = time.Now()
	h.mu.Unlock()
	h.emit("step_started", id, "running", "", 0)
}

// stepDone records a step's outcome. outputs are persisted in checkpoint
// form (scrubOutputs — tainted values never reach disk).
func (h *histRec) stepDone(id string, idx int, step config.Step, status string, outputs map[string]any, errStr string, continued bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	s := h.step(id, idx)
	s.Status = status
	s.Error = errStr
	s.ContinuedOnError = continued
	if outputs != nil {
		s.Outputs = h.r.scrubOutputs(step, outputs)
	}
	if !s.Started.IsZero() {
		s.DurationMS = time.Since(s.Started).Milliseconds()
	}
	dur := s.DurationMS
	h.mu.Unlock()
	h.persist()
	h.emit("step_done", id, status, errStr, dur)
}

// setInputs attaches a step's rendered inputs (already template-resolved),
// scrubbed of tracked secret values.
func (h *histRec) setInputs(id string, inputs map[string]any) {
	if h == nil || len(inputs) == 0 {
		return
	}
	scrubbed := inputs
	if h.r.Secrets != nil {
		if red, ok := h.r.Secrets.RedactValue(inputs).(map[string]any); ok {
			scrubbed = red
		}
	}
	h.mu.Lock()
	// The step's index is stamped by stepStart (which always runs first in
	// runSteps); a lookup miss appends with index 0 — harmless for hooks.
	if i, ok := h.byID[id]; ok {
		h.rec.Steps[i].Inputs = scrubbed
	} else {
		h.rec.Steps = append(h.rec.Steps, store.StepRecord{ID: id, Inputs: scrubbed})
		h.byID[id] = len(h.rec.Steps) - 1
	}
	h.mu.Unlock()
}

// setCost attaches an agent step's usage (#36 §14).
func (h *histRec) setCost(id string, u cost.Usage) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if i, ok := h.byID[id]; ok {
		h.rec.Steps[i].Tokens = u.TotalTokens
		h.rec.Steps[i].CostUSD = u.CostUSD
	}
	h.mu.Unlock()
}

// finish closes the record with the run's outcome and spend totals.
func (h *histRec) finish(status, errStr, failedStep string, acc *costAcc) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.rec.Status = status
	h.rec.Error = errStr
	h.rec.FailedStep = failedStep
	h.rec.Finished = time.Now()
	h.rec.Tokens, h.rec.CostUSD, h.rec.ApproxCost = acc.totals()
	h.mu.Unlock()
	h.persist()
	h.emit("run_done", failedStep, status, errStr, 0)
}

// persist writes the record (best-effort: a failing history write never
// fails the run — the audit still has the step events).
func (h *histRec) persist() {
	h.mu.Lock()
	rec := h.rec
	rec.Steps = append([]store.StepRecord(nil), h.rec.Steps...)
	h.mu.Unlock()
	if err := h.r.Store.PutHistory(rec); err != nil && !h.saved {
		h.r.Log("history: %s: %v", rec.ID, err)
	} else if err == nil {
		h.saved = true
	}
}

// historySetInputs is the execVerb/execAgent-facing hook.
func historySetInputs(ctx context.Context, stepID string, inputs map[string]any) {
	histFrom(ctx).setInputs(stepID, inputs)
}

// historySetCost is the recordUsage-facing hook.
func historySetCost(ctx context.Context, stepID string, u cost.Usage) {
	histFrom(ctx).setCost(stepID, u)
}

// clipText bounds a recorded text input (prompts can be huge).
func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "… (truncated)"
}

// retryOfKey marks a run as a user-driven retry of a recorded execution.
type retryOfKey struct{}

// WithRetryOf tags the context so the retry's own history record backlinks
// the execution it re-ran (the engine's RetryRun sets it).
func WithRetryOf(ctx context.Context, histID string) context.Context {
	return context.WithValue(ctx, retryOfKey{}, histID)
}
