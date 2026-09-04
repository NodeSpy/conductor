package flow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// universalKeys are addressable in every trigger scope regardless of event.
var universalKeys = []string{
	"repo", "owner", "name", "pr", "issue", "number", "head", "base", "url",
	"kind", "title", "labels", "steps",
	// dispatch injects the resolved tokens into every step's template data
	// (legacy prompts reference them, e.g. env: {GH_TOKEN: "{{.gh_token}}"}).
	"gh_token", "app_token",
}

// Validate is the load-time semantic pass over the connectors-model config:
// every `on:` kind, `filters:` key, `uses:` verb, option, `run:` engine, and
// every {{…}}/if: reference is resolved against the connectors' published
// schemas AND against the scope at its position — a config that validates
// cannot reference a value that won't exist when the step runs.
func Validate(cfg *config.Config, reg *connector.Registry) error {
	if err := validateWorkflowCycles(cfg); err != nil {
		return err
	}
	for name, wf := range cfg.Workflows {
		if err := validateWorkflow(cfg, reg, name, wf); err != nil {
			return err
		}
	}
	for i, spec := range cfg.Triggers {
		where := fmt.Sprintf("triggers[%d] (on: %s)", i, spec.On)
		if err := validateTrigger(cfg, reg, where, spec); err != nil {
			return err
		}
	}
	if err := validateNotifyVia(cfg, reg); err != nil {
		return err
	}
	return nil
}

// validateNotifyVia checks the notify.via routes: known connector verbs,
// valid options (required keys may come from connector defaults), and every
// template reference resolvable in the notify scope.
func validateNotifyVia(cfg *config.Config, reg *connector.Registry) error {
	if len(cfg.Notify.Via) == 0 {
		return nil
	}
	sc := &scope{top: map[string]bool{}, steps: map[string]connector.Schema{}}
	for _, k := range []string{"message", "event", "ref", "repo", "number", "kind", "title"} {
		sc.top[k] = true
	}
	for i, r := range cfg.Notify.Via {
		w := fmt.Sprintf("notify.via[%d]", i)
		connName, verb, ok := strings.Cut(r.Uses, ".")
		if !ok || connName == "" || verb == "" {
			return fmt.Errorf("%s: `uses: %s` must be <connector>.<verb>", w, r.Uses)
		}
		in, found := reg.Get(connName)
		if !found {
			return fmt.Errorf("%s: unknown connector %q", w, connName)
		}
		vd, found := in.Decl.Verb(verb)
		if !found {
			return fmt.Errorf("%s: connector %q (%s) has no verb %q (verbs: %s)",
				w, connName, in.Decl.Type, verb, strings.Join(in.Decl.VerbNames(), ", "))
		}
		if !vd.Open {
			if err := connector.ValidateCallOptions(w+" options", vd.Options, r.Options, in.DefaultOptions); err != nil {
				return err
			}
		}
		if err := checkMapRefs(w+" options", r.Options, sc); err != nil {
			return err
		}
		for _, ev := range r.On {
			switch ev {
			case "dispatch", "complete", "escalate", "needs_input", "digest":
			default:
				return fmt.Errorf("%s: unknown event %q in on: (dispatch|complete|escalate|needs_input|digest)", w, ev)
			}
		}
	}
	return nil
}

func validateTrigger(cfg *config.Config, reg *connector.Registry, where string, spec config.TriggerSpec) error {
	if spec.Manual() {
		return validateManualTrigger(cfg, reg, where, spec)
	}
	in, ok := reg.Get(spec.Connector())
	if !ok {
		return fmt.Errorf("%s: unknown connector %q", where, spec.Connector())
	}
	ev, ok := in.Decl.Event(spec.Event())
	if !ok {
		return fmt.Errorf("%s: connector %q (%s) has no event %q (events: %s)",
			where, in.Name, in.Decl.Type, spec.Event(), strings.Join(in.Decl.EventNames(), ", "))
	}
	if ev.Dynamic && in.Impl != nil {
		declared := in.Impl.DeclaredEvents()
		found := false
		for _, d := range declared {
			if d == spec.Event() {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: connector %q declares no %s named %q (declared: %s)",
				where, in.Name, in.Decl.Type, spec.Event(), strings.Join(declared, ", "))
		}
	}
	if len(spec.Filters) > 0 {
		if err := connector.ValidateSchema(where+" filters", ev.Filters, spec.Filters); err != nil {
			return err
		}
	}
	if len(spec.Options) > 0 {
		if err := connector.ValidateSchema(where+" options", ev.Options, spec.Options); err != nil {
			return err
		}
	}

	sc := newScope(ev, cfg, spec.Group != nil)
	// A fan-in trigger's steps are shared across every listed source, so
	// their references check against the UNION of the sources' contexts
	// (heterogeneous fields are read defensively — {{.x | default ""}}).
	// The manual source contributes `inputs`.
	for _, src := range spec.FanSources {
		if src == config.ManualSource {
			sc.add("inputs")
			continue
		}
		conn, evName, _ := strings.Cut(src, ".")
		if sib, ok := reg.Get(conn); ok {
			if sev, ok := sib.Decl.Event(evName); ok {
				for k := range sev.Context {
					sc.add(strings.SplitN(k, ".", 2)[0])
				}
			}
		}
	}
	if spec.Group != nil && spec.Group.Key != "" {
		if err := checkRefs(where+" group.key", spec.Group.Key, sc); err != nil {
			return err
		}
	}
	// Workflow-level at:start hooks see the trigger context only.
	if err := validateHookRefs(cfg, reg, where, spec.Hooks, "start", sc); err != nil {
		return err
	}
	if err := validateStepList(cfg, reg, where, spec.Steps, sc); err != nil {
		return err
	}
	// at:done sees all step outputs; at:fail adds failure metadata.
	if err := validateHookRefs(cfg, reg, where, spec.Hooks, "done", sc); err != nil {
		return err
	}
	failScope := sc.clone()
	failScope.add("error")
	failScope.add("failed_step")
	return validateHookRefs(cfg, reg, where, spec.Hooks, "fail", failScope)
}

// checkKVStore enforces the kv verbs' store: selector at LOAD time: it must
// be a literal name of a defined stores: entry (there is no default store,
// and a templated selector would defeat the load check).
func checkKVStore(cfg *config.Config, w, connName string, opts map[string]any) error {
	if connName != "kv" || cfg == nil {
		return nil
	}
	raw, ok := opts["store"]
	if !ok {
		return fmt.Errorf("%s: kv verbs require store: naming a stores: entry (defined: %s)", w, cfg.StoreNames())
	}
	name, isStr := raw.(string)
	if !isStr || name == "" || strings.Contains(name, "{{") {
		return fmt.Errorf("%s: store: must be a literal store name, got %v", w, raw)
	}
	if _, defined := cfg.Stores[name]; !defined {
		return fmt.Errorf("%s: unknown store %q (defined stores: %s)", w, name, cfg.StoreNames())
	}
	return nil
}

// validateManualTrigger checks an `on: manual` trigger: it publishes no
// event schema (its context is the `conductor run` inputs), so filters and
// source options have nothing to bind to, and step references validate in an
// open scope with `inputs` addressable.
func validateManualTrigger(cfg *config.Config, reg *connector.Registry, where string, spec config.TriggerSpec) error {
	if len(spec.Filters) > 0 {
		return fmt.Errorf("%s: the manual source accepts no filters (a shared base filters: must be a key every listed source accepts)", where)
	}
	if len(spec.Options) > 0 {
		return fmt.Errorf("%s: the manual source accepts no options", where)
	}
	sc := openScope(cfg)
	sc.add("inputs")
	if spec.Group != nil {
		sc.top["group"] = true
		if spec.Group.Key != "" {
			if err := checkRefs(where+" group.key", spec.Group.Key, sc); err != nil {
				return err
			}
		}
	}
	if err := validateHookRefs(cfg, reg, where, spec.Hooks, "start", sc); err != nil {
		return err
	}
	if err := validateStepList(cfg, reg, where, spec.Steps, sc); err != nil {
		return err
	}
	if err := validateHookRefs(cfg, reg, where, spec.Hooks, "done", sc); err != nil {
		return err
	}
	failScope := sc.clone()
	failScope.add("error")
	failScope.add("failed_step")
	return validateHookRefs(cfg, reg, where, spec.Hooks, "fail", failScope)
}

// validateWorkflow checks a reusable workflow standalone. Its trigger-context
// reads can't be bound to one event (any trigger may call it), so unknown
// top-level references are allowed; inputs and its own steps are strict.
func validateWorkflow(cfg *config.Config, reg *connector.Registry, name string, wf config.WorkflowDef) error {
	where := "workflow " + name
	sc := openScope(cfg)
	sc.add("inputs")
	if err := validateStepList(cfg, reg, where, wf.Steps, sc); err != nil {
		return err
	}
	// Declared outputs must reference internal steps.
	ids := map[string]bool{}
	for i, s := range wf.Steps {
		ids[stepID(s, i)] = true
	}
	for out, tmpl := range wf.Outputs {
		refs, err := templateRefs(tmpl)
		if err != nil {
			return fmt.Errorf("%s: output %q: %v", where, out, err)
		}
		for _, ref := range refs {
			root := strings.SplitN(ref, ".", 2)[0]
			if root == "inputs" || ids[root] || isUniversal(root) {
				continue
			}
			return fmt.Errorf("%s: output %q references %q, which is not one of its steps (%s) or inputs",
				where, out, ref, sortedIDs(ids))
		}
	}
	return nil
}

// validateStepList walks steps in order, checking each against the scope at
// its position and then adding its id to the scope.
func validateStepList(cfg *config.Config, reg *connector.Registry, where string, steps []config.Step, sc *scope) error {
	for i, step := range steps {
		id := stepID(step, i)
		w := fmt.Sprintf("%s step %s", where, id)
		if err := validateOneStep(cfg, reg, w, step, sc); err != nil {
			return err
		}
		sc.addStep(id, stepOutputSchema(reg, step))
	}
	return nil
}

func validateOneStep(cfg *config.Config, reg *connector.Registry, w string, step config.Step, sc *scope) error {
	// Position-scoped references in the step's own templated fields. A
	// for_each step's fields additionally see item/index.
	stepScope := sc
	if step.ForEach != "" {
		if err := checkRefs(w+" for_each", step.ForEach, sc); err != nil {
			return err
		}
		stepScope = sc.clone()
		stepScope.add("item")
		stepScope.add("index")
	}
	if step.If != "" {
		if err := checkCondRefs(w+" if", step.If, stepScope); err != nil {
			return err
		}
	}
	for _, tf := range []struct{ label, s string }{
		{"prompt", step.Prompt}, {"workdir", step.WorkDir},
	} {
		if tf.s == "" {
			continue
		}
		if err := checkRefs(w+" "+tf.label, tf.s, stepScope); err != nil {
			return err
		}
	}
	for _, s := range step.Command {
		if err := checkRefs(w+" command", s, stepScope); err != nil {
			return err
		}
	}
	for k, v := range step.Env {
		if err := checkRefs(fmt.Sprintf("%s env.%s", w, k), v, stepScope); err != nil {
			return err
		}
	}
	if err := checkMapRefs(w+" options", step.Options, stepScope); err != nil {
		return err
	}
	if err := checkMapRefs(w+" with", step.With, stepScope); err != nil {
		return err
	}

	switch step.Form() {
	case "agent":
		if step.Agent == "" {
			return fmt.Errorf("%s: agent step needs `agent: <profile>`", w)
		}
		if _, ok := cfg.Agents[step.Agent]; !ok {
			return fmt.Errorf("%s: unknown agent profile %q (defined: %s)", w, step.Agent, agentNames(cfg))
		}
		if step.Handoff != "" {
			if err := checkAskCapable(reg, w, step.Handoff); err != nil {
				return err
			}
		}
	case "verb":
		connName, verb, _ := strings.Cut(step.Uses, ".")
		in, ok := reg.Get(connName)
		if !ok {
			return fmt.Errorf("%s: `uses: %s` — unknown connector %q", w, step.Uses, connName)
		}
		vd, ok := in.Decl.Verb(verb)
		if !ok {
			return fmt.Errorf("%s: connector %q (%s) has no verb %q (verbs: %s)",
				w, connName, in.Decl.Type, verb, strings.Join(in.Decl.VerbNames(), ", "))
		}
		if err := checkKVStore(cfg, w, connName, step.Options); err != nil {
			return err
		}
		if !vd.Open {
			if err := connector.ValidateCallOptions(w+" options", vd.Options, step.Options, in.DefaultOptions); err != nil {
				return err
			}
		}
	case "workflow":
		wf, ok := cfg.Workflows[step.Workflow]
		if !ok {
			return fmt.Errorf("%s: unknown workflow %q", w, step.Workflow)
		}
		for k := range step.With {
			if _, declared := wf.Inputs[k]; !declared {
				return fmt.Errorf("%s: workflow %q has no input %q (inputs: %s)", w, step.Workflow, k, inputNames(wf))
			}
		}
		for name, in := range wf.Inputs {
			if !in.Required || in.Default != nil {
				continue
			}
			if _, ok := step.With[name]; !ok {
				return fmt.Errorf("%s: workflow %q requires input %q", w, step.Workflow, name)
			}
		}
	case "code":
		if step.Run != "js" && step.Run != "go-embed" && step.Run != "go" && strings.TrimSpace(step.Run) == "" {
			return fmt.Errorf("%s: empty run:", w)
		}
	case "parallel":
		// Branch step ids merge into the parent scope after the join; they
		// must not collide with existing ids or each other.
		seen := map[string]bool{}
		for bi, branch := range step.Parallel.Branches {
			bw := fmt.Sprintf("%s branch %d", w, bi+1)
			branchScope := sc.clone()
			for i, bs := range branch {
				bid := stepID(bs, i)
				if seen[bid] || sc.hasStep(bid) {
					return fmt.Errorf("%s: step id %q collides across parallel branches", bw, bid)
				}
				seen[bid] = true
				if err := validateOneStep(cfg, reg, bw+" step "+bid, bs, branchScope); err != nil {
					return err
				}
				branchScope.addStep(bid, stepOutputSchema(reg, bs))
			}
		}
	}

	// Step-level hooks: at:start sees the prior scope; at:done adds this
	// step's own output; at:fail adds failure metadata.
	id := step.ID
	if id == "" {
		id = "this step"
	}
	if err := validateHookRefs(cfg, reg, w, step.Hooks, "start", stepScope); err != nil {
		return err
	}
	doneScope := stepScope.clone()
	if step.ID != "" {
		doneScope.addStep(step.ID, stepOutputSchema(reg, step))
	}
	if err := validateHookRefs(cfg, reg, w, step.Hooks, "done", doneScope); err != nil {
		return err
	}
	failScope := doneScope.clone()
	failScope.add("error")
	failScope.add("failed_step")
	return validateHookRefs(cfg, reg, w, step.Hooks, "fail", failScope)
}

// validateHookRefs checks one phase's hooks: the verb exists, its options
// validate, and every reference resolves in the phase's scope.
func validateHookRefs(cfg *config.Config, reg *connector.Registry, where string, hooks []config.Hook, phase string, sc *scope) error {
	for i, h := range hooks {
		if h.At != phase {
			continue
		}
		w := fmt.Sprintf("%s hooks[%d] (at: %s)", where, i, phase)
		connName, verb, _ := strings.Cut(h.Uses, ".")
		in, ok := reg.Get(connName)
		if !ok {
			return fmt.Errorf("%s: unknown connector %q", w, connName)
		}
		vd, ok := in.Decl.Verb(verb)
		if !ok {
			return fmt.Errorf("%s: connector %q (%s) has no verb %q (verbs: %s)",
				w, connName, in.Decl.Type, verb, strings.Join(in.Decl.VerbNames(), ", "))
		}
		if err := checkKVStore(cfg, w, connName, h.Options); err != nil {
			return err
		}
		if !vd.Open {
			if err := connector.ValidateCallOptions(w+" options", vd.Options, h.Options, in.DefaultOptions); err != nil {
				return err
			}
		}
		if h.If != "" {
			if err := checkCondRefs(w+" if", h.If, sc); err != nil {
				return err
			}
		}
		if err := checkMapRefs(w+" options", h.Options, sc); err != nil {
			return err
		}
	}
	return nil
}

// checkAskCapable verifies a handoff: reference names a connector with an
// ask verb.
func checkAskCapable(reg *connector.Registry, w, name string) error {
	in, ok := reg.Get(name)
	if !ok {
		return fmt.Errorf("%s: handoff %q is not a configured connector", w, name)
	}
	if v, ok := in.Decl.Verb("ask"); !ok || !v.Ask {
		return fmt.Errorf("%s: connector %q (%s) has no ask verb — hand-offs need slack/discord/web", w, name, in.Decl.Type)
	}
	return nil
}

// --- scope tracking ---

// scope is the set of addressable roots at one position: universal keys, the
// event's context keys, secrets/group when present, and prior step ids (with
// their declared output schemas, when a form declares one).
type scope struct {
	top   map[string]bool
	steps map[string]connector.Schema
	// open scopes (workflow bodies) allow unknown top-level refs — a workflow
	// is polymorphic over its callers' trigger contexts.
	open bool
}

func newScope(ev connector.EventDecl, cfg *config.Config, grouped bool) *scope {
	sc := &scope{top: map[string]bool{}, steps: map[string]connector.Schema{}}
	for _, k := range universalKeys {
		sc.top[k] = true
	}
	for k := range ev.Context {
		sc.top[strings.SplitN(k, ".", 2)[0]] = true
	}
	if len(cfg.SecretRefs) > 0 {
		sc.top["secrets"] = true
	}
	if grouped {
		sc.top["group"] = true
	}
	return sc
}

func openScope(cfg *config.Config) *scope {
	sc := &scope{top: map[string]bool{}, steps: map[string]connector.Schema{}, open: true}
	for _, k := range universalKeys {
		sc.top[k] = true
	}
	if len(cfg.SecretRefs) > 0 {
		sc.top["secrets"] = true
	}
	return sc
}

func (s *scope) add(k string) { s.top[k] = true }
func (s *scope) hasStep(id string) bool {
	_, ok := s.steps[id]
	return ok
}

func (s *scope) addStep(id string, outputs connector.Schema) {
	s.top[id] = true
	s.steps[id] = outputs
}

func (s *scope) clone() *scope {
	out := &scope{top: map[string]bool{}, steps: map[string]connector.Schema{}, open: s.open}
	for k := range s.top {
		out.top[k] = true
	}
	for k, v := range s.steps {
		out.steps[k] = v
	}
	return out
}

// check verifies one dotted reference against the scope: the root must be
// addressable, and a verb step's declared outputs are checked field-level.
func (s *scope) check(where, ref string) error {
	parts := strings.SplitN(ref, ".", 3)
	root := parts[0]
	if !s.top[root] {
		if s.open {
			return nil
		}
		return fmt.Errorf("%s: {{.%s}} is not available here (in scope: %s)", where, ref, s.describe())
	}
	if outs, ok := s.steps[root]; ok && outs != nil && len(parts) > 1 {
		field := parts[1]
		if _, declared := outs[field]; !declared {
			known := make([]string, 0, len(outs))
			for k := range outs {
				known = append(known, k)
			}
			sort.Strings(known)
			return fmt.Errorf("%s: step %q has no output %q (outputs: %s)", where, root, field, strings.Join(known, ", "))
		}
	}
	return nil
}

func (s *scope) describe() string {
	keys := make([]string, 0, len(s.top))
	for k := range s.top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 24 {
		keys = append(keys[:24], "…")
	}
	return strings.Join(keys, ", ")
}

// checkRefs validates every {{.x}} reference in a template string.
func checkRefs(where, tmpl string, sc *scope) error {
	refs, err := templateRefs(tmpl)
	if err != nil {
		return fmt.Errorf("%s: %v", where, err)
	}
	for _, ref := range refs {
		if err := sc.check(where, ref); err != nil {
			return err
		}
	}
	return nil
}

// checkCondRefs validates the paths an if: condition reads (both the bare and
// {{.x}} spellings).
func checkCondRefs(where, cond string, sc *scope) error {
	if err := checkRefs(where, cond, sc); err != nil {
		return err
	}
	return nil
}

// checkMapRefs walks an options/with map validating every templated string.
func checkMapRefs(where string, m map[string]any, sc *scope) error {
	var walk func(prefix string, v any) error
	walk = func(prefix string, v any) error {
		switch x := v.(type) {
		case string:
			return checkRefs(where+prefix, x, sc)
		case map[string]any:
			for k, e := range x {
				if err := walk(prefix+"."+k, e); err != nil {
					return err
				}
			}
		case []any:
			for i, e := range x {
				if err := walk(fmt.Sprintf("%s[%d]", prefix, i), e); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("", m)
}

// stepOutputSchema returns the declared outputs a step's references can be
// checked against: a verb's declared Outputs, nil (dynamic) otherwise.
func stepOutputSchema(reg *connector.Registry, step config.Step) connector.Schema {
	if step.Uses == "" {
		return nil
	}
	connName, verb, _ := strings.Cut(step.Uses, ".")
	in, ok := reg.Get(connName)
	if !ok {
		return nil
	}
	if vd, ok := in.Decl.Verb(verb); ok && len(vd.Outputs) > 0 {
		s := connector.Schema{}
		for k, v := range vd.Outputs {
			s[k] = v
		}
		// stubbed appears in dry-run outputs.
		s["stubbed"] = connector.Field{Type: connector.TBool}
		return s
	}
	return nil
}

// validateWorkflowCycles rejects workflow: call cycles among workflows.
func validateWorkflowCycles(cfg *config.Config) error {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := map[string]int{}
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		switch state[name] {
		case grey:
			return fmt.Errorf("config: workflow cycle: %s", strings.Join(append(path, name), " -> "))
		case black:
			return nil
		}
		state[name] = grey
		wf := cfg.Workflows[name]
		for _, s := range wf.Steps {
			if s.Workflow != "" {
				if _, ok := cfg.Workflows[s.Workflow]; ok {
					if err := visit(s.Workflow, append(path, name)); err != nil {
						return err
					}
				}
			}
		}
		state[name] = black
		return nil
	}
	names := make([]string, 0, len(cfg.Workflows))
	for n := range cfg.Workflows {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return err
		}
	}
	return nil
}

func agentNames(cfg *config.Config) string {
	if len(cfg.Agents) == 0 {
		return "none"
	}
	names := make([]string, 0, len(cfg.Agents))
	for n := range cfg.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func inputNames(wf config.WorkflowDef) string {
	if len(wf.Inputs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(wf.Inputs))
	for n := range wf.Inputs {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func sortedIDs(ids map[string]bool) string {
	names := make([]string, 0, len(ids))
	for n := range ids {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func isUniversal(k string) bool {
	for _, u := range universalKeys {
		if u == k {
			return true
		}
	}
	return false
}
