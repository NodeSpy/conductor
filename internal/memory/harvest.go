package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The output contract: an agent's FINAL output may carry a structured
// remember: block, which conductor parses post-run and persists with the
// run's provenance. It works for every runtime — no per-runtime tooling —
// because it rides on the output text itself. Two shapes are accepted:
//
//  1. A JSON output object carrying a "remember" key (works with
//     output_schema steps): a string, a list of strings, or a list of
//     { text, tags?, scope? } objects (the same wrapper unwrapping the
//     step-output parser applies — output/result/outputs).
//
//  2. A fenced block anywhere in a text output:
//
//     ```remember
//     - text: integration tests need the fake clock
//       tags: [testing]
//       scope: repo
//     - plain one-line memories work too
//     ```
//
//     The fence body is YAML: a list (strings or objects), a single object,
//     or a bare paragraph (one memory).

// HarvestOutput parses an agent's final output for the remember: contract and
// persists every carried memory with the given provenance. It returns the
// persisted entries; a malformed block returns an error (and persists
// nothing) so the failure is visible in logs/audit rather than silently
// dropped.
func (m *Manager) HarvestOutput(output string, src Source) ([]Entry, error) {
	notes, err := parseRememberBlocks(output)
	if err != nil {
		return nil, err
	}
	// The write guard vets every note BEFORE anything persists (all or
	// nothing): agent output is the least-trusted remember path, and without
	// this a tracked secret in a ```remember block landed in durable shared
	// memory verbatim — unlike the verb and code-binding paths.
	for _, n := range notes {
		if gerr := m.checkGuard(n.Text); gerr != nil {
			return nil, gerr
		}
		// The output contract is agent-authored, so the shared scope is
		// not the agent's to write into (see CheckAgentScope).
		if serr := CheckAgentScope(n.Scope); serr != nil {
			return nil, serr
		}
	}
	var out []Entry
	for _, n := range notes {
		e, err := m.Remember(n.Text, n.Tags, n.Scope, src)
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

// note is one parsed remember: item before persistence.
type note struct {
	Text  string   `json:"text" yaml:"text"`
	Tags  []string `json:"tags" yaml:"tags"`
	Scope string   `json:"scope" yaml:"scope"`
}

// parseRememberBlocks extracts the remember: contract from raw output text.
func parseRememberBlocks(output string) ([]note, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}
	// Shape 1: a JSON object output with a "remember" key (possibly under the
	// standard wrapper keys the step-output parser unwraps).
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err == nil {
		for _, k := range []string{"output", "result", "outputs"} {
			if inner, ok := obj[k].(map[string]any); ok {
				obj = inner
				break
			}
		}
		if raw, ok := obj["remember"]; ok {
			return normalizeNotes(raw)
		}
		return nil, nil
	}
	// Shape 2: fenced ```remember blocks in a text output.
	var notes []note
	rest := output
	for {
		_, after, found := strings.Cut(rest, "```remember")
		if !found {
			break
		}
		// The fence marker must be its own line ("```remembering…" is prose).
		nl := strings.IndexByte(after, '\n')
		if nl < 0 {
			if strings.TrimSpace(after) == "" {
				return nil, fmt.Errorf("memory: unterminated ```remember block in agent output")
			}
			rest = after // trailing prose, not a fence
			continue
		}
		if strings.TrimSpace(after[:nl]) != "" {
			rest = after
			continue
		}
		after = after[nl+1:]
		body, tail, closed := strings.Cut(after, "```")
		if !closed {
			return nil, fmt.Errorf("memory: unterminated ```remember block in agent output")
		}
		var v any
		if err := yaml.Unmarshal([]byte(strings.TrimSpace(body)), &v); err != nil {
			return nil, fmt.Errorf("memory: bad ```remember block: %w", err)
		}
		ns, err := normalizeNotes(v)
		if err != nil {
			return nil, err
		}
		notes = append(notes, ns...)
		rest = tail
	}
	return notes, nil
}

// normalizeNotes coerces the accepted remember: shapes into notes.
func normalizeNotes(v any) ([]note, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, nil
		}
		return []note{{Text: x}}, nil
	case []any:
		var out []note
		for _, item := range x {
			ns, err := normalizeNotes(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ns...)
		}
		return out, nil
	case map[string]any:
		n := note{}
		text, _ := x["text"].(string)
		n.Text = text
		if n.Text == "" {
			return nil, fmt.Errorf("memory: remember item needs a text: field, got %v", x)
		}
		if s, ok := x["scope"].(string); ok {
			n.Scope = s
		}
		switch tags := x["tags"].(type) {
		case []any:
			for _, t := range tags {
				n.Tags = append(n.Tags, fmt.Sprint(t))
			}
		case []string:
			n.Tags = tags
		case string:
			for _, t := range strings.Split(tags, ",") {
				n.Tags = append(n.Tags, strings.TrimSpace(t))
			}
		}
		return []note{n}, nil
	default:
		return nil, fmt.Errorf("memory: remember must be text, a list, or {text, tags, scope} — got %T", v)
	}
}
