package secrets

import "fmt"

// Boundary handles (#36 §12). `{{secret "name"}}` renders as an OPAQUE
// handle everywhere a template renders — prompts, tool args, env — and the
// flow runner swaps in the real value only at conductor's own egress
// boundary (verb invocation, code-step env/args, remote-command env/argv),
// and only for config-authored steps. Anything agent-visible therefore holds
// the handle, never the value: an agent step's env-at-rest carries
// "«secret:name»", and an agent that needs the value goes through the
// broker (policy-gated, single-use, audited).
//
// The handle is deliberately NOT resolvable from agent-supplied strings: the
// runner computes resolution eligibility from the config-authored template
// source (which fields literally call {{secret "name"}}), so a handle pasted
// into agent output, a plan step, or a saved workflow never resolves.

const handlePrefix = "«secret:"
const handleSuffix = "»"

// Handle returns the opaque boundary handle for a named secret.
func Handle(name string) string { return handlePrefix + name + handleSuffix }

// SecretTemplateFunc is the `secret` template function shared by the flow
// and dispatch renderers: {{secret "name"}} → the opaque handle. The name
// must be a literal string — a computed name still renders a handle, but
// boundary resolution is keyed to literal calls and will never resolve it.
func SecretTemplateFunc(args ...any) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf(`secret takes exactly one name: {{secret "name"}}`)
	}
	name, ok := args[0].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("secret: the name must be a non-empty string, got %T", args[0])
	}
	return Handle(name), nil
}
