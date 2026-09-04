package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
)

// processFlow handles a trigger whose lowered action carries a FlowRef — the
// connectors-model path. It runs after process()'s generic gates (kill
// switch, pause, dedup, backoff), so those behave identically for both
// schemas; here we add the flow-side filters, the scoped policy gates, and
// group batching, then hand the run to the flow runner.
func (e *Engine) processFlow(ctx context.Context, t core.Trigger, act config.Action, key, dkind, head string) {
	spec, ok := e.flow.SpecFor(act.FlowRef)
	if !ok {
		e.log("%s stale flow ref %q — config changed; dropping", tag(t), act.FlowRef)
		return
	}
	if !spec.IsEnabled() {
		return
	}
	// Flow-side filters (synthetic sources): a non-matching event is dropped
	// before any dedup state is consumed.
	if ok, err := e.flow.FilterMatch(t, spec); err != nil {
		e.log("%s filter error: %v", tag(t), err)
		return
	} else if !ok {
		return
	}

	// Scoped policy: trigger → connector → global, most specific wins. (No
	// policy-level enabled — the global kill switch is the runtime
	// `conductor pause`; connectors and triggers disable via their own
	// `enabled:` fields, checked above and at source build.)
	pol := e.policyFor(spec)
	if pol.PauseLabel != nil && *pol.PauseLabel != "" && triggerHasLabel(t, *pol.PauseLabel) {
		e.log("%s skipped — carries pause label %q", tag(t), *pol.PauseLabel)
		return
	}
	if ig := pol.Ignore; ig != nil && len(ig.Users) > 0 {
		if author, _ := t.Context["author"].(string); author != "" {
			for _, u := range ig.Users {
				if strings.EqualFold(u, author) {
					e.log("%s skipped — author %q is ignored by policy", tag(t), author)
					return
				}
			}
		}
	}
	if q := pol.QuietHours; q != nil {
		if in, until := flow.InQuietWindow(q, time.Now()); in {
			if q.Hold == nil || *q.Hold {
				delay := time.Until(until)
				e.log("%s quiet hours — holding for %s", tag(t), delay.Round(time.Minute))
				tt := t
				time.AfterFunc(delay, func() { e.Emit(context.WithoutCancel(ctx), tt) })
			} else {
				e.log("%s quiet hours — dropped (hold: false)", tag(t))
			}
			return
		}
	}

	shadow := e.cfg.Control.Shadow || (pol.Shadow != nil && *pol.Shadow) || (act.Shadow != nil && *act.Shadow)

	// Consume dedup state now (mirrors the legacy multi-step branch): the
	// event is committed to a run — grouped events buffer after this point.
	if !shadow {
		if livenessGated(t.Kind) || t.Force {
			_ = e.store.RecordAttempt(key, dkind, head)
		} else {
			_ = e.store.Record(key, dkind, t.Dedup, head)
		}
	}
	// A comment was accepted for handling — raise the high-water mark.
	if t.Kind == "new_comment" {
		if id := commentID(t); id > 0 {
			_ = e.store.AdvanceCommentID(key, commentMarkKind(t), id)
		}
	}

	if spec.Group != nil {
		gkey, err := flow.GroupKey(spec.Group.Key, t, e.flowBaseData(t))
		if err != nil {
			e.log("%s group key: %v — running ungrouped", tag(t), err)
			gkey = t.Dedup
		}
		full := act.FlowRef + "\x00" + gkey
		e.grouper.Add(full, t, spec.Group.Window.D(), spec.Group.MaxWait.D())
		return
	}
	e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
	e.startFlowRun(ctx, t, spec, nil, shadow)
}

// startFlowRun takes a concurrency slot and runs one flow (or batch) in its
// own goroutine.
func (e *Engine) startFlowRun(ctx context.Context, t core.Trigger, spec config.TriggerSpec, batch *flow.Batch, shadow bool) {
	if !shadow && !e.acquire(ctx) {
		return
	}
	run := e.newFlowRun(t, spec, shadow)
	go func() {
		if !shadow {
			defer e.release()
		}
		e.flow.Run(ctx, run, t, spec, batch, shadow)
	}()
}

// runBatch fires a grouped batch: the last event is the representative
// trigger (freshest context/tokens), the whole burst rides under {{.group.*}}.
func (e *Engine) runBatch(fullKey string, events []core.Trigger) {
	if len(events) == 0 {
		return
	}
	t := events[len(events)-1]
	ref, gkey, _ := strings.Cut(fullKey, "\x00")
	spec, ok := e.flow.SpecFor(ref)
	if !ok {
		e.log("%s stale flow ref %q at batch fire — dropping %d events", tag(t), ref, len(events))
		return
	}
	ctx := context.Background()
	e.notif.Emit(ctx, notify.EventDispatch, t, fmt.Sprintf("workflow (batch of %d)", len(events)))
	e.log("%s grouped batch firing (%d events, key %q)", tag(t), len(events), gkey)

	// Synchronous: the grouper's one-run-per-key guarantee depends on this
	// call not returning until the run finishes.
	if !e.acquire(ctx) {
		return
	}
	defer e.release()
	run := e.newFlowRun(t, spec, false)
	e.flow.Run(ctx, run, t, spec, &flow.Batch{Key: gkey, Events: events}, false)
}

// policyFor merges the policy scopes that govern one trigger.
func (e *Engine) policyFor(spec config.TriggerSpec) config.Policy {
	var connPol *config.Policy
	if ref, ok := e.cfg.ConnectorsMap[spec.Connector()]; ok {
		connPol = ref.Policy
	}
	return config.MergePolicy(e.cfg.Policy, connPol, spec.Policy)
}

// retryPolicyFor resolves the policy that governs an action's retry/backoff
// gate: the fully scoped merge for a flow trigger, the global block for a
// legacy action (legacy integrations carry no policy of their own).
func (e *Engine) retryPolicyFor(act config.Action) config.Policy {
	if act.FlowRef != "" && e.flow != nil {
		if spec, ok := e.flow.SpecFor(act.FlowRef); ok {
			return e.policyFor(spec)
		}
	}
	return config.MergePolicy(e.cfg.Policy)
}

// flowBaseData mirrors the runner's base scope for group-key rendering.
func (e *Engine) flowBaseData(t core.Trigger) map[string]any {
	d := map[string]any{
		"repo": t.Target.Repo, "owner": t.Target.Owner, "name": t.Target.Name,
		"pr": t.Target.PR, "issue": t.Target.Issue, "number": t.Target.Number,
		"head": t.Target.HeadSHA, "base": t.Target.BaseRef, "url": t.Target.HTMLURL,
		"kind": t.Kind, "title": t.Title,
	}
	for k, v := range t.Context {
		if _, ok := d[k]; !ok {
			d[k] = v
		}
	}
	return d
}

// newFlowRun persists a resumable run for a flow trigger (tokens stripped,
// exactly like legacy workflow runs).
func (e *Engine) newFlowRun(t core.Trigger, spec config.TriggerSpec, shadow bool) store.WorkflowRun {
	run := store.WorkflowRun{
		ID:       "flow:" + t.Kind + ":" + t.Key(),
		Source:   t.Source,
		Instance: t.Instance,
		Kind:     t.Kind,
		Repo:     t.Target.Repo,
		Number:   t.Target.Number,
		Outputs:  map[string]map[string]any{},
	}
	tp := t
	act, _ := tp.Action.(config.Action)
	tp.Action = nil
	tp.Context = sanitizeContext(t.Context)
	run.Trigger, _ = json.Marshal(tp)
	run.Action, _ = json.Marshal(act)
	if shadow || e.store == nil {
		run.ID = ""
		return run
	}
	_ = e.store.PutRun(run)
	return run
}

// resumeFlowRun resumes one persisted flow run (the FlowRef path of
// ResumeWorkflows): re-find the spec, re-mint tokens, continue after the last
// checkpointed step.
func (e *Engine) resumeFlowRun(ctx context.Context, r store.WorkflowRun, t core.Trigger, act config.Action) {
	spec, ok := e.flow.SpecFor(act.FlowRef)
	if !ok {
		e.log("engine: resume %s: trigger no longer in config — dropping", r.ID)
		_ = e.store.DeleteRun(r.ID)
		return
	}
	t.Action = act
	e.log("%s resuming flow from step %d", tag(t), r.StepIndex)
	e.store.Audit(map[string]any{"event": "resume", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "step_index": r.StepIndex})
	if !e.acquire(ctx) {
		return
	}
	go func() {
		defer e.release()
		e.flow.Run(ctx, r, t, spec, nil, false)
	}()
}

// askChannelFor resolves the hand-off channel a background step presents on:
// an ask-capable connector by name, then the legacy handoffs: registry, then
// nil (runtime-native — the notify-to-open-paseo fallback).
func (e *Engine) askChannelFor(name string) handoff.Channel {
	if name != "" && e.connectors != nil {
		if in, ok := e.connectors.Get(name); ok && in.Impl != nil {
			if ac, isAsk := in.Impl.(connector.AskChanneler); isAsk {
				ch, err := ac.AskChannel(in.DefaultOptions)
				if err != nil {
					e.log("handoff connector %q: %v", name, err)
					return nil
				}
				return ch
			}
		}
	}
	if e.handoffs != nil {
		ch, err := e.handoffs.Resolve(name)
		if err != nil {
			e.log("handoff %q: %v", name, err)
			return nil
		}
		return ch
	}
	return nil
}

// flowAgentServices builds the engine-owned services the flow runner needs
// for agent/command steps: runtime resolution, tokens, guidance, and the
// background review hand-off.
func (e *Engine) flowAgentServices() flow.AgentServices {
	return flow.AgentServices{
		Dispatch: func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			runner := Dispatcher(e.disp)
			if req.Action.Type == "agent" {
				r, err := e.runnerFor(req.Profile)
				if err != nil {
					return dispatch.RunRef{}, err
				}
				runner = r
			}
			req.Author = e.author
			return runner.Dispatch(ctx, req)
		},
		Tokens: func(t core.Trigger) dispatch.Tokens {
			appTok, _ := t.Context["app_token"].(string)
			if e.readTok != nil {
				if tok, err := e.readTok(); err == nil && tok != "" {
					appTok = tok
				}
			}
			userTok := ""
			if e.userTok != nil {
				userTok, _ = e.userTok()
			}
			return dispatch.Tokens{App: appTok, User: userTok}
		},
		Guidance: e.agentGuidance,
		Background: func(ctx context.Context, t core.Trigger, stepID string, p config.AgentProfile, ref dispatch.RunRef, handoffConn string) {
			e.hold.Add(ref.AgentID)
			ch := e.askChannelFor(handoffConn)
			if ch != nil && e.broker != nil && ref.AgentID != "" {
				e.startReviewHandoff(ctx, t, stepID, p, ref.AgentID, ch)
				return
			}
			e.notif.Emit(ctx, notify.EventNeedsInput, t,
				fmt.Sprintf("interactive agent for %q is live (agent %s) — open it to review/refine", stepID, ref.AgentID))
		},
		Archive: func(agentID string) {
			go func() { _ = e.disp.Archive(context.Background(), agentID) }()
		},
	}
}
