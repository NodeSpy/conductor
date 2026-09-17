package config

import "fmt"

// HELPER STEPS are the step forms conductor runs ITSELF — flow control and
// small utilities with no agent, no engine, no connector verb and no command
// behind them. They are a form like any other (mutually exclusive with
// `type:`/`use:`/`uses:`/`call:`), they honor `id:`/`if:`/`for_each:` and they
// appear in the run timeline; what makes them a family is that the runner
// needs nothing from the outside world to execute one, so each is a handful
// of lines rather than a dispatch path.
//
// `sleep:` is the first. Adding a second (`log:`, `noop:`) is four edits, all
// of them here-shaped:
//
//  1. the field on Step (connectors.go, under "helper form"),
//  2. a HelperXxx constant and its case in Step.HelperForm below,
//  3. its case in validateHelperStep below, if the value can be wrong,
//  4. its case in the runner's execHelper (internal/flow/helpers.go).
//
// Nothing else branches on the form: Step.Form, the mutual-exclusion check,
// the step dispatcher, the plan validator and the plan guard all ask
// HelperForm/IsHelper rather than naming a helper, so a new one reaches every
// one of them by being listed above.
const (
	// HelperSleep is `sleep: <duration>` — pause the flow, respecting
	// cancellation.
	HelperSleep = "sleep"
)

// HelperForm returns which helper this step is ("" when it is not one). It is
// the single place a helper is recognized from its fields.
func (s Step) HelperForm() string {
	switch {
	// A negative duration counts as the sleep form so validateHelperStep can
	// say what is wrong with it, rather than the step reading as formless.
	// `sleep: 0` is the one value that cannot be seen here — it decodes
	// identically to an absent key — and is rejected on the YAML node
	// instead, in Step.UnmarshalYAML.
	case s.Sleep != 0:
		return HelperSleep
	}
	return ""
}

// IsHelper reports whether this step is a helper step.
func (s Step) IsHelper() bool { return s.HelperForm() != "" }

// validateHelperStep checks a helper step's own value. A step that is not a
// helper is not this function's business.
func validateHelperStep(w string, s Step) error {
	switch s.HelperForm() {
	case HelperSleep:
		if s.Sleep.D() <= 0 {
			return fmt.Errorf("config: %s: `sleep: %s` must be a POSITIVE duration (e.g. `sleep: 5s`)", w, s.Sleep.D())
		}
	}
	return nil
}
