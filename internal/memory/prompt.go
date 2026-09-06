package memory

import (
	"fmt"
	"strings"
)

// Filter selects what an opted-in agent profile gets injected: which scopes,
// an optional tag narrowing, and a cap. Zero values fall back to the
// defaults: global + the target repo + the agent's own scope, capped at
// DefaultInjectLimit.
type Filter struct {
	Scopes []string
	Tags   []string
	Limit  int
}

// DefaultInjectLimit caps an injected memory section when the profile doesn't
// set its own limit.
const DefaultInjectLimit = 20

// PromptSection renders the memory block appended to an opted-in agent's
// prompt — through the same append path agent_guidance uses. repo/agent are
// the dispatch's target repo and agent profile name, used to resolve the
// default scopes and any relative scope in the filter. Returns "" when
// nothing matches, so a non-matching opt-in costs no tokens.
func (m *Manager) PromptSection(f Filter, repo, agent string) string {
	src := Source{Repo: repo, Agent: agent}
	scopes := f.Scopes
	if len(scopes) == 0 {
		scopes = []string{"global", "repo", "agent"}
	}
	var resolved []string
	for _, s := range scopes {
		r, err := ResolveScope(s, src)
		if err != nil {
			continue // a relative scope with no referent (no repo/agent) just drops
		}
		resolved = append(resolved, r)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultInjectLimit
	}
	entries, err := m.Recall(Query{Scopes: resolved, Tags: f.Tags, Limit: limit})
	if err != nil || len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n---\nShared memory — notes left by earlier agent runs; use them where relevant:\n")
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(e.Text), "\n", "\n  "))
		var meta []string
		if len(e.Tags) > 0 {
			meta = append(meta, "tags: "+strings.Join(e.Tags, ", "))
		}
		if e.Source.Agent != "" {
			meta = append(meta, "from: "+e.Source.Agent)
		}
		if len(meta) > 0 {
			b.WriteString(fmt.Sprintf(" (%s)", strings.Join(meta, "; ")))
		}
		b.WriteString("\n")
	}
	b.WriteString("To leave a note for future runs, end your final output with a fenced ```remember block — a YAML list of items, each `text:` with optional `tags:` and `scope:` (global | repo | agent).\n")
	// Redact on the way OUT: memories written before the write guard existed
	// (or via a trusted path) may carry tracked secrets, and this section is
	// appended to a plaintext agent prompt.
	return m.redactText(b.String())
}
