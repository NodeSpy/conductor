package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/code"
	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/connector"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/expr"
	"github.com/NodeSpy/paseo-conductor/internal/hosts"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

// Store is the persistence surface the runner needs (checkpointed runs +
// the audit log). *store.Store satisfies it.
type Store interface {
	Audit(entry map[string]any)
	PutRun(r store.WorkflowRun) error
	DeleteRun(id string) error
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
	// Guidance is the house prompt guidance for a profile.
	Guidance func(p config.AgentProfile) string
	// Background is invoked after a background agent step launches: register
	// the hold, and start the interactive review hand-off on handoffConn (an
	// ask-capable connector name; "" = runtime-native).
	Background func(ctx context.Context, t core.Trigger, stepID string, p config.AgentProfile, ref dispatch.RunRef, handoffConn string)
	// Archive soft-deletes a finished non-interactive agent.
	Archive func(agentID string)
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
	Store      Store
	Notif      Notifier
	Log        func(string, ...any)
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
// trigger spec. The on-part is verified so a stale index from a resumed run
// against an edited config is caught instead of running the wrong trigger.
func (r *Runner) SpecFor(ref string) (config.TriggerSpec, bool) {
	idxStr, on, ok := strings.Cut(ref, ":")
	if !ok {
		return config.TriggerSpec{}, false
	}
	var idx int
	if _, err := fmt.Sscanf(idxStr, "%d", &idx); err != nil {
		return config.TriggerSpec{}, false
	}
	if idx < 0 || idx >= len(r.Cfg.Triggers) || r.Cfg.Triggers[idx].On != on {
		return config.TriggerSpec{}, false
	}
	return r.Cfg.Triggers[idx], true
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
func (r *Runner) Run(ctx context.Context, run store.WorkflowRun, t core.Trigger, spec config.TriggerSpec, batch *Batch, shadow bool) {
	data := baseData(t, r.SecretVals)
	if batch != nil {
		data["group"] = groupData(batch, r.SecretVals)
	}
	stepsOut := map[string]any{}
	data["steps"] = stepsOut
	for id, out := range run.Outputs { // restore checkpointed outputs (resume)
		data[id] = anyMap(out)
		stepsOut[id] = map[string]any{"outputs": out}
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
			r.Notif.Emit(ctx, "escalate", t, fmt.Sprintf("workflow failed: %v", err))
		}
		// A partial failure stays visible: the audit records where it stopped,
		// and the run is removed (the sweep/backoff machinery re-derives).
		r.audit(map[string]any{"event": "workflow_failed", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "error": err.Error(), "failed_step": failedStepID(err)})
		r.finishRun(run)
		return
	}
	r.runHooks(ctx, t, spec.Hooks, "done", data, "workflow")
	if r.Notif != nil {
		r.Notif.Emit(ctx, "complete", t, "workflow")
	}
	r.finishRun(run)
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
	for i := start; i < len(steps); i++ {
		step := steps[i]
		id := stepID(step, i)

		if step.If != "" {
			ok, err := expr.Eval(step.If, data)
			if err != nil {
				return &stepError{id: id, err: fmt.Errorf("if: %w", err)}
			}
			if !ok {
				r.audit(map[string]any{"event": "step_skipped", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "if": step.If})
				r.checkpoint(run, i, id, nil, checkpoint)
				continue
			}
		}

		r.runHooks(ctx, t, step.Hooks, "start", data, "step "+id)
		outputs, err := r.execStepWithFlow(ctx, t, step, id, data, shadow)
		if err != nil {
			fdata := cloneData(data)
			fdata["error"] = err.Error()
			fdata["failed_step"] = id
			r.runHooks(ctx, t, step.Hooks, "fail", fdata, "step "+id)
			r.audit(map[string]any{"event": "step_error", "repo": t.Target.Repo,
				"number": t.Target.Number, "kind": t.Kind, "step": id, "error": err.Error()})
			if step.ContinueOnError {
				r.Log("%s step %s failed (continue_on_error): %v", flowTag(t), id, err)
				outputs = map[string]any{"error": err.Error(), "failed": true}
				r.recordOutputs(data, id, outputs)
				r.checkpoint(run, i, id, outputs, checkpoint)
				continue
			}
			return &stepError{id: id, err: err}
		}
		r.recordOutputs(data, id, outputs)
		entry := map[string]any{"event": "step", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "step": id}
		if len(outputs) > 0 && r.Secrets != nil {
			entry["outputs"] = r.Secrets.RedactValue(outputs)
		}
		r.audit(entry)
		// Step-done hooks see the step's own output (position-scoped).
		r.runHooks(ctx, t, step.Hooks, "done", data, "step "+id)
		r.checkpoint(run, i, id, outputs, checkpoint)
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

// checkpoint advances the run past a completed top-level step.
func (r *Runner) checkpoint(run *store.WorkflowRun, i int, id string, outputs map[string]any, active bool) {
	if !active || run.ID == "" {
		return
	}
	run.StepIndex = i + 1
	if outputs != nil {
		if run.Outputs == nil {
			run.Outputs = map[string]map[string]any{}
		}
		run.Outputs[id] = outputs
	}
	_ = r.Store.PutRun(*run)
}

func (r *Runner) finishRun(run store.WorkflowRun) {
	if run.ID != "" {
		_ = r.Store.DeleteRun(run.ID)
	}
}

// execStepWithFlow wraps one step's execution with the control-flow
// modifiers: for_each fan-out, parallel branches, timeout, and retry.
func (r *Runner) execStepWithFlow(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
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
		return r.execForEach(ctx, t, step, id, data, shadow)
	}

	return r.execWithRetry(ctx, t, step, id, data, shadow)
}

// execBranches runs `parallel: [[…],[…]]` branch lists concurrently.
func (r *Runner) execBranches(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
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
func (r *Runner) execForEach(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	items, err := resolveList(step.ForEach, data)
	if err != nil {
		return nil, fmt.Errorf("for_each: %w", err)
	}
	results := make([]any, len(items))
	concurrent := step.Parallel != nil && step.Parallel.Concurrent
	var firstErr error
	runOne := func(i int) error {
		local := cloneData(data)
		local["item"] = items[i]
		local["index"] = i
		out, err := r.execWithRetry(ctx, t, step, fmt.Sprintf("%s[%d]", id, i), local, shadow)
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
func (r *Runner) execWithRetry(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
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
		out, raw, err = r.execStep(ctx, t, step, id, data, shadow)
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
			out, raw, err = r.execStep(ctx, t, step, id, data, shadow)
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
func (r *Runner) execStep(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
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
		return r.execAgent(ctx, t, step, id, data, shadow)
	case "command":
		return r.execCommand(ctx, t, step, id, data, shadow)
	}
	return nil, "", fmt.Errorf("step %q has no recognizable form", id)
}

// execVerb invokes uses: <connector>.<verb> with rendered, merged options.
func (r *Runner) execVerb(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	connName, verb, _ := strings.Cut(step.Uses, ".")
	in, ok := r.Conns.Get(connName)
	if !ok {
		return nil, fmt.Errorf("unknown connector %q", connName)
	}
	merged := connector.MergeOptions(in.DefaultOptions, step.Options)
	rendered, err := renderOptions(merged, data)
	if err != nil {
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	if shadow {
		r.Log("%s [dry-run] would invoke %s.%s", flowTag(t), connName, verb)
		r.auditVerb(t, connName, verb, rendered, "stubbed", nil)
		return stubOutputs(in, verb), nil
	}
	start := time.Now()
	out, err := in.InvokeFinal(ctx, verb, rendered)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		r.auditVerb(t, connName, verb, rendered, "failed", err)
		return nil, fmt.Errorf("uses %s: %w", step.Uses, err)
	}
	r.Log("%s %s.%s done in %s", flowTag(t), connName, verb, took)
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
		entry["error"] = err.Error()
	}
	r.audit(entry)
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

// execWorkflowCall runs { workflow: <name>, with: {…} } — an encapsulated
// child scope seeded with the trigger context + declared inputs; the caller
// reads the workflow's declared outputs off this step's id.
func (r *Runner) execWorkflowCall(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, error) {
	wf, ok := r.Cfg.Workflows[step.Workflow]
	if !ok {
		return nil, fmt.Errorf("unknown workflow %q", step.Workflow)
	}
	with, err := renderOptions(step.With, data)
	if err != nil {
		return nil, fmt.Errorf("with: %w", err)
	}
	inputs := map[string]any{}
	for name, spec := range wf.Inputs {
		v, present := with[name]
		if !present {
			if spec.Required {
				return nil, fmt.Errorf("workflow %q: missing required input %q", step.Workflow, name)
			}
			if spec.Default != nil {
				v = spec.Default
			}
		}
		inputs[name] = v
	}
	for name := range with {
		if _, declared := wf.Inputs[name]; !declared {
			return nil, fmt.Errorf("workflow %q: unknown input %q", step.Workflow, name)
		}
	}
	// The child sees the trigger context + its inputs + its own steps — not
	// the caller's other step outputs (pass those via with:).
	child := baseData(t, r.SecretVals)
	child["inputs"] = inputs
	child["steps"] = map[string]any{}
	if g, ok := data["group"]; ok {
		child["group"] = g
	}
	var childRun store.WorkflowRun
	if err := r.runSteps(ctx, &childRun, t, wf.Steps, child, shadow, false); err != nil {
		return nil, fmt.Errorf("workflow %q: %w", step.Workflow, err)
	}
	outputs := map[string]any{}
	for name, tmpl := range wf.Outputs {
		v, err := renderValue(tmpl, child)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: output %q: %w", step.Workflow, name, err)
		}
		outputs[name] = v
	}
	return outputs, nil
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
	workdir, err := render(step.WorkDir, data)
	if err != nil {
		return nil, "", err
	}
	spec := code.Spec{Run: step.Run, Code: step.Code, Args: args, Env: env, WorkDir: workdir}
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

// codeCtx strips the legacy steps index and secrets from the ctx handed to
// user code (secrets reach code only via explicitly templated args/env).
func codeCtx(data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		if k == "steps" || k == "secrets" {
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

// execAgent dispatches a type: agent step through the engine-provided
// services (runtime resolution, tokens, guidance, background hand-off).
func (r *Runner) execAgent(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	profile := r.Cfg.Agents[step.Agent]
	act := config.Action{
		Type: "agent", ID: id, Agent: step.Agent,
		Prompt: step.Prompt, Checkout: step.Checkout, WorkDir: step.WorkDir,
		Env: step.Env, OutputSchema: step.OutputSchema, Background: step.Background,
		Backend: step.Backend, RerequestReview: step.RerequestReview,
	}
	if step.Background {
		profile.ArchiveWhenDone = false
	}
	if act.Prompt != "" {
		act.Prompt += dispatch.WriteWrapperGuidance
		if r.Agents.Guidance != nil {
			act.Prompt += r.Agents.Guidance(profile)
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
	req := dispatch.Request{
		Trigger: t, Action: act, Profile: profile, Tokens: tokens,
		Shadow: shadow, Wait: !step.Background, Interactive: step.Background, Data: data,
	}
	ref, err := r.Agents.Dispatch(ctx, req)
	if err != nil {
		return nil, ref.Output, err
	}
	if step.Background {
		if r.Agents.Background != nil {
			r.Agents.Background(ctx, t, id, profile, ref, step.Handoff)
		}
		return map[string]any{"agent_id": ref.AgentID, "background": true}, "", nil
	}
	outputs := extractOutputs(ref.Output)
	outputs["agent_id"] = ref.AgentID
	if profile.ArchiveWhenDone && ref.AgentID != "" && r.Agents.Archive != nil {
		r.Agents.Archive(ref.AgentID)
	}
	return outputs, ref.Output, nil
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
		merged := connector.MergeOptions(in.DefaultOptions, h.Options)
		rendered, err := renderOptions(merged, data)
		if err != nil {
			r.Log("%s %s hook[%d] render: %v", flowTag(t), where, i, err)
			r.auditVerb(t, connName, verb, nil, "hook_render_failed", err)
			continue
		}
		if r.DryRun {
			r.Log("%s [dry-run] would invoke hook %s.%s (at: %s)", flowTag(t), connName, verb, phase)
			r.auditVerb(t, connName, verb, rendered, "stubbed", nil)
			continue
		}
		if _, err := in.InvokeFinal(ctx, verb, rendered); err != nil {
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
