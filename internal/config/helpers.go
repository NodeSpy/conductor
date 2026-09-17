package config

import (
	"fmt"
	"strings"
)

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
	// HelperLog is `log: <message>` — render a message into the run log.
	HelperLog = "log"
	// HelperSet is `set: {<key>: <value>}` — publish computed values as the
	// step's outputs, addressable as {{.<id>.<key>}}.
	HelperSet = "set"
	// HelperAssert is `assert: <expr>` — fail the run unless the expr is truthy.
	HelperAssert = "assert"
	// HelperFail is `fail: <message>` — stop the run with a rendered message.
	HelperFail = "fail"
	// HelperWaitFor is `wait_for: {uses, until, every, timeout}` — poll a read
	// verb until a condition holds or the timeout elapses.
	HelperWaitFor = "wait_for"
)

// HelperForm returns which helper this step is ("" when it is not one). It is
// the single place a helper is recognized from its fields — each helper sets a
// field no other form reads, so recognition is field identity, never a name.
func (s Step) HelperForm() string {
	switch {
	// A negative duration counts as the sleep form so validateHelperStep can
	// say what is wrong with it, rather than the step reading as formless.
	// `sleep: 0` is the one value that cannot be seen here — it decodes
	// identically to an absent key — and is rejected on the YAML node
	// instead, in Step.UnmarshalYAML.
	case s.Sleep != 0:
		return HelperSleep
	case s.Log != "":
		return HelperLog
	case s.Set != nil:
		return HelperSet
	case s.Assert != "":
		return HelperAssert
	case s.Fail != "":
		return HelperFail
	case s.WaitFor != nil:
		return HelperWaitFor
	}
	return ""
}

// IsHelper reports whether this step is a helper step.
func (s Step) IsHelper() bool { return s.HelperForm() != "" }

// helperFormsSet lists every helper field this step sets. HelperForm returns
// only the first (so a step is never formless), but the family counts as ONE
// form in the mutual-exclusion tally — which means two helper fields on one
// step would otherwise slip through as a single form. validateHelperStep uses
// this to reject that. Order matches HelperForm's switch.
func helperFormsSet(s Step) []string {
	var out []string
	if s.Sleep != 0 {
		out = append(out, HelperSleep)
	}
	if s.Log != "" {
		out = append(out, HelperLog)
	}
	if s.Set != nil {
		out = append(out, HelperSet)
	}
	if s.Assert != "" {
		out = append(out, HelperAssert)
	}
	if s.Fail != "" {
		out = append(out, HelperFail)
	}
	if s.WaitFor != nil {
		out = append(out, HelperWaitFor)
	}
	return out
}

// validateHelperStep checks a helper step's own value. A step that is not a
// helper is not this function's business.
func validateHelperStep(w string, s Step) error {
	// Two helper fields read as one form in validateStep's tally (the family is
	// counted once), so the "exactly one form" check cannot see them — catch it
	// here, with the same message a helper-beside-a-verb gets.
	if forms := helperFormsSet(s); len(forms) > 1 {
		return fmt.Errorf("config: %s: step forms are mutually exclusive (set exactly one of type/use/uses/call or a helper: sleep/log/set/assert/fail/wait_for) — this step sets %s", w, strings.Join(forms, " and "))
	}
	switch s.HelperForm() {
	case HelperSleep:
		if s.Sleep.D() <= 0 {
			return fmt.Errorf("config: %s: `sleep: %s` must be a POSITIVE duration (e.g. `sleep: 5s`)", w, s.Sleep.D())
		}
	case HelperSet:
		// An empty map decodes indistinguishably from an absent key (both give
		// a nil map, so HelperForm never even sees it); a map with no entries is
		// the operator writing `set: {}`, which sets nothing.
		if len(s.Set) == 0 {
			return fmt.Errorf("config: %s: `set:` needs at least one `key: value` to publish", w)
		}
	case HelperWaitFor:
		wf := s.WaitFor
		if strings.TrimSpace(wf.Uses) == "" {
			return fmt.Errorf("config: %s: `wait_for:` needs `uses:` — the read verb to poll (e.g. gh.get_run)", w)
		}
		if conn, verb, ok := strings.Cut(wf.Uses, "."); !ok || conn == "" || verb == "" {
			return fmt.Errorf("config: %s: `wait_for.uses: %s` must be <connector>.<verb>", w, wf.Uses)
		}
		if strings.TrimSpace(wf.Until) == "" {
			return fmt.Errorf("config: %s: `wait_for:` needs `until:` — the condition to wait for (e.g. \"status == 'completed'\")", w)
		}
		if wf.Timeout.D() <= 0 {
			return fmt.Errorf("config: %s: `wait_for:` needs a POSITIVE `timeout:` (e.g. `timeout: 2m`)", w)
		}
		if wf.Every.D() < 0 {
			return fmt.Errorf("config: %s: `wait_for.every: %s` must not be negative", w, wf.Every.D())
		}
	}
	return nil
}
