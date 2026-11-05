package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ContinueOnError is a step's `continue_on_error:` — `true` (keep going
// past any error of the step), or a condition in the `if:` grammar: keep
// going past an error only when it holds, evaluated over the step's scope
// with the failed step's own outputs under its id and the failure (the
// hook.failure shape) as `failure`. `false` or absent: an error stops the
// workflow.
type ContinueOnError struct {
	Always bool
	When   string
}

// Set reports whether the step may continue past an error at all.
func (c ContinueOnError) Set() bool { return c.Always || c.When != "" }

// UnmarshalYAML accepts a boolean or a condition string.
func (c *ContinueOnError) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("continue_on_error: expected true/false or a condition")
	}
	var b bool
	if n.Tag == "!!bool" && n.Decode(&b) == nil {
		*c = ContinueOnError{Always: b}
		return nil
	}
	*c = ContinueOnError{When: n.Value}
	return nil
}

// IsZero keeps an unset continue_on_error out of what conductor writes.
func (c ContinueOnError) IsZero() bool { return !c.Set() }

// MarshalYAML writes the authored form back.
func (c ContinueOnError) MarshalYAML() (any, error) {
	if c.When != "" {
		return c.When, nil
	}
	return c.Always, nil
}
