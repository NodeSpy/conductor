package config

import (
	"fmt"
	"regexp"
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
//	workspace: { from: "{{.snap.blob}}" }        # start from an artifact
//
// `from:` starts the agent in a copy of a directory an earlier step stored
// with blob.put (a tar archive blob): conductor unpacks it into a fresh
// directory of its own for this dispatch alone, instead of making a checkout.
// It replaces the checkout, so it excludes `pin:` and a repo checkout.
//
//	workspace: { inputs: { build: "{{.build.archive}}" } }  # extra artifacts
//
// `inputs:` names extra artifacts the step gets beside its workspace, like a
// build's secondary sources: each a fresh copy of a blob the run holds, made
// for this step alone in a directory of its own (never inside the
// workspace), unpacked when it is a tar archive and copied as a file
// otherwise, and removed when the step ends. It works with `from:` or
// without it (a normal workspace plus inputs), on agent and code steps.
//
// DO NOT CONFUSE THIS WITH dispatch.Request.Workspace, a workspace ID/path to
// worktree from, which shares the name. See the flow map in
// docs/design/workspace-pin.md.
type Workspace struct {
	// Isolation is the runtime's workspace isolation mode for this dispatch:
	// "local" | "worktree", or "" for the runtime's own default.
	Isolation string
	// Pin names a runtime workspace this step always runs in — created on
	// first use, reused thereafter. "" means no pin (the default: the
	// dispatch gets its own workspace, reclaimed when it finishes).
	Pin string
	// From is the blob handle (a sole template reference to a step's .blob
	// output, or a sha256:… digest) of a directory archive the agent starts
	// in. "" means a checkout as usual.
	From string
	// Inputs are the named extra artifacts the step gets beside its
	// workspace, in the order written.
	Inputs []WorkspaceInput
}

// WorkspaceInput is one `workspace.inputs` entry: a name the step sees it
// under and the blob it is a copy of (a template or a sha256:… digest, as
// for From).
type WorkspaceInput struct {
	Name string
	From string
}

// inputName is what an input may be called: a lower-case identifier, so it
// is a path segment, a template key and (upper-cased) an env var name alike.
var inputName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// IsZero reports whether the value carries nothing, so `omitempty` drops it and
// the `extends:` merge treats it as unset (mergeStruct's scalar rule reads
// IsZero, and a struct is zero only when every field is).
func (w Workspace) IsZero() bool {
	return w.Isolation == "" && w.Pin == "" && w.From == "" && len(w.Inputs) == 0
}

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
			return fmt.Errorf("workspace: want %s or { isolation, pin, from, inputs }: %w", orList(workspaceModes), err)
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
			Isolation string    `yaml:"isolation,omitempty"`
			Pin       string    `yaml:"pin,omitempty"`
			From      string    `yaml:"from,omitempty"`
			Inputs    yaml.Node `yaml:"inputs,omitempty"`
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
		if valueAt(n, "from") != nil && strings.TrimSpace(body.From) == "" {
			return fmt.Errorf("workspace: from is blank — name the artifact to start from, or drop `from:`")
		}
		inputs, err := decodeInputs(valueAt(n, "inputs"))
		if err != nil {
			return err
		}
		*w = Workspace{Isolation: body.Isolation, Pin: strings.TrimSpace(body.Pin), From: strings.TrimSpace(body.From), Inputs: inputs}
		return nil
	}
	return fmt.Errorf("workspace: want %s or { isolation, pin, from, inputs }, got a %s",
		orList(workspaceModes), nodeKindName(n))
}

// decodeInputs reads `inputs:`, a map of name to blob reference, keeping the
// order written. nil (no key) is no inputs.
func decodeInputs(n *yaml.Node) ([]WorkspaceInput, error) {
	if n == nil {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		return nil, fmt.Errorf("workspace: inputs must be a map of name to artifact, e.g. { build: \"{{.build.archive}}\" }")
	}
	var out []WorkspaceInput
	for i := 0; i+1 < len(n.Content); i += 2 {
		var name, from string
		if err := n.Content[i].Decode(&name); err != nil {
			return nil, fmt.Errorf("workspace: inputs: %w", err)
		}
		if err := n.Content[i+1].Decode(&from); err != nil || n.Content[i+1].Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("workspace: inputs.%s must be an artifact reference (a string)", name)
		}
		if strings.TrimSpace(from) == "" {
			return nil, fmt.Errorf("workspace: inputs.%s is blank — name the artifact, or drop it", name)
		}
		out = append(out, WorkspaceInput{Name: name, From: strings.TrimSpace(from)})
	}
	return out, nil
}

// MarshalYAML re-emits the shape that was written: a pinless workspace is the
// bare isolation string it has always been (so a round-trip through the config
// writer leaves existing files byte-identical), and only a pin or from forces
// the map.
func (w Workspace) MarshalYAML() (any, error) {
	if w.Pin == "" && w.From == "" && len(w.Inputs) == 0 {
		return w.Isolation, nil
	}
	body := map[string]any{}
	if w.Pin != "" {
		body["pin"] = w.Pin
	}
	if w.From != "" {
		body["from"] = w.From
	}
	if w.Isolation != "" {
		body["isolation"] = w.Isolation
	}
	if len(w.Inputs) > 0 {
		in := &yaml.Node{Kind: yaml.MappingNode}
		for _, i := range w.Inputs {
			in.Content = append(in.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: i.Name}, &yaml.Node{Kind: yaml.ScalarNode, Value: i.From})
		}
		body["inputs"] = in
	}
	return body, nil
}

// Validate checks the isolation mode, the from reference and the inputs.
// where labels the step for the error.
func (w Workspace) Validate(where string) error {
	if f := w.From; f != "" && !blobRef(f) {
		return fmt.Errorf("config: %s: workspace.from must be a blob handle — a template such as \"{{.snap.blob}}\" or a sha256:… digest — got %q", where, f)
	}
	seen := map[string]bool{}
	for _, in := range w.Inputs {
		switch {
		case !inputName.MatchString(in.Name):
			return fmt.Errorf("config: %s: workspace.inputs: %q is not a valid input name (a lower-case identifier: a-z, 0-9, _; starting with a letter)", where, in.Name)
		case in.Name == "workspace":
			return fmt.Errorf("config: %s: workspace.inputs: %q names the step's own workspace; call the input something else", where, in.Name)
		case seen[in.Name]:
			return fmt.Errorf("config: %s: workspace.inputs: %q is named twice", where, in.Name)
		case !blobRef(in.From):
			return fmt.Errorf("config: %s: workspace.inputs.%s must be a blob handle — a template such as \"{{.build.archive}}\" or a sha256:… digest — got %q", where, in.Name, in.From)
		}
		seen[in.Name] = true
	}
	switch w.Isolation {
	case "", "local", "worktree":
		return nil
	}
	return fmt.Errorf("config: %s: workspace isolation must be %s, got %q",
		where, orList(workspaceModes), w.Isolation)
}

// blobRef reports whether ref can name a blob: a template (resolved at run
// time) or a sha256:… digest.
func blobRef(ref string) bool {
	return strings.Contains(ref, "{{") || (strings.HasPrefix(ref, "sha256:") && len(ref) == len("sha256:")+64)
}

// orList renders choices as `a|b` for an error message.
func orList(opts []string) string { return strings.Join(opts, "|") }
