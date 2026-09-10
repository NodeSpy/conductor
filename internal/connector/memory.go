package connector

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
)

// memoryDecl declares the built-in shared agent memory. Like kv/sql/conductor
// the connector is registered unconditionally (the name is reserved); the
// verbs work once a top-level `memory:` section picks a backend, and error
// plainly when none is configured. Every invocation goes through the flow
// runner's verb path, so writes are audited and carry the run's provenance
// (agent/run/trigger/repo) automatically.
var memoryDecl = &TypeDecl{
	Type: "memory",
	Desc: "Built-in shared agent memory (configured by the top-level memory: section); durable notes with provenance and scope.",
	Verbs: []VerbDecl{
		{
			Name: "remember", Desc: "store one memory",
			Options: Schema{
				"text":  {Type: TString, Required: true, Desc: "the note to keep"},
				"tags":  {Type: TList, Desc: "labels recall filters on"},
				"scope": {Type: TString, Desc: "global (default) | repo | agent | repo:<owner/repo> | agent:<name>"},
			},
			Outputs: Schema{
				"id":    {Type: TString},
				"scope": {Type: TString, Desc: "the resolved canonical scope"},
			},
		},
		{
			Name: "recall", Desc: "fetch memories: tags + scope + substring, newest first",
			Options: Schema{
				"tags":      {Type: TList, Desc: "require ALL of these tags"},
				"scope":     {Type: TString, Desc: "limit to one scope (relative forms resolve against the run)"},
				"substring": {Type: TString, Desc: "case-insensitive text filter"},
				"limit":     {Type: TInt, Desc: "cap the result (0 = all)"},
			},
			Outputs: Schema{
				"memories": {Type: TList, Desc: "entries, newest first: {id, text, tags, scope, source, created}"},
				"count":    {Type: TInt},
			},
		},
		{
			Name: "forget", Desc: "remove one memory by id",
			Options: Schema{
				"id": {Type: TString, Required: true},
			},
			Outputs: Schema{"found": {Type: TBool}},
		},
		{
			Name: "list", Desc: "every memory, newest first (optionally one scope)",
			Options: Schema{
				"scope": {Type: TString},
			},
			Outputs: Schema{
				"memories": {Type: TList},
				"count":    {Type: TInt},
			},
		},
	},
}

func init() { RegisterType(memoryDecl, newMemoryImpl) }

type memoryImpl struct{}

func newMemoryImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return memoryImpl{}, nil
}

func (memoryImpl) Validate() error          { return nil }
func (memoryImpl) DeclaredEvents() []string { return nil }
func (memoryImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("memory has no source events")
}

func (memoryImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	m := memory.Active()
	if m == nil {
		return nil, fmt.Errorf("memory: not configured — add a top-level memory: section (store:/dir:/type: memory)")
	}
	str := func(k string) string { s, _ := opts[k].(string); return s }
	src := memory.SourceFrom(ctx)
	// EVERY verb is gated, not just remember. This face is reachable by any
	// `skill.verbs: [memory.*]` grant, so recall/list/forget were as open as
	// remember was before the H8 guard landed on it — a grant could read the
	// shared bucket it could not write, list every tenant's entries, and
	// delete an id belonging to another scope. memory.CheckOp is the one gate
	// this and the run:code binding share; for forget the scope is the stored
	// entry's, which is how ownership is enforced.
	scope := str("scope")
	if verb == "forget" {
		s, found, err := m.ScopeOf(str("id"))
		if err != nil {
			return nil, err
		}
		if found {
			scope = s
		}
	}
	if err := m.CheckOp(verb, scope); err != nil {
		return nil, err
	}
	switch verb {
	case "remember":
		e, err := m.Remember(str("text"), stringList(opts["tags"]), str("scope"), src)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": e.ID, "scope": e.Scope}, nil
	case "recall", "list":
		q := memory.Query{}
		if verb == "recall" {
			q.Tags = stringList(opts["tags"])
			q.Substring = str("substring")
			if n, ok := kvInt(opts["limit"]); ok {
				q.Limit = n
			}
		}
		if s := str("scope"); s != "" {
			q.Scopes = []string{memory.NormalizeScope(s)}
		}
		// `list` shares this branch: with a scope named it is a scoped
		// read like recall, and without one it is bounded by whatever the
		// installed scope guard permits (CheckOp above), not by nothing.
		entries, err := m.Recall(q)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.Map()
		}
		return map[string]any{"memories": out, "count": len(out)}, nil
	case "forget":
		found, err := m.Forget(str("id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"found": found}, nil
	}
	return nil, fmt.Errorf("memory: no verb %q", verb)
}

// stringList coerces a rendered tags option ([]any from YAML/JSON, []string
// from templates) into strings.
func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}
