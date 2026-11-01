package memory

import (
	"fmt"
	"strings"
)

// Filter selects what an opted-in step gets injected: which scope keys, an
// optional tag narrowing, and a cap. An empty Scopes list falls back to the
// caller's default keys (the engine supplies the shared set plus the run's
// context keys — see PromptSection). Extra is appended to whichever of the
// two applies, so a step can add keys without restating the defaults.
type Filter struct {
	Scopes []string
	Extra  []string
	Tags   []string
	Limit  int
}

// DefaultInjectLimit caps an injected memory section when the profile doesn't
// set its own limit.
const DefaultInjectLimit = 20

// PromptSection renders the memory block appended to an opted-in step's
// prompt — through the same append path the guidance stack uses.
//
// The scope keys are OPAQUE (docs/design/agents-removal.md §2): this package
// does not know what any of them mean. f.Scopes, when set, is used verbatim;
// when empty the caller's contextKeys are used, which is how the engine
// supplies its convention (the shared set, the repo string, the workflow
// name, the step identity) without the memory core learning those concepts.
// f.Extra is appended to either. Returns "" when nothing matches, so a
// non-matching opt-in costs no tokens.
func (m *Manager) PromptSection(f Filter, contextKeys []string) string {
	explicit := len(f.Scopes) > 0
	scopes := f.Scopes
	if !explicit {
		scopes = contextKeys
	}
	base := len(scopes)
	scopes = append(append([]string(nil), scopes...), f.Extra...)
	var resolved []string
	seen := map[string]bool{}
	for i, s := range scopes {
		r := NormalizeScope(s)
		if s == "" && (explicit || i >= base) {
			continue // an explicitly-empty entry in a filter is a no-op
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		resolved = append(resolved, r)
	}
	if len(resolved) == 0 {
		return ""
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
		if e.Source.Step != "" {
			meta = append(meta, "from: "+e.Source.Step)
		}
		if len(meta) > 0 {
			b.WriteString(fmt.Sprintf(" (%s)", strings.Join(meta, "; ")))
		}
		b.WriteString("\n")
	}
	b.WriteString("To leave a note for future runs, end your final output with a fenced ```remember block — a YAML list of items, each `text:` with optional `tags:` and `scope:` (omit scope: for the shared set, or name one of the scope keys above).\n")
	// Redact on the way OUT: memories written before the write guard existed
	// (or via a trusted path) may carry tracked secrets, and this section is
	// appended to a plaintext agent prompt.
	return m.redactText(b.String())
}

// ---------------------------------------------------------------------------
// The engine's context-key CONVENTION
//
// These helpers live here for discoverability, but note what they are NOT:
// the memory core does not interpret a scope key, and nothing below is
// privileged. A caller could pass the same strings by hand. They exist so the
// engine and the flow runner agree on which keys a run contributes. Scope
// keys an operator writes are rendered by RenderScopeKey (scopekey.go).
// ---------------------------------------------------------------------------

// LegacyStepScope is the key a step's private namespace had before `agents:`
// was removed: memories written by `agent: fixer` landed under
// "agent:fixer". A migrated step keeps the old agent name as its `name:`, so
// including this alias in the default recall set means an operator's
// accumulated memories stay VISIBLE across the upgrade instead of silently
// vanishing. Writes always go to the clean identity key; this is read-side
// only, and can be dropped once configs have turned over.
func LegacyStepScope(identity string) string {
	if identity == "" {
		return ""
	}
	return "agent:" + identity
}

// ContextKeys is the default recall set for an opted-in step: the shared
// (no-key) set, plus whichever of the run's context keys exist — the repo
// string, the workflow name, the step identity, and the step's legacy
// pre-removal alias. Empty components are skipped rather than producing a
// key that matches nothing.
func ContextKeys(repo, workflow, stepIdentity string) []string {
	keys := []string{GlobalScope}
	for _, k := range []string{repo, workflow, stepIdentity, LegacyStepScope(stepIdentity)} {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	return keys
}
