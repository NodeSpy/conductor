package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// User-driven re-run-from-step (#36 §20): the crash-resume checkpoint
// machinery, invoked deliberately. The recorded execution's trigger is
// rehydrated (tokens re-minted), the outputs of every successful step BEFORE
// the chosen one are pinned exactly as recorded, and the run resumes from
// that step through the ordinary flow runner — checkpoints, policy, audit,
// and a fresh history record (backlinked via retry_of) included.

// RetryRunByID loads a recorded execution and re-runs it from fromStep
// (empty = the recorded failed step, else the beginning). Returns a
// human-readable acknowledgment. forceReplay permits re-running steps the
// record says already SUCCEEDED — replaying committed side effects (posted
// comments, pushes) is a deliberate, audited act, never the default
// (#36 review H4).
func (e *Engine) RetryRunByID(ctx context.Context, histID, fromStep string, forceReplay bool) (string, error) {
	// The verified read: a record whose HMAC doesn't check out (edited on
	// disk, smuggled in) must not feed its pinned outputs back into a run
	// (#36 review M8).
	rec, err := e.store.GetHistoryVerified(histID)
	if err != nil {
		if _, ok := e.store.GetHistory(histID); !ok {
			return "", fmt.Errorf("no recorded run %q (see `conductor runs`)", histID)
		}
		return "", err
	}
	return e.retryRun(ctx, rec, fromStep, forceReplay)
}

func (e *Engine) retryRun(ctx context.Context, rec store.RunHistory, fromStep string, forceReplay bool) (string, error) {
	if e.flow == nil {
		return "", fmt.Errorf("retry: no connectors-model flow runner is configured")
	}
	if rec.Status == "running" {
		return "", fmt.Errorf("run %s is still running", rec.ID)
	}
	// A run on the same target and event is in flight (a stale record's own
	// resume included): retrying now would put a second agent on it, and
	// overwrite its checkpoint. Retry once it has ended.
	if rec.RunID != "" {
		for _, p := range e.store.PendingRuns() {
			if p.ID == rec.RunID {
				return "", fmt.Errorf("run %s: a run for %s#%d (%s) is in flight — it resumed after a restart or was started since; retry once it ends (see `conductor runs`)", rec.ID, rec.Repo, rec.Number, rec.Kind)
			}
		}
	}
	var t core.Trigger
	var act config.Action
	if len(rec.Trigger) == 0 || json.Unmarshal(rec.Trigger, &t) != nil ||
		len(rec.Action) == 0 || json.Unmarshal(rec.Action, &act) != nil {
		return "", fmt.Errorf("run %s carries no retryable trigger record", rec.ID)
	}
	if act.FlowRef == "" {
		return "", fmt.Errorf("run %s is not a connectors-model run — retry applies to flow runs", rec.ID)
	}
	// The recorded ref is the trigger's position in the config the run ran
	// under; unrelated config changes since move it. Find the trigger by its
	// identity and rebind the ref to where it sits now.
	act, spec, tidx, ok := e.resolveFlow(act)
	if !ok {
		return "", fmt.Errorf("run %s: its trigger %s is no longer in the config", rec.ID, recordedTrigger(rec, act))
	}
	if extra := connector.OptionHooks(t, spec.Options); len(extra) > 0 {
		spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
	}
	// A retry always runs the CURRENT definition. When it differs from the
	// one the run ran (a pack upgrade), the recorded step positions may not
	// map onto it: steps are matched by id, and a recorded step that no
	// longer exists, or a changed run-up to it, restarts from the top.
	changed := defChanged(rec, spec.Steps)
	var notes []string
	if changed {
		notes = append(notes, "the trigger's steps changed since this run; retrying with the current definition")
	}

	// Resolve the starting step: an explicit id, else the recorded failure,
	// else the top.
	explicit := fromStep != ""
	if fromStep == "" {
		fromStep = rec.FailedStep
	}
	if fromStep == "" && rec.Status == "interrupted" {
		// The daemon died mid-run: continue where the record stops — the
		// first step it does not show done — rather than replaying the top.
		fromStep = firstUndone(rec, spec.Steps)
	}
	recordedFrom := fromStep
	if fromStep != "" && !explicit {
		switch i := specStepIndex(spec.Steps, fromStep); {
		case i < 0:
			notes = append(notes, fmt.Sprintf("its recorded step %q is no longer in the trigger, so it starts from the beginning", fromStep))
			fromStep = ""
		case changed && !prefixDone(rec, spec.Steps, i):
			notes = append(notes, fmt.Sprintf("the steps before %q changed, so it starts from the beginning", fromStep))
			fromStep = ""
		}
		if fromStep == "" && !forceReplay {
			if done := succeededSteps(rec); len(done) > 0 {
				return "", fmt.Errorf("run %s: %s; that re-runs steps that already succeeded (%s) and replays their side effects — pass --force-replay to do that deliberately",
					rec.ID, strings.Join(notes, "; "), strings.Join(done, ", "))
			}
		}
	}
	startIdx := 0
	if fromStep != "" {
		cur := specStepIndex(spec.Steps, fromStep)
		s, ok := rec.Step(fromStep)
		if cur < 0 || !ok && rec.Status != "interrupted" && !changed {
			ids := make([]string, 0, len(spec.Steps))
			for i, st := range spec.Steps {
				ids = append(ids, specStepID(st, i))
			}
			sort.Strings(ids)
			return "", fmt.Errorf("run %s has no step %q (steps: %s)", rec.ID, fromStep, strings.Join(ids, ", "))
		}
		if changed && !prefixDone(rec, spec.Steps, cur) {
			return "", fmt.Errorf("run %s: the trigger's steps before %q changed since this run, so starting there would skip steps it never ran — retry from an earlier step", rec.ID, fromStep)
		}
		if !ok {
			// A step the record never reached (the interrupted run stopped
			// before it, or the upgrade added it after what did run):
			// nothing to replay.
			s = store.StepRecord{ID: fromStep, Index: cur}
		}
		// Re-running a step the record says SUCCEEDED replays its committed
		// side effects (a posted comment, a push) — refuse unless forced.
		// The same applies to any target before the recorded failure point:
		// everything up to it ran to completion.
		if !forceReplay {
			if s.Status == "ok" {
				return "", fmt.Errorf("run %s step %q already succeeded — re-running it replays its side effects; pass --force-replay to do that deliberately", rec.ID, fromStep)
			}
			if fs, ok := rec.Step(rec.FailedStep); ok && s.Index < fs.Index {
				return "", fmt.Errorf("run %s step %q is before the recorded failure (%q) — its steps already ran; pass --force-replay to re-run them", rec.ID, fromStep, rec.FailedStep)
			}
		}
		// The CURRENT position: the run executes the current definition.
		startIdx = cur
	}

	// Pin the recorded inputs: outputs of every successful step before the
	// starting index restore into scope exactly as persisted (checkpoint
	// form — the resume path re-resolves vault-read markers itself).
	outputs := map[string]map[string]any{}
	for i := 0; i < startIdx; i++ {
		id := specStepID(spec.Steps[i], i)
		if s, ok := rec.Step(id); ok && s.Status == "ok" && s.Outputs != nil {
			outputs[id] = s.Outputs
		}
	}

	// Credentials are minted at dispatch, as on crash resume — the recorded
	// ones were never persisted.
	t.Action = act
	actRaw, err := json.Marshal(act) // the rebound ref, for a resume of this retry
	if err != nil {
		return "", fmt.Errorf("run %s: %w", rec.ID, err)
	}

	run := store.WorkflowRun{
		ID: rec.RunID, Source: t.Source, Instance: t.Instance,
		Kind: rec.Kind, Repo: rec.Repo, Number: rec.Number,
		Trigger: rec.Trigger, Action: actRaw,
		Outputs: outputs, StepIndex: startIdx,
		// A retry starting from step 0 is a fresh attempt — its start
		// hooks fire, same as a brand new run (StartHooksFired false, the
		// zero value). A retry continuing from a LATER step is the same
		// attempt resuming partway through: its start hooks already fired
		// the first time, so this record says so up front (finding 4a) —
		// flow.Runner.Run never gets the chance to double-fire them.
		StartHooksFired: startIdx > 0,
	}
	if run.ID == "" {
		run.ID = t.Kind + ":" + t.Key()
	}
	_ = e.store.PutRun(run)

	e.store.Audit(map[string]any{"event": "retry_from_step", "repo": rec.Repo,
		"number": rec.Number, "kind": rec.Kind, "run": rec.ID, "from_step": fromStep,
		"step_index": startIdx, "force_replay": forceReplay, "definition_changed": changed,
		"recorded_step": recordedFrom})
	e.log("%s retrying recorded run %s from step %d (%s)", tag(t), rec.ID, startIdx, orTop(fromStep))

	rctx := flow.WithRetryOf(ctx, rec.ID)
	caps := e.runCapsOf(t, act.FlowRef, spec)
	if !e.acquireRunCaps(rctx, t, caps, nil) {
		return "", fmt.Errorf("retry cancelled while waiting for a run slot")
	}
	if !e.acquire(rctx) {
		releaseRunCaps(caps)
		return "", fmt.Errorf("retry cancelled while waiting for a slot")
	}
	go func() {
		defer e.recoverDispatch(rctx, t, run, "flow retry")
		defer releaseRunCaps(caps)
		defer e.release()
		e.runFlow(rctx, run, t, spec, tidx, nil, nil)
	}()
	msg := fmt.Sprintf("retrying run %s from %s (recorded inputs pinned)", rec.ID, orTop(fromStep))
	if len(notes) > 0 {
		msg += " — " + strings.Join(notes, "; ")
	}
	return msg, nil
}

// recordedTrigger names a record's trigger for an error: its name on its
// `on:` when it has one, else the recorded ref.
func recordedTrigger(rec store.RunHistory, act config.Action) string {
	if act.Name != "" {
		return fmt.Sprintf("%q (on %s)", act.Name, rec.On)
	}
	return fmt.Sprintf("%q", act.FlowRef)
}

// defChanged reports whether the trigger's current steps differ from the ones
// the recorded run executed: by the record's definition fingerprint when it
// carries one, else (an older record) by whether every step it recorded
// still sits at the same position.
func defChanged(rec store.RunHistory, steps []config.Step) bool {
	if rec.Def != "" {
		return rec.Def != config.StepsFingerprint(steps)
	}
	for _, s := range rec.Steps {
		if specStepIndex(steps, s.ID) != s.Index {
			return true
		}
	}
	return false
}

// prefixDone reports whether the record shows every current step before
// index i done — so starting at i skips nothing the run never did.
func prefixDone(rec store.RunHistory, steps []config.Step, i int) bool {
	for j := 0; j < i; j++ {
		r, ok := rec.Step(specStepID(steps[j], j))
		if !ok || (r.Status != "ok" && r.Status != "skipped" && !r.ContinuedOnError) {
			return false
		}
	}
	return true
}

// succeededSteps lists the steps the record shows succeeded, in run order.
func succeededSteps(rec store.RunHistory) []string {
	var out []string
	for _, s := range rec.Steps {
		if s.Status == "ok" {
			out = append(out, s.ID)
		}
	}
	return out
}

// checkpointFits reports whether a persisted run's checkpoint still lines up
// with the trigger's steps: its step index is within them, and every step it
// holds outputs for sits before that index.
func checkpointFits(r store.WorkflowRun, steps []config.Step) bool {
	if r.StepIndex < 0 || r.StepIndex > len(steps) {
		return false
	}
	for id := range r.Outputs {
		if i := specStepIndex(steps, id); i < 0 || i >= r.StepIndex {
			return false
		}
	}
	return true
}

// specStepID is a top-level step's id as the flow runner records it.
func specStepID(s config.Step, i int) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("step%d", i+1)
}

func specStepIndex(steps []config.Step, id string) int {
	for i, s := range steps {
		if specStepID(s, i) == id {
			return i
		}
	}
	return -1
}

// firstUndone is the first step an interrupted record does not show done
// ("" when every step is: the run ended, its record did not).
func firstUndone(rec store.RunHistory, steps []config.Step) string {
	for i, s := range steps {
		id := specStepID(s, i)
		if r, ok := rec.Step(id); !ok || (r.Status != "ok" && r.Status != "skipped") {
			return id
		}
	}
	return ""
}

func orTop(step string) string {
	if step == "" {
		return "the beginning"
	}
	return "step " + step
}
