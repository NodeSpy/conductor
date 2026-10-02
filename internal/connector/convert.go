package connector

import (
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/core"
)

// --- shared option/filter coercion helpers ---

// toStrings coerces a YAML list (or single string) into []string.
func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprintf("%v", e))
			}
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

// toInt coerces YAML integer shapes.
func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case uint64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// toDuration coerces a duration string or integer seconds ("" -> 0).
func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case string:
		if x == "" {
			return 0, nil
		}
		return time.ParseDuration(x)
	case int:
		return time.Duration(x) * time.Second, nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x) * time.Second, nil
	}
	return 0, fmt.Errorf("want a duration, got %T", v)
}

// truthy mirrors YAML-ish truthiness for option maps.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "no" && x != "0"
	case int:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// buildIntegration constructs a legacy integration instance from an in-memory
// config struct by round-tripping it through YAML into core.Build — the same
// decode path a hand-written legacy config takes, so lowered connectors run
// the exact code legacy configs run.
func buildIntegration(typ, name string, cfg any) (core.Integration, error) {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("connector %q: lower to %s config: %w", name, typ, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return nil, fmt.Errorf("connector %q: reparse %s config: %w", name, typ, err)
	}
	decode := func(v any) error { return node.Decode(v) }
	return core.Build(typ, name, decode)
}

// sortedFilterKeys is a debug/introspection helper listing a schema's keys.
func sortedFilterKeys(s Schema) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
