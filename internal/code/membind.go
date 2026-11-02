package code

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/memory"
)

// memInvoke is the single ctx.memory dispatcher behind every out-of-process
// engine's data plane — the `cli` engine's socket and an engine PLUGIN's
// host.memory callbacks both land here through CtxHandler (ctxhost.go).
// (Host-interpreter steps run in a separate process with no data plane and
// use the memory.* verbs instead.) It reads the
// process-wide configured memory; without a memory: section every op errors
// plainly. Code-path writes carry no run provenance, so relative scopes
// ("repo"/"agent") need their explicit forms here (repo:<owner/repo>,
// agent:<name>).
//
// `key` is the exception to all of that: it renders a scope key and touches
// no memory, so it needs no memory: section and passes no gate (memKey).
// redact is the run's tracked-secret redactor, for the facts key renders.
func memInvoke(guard DataGuard, redact func(any) any, op string, args []any) (any, error) {
	if op == "key" {
		return memKey(redact, args)
	}
	m := memory.Active()
	if m == nil {
		return nil, fmt.Errorf("memory: not configured — add a top-level memory: section")
	}
	argStr := func(i int) string {
		if i < len(args) && args[i] != nil {
			return fmt.Sprint(args[i])
		}
		return ""
	}
	// EVERY op is gated, not just remember. The scope this op touches is
	// resolved first (for forget that means the stored scope of the id it
	// names — ownership is the same allowlist that governs reading), then
	// authorized through the one gate both agent-facing faces share.
	scope, err := memScopeOf(m, op, args, argStr)
	if err != nil {
		return nil, err
	}
	// The zero Caller: this face's allowlist enforcement rides the DataGuard
	// below, which the flow layer installs per EXECUTION (so it knows whether
	// the step was agent- or config-authored). CheckOp still applies the
	// unconditional reserved-bucket rule to every caller.
	if err := m.CheckOp(memory.Caller{}, op, scope); err != nil {
		return nil, err
	}
	if guard != nil {
		if err := guard("memory", op, scope, args); err != nil {
			return nil, err
		}
	}
	switch op {
	case "remember":
		if len(args) < 1 {
			return nil, fmt.Errorf("memory.remember: want (text, tags?, scope?)")
		}
		var tags []string
		if len(args) > 1 && args[1] != nil {
			switch x := args[1].(type) {
			case []any:
				for _, t := range x {
					tags = append(tags, fmt.Sprint(t))
				}
			case []string:
				tags = x
			case string:
				tags = []string{x}
			default:
				return nil, fmt.Errorf("memory.remember: tags must be a list, got %T", args[1])
			}
		}
		// Scope already authorized above (memory.CheckOp → CheckAgentScope
		// for the reserved bucket, plus the operator's scope allowlist).
		e, err := m.Remember(argStr(0), tags, argStr(2), memory.Source{})
		if err != nil {
			return nil, err
		}
		return e.Map(), nil
	case "recall":
		q := memory.Query{}
		if len(args) > 0 && args[0] != nil {
			opts, ok := args[0].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("memory.recall: want an options map {tags, scope, substring, limit}, got %T", args[0])
			}
			for _, t := range asAnyList(opts["tags"]) {
				q.Tags = append(q.Tags, fmt.Sprint(t))
			}
			if s, ok := opts["scope"].(string); ok && s != "" {
				q.Scopes = []string{memory.NormalizeScope(s)}
			}
			if s, ok := opts["substring"].(string); ok {
				q.Substring = s
			}
			switch n := opts["limit"].(type) {
			case int:
				q.Limit = n
			case int64:
				q.Limit = int(n)
			case float64:
				q.Limit = int(n)
			}
		}
		entries, err := m.Recall(q)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.Map()
		}
		return out, nil
	case "forget":
		if argStr(0) == "" {
			return nil, fmt.Errorf("memory.forget: want (id)")
		}
		return m.Forget(argStr(0))
	case "list":
		// Recall, not List: a scoped caller sees its own scope's entries,
		// not the whole daemon's. memScopeOf resolved which that is.
		entries, err := m.Recall(memory.Query{Scopes: memScopeList(scope)})
		if err != nil {
			return nil, err
		}
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.Map()
		}
		return out, nil
	}
	return nil, fmt.Errorf("memory: no operation %q", op)
}

func asAnyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []any{x}
	}
	return nil
}

// memOps is the ctx.memory method set the data plane exposes — the ops
// CtxHandler will dispatch, named in the reference client's usage errors.
var memOps = []string{"remember", "recall", "forget", "list", "key"}

// memKey is ctx.memory.key(template, facts?): core's scope-key renderer
// (memory.RenderScopeKey) over facts the step supplies, so a pack that
// builds a key in code builds exactly the key a selector or grant with the
// same template would — same grammar, same refusals, same fail-closed probe.
//
// The answer is {key, ok, reason}. A key that does not apply — it reads a
// fact with no value, uses a construct a key may not, or carries a control
// character — is ok:false with the reason and key "", not an error: that is
// an answer about the template, and the step decides what to do with it.
// Only a malformed CALL (no template, facts not an object) errors.
//
// It reads and writes no memory, so it works without a memory: section and
// is not put to the DataGuard (there is no scope it touches). The facts go
// through memory.CallerFacts: JSON numbers judged as a run's ints, secrets
// and vaults dropped, tracked secret values redacted.
func memKey(redact func(any) any, args []any) (any, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("memory.key: want (template, facts?)")
	}
	tmpl, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("memory.key: template must be a string, got %T", args[0])
	}
	var facts map[string]any
	if len(args) == 2 && args[1] != nil {
		if facts, ok = args[1].(map[string]any); !ok {
			return nil, fmt.Errorf("memory.key: facts must be an object, got %T", args[1])
		}
	}
	key, err := memory.RenderScopeKey(tmpl, memory.CallerFacts(facts, redact))
	if err != nil {
		return map[string]any{"key": "", "ok": false, "reason": err.Error()}, nil
	}
	return map[string]any{"key": key, "ok": true, "reason": ""}, nil
}

// memScopeOf resolves which scope an op touches, so one gate can authorize
// them all. `forget` names an id rather than a scope, so its scope is the
// stored entry's — that is what makes ownership enforceable.
func memScopeOf(m *memory.Manager, op string, args []any, argStr func(int) string) (string, error) {
	switch op {
	case "remember":
		return argStr(2), nil
	case "recall":
		if len(args) > 0 && args[0] != nil {
			if opts, ok := args[0].(map[string]any); ok {
				if s, ok := opts["scope"].(string); ok {
					return s, nil
				}
			}
		}
		return "", nil
	case "forget":
		scope, found, err := m.ScopeOf(argStr(0))
		if err != nil {
			return "", err
		}
		if !found {
			// Unknown id: nothing to authorize, and the op reports
			// not-found below rather than leaking whether it exists.
			return "", nil
		}
		return scope, nil
	}
	return "", nil
}

// memScopeList turns a resolved scope into a Recall filter. An empty scope
// means "no filter" — reachable only when no scope guard is installed, since
// the flow guard refuses an unscoped read.
func memScopeList(scope string) []string {
	if strings.TrimSpace(scope) == "" {
		return nil
	}
	return []string{memory.NormalizeScope(scope)}
}
