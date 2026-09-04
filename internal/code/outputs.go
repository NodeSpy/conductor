package code

import (
	"encoding/json"
	"strings"
)

// ParseOutputs turns a host interpreter's raw stdout into a step's outputs
// map, per the shared output contract every `run:` engine (in-process or
// host) honors so that templates downstream never need to know which
// engine produced a given step's result:
//
//   - blank (after trimming) -> {} (no outputs; a script that only has side
//     effects, e.g. a notification, shouldn't have to print anything)
//   - a JSON object -> that object, verbatim (its keys become named
//     outputs — the common case: `echo '{"count": 3}'`)
//   - any other valid JSON (a bare number/string/bool/array) -> {"value": v}
//     (it parsed, but it isn't a set of named outputs, so it becomes the
//     step's single `value` output rather than being rejected)
//   - anything that isn't valid JSON at all -> {"text": s} (the script just
//     printed plain text; that's not an error, it's just not structured)
func ParseOutputs(s string) map[string]any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}

	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{"text": s}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"value": v}
}
