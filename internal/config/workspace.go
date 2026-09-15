package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workspace is a step's `workspace:` value. It is POLYMORPHIC, and the two
// shapes answer two different questions:
//
//	workspace: worktree                          # isolation MODE only
//	workspace: { isolation: local, pin: triage }  # mode + a named workspace
//
// A bare string is the isolation mode the runtime creates the dispatch's
// workspace with (`local | worktree`) — exactly what the field has always
// meant, parsed exactly as before. The object form adds `pin:`, which NAMES a
// runtime workspace the step runs in, created on first use and REUSED by every
// later run of that step. A pin is state that outlives the run: it is how a
// long-lived triage or chat agent keeps its working directory (and anything it
// left there) between dispatches, instead of getting a fresh throwaway each
// time.
//
// The two fields are independent — `isolation` says how a workspace is made,
// `pin` says which one to reuse — so the object form may set either or both.
//
// DO NOT CONFUSE THIS WITH THE LEGACY GITHUB RULE'S `workspace:` (internal/
// integrations/github.Rule.Workspace), which is a different key in a different
// layer that happens to share a name: there it is a workspace ID/path to
// worktree from, and it flows to dispatch.Request.Workspace. See the flow map
// in docs/design/workspace-pin.md.
type Workspace struct {
	// Isolation is the runtime's workspace isolation mode for this dispatch:
	// "local" | "worktree", or "" for the runtime's own default.
	Isolation string
	// Pin names a runtime workspace this step always runs in — created on
	// first use, reused thereafter. "" means no pin (the default: the
	// dispatch gets its own workspace, reclaimed when it finishes).
	Pin string
}

// IsZero reports whether the value carries nothing, so `omitempty` drops it and
// the `extends:` merge treats it as unset (mergeStruct's scalar rule reads
// IsZero, and a struct is zero only when every field is).
func (w Workspace) IsZero() bool { return w == Workspace{} }

// workspaceModes are the isolation modes a runtime understands. "" is also
// accepted everywhere and means "the runtime's default".
var workspaceModes = []string{"local", "worktree"}

// UnmarshalYAML accepts either shape: a bare string is the isolation mode (the
// original, unchanged parse), a mapping is `{ isolation, pin }`. Anything else
// — a list, a bool, a null — is rejected with the two shapes spelled out,
// because a mistyped workspace silently landing on the default is the kind of
// thing nobody notices until an agent is running in the wrong directory.
func (w *Workspace) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		// A valueless `workspace:` never reaches here — yaml.v3 skips a custom
		// unmarshaler for a null node — and decodes to the zero value, i.e.
		// "unset", exactly like omitting the key. An explicit `""` lands here
		// and means the same thing.
		var s string
		if err := n.Decode(&s); err != nil {
			return fmt.Errorf("workspace: want %s or { isolation, pin }: %w", orList(workspaceModes), err)
		}
		*w = Workspace{Isolation: s}
		return nil

	case yaml.MappingNode:
		if len(n.Content) == 0 {
			return fmt.Errorf("workspace: an empty map configures nothing — remove it, or set isolation/pin")
		}
		// Strict: KnownFields does not reach into a custom unmarshaler, so a
		// typo'd key (`pinned:`, `isolate:`) would otherwise drop silently and
		// leave the step on the defaults it was trying to override.
		var body struct {
			Isolation string `yaml:"isolation,omitempty"`
			Pin       string `yaml:"pin,omitempty"`
		}
		if err := strictNodeDecode(n, &body); err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		// A `pin:` key that is present but blank is always a mistake — it reads
		// as "pin me" while behaving as "no pin". Catch it here rather than at
		// Validate so the error names the shape the author actually wrote.
		if valueAt(n, "pin") != nil && strings.TrimSpace(body.Pin) == "" {
			return fmt.Errorf("workspace: pin is blank — name the workspace to reuse, or drop `pin:`")
		}
		*w = Workspace{Isolation: body.Isolation, Pin: strings.TrimSpace(body.Pin)}
		return nil
	}
	return fmt.Errorf("workspace: want %s or { isolation, pin }, got a %s",
		orList(workspaceModes), nodeKindName(n))
}

// MarshalYAML re-emits the shape that was written: a pinless workspace is the
// bare isolation string it has always been (so a round-trip through the config
// writer leaves existing files byte-identical), and only a pin forces the map.
func (w Workspace) MarshalYAML() (any, error) {
	if w.Pin == "" {
		return w.Isolation, nil
	}
	body := map[string]string{"pin": w.Pin}
	if w.Isolation != "" {
		body["isolation"] = w.Isolation
	}
	return body, nil
}

// Validate checks the isolation mode. where labels the step for the error.
func (w Workspace) Validate(where string) error {
	switch w.Isolation {
	case "", "local", "worktree":
		return nil
	}
	return fmt.Errorf("config: %s: workspace isolation must be %s, got %q",
		where, orList(workspaceModes), w.Isolation)
}

// orList renders choices as `a|b` for an error message.
func orList(opts []string) string { return strings.Join(opts, "|") }
