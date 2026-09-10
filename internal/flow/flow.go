package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/code"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/expr"
	"github.com/NodeSpy/conductor/internal/gitdiff"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// Store is the persistence surface the runner needs (checkpointed runs,
// in-flight plan checkpoints, and the audit log). *store.Store satisfies it.
type Store interface {
	Audit(entry map[string]any)
	PutRun(r store.WorkflowRun) error
	DeleteRun(id string) error
	// Plan checkpoints: an agent-emitted plan persists its committed
	// progress so a restart resumes after the last committed step (#36 §11).
	PutPlan(rec store.PlanRecord) error
	GetPlan(runID, stepID string) (store.PlanRecord, bool)
	DeletePlan(runID, stepID string) error
	// PutHistory persists the run's execution record (#36 §20).
	PutHistory(rec store.RunHistory) error
}

// Notifier emits lifecycle notifications. *notify.Notifier satisfies it.
type Notifier interface {
	Emit(ctx context.Context, event string, t core.Trigger, msg string)
}

// AgentServices are the engine-owned facilities agent/command steps need. The
// engine injects them at wiring so flow never imports the engine (and tests
// inject fakes).
type AgentServices struct {
	// Dispatch runs one request through the runtime that owns the profile
	// (explicit runtime → default → built-in paseo).
	Dispatch func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error)
	// Tokens resolves the acts-as-you / App tokens for a trigger.
	Tokens func(t core.Trigger) dispatch.Tokens
	// Guidance is the house prompt guidance for a step (its IDENTITY keys the
	// optional outcome-feedback tuning — #36 §18). pol is the trigger's
	// resolved policy cascade — its Guidance is the scoped layer-0 baseline
	// the step's own guidance stacks onto.
	Guidance func(identity string, s config.Step, pol config.Policy) string
	// Memory renders the shared-memory prompt section for an opted-in step
	// ("" otherwise) — appended through the same path Guidance uses. The
	// engine supplies the run's opaque context keys; see
	// docs/design/agents-removal.md §2.
	Memory func(identity string, s config.Step, t core.Trigger, workflow string) string
	// ResolveModel picks the model this step runs (the design §2.3 ladder).
	// "" is a BARE LAUNCH — dispatch with no --model. nil = no model layer
	// (tests): every dispatch bare-launches.
	ResolveModel func(ctx context.Context, s config.Step) string
	// Revise delivers a supervise-loop follow-up to the authoring agent's
	// live session (§10) and returns the captured reply. ok=false when the
	// agent has no bound session (or the runtime can't capture follow-up
	// output) — the plan then escalates instead of revising.
	Revise func(ctx context.Context, identity string, t core.Trigger, prompt string) (output string, ok bool, err error)
	// Background is invoked after a background agent step launches: register
	// the hold, and start the interactive review hand-off on handoffConn (an
	// ask-capable connector name; "" = runtime-native).
	Background func(ctx context.Context, t core.Trigger, stepID, identity string, s config.Step, ref dispatch.RunRef, handoffConn string)
	// Archive soft-deletes a finished non-interactive agent.
	Archive func(agentID string)
	// CheckBudget vets an agent dispatch against the spend caps (#36 §14):
	// global, the RUNTIME it executes on (docs/design/agents-removal.md §1 —
	// a budget caps execution cost on a backend), and the run's workflow-scope budget
	// (wf/wfScope, resolved by the runner from the trigger's merged policy).
	// An admitted dispatch holds a RESERVATION for est (#36 review H7) that
	// RecordUsage settles or CancelBudget releases — concurrent under-cap
	// checks (a team's parallel workers) cannot overshoot a hard cap. A
	// non-nil error sheds the dispatch. nil = no budget layer (tests).
	CheckBudget func(runtime string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error)
	// RecordUsage charges one agent run's token/$ usage to its budget scopes
	// (settling res) and the audit, and records the outcome engagement
	// (#36 §18) — savedWF names the enclosing saved workflow ("" outside
	// one). nil = tests.
	RecordUsage func(t core.Trigger, identity, runtime, stepID, runID, wfScope, savedWF string, res *cost.Reservation, u cost.Usage)
	// CancelBudget releases a reservation whose dispatch never charged
	// (shadowed / skipped / queued / errored). nil = no budget layer.
	CancelBudget func(res *cost.Reservation)
	// FollowUp delivers a gate-revise prompt to a live agent and captures the
	// reply (#36 §16): a bound session (§10) or a paseo send-capture. ok=false
	// when the runtime can't take one — the gate then escalates.
	FollowUp func(ctx context.Context, agentID, identity string, t core.Trigger, prompt string) (string, bool, error)
}

// identScopeKey carries the enclosing identity scope (the qualified trigger,
// workflow, or check) so every step can resolve its stable identity — the
// single key memory, sessions, and outcomes default to (design §5).
type identScopeKey struct{}

// withIdentityScope stamps the enclosing scope on the context.
func withIdentityScope(ctx context.Context, scope config.IdentityScope) context.Context {
	return context.WithValue(ctx, identScopeKey{}, scope)
}

// identityScopeFrom reads the enclosing scope (zero value outside one).
func identityScopeFrom(ctx context.Context) config.IdentityScope {
	sc, _ := ctx.Value(identScopeKey{}).(config.IdentityScope)
	return sc
}

// stepIdentity resolves a step's stable identity in the current scope.
//
// The slot comes from config.StepSlot — the SAME rule every lookup path
// uses (WalkSteps, a step reference, the affinity sweep). It is
// deliberately not the runner's own `stepID`: that one is the user-facing
// output key (`{{.steps.step1.outputs}}`) and is 1-based for readability,
// which is a different job. Computing identity from it is how a session
// came to be bound under `…/step1` and swept under `…/0`.
func stepIdentity(ctx context.Context, s config.Step, slot string) string {
	return config.IdentityFor(identityScopeFrom(ctx), s.Name, slot, s.Fingerprint)
}

// savedWFKey stamps execution inside a SAVED workflow with its name, so
// outcome engagements attribute to it (#36 §18).
type savedWFKey struct{}

// savedWFFrom reads the enclosing saved workflow's name ("" outside one).
func savedWFFrom(ctx context.Context) string {
	name, _ := ctx.Value(savedWFKey{}).(string)
	return name
}

// Runner executes fired triggers through the trigger grammar.
type Runner struct {
	Cfg     *config.Config
	Conns   *connector.Registry
	Agents  AgentServices
	Code    *code.Executor
	Secrets *secrets.Resolver
	// SecretVals are the resolved named secrets: values ({{.secrets.<name>}}).
	SecretVals map[string]string
	// VaultVals are the preloaded listable-vault entries
	// ({{.vaults.<name>.<key>}}) — see vaults.PreloadListable.
	VaultVals map[string]map[string]string
	Store     Store
	Notif     Notifier
	Log       func(string, ...any)
	// Blobs is the content-addressed artifact store (#36 §21): verb-level
	// binary IO stages through it and a run's blobs are GC'd with the run.
	// nil = no binary handling (binary-declaring verbs then error plainly).
	Blobs *blob.Store
	// Events is the live-observability hub (#36 §17): recorded runs publish
	// run/step/gate events `conductor watch` streams. nil = no events.
	Events *EventHub
	// DryRun stubs every outbound verb and agent/command dispatch (replay).
	DryRun bool
	// sleep is injectable for retry tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// New builds a Runner with defaults filled in.
func New(r Runner) *Runner {
	if r.Log == nil {
		r.Log = func(string, ...any) {}
	}
	if r.sleep == nil {
		r.sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if r.Code == nil {
		r.Code = &code.Executor{}
	}
	return &r
}

// SpecFor resolves a lowered action's FlowRef ("<index>:<on>") back to its
// trigger spec AND its position in the list. The on-part is verified so a
// stale index from a resumed run against an edited config is caught instead
// of running the wrong trigger.
//
// The index is not incidental: an unnamed list-form trigger's identity
// scope is `<on>[<index>]`, so a caller that drops it dispatches every
// such trigger under index 0 — binding sessions and track records that no
// lookup path can ever find again.
func (r *Runner) SpecFor(ref string) (config.TriggerSpec, int, bool) {
	idxStr, on, ok := strings.Cut(ref, ":")
	if !ok {
		return config.TriggerSpec{}, 0, false
	}
	var idx int
	if _, err := fmt.Sscanf(idxStr, "%d", &idx); err != nil {
		return config.TriggerSpec{}, 0, false
	}
	if idx < 0 || idx >= len(r.Cfg.Triggers) || r.Cfg.Triggers[idx].On != on {
		return config.TriggerSpec{}, 0, false
	}
	return r.Cfg.Triggers[idx], idx, true
}

// IndexOf locates a spec in the configured trigger list, for a caller that
// did not come through SpecFor. A named trigger matches by name (names are
// unique); an unnamed one by `on:` plus deep equality of its steps, which
// is the best available and still beats assuming 0.
func (r *Runner) IndexOf(spec config.TriggerSpec) int {
	if r.Cfg == nil {
		return 0
	}
	for i := range r.Cfg.Triggers {
		c := r.Cfg.Triggers[i]
		if spec.Name != "" {
			if c.Name == spec.Name {
				return i
			}
			continue
		}
		if c.Name == "" && c.On == spec.On && len(c.Steps) == len(spec.Steps) {
			return i
		}
	}
	return 0
}

// FilterMatch evaluates a trigger's flow-side filters (connector types whose
// declaration provides a Filter func) against a fired event's context. Types
// whose lowered integration already evaluated filters return true.
func (r *Runner) FilterMatch(t core.Trigger, spec config.TriggerSpec) (bool, error) {
	in, ok := r.Conns.Get(spec.Connector())
	if !ok || in.Decl.Filter == nil || len(spec.Filters) == 0 {
		return true, nil
	}
	return in.Decl.Filter(spec.Event(), spec.Filters, t.Context)
}

// Batch is one grouped firing: the resolved group key and the burst of events
// that share it. A nil/single-event batch is the ungrouped case.
type Batch struct {
	Key    string
	Events []core.Trigger
}

// Run executes one fired trigger (or one grouped batch) through its steps and
// hooks. It owns the full lifecycle: at-start hooks, steps with per-step
// checkpoints, at-done/at-fail hooks, notifications, and audit.
// botReplyKey carries the run's resolved bot-reply state through the step
// tree on the context (the runner is shared across concurrent runs).
type botReplyKey struct{}

// botReplyState is the per-run reply_to_bots resolution: the merged policy
// mode and whether the triggering event's author is a bot.
type botReplyState struct {
	mode        string
	authorIsBot bool
	login       string
}

// botReply reads the run's bot-reply state off the context.
func botReply(ctx context.Context) (botReplyState, bool) {
	st, ok := ctx.Value(botReplyKey{}).(botReplyState)
	return st, ok
}

// policyKey carries the run's resolved policy cascade (global → connector →
// trigger) through the step context, so agent steps can read the scoped
// guidance baseline without re-resolving it.
type policyKey struct{}

// policyFrom reads the run's resolved policy off the context (zero Policy if
// unset — a legacy or test path that never stashed one).
func policyFrom(ctx context.Context) config.Policy {
	p, _ := ctx.Value(policyKey{}).(config.Policy)
	return p
}

// resolvePolicy merges the trigger's policy scopes most-specific-last (global →
// connector → trigger).
func (r *Runner) resolvePolicy(spec config.TriggerSpec) config.Policy {
	var connPol, global *config.Policy
	if r.Cfg != nil {
		if ref, ok := r.Cfg.ConnectorsMap[spec.Connector()]; ok {
			connPol = ref.Policy
		}
		global = r.Cfg.Policy
	}
	return config.MergePolicy(global, connPol, spec.Policy)
}

// resolveBotReply pairs the reply_to_bots mode from the resolved policy with the
// trigger's author facts.
func (r *Runner) resolveBotReply(t core.Trigger, spec config.TriggerSpec) botReplyState {
	pol := r.resolvePolicy(spec)
	isBot, _ := t.Context["author_is_bot"].(bool)
	login, _ := t.Context["author"].(string)
	return botReplyState{mode: pol.ReplyToBotsMode(), authorIsBot: isBot, login: login}
}

// Run executes one fired trigger. triggerIndex is the spec's position in
// `config.Triggers` — it is half of an unnamed list-form trigger's identity
// scope (`<on>[<index>]`), so passing a wrong one silently detaches this
// run's sessions, memory, and track record from every lookup path.
// SpecFor returns it; a caller holding a spec from elsewhere can use
// Runner.IndexOf.
func (r *Runner) Run(ctx context.Context, run store.WorkflowRun, t core.Trigger, spec config.TriggerSpec, triggerIndex int, batch *Batch, shadow bool) {
	ctx = withIdentityScope(ctx, config.ScopeForTrigger(spec, triggerIndex))
	ctx = context.WithValue(ctx, policyKey{}, r.resolvePolicy(spec))
	ctx = context.WithValue(ctx, botReplyKey{}, r.resolveBotReply(t, spec))
	ctx = withDefaultGate(ctx, spec.Gate)
	ctx = r.withRunBudget(ctx, t, spec)
	ctx, runCost := withCostAcc(ctx)
	ctx, hist := r.beginHistory(ctx, run, t, spec, shadow || r.DryRun || (spec.Shadow != nil && *spec.Shadow))
	// Stamp the run's provenance for memory writes: a `uses: memory.remember`
	// step or hook records where the memory came from with no step plumbing.
	ctx = memory.WithSource(ctx, memory.Source{Run: run.ID, Trigger: t.Kind, Repo: t.Target.Repo})
	// Blobs are owned per EXECUTION, not per run-dedup key (H2): run.ID is stable
	// across every trigger of a target, so keying blob refs on it means the first
	// execution's ReleaseRun tombstones the id and the next trigger can never Put
	// again. Thread a per-Run owner id (the history id, or run.ID + a nonce) so a
	// re-trigger gets a fresh, releasable namespace; nested workflow-calls inherit
	// it via this ctx.
	blobOwner := blobOwnerID(run.ID, hist)
	ctx = blob.WithOwner(ctx, blobOwner)
	data := baseData(t, r.SecretVals)
	addVaultData(data, r.VaultVals)
	if batch != nil {
		data["group"] = groupData(batch, r.SecretVals)
	}
	stepsOut := map[string]any{}
	data["steps"] = stepsOut
	for id, out := range run.Outputs { // restore checkpointed outputs (resume)
		restored := r.restoreOutputs(ctx, t, spec.Steps, id, out, data)
		data[id] = restored
		stepsOut[id] = map[string]any{"outputs": restored}
	}

	shadow = shadow || r.DryRun || (spec.Shadow != nil && *spec.Shadow)
	r.runHooks(ctx, t, spec.Hooks, "start", data, "workflow")

	err := r.runSteps(ctx, &run, t, spec.Steps, data, shadow, true)
	if err != nil {
		r.Log("%s workflow failed: %v", flowTag(t), err)
		fdata := cloneData(data)
		fdata["error"] = err.Error()
		fdata["failed_step"] = failedStepID(err)
		r.runHooks(ctx, t, spec.Hooks, "fail", fdata, "workflow")
		if r.Notif != nil {
			// "failed" is the run-errored lifecycle event (conductor.failed);
			// "escalate" stays the engine's gave-up-after-retries signal.
			r.Notif.Emit(ctx, "failed", t, fmt.Sprintf("workflow failed: %v", err))
		}
		// A partial failure stays visible: the audit records where it stopped,
		// and the run is removed (the sweep/backoff machinery re-derives).
		r.audit(map[string]any{"event": "workflow_failed", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "error": err.Error(), "failed_step": failedStepID(err)})
		r.auditRunCost(t, run.ID, runCost)
		hist.finish("failed", r.redactErr(err), failedStepID(err), runCost)
		r.finishRun(ctx, run)
		return
	}
	r.runHooks(ctx, t, spec.Hooks, "done", data, "workflow")
	if r.Notif != nil {
		r.Notif.Emit(ctx, "complete", t, "workflow")
	}
	r.auditRunCost(t, run.ID, runCost)
	hist.finish("ok", "", "", runCost)
	r.finishRun(ctx, run)
}

// auditRunCost writes the run's total spend (#36 §14 — cost per run) when
// any agent step metered usage.
func (r *Runner) auditRunCost(t core.Trigger, runID string, acc *costAcc) {
	tokens, usd, approx := acc.totals()
	if tokens == 0 && usd == 0 {
		return
	}
	r.audit(map[string]any{"event": "workflow_cost", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "run": runID,
		"tokens": tokens, "cost_usd": usd, "approximate": approx})
}

// stepError carries the failing step's id up to the workflow fail hooks.
type stepError struct {
	id  string
	err error
}

func (e *stepError) Error() string { return fmt.Sprintf("step %q: %v", e.id, e.err) }
func (e *stepError) Unwrap() error { return e.err }

func failedStepID(err error) string {
	var se *stepError
	if ok := asStepError(err, &se); ok {
		return se.id
	}
	return ""
}

func asStepError(err error, target **stepError) bool {
	for err != nil {
		if se, ok := err.(*stepError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// runSteps executes a step list in order. checkpoint=true persists per-step
// progress into the run (top-level steps only — nested scopes re-run whole
// steps on resume, at-least-once).
func (r *Runner) runSteps(ctx context.Context, run *store.WorkflowRun, t core.Trigger, steps []config.Step, data map[string]any, shadow, checkpoint bool) error {
	start := 0
	if checkpoint {
		start = run.StepIndex
	}
	// History records top-level steps only (checkpoint parity) — nested
	// scopes re-run whole steps on resume/retry, so recording them would
	// suggest a resolution the retry machinery doesn't have.
	var hist *histRec
	if checkpoint {
		hist = histFrom(ctx)
	}
	for i := start; i < len(steps); i++ {
		// Two different jobs, deliberately: `id` is the user-facing output
		// key (`{{.steps.step1.outputs}}`, 1-based for readability) and
		// `slot` is the IDENTITY slot — the same string every lookup path
		// computes (config.StepSlot). Conflating them is how a session got
		// bound under `…/step1` and swept under `…/0`.
		step := steps[i]
		id := stepID(step, i)
		slot := config.StepSlot(step, i)

		if step.If != "" {
			ok, err := expr.Eval(step.If, data)
			if err != nil {
				return &stepError{id: id, err: fmt.Errorf("if: %w", err)}
			}
			if !ok {
				r.audit(map[string]any{"event": "step_skipped", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "if": step.If})
				hist.stepDone(id, i, step, "skipped", nil, "", false)
				r.checkpoint(ctx, run, i, id, step, nil, checkpoint)
				continue
			}
		}

		r.runHooks(ctx, t, step.Hooks, "start", data, "step "+id)
		hist.stepStart(id, i)
		outputs, err := r.execStepWithFlow(ctx, t, step, id, slot, data, shadow)
		if err != nil {
			// Error strings carry whatever the failing transport embedded —
			// a REST secret in a URL query rides url.Error verbatim. Redact
			// before the string reaches hooks' template scope or disk.
			errStr := r.redactErr(err)
			fdata := cloneData(data)
			fdata["error"] = errStr
			fdata["failed_step"] = id
			r.runHooks(ctx, t, step.Hooks, "fail", fdata, "step "+id)
			r.audit(map[string]any{"event": "step_error", "repo": t.Target.Repo,
				"number": t.Target.Number, "kind": t.Kind, "step": id, "error": errStr})
			if step.ContinueOnError {
				r.Log("%s step %s failed (continue_on_error): %v", flowTag(t), id, err)
				outputs = map[string]any{"error": errStr, "failed": true}
				r.recordOutputs(data, id, outputs)
				hist.stepDone(id, i, step, "failed", outputs, errStr, true)
				r.checkpoint(ctx, run, i, id, step, outputs, checkpoint)
				continue
			}
			hist.stepDone(id, i, step, "failed", nil, errStr, false)
			return &stepError{id: id, err: err}
		}
		r.recordOutputs(data, id, outputs)
		hist.stepDone(id, i, step, "ok", outputs, "", false)
		entry := map[string]any{"event": "step", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "step": id}
		if len(outputs) > 0 && r.Secrets != nil {
			entry["outputs"] = r.Secrets.RedactValue(outputs)
		}
		r.audit(entry)
		// Step-done hooks see the step's own output (position-scoped).
		r.runHooks(ctx, t, step.Hooks, "done", data, "step "+id)
		r.checkpoint(ctx, run, i, id, step, outputs, checkpoint)
	}
	return nil
}

// recordOutputs makes a step's outputs addressable both ways: the connectors
// grammar's {{.<id>.<field>}} and the legacy {{.steps.<id>.outputs.<field>}}.
func (r *Runner) recordOutputs(data map[string]any, id string, outputs map[string]any) {
	if outputs == nil {
		outputs = map[string]any{}
	}
	data[id] = outputs
	if so, ok := data["steps"].(map[string]any); ok {
		so[id] = map[string]any{"outputs": outputs}
	}
}

// checkpoint advances the run past a completed top-level step. Outputs are
// scrubbed before they touch disk: tainted values (vault reads) never
// persist cleartext — see scrubOutputs. The run's spend tally rides along so
// the persisted record carries cost (#36 §14).
func (r *Runner) checkpoint(ctx context.Context, run *store.WorkflowRun, i int, id string, step config.Step, outputs map[string]any, active bool) {
	if !active || run.ID == "" {
		return
	}
	run.StepIndex = i + 1
	if outputs != nil {
		if run.Outputs == nil {
			run.Outputs = map[string]map[string]any{}
		}
		run.Outputs[id] = r.scrubOutputs(step, outputs)
	}
	run.Tokens, run.CostUSD, run.ApproxCost = costAccFrom(ctx).totals()
	_ = r.Store.PutRun(*run)
}

// reresolveMarker flags a checkpointed step whose outputs were secret
// material: the values are NOT on disk; a resume re-runs the (idempotent,
// read-only) verb to rebuild them.
const reresolveMarker = "__reresolve__"

// scrubOutputs returns the persistable form of a step's outputs: clean
// outputs pass through; a tainted vault-read step persists only a re-resolve
// marker (the resume re-reads the vault, so templating still works); other
// tainted outputs persist REDACTED — the secret never reaches disk, at the
// documented cost that resumed templates render the placeholder for those
// exact values (see Secrets.md).
func (r *Runner) scrubOutputs(step config.Step, outputs map[string]any) map[string]any {
	if len(outputs) == 0 || r.Secrets == nil {
		return outputs
	}
	red, ok := r.Secrets.RedactValue(outputs).(map[string]any)
	if !ok || reflect.DeepEqual(red, outputs) {
		return outputs // no tracked secret inside
	}
	if connName, _, _ := strings.Cut(step.Uses, "."); connName != "" {
		if _, isVault := r.Cfg.Vaults[connName]; isVault {
			return map[string]any{reresolveMarker: true}
		}
	}
	return red
}

// restoreOutputs rebuilds one checkpointed step's outputs on resume: a
// re-resolve marker re-runs the step's verb (a vault read) against the
// restored scope; anything else restores as persisted.
func (r *Runner) restoreOutputs(ctx context.Context, t core.Trigger, steps []config.Step, id string, out map[string]any, data map[string]any) map[string]any {
	if out == nil || out[reresolveMarker] != true {
		return anyMap(out)
	}
	for i, s := range steps {
		if stepID(s, i) != id || s.Uses == "" {
			continue
		}
		connName, verb, _ := strings.Cut(s.Uses, ".")
		in, ok := r.Conns.Get(connName)
		if !ok {
			break
		}
		merged := connector.MergeOptions(in.DefaultOptions, s.Options)
		rendered, err := renderOptions(merged, data)
		if err != nil {
			r.Log("%s resume: re-resolve %s options: %v", flowTag(t), id, err)
			break
		}
		fresh, err := in.InvokeFinal(ctx, verb, rendered)
		if err != nil {
			r.Log("%s resume: re-resolve %s: %v", flowTag(t), id, err)
			break
		}
		r.auditVerb(t, connName, verb, map[string]any{"resume_reresolve": id}, "ok", nil)
		return fresh
	}
	return map[string]any{}
}

func (r *Runner) finishRun(ctx context.Context, run store.WorkflowRun) {
	if run.ID != "" {
		_ = r.Store.DeleteRun(run.ID)
	}
	// A run's artifacts are GC'd with it (#36 §21): drop its blob references
	// and delete anything no other run still holds. Released by the per-EXECUTION
	// blob owner (H2), not run.ID — so a re-trigger of the same dedup key is not
	// tombstoned out of its own blob namespace.
	r.releaseRunBlobs(blob.OwnerFrom(ctx))
}

// execStepWithFlow wraps one step's execution with the control-flow
// modifiers: for_each fan-out, parallel branches, timeout, and retry.
func (r *Runner) execStepWithFlow(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, error) {
	if step.Timeout.D() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, step.Timeout.D())
		defer cancel()
	}

	// Parallel branches: run each branch's steps concurrently on a copy of
	// the scope, then join and merge their step outputs back.
	if step.Parallel != nil && len(step.Parallel.Branches) > 0 {
		return r.execBranches(ctx, t, step, id, data, shadow)
	}

	if step.ForEach != "" {
		return r.execForEach(ctx, t, step, id, slot, data, shadow)
	}

	return r.execWithRetry(ctx, t, step, id, slot, data, shadow)
}

// execBranches runs `parallel: [[…],[…]]` branch lists concurrently.
func (r *Runner) execBranches(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	// Same fan-out cap as for_each (#36 §146 F3): bound the number of branches
	// a single parallel step spawns concurrently.
	if r.Cfg != nil {
		if max := r.Cfg.FlowMaxFanOut(); len(step.Parallel.Branches) > max {
			return nil, fmt.Errorf("parallel fans out to %d branches, over policy.max_fan_out %d", len(step.Parallel.Branches), max)
		}
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		outs = map[string]any{}
	)
	for bi, branch := range step.Parallel.Branches {
		wg.Add(1)
		go func(bi int, branch []config.Step) {
			defer wg.Done()
			local := cloneData(data)
			local["steps"] = map[string]any{}
			var localRun store.WorkflowRun
			err := r.runSteps(ctx, &localRun, t, branch, local, shadow, false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("branch %d: %w", bi+1, err))
				return
			}
			// Publish each branch step's outputs into the join scope.
			for bj, bs := range branch {
				bid := stepID(bs, bj)
				if v, ok := local[bid]; ok {
					outs[bid] = v
				}
			}
		}(bi, branch)
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, errs[0]
	}
	// Merge joined outputs into the parent scope so later steps read them.
	for k, v := range outs {
		if m, ok := v.(map[string]any); ok {
			r.recordOutputs(data, k, m)
		}
	}
	return map[string]any{"branches": len(step.Parallel.Branches)}, nil
}

// execForEach fans one step over a collection; {{.item}} / {{.index}} are in
// scope per iteration. With parallel: true iterations run concurrently
// (bounded), else in order. Outputs: { items: [each iteration's outputs] }.
func (r *Runner) execForEach(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, error) {
	items, err := resolveList(step.ForEach, data)
	if err != nil {
		return nil, fmt.Errorf("for_each: %w", err)
	}
	// Fan-out cap (#36 §146 F3): the list can be data-driven and externally
	// influenced, so refuse an oversized one rather than spawning an unbounded
	// number of dispatches. Checked before any iteration runs.
	if r.Cfg != nil {
		if max := r.Cfg.FlowMaxFanOut(); len(items) > max {
			return nil, fmt.Errorf("for_each fans out to %d items, over policy.max_fan_out %d", len(items), max)
		}
	}
	results := make([]any, len(items))
	concurrent := step.Parallel != nil && step.Parallel.Concurrent
	var firstErr error
	runOne := func(i int) error {
		local := cloneData(data)
		local["item"] = items[i]
		local["index"] = i
		out, err := r.execWithRetry(ctx, t, step, fmt.Sprintf("%s[%d]", id, i), fmt.Sprintf("%s[%d]", slot, i), local, shadow)
		if err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
		results[i] = out
		return nil
	}
	if concurrent {
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, 8) // bounded fan-out
		for i := range items {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if err := runOne(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
	} else {
		for i := range items {
			if err := runOne(i); err != nil {
				firstErr = err
				break
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return map[string]any{"items": results, "count": len(items)}, nil
}

// resolveList resolves a for_each expression to a list: a sole {{.path}}
// reference to a list value, or a rendered comma/newline-separated string.
func resolveList(exprStr string, data map[string]any) ([]any, error) {
	if path, ok := soleFieldRef(exprStr); ok {
		v, found := lookupPath(data, path)
		if !found {
			return nil, fmt.Errorf("%q resolves to nothing", exprStr)
		}
		switch x := v.(type) {
		case []any:
			return x, nil
		case []string:
			out := make([]any, len(x))
			for i, s := range x {
				out[i] = s
			}
			return out, nil
		default:
			return nil, fmt.Errorf("%q is %T, not a list", exprStr, v)
		}
	}
	s, err := render(exprStr, data)
	if err != nil {
		return nil, err
	}
	var out []any
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// execWithRetry wraps execStep with the error-retry half of retry: (max /
// backoff) and the defer-retry half (while_output_matches / interval /
// timeout — re-run while the output still says "not ready").
func (r *Runner) execWithRetry(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, error) {
	max := 0
	backoff := 10 * time.Second
	if step.Retry != nil {
		max = step.Retry.Max
		if d := step.Retry.Backoff.D(); d > 0 {
			backoff = d
		}
	}
	var out map[string]any
	var raw string
	var err error
	for attempt := 0; ; attempt++ {
		out, raw, err = r.execStep(ctx, t, step, id, slot, data, shadow)
		if err == nil || attempt >= max || ctx.Err() != nil {
			break
		}
		r.Log("%s step %s attempt %d failed: %v — retrying in %s", flowTag(t), id, attempt+1, err, backoff)
		if serr := r.sleep(ctx, backoff); serr != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	// Defer-retry: the step exited cleanly but reports it isn't done yet.
	if step.Retry != nil && step.Retry.WhileOutputMatches != "" && !shadow {
		re, rerr := regexp.Compile(step.Retry.WhileOutputMatches)
		if rerr != nil {
			return nil, fmt.Errorf("retry.while_output_matches: %w", rerr)
		}
		interval := step.Retry.Interval.D()
		if interval <= 0 {
			interval = time.Minute
		}
		deadline := time.Now().Add(retryTimeout(step.Retry))
		for re.MatchString(raw) {
			if time.Now().After(deadline) {
				r.Log("%s step %s still deferred after %s — giving up", flowTag(t), id, retryTimeout(step.Retry))
				break
			}
			if serr := r.sleep(ctx, interval); serr != nil {
				return out, nil
			}
			out, raw, err = r.execStep(ctx, t, step, id, slot, data, shadow)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func retryTimeout(rs *config.RetrySpec) time.Duration {
	if d := rs.Timeout.D(); d > 0 {
		return d
	}
	return 15 * time.Minute
}

// execStep runs one step form once. raw is the unparsed output (for
// while_output_matches).
func (r *Runner) execStep(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, string, error) {
	switch step.Form() {
	case "verb":
		out, err := r.execVerb(ctx, t, step, id, data, shadow)
		return out, "", err
	case "workflow":
		out, err := r.execWorkflowCall(ctx, t, step, id, data, shadow)
		return out, "", err
	case "code":
		return r.execCode(ctx, t, step, id, data, shadow)
	case "agent":
		return r.execAgent(ctx, t, step, id, slot, data, shadow)
	case "command":
		return r.execCommand(ctx, t, step, id, data, shadow)
	case "team":
		return r.execTeam(ctx, t, step, id, data, shadow)
	}
	return nil, "", fmt.Errorf("step %q has no recognizable form", id)
}

// execVerb invokes uses: <connector>.<verb> with rendered, merged options.
// skipBotReply reports whether a verb call is a conversational reply back to
// the bot that authored the triggering event, under reply_to_bots=off: a
// comment/reply verb on a github connector. The substantive work (fixes,
// labels, thread resolution) is never gated here.
func (r *Runner) skipBotReply(ctx context.Context, in *connector.Instance, verb string) (string, bool) {
	st, ok := botReply(ctx)
	if !ok || !st.authorIsBot || st.mode != config.ReplyToBotsOff {
		return "", false
	}
	if in.Decl == nil || in.Decl.Type != "github" {
		return "", false
	}
	if verb != "comment" && verb != "reply" {
		return "", false
	}
	return st.login, true
}

func (r *Runner) execVerb(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	connName, verb, _ := strings.Cut(step.Uses, ".")
	in, ok := r.Conns.Get(connName)
	if !ok {
		return nil, fmt.Errorf("unknown connector %q", connName)
	}
	if login, skip := r.skipBotReply(ctx, in, verb); skip {
		r.Log("%s reply_to_bots=off: skipped %s.%s to bot %s", flowTag(t), connName, verb, login)
		r.auditVerb(t, connName, verb, nil, "skipped_reply_to_bots", nil)
		return map[string]any{"skipped": true}, nil
	}
	merged := connector.MergeOptions(in.DefaultOptions, step.Options)
	rendered, err := renderOptions(merged, data)
	if err != nil {
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	// The run history records the step's RENDERED inputs (#36 §20) — the
	// handle-form options, scrubbed of tracked secret values like the audit.
	historySetInputs(ctx, id, map[string]any{"uses": step.Uses, "options": rendered})
	// workflow.* runs in the flow runner itself (it owns the scope, the
	// depth guard, and the plan guard). Not stubbed under shadow — the
	// called workflow's own steps stub instead, so a dry-run previews the
	// whole tree; workflow.save persists nothing real either way (audited).
	if connName == "workflow" {
		out, err := r.execWorkflowVerb(ctx, t, verb, rendered, data, shadow)
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		r.auditVerb(t, connName, verb, map[string]any{"name": rendered["name"], "reason": rendered["reason"]}, outcome, err)
		if err != nil {
			return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
		}
		return out, nil
	}
	if shadow {
		r.Log("%s [dry-run] would invoke %s.%s", flowTag(t), connName, verb)
		r.auditVerb(t, connName, verb, rendered, "stubbed", nil)
		return stubOutputs(in, verb), nil
	}
	// The resource-allowlist runtime belt (#124): an agent-authored step's
	// RENDERED options carry the concrete store/repo names the static guard
	// couldn't evaluate — refuse + audit outside the allowlists.
	if agentAuthored(ctx) {
		if rerr := r.checkVerbResources(r.planPolicy(), t, step.Uses, rendered); rerr != nil {
			rerr = fmt.Errorf("agent_authored allowlist: %w", rerr)
			r.auditVerb(t, connName, verb, map[string]any{"barrier": "resource_allowlist"}, "blocked", rerr)
			return nil, rerr
		}
	}
	// The plan write barrier (no_secret_egress, runtime half): an unapproved
	// agent plan may not park secret material in durable shared state — the
	// static guard gates the vault-read+kv-write combos it can SEE; this
	// catches values laundered through step outputs or the trigger context.
	if planBarrier(ctx) && isInternalWrite(step.Uses) && r.containsTrackedSecret(rendered) {
		err := fmt.Errorf("no_secret_egress: refusing to write secret material into %s from an agent plan — approval required", step.Uses)
		r.auditVerb(t, connName, verb, map[string]any{"barrier": "secret_write"}, "blocked", err)
		return nil, err
	}
	// The relay barrier (the READ half): an unapproved plan may not hand
	// tracked secret material to an EXTERNAL connector either — the
	// read-and-relay path (kv.get of a parked secret → gh.comment) that the
	// static scan can't see, because reading a store isn't secret access
	// until the value comes back.
	if planBarrier(ctx) && !internalConnectors[connName] && r.containsTrackedSecret(rendered) {
		err := fmt.Errorf("no_secret_egress: refusing to send secret material to %s from an agent plan — approval required", step.Uses)
		r.auditVerb(t, connName, verb, map[string]any{"barrier": "secret_relay"}, "blocked", err)
		return nil, err
	}
	// The {{secret}} egress boundary: swap eligible handles for real values
	// in what goes OUT — audit keeps the handle-form `rendered` map. Only
	// names literally called in this step's own raw options are eligible, and
	// agent-authored execution resolves nothing (see handles.go).
	final := rendered
	if eligible := secretCallsIn(merged); len(eligible) > 0 {
		rv, rerr := r.resolveSecretHandles(ctx, rendered, eligible)
		if rerr != nil {
			r.auditVerb(t, connName, verb, rendered, "failed", rerr)
			return nil, fmt.Errorf("uses %s: %w", step.Uses, rerr)
		}
		final = rv.(map[string]any)
	}
	// Verb-level binary IO (#36 §21): declared binary-in options resolve
	// from blob handles to local paths; declared binary-out outputs come
	// back as bytes and leave as run-scoped handles.
	decl, _ := in.Decl.Verb(verb)
	if final, err = r.stageBlobInputs(ctx, decl, final); err != nil {
		r.auditVerb(t, connName, verb, rendered, "failed", err)
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	start := time.Now()
	out, err := in.InvokeFinal(ctx, verb, final)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		r.auditVerb(t, connName, verb, rendered, "failed", err)
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	if out, err = r.storeBlobOutputs(ctx, decl, out); err != nil {
		r.auditVerb(t, connName, verb, rendered, "failed", err)
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	r.Log("%s step %s (%s.%s) done in %s", flowTag(t), id, connName, verb, took)
	r.auditVerb(t, connName, verb, rendered, "ok", nil)
	return out, nil
}

// auditVerb records one verb invocation with secrets redacted from options.
func (r *Runner) auditVerb(t core.Trigger, conn, verb string, opts map[string]any, outcome string, err error) {
	entry := map[string]any{
		"event": "verb", "connector": conn, "verb": verb, "outcome": outcome,
		"repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind,
	}
	if r.Secrets != nil {
		entry["options"] = r.Secrets.RedactValue(opts)
	}
	if err != nil {
		entry["error"] = r.redactErr(err)
	}
	r.audit(entry)
}

// redactErr scrubs tracked secret values from an error string before it
// reaches an audit entry, a hook scope, or a step output — the options and
// outputs beside it are already redacted; the error must not be the leak.
func (r *Runner) redactErr(err error) string {
	if err == nil {
		return ""
	}
	if r.Secrets == nil {
		return err.Error()
	}
	return r.Secrets.Redact(err.Error())
}

// stubOutputs synthesizes zero-valued outputs matching a verb's output
// schema, so a dry-run's later steps still resolve their references.
func stubOutputs(in *connector.Instance, verb string) map[string]any {
	out := map[string]any{"stubbed": true}
	if v, ok := in.Decl.Verb(verb); ok {
		for k, f := range v.Outputs {
			switch f.Type {
			case connector.TInt, connector.TFloat:
				out[k] = 0
			case connector.TBool:
				out[k] = false
			case connector.TList:
				out[k] = []any{}
			case connector.TMap:
				out[k] = map[string]any{}
			default:
				out[k] = ""
			}
		}
	}
	return out
}

// workflowDepthKey tracks nested workflow-call depth on the context. Static
// names are cycle-checked at load; a dynamic (templated) name skips that, so
// every call is depth-guarded at runtime instead — exceed → halt with a
// clear error, never spin.
type workflowDepthKey struct{}

// MaxWorkflowDepth caps runtime workflow-call nesting (dynamic names,
// agent-chosen workflows, saved workflows calling workflows).
const MaxWorkflowDepth = 8

// execWorkflowCall runs { workflow: <name>, with: {…} } — an encapsulated
// child scope seeded with the trigger context + declared inputs; the caller
// reads the workflow's declared outputs off this step's id. The name may be
// templated ("{{.pick}}"), resolved at runtime against the workflow set.
func (r *Runner) execWorkflowCall(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	name, err := render(step.Workflow, data)
	if err != nil {
		return nil, fmt.Errorf("workflow name %q: %w", step.Workflow, err)
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("workflow name %q resolved empty", step.Workflow)
	}
	depth, _ := ctx.Value(workflowDepthKey{}).(int)
	if depth >= MaxWorkflowDepth {
		return nil, fmt.Errorf("workflow %q: call depth %d exceeds the limit %d (dynamic workflow names are depth-guarded)", name, depth, MaxWorkflowDepth)
	}
	ctx = context.WithValue(ctx, workflowDepthKey{}, depth+1)
	wf, ok := r.lookupWorkflow(name)
	if !ok {
		return nil, fmt.Errorf("unknown workflow %q (defined: %s)", name, r.workflowNames())
	}
	// A saved (agent-promoted) workflow earns trust before unattended reuse:
	// unreviewed ones dry-run freely but refuse a real run (trust: full is
	// the deliberate bypass). Real outcomes feed its health record.
	var saved *SavedWorkflow
	if _, fromConfig := r.Cfg.Workflows[name]; !fromConfig {
		if st := SavedWorkflows(); st != nil {
			if w, isSaved := st.Get(name); isSaved {
				saved = &w
			}
		}
	}
	if saved != nil && !saved.Reviewed && !shadow && !r.planPolicy().TrustFull() {
		return nil, fmt.Errorf("saved workflow %q (v%d) is unreviewed — dry-run it, then `conductor workflows review %s` (or trust: full) before real runs", name, saved.Version, name)
	}
	// A saved workflow is agent-authored no matter who invokes it: re-guard
	// its steps under the CURRENT policy on every run (review is a human
	// trust signal, not a policy bypass — a policy tightened after the save
	// applies immediately), and approval-gated steps go through the same
	// dry-run + hand-off gate as an inline plan.
	steps := wf.Steps
	if saved != nil {
		guarded, gerr := r.guardSavedWorkflow(ctx, t, name, saved, shadow)
		if gerr != nil {
			return nil, gerr
		}
		steps = guarded
	}
	with, err := renderOptions(step.With, data)
	if err != nil {
		return nil, fmt.Errorf("with: %w", err)
	}
	inputs := map[string]any{}
	for in, spec := range wf.Inputs {
		v, present := with[in]
		if !present {
			if spec.Required {
				return nil, fmt.Errorf("workflow %q: missing required input %q", name, in)
			}
			if spec.Default != nil {
				v = spec.Default
			}
		}
		inputs[in] = v
	}
	for in := range with {
		if _, declared := wf.Inputs[in]; !declared {
			return nil, fmt.Errorf("workflow %q: unknown input %q", name, in)
		}
	}
	// The child sees the trigger context + its inputs + its own steps — not
	// the caller's other step outputs (pass those via with:).
	child := baseData(t, r.SecretVals)
	addVaultData(child, r.VaultVals)
	if saved != nil {
		// A saved workflow is agent-authored: it runs under the SAME scope
		// contract as an inline plan (planScope) — no named secrets, no
		// preloaded vault values. With the full maps in scope, a whole-root
		// template dump ({{printf "%v" $}}) exfiltrates the secret map while
		// mentioning neither "secrets" nor "vaults", sailing past the egress
		// detector. Present-but-empty so a stray reference renders "".
		child = baseData(t, nil)
		child["secrets"] = map[string]any{}
		child["vaults"] = map[string]any{}
		// And {{secret}} boundary handles never resolve in its steps.
		ctx = markAgentAuthored(ctx)
	}
	child["inputs"] = inputs
	child["steps"] = map[string]any{}
	if g, ok := data["group"]; ok {
		child["group"] = g
	}
	var childRun store.WorkflowRun
	// The workflow's own default gate (#36 §16) governs ITS agent steps; a
	// SAVED workflow additionally stamps its name so agent-step engagements
	// attribute outcomes to it (#36 §18).
	stepCtx := withDefaultGate(ctx, wf.Gate)
	if saved != nil {
		stepCtx = context.WithValue(stepCtx, savedWFKey{}, name)
	}
	runErr := r.runSteps(stepCtx, &childRun, t, steps, child, shadow, false)
	if saved != nil && !shadow {
		SavedWorkflows().RecordOutcome(name, runErr == nil)
	}
	if runErr != nil {
		return nil, fmt.Errorf("workflow %q: %w", name, runErr)
	}
	outputs := map[string]any{}
	for out, tmpl := range wf.Outputs {
		v, err := renderValue(tmpl, child)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: output %q: %w", name, out, err)
		}
		outputs[out] = v
	}
	return outputs, nil
}

// lookupWorkflow resolves a runtime workflow name: the config's workflows:
// section first, then the saved (agent-promoted) registry.
func (r *Runner) lookupWorkflow(name string) (config.WorkflowDef, bool) {
	if wf, ok := r.Cfg.Workflows[name]; ok {
		return wf, true
	}
	return savedWorkflowDef(name)
}

// workflowNames lists every invocable workflow, for unknown-name errors.
func (r *Runner) workflowNames() string {
	names := make([]string, 0, len(r.Cfg.Workflows))
	for n := range r.Cfg.Workflows {
		names = append(names, n)
	}
	names = append(names, savedWorkflowNames()...)
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// execCode runs a run: code step through internal/code, remotely when the
// step names a host.
func (r *Runner) execCode(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	if shadow {
		r.Log("%s [dry-run] would run code step (%s)", flowTag(t), step.Run)
		return map[string]any{"stubbed": true}, "", nil
	}
	env, err := renderStringMap(step.Env, data)
	if err != nil {
		return nil, "", err
	}
	args, err := renderStrings(step.Args, data)
	if err != nil {
		return nil, "", err
	}
	// The {{secret}} egress boundary for code steps: conductor executes the
	// code itself, so eligible handles in env/args resolve here — never for
	// agent-authored steps (see handles.go).
	if eligible := secretCallsIn(step.Env, step.Args); len(eligible) > 0 {
		if env, err = r.resolveHandleStringMap(ctx, env, eligible); err != nil {
			return nil, "", err
		}
		if args, err = r.resolveHandleStrings(ctx, args, eligible); err != nil {
			return nil, "", err
		}
	}
	workdir, err := render(step.WorkDir, data)
	if err != nil {
		return nil, "", err
	}
	spec := code.Spec{Run: step.Run, Code: step.Code, Args: args, Env: env, WorkDir: workdir,
		DataGuard: r.planDataGuard(ctx)}
	if target, terr := r.hostTarget(step); terr != nil {
		return nil, "", terr
	} else if target != nil {
		spec.Host = target
	}
	// ctx (the injected data) excludes the internal steps index; code reads
	// outputs at the top level.
	out, err := r.Code.Exec(ctx, spec, codeCtx(data))
	if err != nil {
		return nil, "", err
	}
	raw := ""
	if s, ok := out["text"].(string); ok {
		raw = s
	} else if b, jerr := json.Marshal(out); jerr == nil {
		raw = string(b)
	}
	return out, raw, nil
}

// codeCtx strips the legacy steps index and the secret scopes — secrets AND
// preloaded vault values — from the ctx handed to user code (secrets reach
// code only via explicitly templated args/env). Leaving vaults in exposed
// every preloaded vault entry to any code step as ctx.vaults.*.
func codeCtx(data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		if k == "steps" || k == "secrets" || k == "vaults" {
			continue
		}
		out[k] = v
	}
	return out
}

// hostTarget resolves a step's host:/ssh: to an SSH target (nil = local).
func (r *Runner) hostTarget(step config.Step) (*hosts.Target, error) {
	if step.SSH != nil {
		return &hosts.Target{Name: "(inline)", Cfg: *step.SSH}, nil
	}
	if step.Host == "" {
		return nil, nil
	}
	hc, ok := r.Cfg.Hosts[step.Host]
	if !ok {
		return nil, fmt.Errorf("unknown host %q", step.Host)
	}
	return &hosts.Target{Name: step.Host, Cfg: hc}, nil
}

// triggerLabel names a trigger for diagnostics.
func triggerLabel(spec config.TriggerSpec) string {
	if spec.Name != "" {
		return "trigger " + spec.Name
	}
	return "trigger " + spec.On
}

// runtimeOf is the `runtimes:` entry a step executes on — its own pin, else
// the fleet default. It is the budget anchor (design §1) and half the
// session-affinity partition (§3).
func (r *Runner) runtimeOf(step config.Step) string {
	if step.Runtime != "" {
		return step.Runtime
	}
	if r.Cfg != nil {
		if def := r.Cfg.DefaultRuntimeName(); def != "" {
			return def
		}
	}
	return config.BuiltinPaseoRuntime
}

// execAgent dispatches a type: agent step through the engine-provided
// services (runtime resolution, tokens, guidance, background hand-off).
func (r *Runner) execAgent(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, string, error) {
	// `agent:` is retained as a free-form ATTRIBUTION label on the dispatch
	// (it used to name a profile; profiles are gone — design §6). It may be
	// templated, so render it before use; step is a value copy.
	if strings.Contains(step.Agent, "{{") {
		if rendered, err := render(step.Agent, data); err == nil {
			step.Agent = strings.TrimSpace(rendered)
		}
	}
	// The step IS the profile now (design §6), and its identity is the key
	// memory, sessions, and outcomes use (§5).
	identity := stepIdentity(ctx, step, slot)
	// The RESOLVED model — "" is a bare launch, a first-class outcome.
	model := ""
	if r.Agents.ResolveModel != nil {
		model = r.Agents.ResolveModel(ctx, step)
	}
	// Crash resume: a persisted plan checkpoint for this run+step means the
	// agent already ran and its plan was interrupted mid-way — resume the
	// PLAN after its last committed step instead of re-dispatching the agent
	// (which would re-run committed side effects).
	runID := memory.SourceFrom(ctx).Run
	if !shadow && !step.Background && runID != "" && r.Store != nil {
		if rec, ok := r.Store.GetPlan(runID, id); ok {
			r.Log("%s resuming interrupted plan for step %s (from step %d)", flowTag(t), id, rec.Next)
			planOut, plErr := r.resumePlan(ctx, t, rec, shadow)
			outputs := map[string]any{"resumed_plan": true}
			if planOut != nil {
				outputs["plan"] = planOut
			}
			if plErr != nil {
				return outputs, "", fmt.Errorf("agent plan (resumed): %w", plErr)
			}
			return outputs, "", nil
		}
	}
	act := config.Action{
		// Agent is the human ATTRIBUTION LABEL the operator wrote (it selects
		// nothing — design §6). The stable key everything else uses travels
		// as Request.Identity.
		Type: "agent", ID: id, Agent: step.Agent,
		Prompt: step.Prompt, Checkout: step.Checkout, WorkDir: step.WorkDir,
		Env: step.Env, OutputSchema: step.OutputSchema, Background: step.Background,
		Backend: step.Backend, RerequestReview: step.RerequestReview,
	}
	if step.Background {
		// A background step hands off a live agent for you to drive and close
		// yourself; it sits idle *because* it is waiting for you, so the
		// reaper must never archive it — regardless of what the step says.
		step.ArchiveWhenDone = false
	}
	if act.Prompt != "" {
		act.Prompt += dispatch.WriteWrapperGuidance
		if r.Agents.Guidance != nil {
			act.Prompt += r.Agents.Guidance(identity, step, policyFrom(ctx))
		}
		// Opt-in shared memory rides the same append path as guidance; a step
		// without memory: gets nothing (no token cost).
		if r.Agents.Memory != nil {
			_, wfScope := budgetFrom(ctx)
			act.Prompt += r.Agents.Memory(identity, step, t, wfScope)
		}
		// A bot author can't read pleasantries: under decline_only (the
		// default) the agent fixes silently and replies only to decline.
		if st, ok := botReply(ctx); ok && st.authorIsBot && st.mode == config.ReplyToBotsDeclineOnly {
			act.Prompt += dispatch.BotReplyGuidance
		}
		if act.RerequestReview {
			act.Prompt += dispatch.RerequestReviewGuidance
		}
		if step.Background {
			act.Prompt += dispatch.HandoffGuidance
		}
	}
	var tokens dispatch.Tokens
	if r.Agents.Tokens != nil {
		tokens = r.Agents.Tokens(t)
	}
	// Spend budget (#36 §14): an over-cap dispatch sheds — the step fails
	// with the budget error (the workflow's fail path notifies, and the
	// sweep/backoff machinery re-derives PR-kind work once the window frees).
	// An admitted dispatch reserves its estimated spend (#36 review H7).
	var spendRes *cost.Reservation
	est := cost.Estimate(model, act.Prompt, "")
	if !shadow {
		res, berr := r.checkBudget(ctx, r.runtimeOf(step), est)
		if berr != nil {
			return nil, "", berr
		}
		spendRes = res
	}
	historySetInputs(ctx, id, map[string]any{"agent": identity, "prompt": clipText(act.Prompt, 4000)})
	req := dispatch.Request{
		Trigger: t, Action: act, Step: step, Identity: identity, Model: model, Tokens: tokens,
		Shadow: shadow, Wait: !step.Background, Interactive: step.Background, Data: data,
		AgentAuthored: agentAuthored(ctx),
	}
	ref, err := r.Agents.Dispatch(ctx, req)
	switch {
	case shadow || ref.Shadowed || ref.Skipped || ref.Queued || err != nil:
		if r.Agents.CancelBudget != nil {
			r.Agents.CancelBudget(spendRes)
		}
	case step.Background:
		// Background/hand-off dispatch (#36 §146 F1): ref.Output is the paseo
		// LAUNCH confirmation (an agent id), NOT the agent's transcript, so
		// cost.FromRun would settle this reservation at a bogus ~0. A
		// fire-and-forget agent's real usage is unobtainable (paseo exposes no
		// cumulative cost at inspect time), so we never settle a known-wrong
		// figure: the reservation stays OPEN, holding its estimated spend
		// against the caps until the meter's reservationMaxAge backstop reclaims
		// it. The estimate is still tallied on the run record / history / audit
		// as approximate so reporting shows a provisional charge, not nothing.
		r.recordBackgroundEstimate(ctx, t, identity, r.runtimeOf(step), id, est)
	default:
		r.recordUsage(ctx, t, identity, r.runtimeOf(step), id, spendRes, cost.FromRun(model, act.Prompt, ref.Output))
	}
	if err != nil {
		return nil, ref.Output, err
	}
	if step.Background {
		if r.Agents.Background != nil {
			r.Agents.Background(ctx, t, id, identity, step, ref, step.Handoff)
		}
		return map[string]any{"agent_id": ref.AgentID, "background": true}, "", nil
	}
	if !shadow {
		r.harvestMemory(ctx, t, identity, step, ref.Output)
	}
	outputs := extractOutputs(ref.Output)
	outputs["agent_id"] = ref.AgentID
	// The quality gate (#36 §16): the agent's PROPOSED change must pass its
	// checks before this step's outputs promote. Runs while the agent is
	// still live so a failure can loop back as a revise follow-up.
	if gspec := r.effectiveGate(ctx, step); gspec != nil && !shadow {
		rounds, gerr := r.runGate(ctx, t, step, id, slot, gspec, ref, data)
		if gerr != nil {
			return outputs, ref.Output, gerr
		}
		outputs["gate"] = map[string]any{"passed": true, "rounds": rounds}
	}
	// Diff preview (#36 §17): the PROPOSED change (uncommitted + unpushed)
	// read from the agent's worktree lands in the step's outputs — later
	// steps can present it on an ask verb ({{.<id>.diff}}) for approval
	// before anything applies it, and the run record persists it (scrubbed,
	// clipped) with the step timeline and cost. Best-effort: an unreadable
	// worktree never fails a step that already succeeded.
	if !shadow && !step.Background && ref.Workdir != "" && !inGate(ctx) {
		outputs["workdir"] = ref.Workdir
		if diff, derr := gitdiff.Proposed(ctx, ref.Workdir, 0); derr == nil && diff != "" {
			if r.Secrets != nil {
				diff = r.Secrets.Redact(diff)
			}
			outputs["diff"] = diff
		}
	}
	// The plan output contract (#36 §11): a plan: block in the final output
	// is validated, guarded by policy.agent_authored, and executed through
	// this same runner. A malformed or rejected plan fails the step — the
	// plan was its purpose.
	if plan, found, perr := ParsePlan(ref.Output); perr != nil {
		return nil, ref.Output, perr
	} else if found {
		planOut, plErr := r.runPlan(ctx, t, identity, runID, id, plan, shadow)
		if planOut != nil {
			outputs["plan"] = planOut
		}
		if plErr != nil {
			return outputs, ref.Output, fmt.Errorf("agent plan: %w", plErr)
		}
	}
	if step.ArchiveWhenDone && ref.AgentID != "" && r.Agents.Archive != nil {
		r.Agents.Archive(ref.AgentID)
	}
	return outputs, ref.Output, nil
}

// harvestMemory applies the memory output contract to an agent step's final
// output: a `remember:` block (fenced or a JSON key) persists with the run's
// provenance plus the agent's name. Best-effort — a malformed block is
// logged and audited, never a step failure.
func (r *Runner) harvestMemory(ctx context.Context, t core.Trigger, identity string, step config.Step, output string) {
	m := memory.Active()
	if m == nil || strings.TrimSpace(output) == "" {
		return
	}
	// Writing is opt-in per STEP, exactly like reading: only a step whose
	// memory: is enabled may harvest its output into the shared store.
	// Otherwise any dispatched agent — including one an untrusted event
	// steered — could poison shared memory via its output contract without
	// the operator ever granting it memory access (#57 M8).
	if step.Memory == nil || !step.Memory.Enabled {
		return
	}
	agent := identity
	src := memory.SourceFrom(ctx)
	src.Step = identity
	entries, err := m.HarvestOutput(output, src)
	if err != nil {
		r.Log("%s memory output contract: %v", flowTag(t), err)
		r.audit(map[string]any{"event": "memory_remember", "via": "output", "outcome": "failed",
			"repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind, "agent": agent, "error": err.Error()})
		return
	}
	for _, e := range entries {
		r.audit(map[string]any{"event": "memory_remember", "via": "output", "outcome": "ok",
			"repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind, "agent": agent,
			"id": e.ID, "scope": e.Scope})
	}
}

// execCommand runs a type: command step — locally through dispatch (the
// legacy path: templating, identity env, output capture), or remotely over
// SSH when the step names a host.
func (r *Runner) execCommand(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	if target, err := r.hostTarget(step); err != nil {
		return nil, "", err
	} else if target != nil {
		return r.execRemoteCommand(ctx, t, step, target, data, shadow)
	}
	act := config.Action{
		Type: "command", ID: id, Command: step.Command,
		WorkDir: step.WorkDir, Env: step.Env, Backend: step.Backend,
	}
	var tokens dispatch.Tokens
	if r.Agents.Tokens != nil {
		tokens = r.Agents.Tokens(t)
	}
	req := dispatch.Request{
		Trigger: t, Action: act, Tokens: tokens,
		Shadow: shadow, Wait: true, Data: data,
	}
	ref, err := r.Agents.Dispatch(ctx, req)
	if err != nil {
		return nil, ref.Output, err
	}
	outputs := extractOutputs(ref.Output)
	return outputs, ref.Output, nil
}

// execRemoteCommand runs a command step on a named host over SSH.
func (r *Runner) execRemoteCommand(ctx context.Context, t core.Trigger, step config.Step, target *hosts.Target, data map[string]any, shadow bool) (map[string]any, string, error) {
	argv, err := renderStrings(step.Command, data)
	if err != nil {
		return nil, "", err
	}
	if len(argv) == 0 {
		return nil, "", fmt.Errorf("command step has no command")
	}
	if shadow {
		r.Log("%s [dry-run] would run on %s: %s", flowTag(t), target.Name, strings.Join(argv, " "))
		return map[string]any{"stubbed": true, "stdout": "", "stderr": "", "exit_code": 0}, "", nil
	}
	env, err := renderStringMap(step.Env, data)
	if err != nil {
		return nil, "", err
	}
	// The {{secret}} egress boundary for remote commands: conductor itself
	// SSHes to a config-named host, so eligible handles in env/argv resolve
	// here. (Local `type: command` steps run through the agent runtime — the
	// handle stays opaque there; use a code step for conductor-side exec.)
	if eligible := secretCallsIn(step.Env, step.Command); len(eligible) > 0 {
		if env, err = r.resolveHandleStringMap(ctx, env, eligible); err != nil {
			return nil, "", err
		}
		if argv, err = r.resolveHandleStrings(ctx, argv, eligible); err != nil {
			return nil, "", err
		}
	}
	cwd, err := render(step.WorkDir, data)
	if err != nil {
		return nil, "", err
	}
	script := shellJoin(argv)
	client := r.Code.SSH
	if client == nil {
		client = &hosts.Client{}
	}
	res, err := client.Script(ctx, *target, script, nil, env, cwd)
	if err != nil {
		return nil, "", fmt.Errorf("host %s: %w", target.Name, err)
	}
	outputs := map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "exit_code": res.ExitCode}
	if res.ExitCode != 0 {
		return outputs, res.Stdout, fmt.Errorf("host %s: exit %d: %s", target.Name, res.ExitCode, tail(res.Stderr, 400))
	}
	return outputs, res.Stdout, nil
}

// shellJoin quotes an argv for `sh -c` execution.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}

// extractOutputs parses captured output into an outputs map (mirrors the
// legacy engine's behavior: a JSON object becomes the outputs, unwrapping the
// common paseo wrapper keys; anything else is exposed as .text).
func extractOutputs(out string) map[string]any {
	out = strings.TrimSpace(out)
	if out == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		for _, k := range []string{"output", "result", "outputs"} {
			if inner, ok := m[k].(map[string]any); ok {
				return inner
			}
		}
		return m
	}
	return map[string]any{"text": out}
}

// runHooks fires the hooks of one phase, in order, best-effort: a failing
// hook is logged and audited but never fails the workflow (matching the
// legacy slack-feedback semantics).
func (r *Runner) runHooks(ctx context.Context, t core.Trigger, hooks []config.Hook, phase string, data map[string]any, where string) {
	for i, h := range hooks {
		if h.At != phase {
			continue
		}
		if h.If != "" {
			ok, err := expr.Eval(h.If, data)
			if err != nil {
				r.Log("%s %s hook[%d] if-error: %v", flowTag(t), where, i, err)
				continue
			}
			if !ok {
				continue
			}
		}
		connName, verb, _ := strings.Cut(h.Uses, ".")
		in, ok := r.Conns.Get(connName)
		if !ok {
			r.Log("%s %s hook[%d]: unknown connector %q", flowTag(t), where, i, connName)
			continue
		}
		if login, skip := r.skipBotReply(ctx, in, verb); skip {
			r.Log("%s reply_to_bots=off: skipped %s.%s to bot %s", flowTag(t), connName, verb, login)
			r.auditVerb(t, connName, verb, nil, "skipped_reply_to_bots", nil)
			continue
		}
		merged := connector.MergeOptions(in.DefaultOptions, h.Options)
		rendered, err := renderOptions(merged, data)
		if err != nil {
			r.Log("%s %s hook[%d] render: %v", flowTag(t), where, i, err)
			r.auditVerb(t, connName, verb, nil, "hook_render_failed", err)
			continue
		}
		if connName == "workflow" {
			// Best-effort like any hook verb; runs in the flow runner.
			if _, werr := r.execWorkflowVerb(ctx, t, verb, rendered, data, r.DryRun); werr != nil {
				r.Log("%s %s hook %s.%s failed (best-effort): %v", flowTag(t), where, connName, verb, werr)
				r.auditVerb(t, connName, verb, rendered, "hook_failed", werr)
			} else {
				r.auditVerb(t, connName, verb, map[string]any{"name": rendered["name"]}, "ok", nil)
			}
			continue
		}
		if r.DryRun {
			r.Log("%s [dry-run] would invoke hook %s.%s (at: %s)", flowTag(t), connName, verb, phase)
			r.auditVerb(t, connName, verb, rendered, "stubbed", nil)
			continue
		}
		if agentAuthored(ctx) {
			if rerr := r.checkVerbResources(r.planPolicy(), t, h.Uses, rendered); rerr != nil {
				rerr = fmt.Errorf("agent_authored allowlist: %w", rerr)
				r.Log("%s %s hook %s.%s blocked: %v", flowTag(t), where, connName, verb, rerr)
				r.auditVerb(t, connName, verb, map[string]any{"barrier": "resource_allowlist"}, "blocked", rerr)
				continue
			}
		}
		if planBarrier(ctx) && isInternalWrite(h.Uses) && r.containsTrackedSecret(rendered) {
			berr := fmt.Errorf("no_secret_egress: refusing to write secret material into %s from an agent plan hook", h.Uses)
			r.Log("%s %s hook %s.%s blocked: %v", flowTag(t), where, connName, verb, berr)
			r.auditVerb(t, connName, verb, map[string]any{"barrier": "secret_write"}, "blocked", berr)
			continue
		}
		if planBarrier(ctx) && !internalConnectors[connName] && r.containsTrackedSecret(rendered) {
			berr := fmt.Errorf("no_secret_egress: refusing to send secret material to %s from an agent plan hook", h.Uses)
			r.Log("%s %s hook %s.%s blocked: %v", flowTag(t), where, connName, verb, berr)
			r.auditVerb(t, connName, verb, map[string]any{"barrier": "secret_relay"}, "blocked", berr)
			continue
		}
		// Same {{secret}} egress boundary as execVerb; audit keeps handles.
		final := rendered
		if eligible := secretCallsIn(merged); len(eligible) > 0 {
			rv, rerr := r.resolveSecretHandles(ctx, rendered, eligible)
			if rerr != nil {
				r.Log("%s %s hook %s.%s: %v", flowTag(t), where, connName, verb, rerr)
				r.auditVerb(t, connName, verb, rendered, "hook_failed", rerr)
				continue
			}
			final = rv.(map[string]any)
		}
		if _, err := in.InvokeFinal(ctx, verb, final); err != nil {
			r.Log("%s %s hook %s.%s failed (best-effort): %v", flowTag(t), where, connName, verb, err)
			r.auditVerb(t, connName, verb, rendered, "hook_failed", err)
			continue
		}
		r.auditVerb(t, connName, verb, rendered, "ok", nil)
	}
}

func (r *Runner) audit(entry map[string]any) {
	if r.Store != nil {
		r.Store.Audit(entry)
	}
}

// groupData builds the {{.group.*}} scope for a batched run.
func groupData(b *Batch, secretVals map[string]string) map[string]any {
	events := make([]any, len(b.Events))
	for i, ev := range b.Events {
		events[i] = baseData(ev, nil)
	}
	g := map[string]any{"key": b.Key, "events": events, "count": len(b.Events)}
	if len(events) > 0 {
		g["first"] = events[0]
		g["last"] = events[len(events)-1]
	}
	return g
}

// cloneData shallow-copies the top level of a scope (enough for scoped
// additions like error/item without leaking into siblings).
func cloneData(data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+2)
	for k, v := range data {
		out[k] = v
	}
	return out
}

func anyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func stepID(s config.Step, i int) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("step%d", i+1)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// flowTag is the stable log prefix for one trigger's flow run.
func flowTag(t core.Trigger) string {
	id := t.Target.Repo
	if t.Target.Number > 0 {
		id = fmt.Sprintf("%s#%d", id, t.Target.Number)
	}
	return fmt.Sprintf("flow[%s %s %s]", t.Instance, id, t.Kind)
}

// renderStrings renders each element of a string slice.
func renderStrings(in []string, data map[string]any) ([]string, error) {
	out := make([]string, len(in))
	for i, s := range in {
		r, err := render(s, data)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// renderStringMap renders each value of a string map.
func renderStringMap(in map[string]string, data map[string]any) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		r, err := render(v, data)
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}
