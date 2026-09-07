package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
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
	var t core.Trigger
	var act config.Action
	if len(rec.Trigger) == 0 || json.Unmarshal(rec.Trigger, &t) != nil ||
		len(rec.Action) == 0 || json.Unmarshal(rec.Action, &act) != nil {
		return "", fmt.Errorf("run %s carries no retryable trigger record", rec.ID)
	}
	if act.FlowRef == "" {
		return "", fmt.Errorf("run %s is not a connectors-model run — retry applies to flow runs", rec.ID)
	}
	spec, ok := e.flow.SpecFor(act.FlowRef)
	if !ok {
		return "", fmt.Errorf("run %s: its trigger is no longer in the config", rec.ID)
	}

	// Resolve the starting step: an explicit id, else the recorded failure,
	// else the top.
	if fromStep == "" {
		fromStep = rec.FailedStep
	}
	startIdx := 0
	if fromStep != "" {
		s, ok := rec.Step(fromStep)
		if !ok {
			ids := make([]string, 0, len(rec.Steps))
			for _, sr := range rec.Steps {
				ids = append(ids, sr.ID)
			}
			sort.Strings(ids)
			return "", fmt.Errorf("run %s has no step %q (steps: %s)", rec.ID, fromStep, strings.Join(ids, ", "))
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
		startIdx = s.Index
	}

	// Pin the recorded inputs: outputs of every successful step before the
	// starting index restore into scope exactly as persisted (checkpoint
	// form — the resume path re-resolves vault-read markers itself).
	outputs := map[string]map[string]any{}
	for _, s := range rec.Steps {
		if s.Index >= startIdx {
			continue
		}
		if s.Status == "ok" && s.Outputs != nil {
			outputs[s.ID] = s.Outputs
		}
	}

	// Re-mint tokens like crash resume — the recorded ones were stripped.
	if e.refreshTok != nil && t.Context != nil {
		if appTok, err := e.refreshTok(t); err == nil && appTok != "" {
			t.Context["app_token"] = appTok
		}
	}
	t.Action = act

	run := store.WorkflowRun{
		ID: rec.RunID, Source: t.Source, Instance: t.Instance,
		Kind: rec.Kind, Repo: rec.Repo, Number: rec.Number,
		Trigger: rec.Trigger, Action: rec.Action,
		Outputs: outputs, StepIndex: startIdx,
	}
	if run.ID == "" {
		run.ID = t.Kind + ":" + t.Key()
	}
	_ = e.store.PutRun(run)

	e.store.Audit(map[string]any{"event": "retry_from_step", "repo": rec.Repo,
		"number": rec.Number, "kind": rec.Kind, "run": rec.ID, "from_step": fromStep,
		"step_index": startIdx, "force_replay": forceReplay})
	e.log("%s retrying recorded run %s from step %d (%s)", tag(t), rec.ID, startIdx, orTop(fromStep))

	rctx := flow.WithRetryOf(ctx, rec.ID)
	if !e.acquire(rctx) {
		return "", fmt.Errorf("retry cancelled while waiting for a slot")
	}
	go func() {
		defer e.release()
		e.flow.Run(rctx, run, t, spec, nil, false)
	}()
	from := orTop(fromStep)
	return fmt.Sprintf("retrying run %s from %s (recorded inputs pinned)", rec.ID, from), nil
}

func orTop(step string) string {
	if step == "" {
		return "the beginning"
	}
	return "step " + step
}
