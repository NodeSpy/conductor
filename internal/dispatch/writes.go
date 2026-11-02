package dispatch

// reviewKinds are trigger kinds whose agents review rather than fix.
var reviewKinds = map[string]bool{"review_requested": true, "self_review": true}

// ReviewRole reports a dispatch whose agent reviews rather than fixes: a
// decide step, a step with an output schema, a checkout-less step, or a
// review trigger — unless the step says `expect_push:`. conductor pushes a
// sandboxed agent's work (git.push, the end-of-step push) only for a
// dispatch that is not a review: a reviewer's commits stay in its workspace.
func ReviewRole(req Request) bool { return ReviewBy(req) != "" }

// The reasons ReviewBy gives.
const (
	ReviewByDecide       = "decide"
	ReviewByCheckoutNone = "checkout_none"
	ReviewByTrigger      = "review_trigger"
	ReviewByOutputSchema = "output_schema"
)

// ReviewBy names why a dispatch is a review ("" = it is not). An output
// schema is named only when it is the sole reason: a structured result is
// what a reviewer returns, but a fixer may report one too (the threads it
// addressed), so it is the one reason that can be a fixer missing its
// `expect_push:` — flow fails such a step that leaves commits behind
// instead of dropping them (docs/wiki/Isolation.md).
func ReviewBy(req Request) string {
	s := req.Step
	switch {
	case s.ExpectPush:
		return ""
	case s.DecisionLaunch != nil:
		return ReviewByDecide
	case s.Checkout == "none":
		return ReviewByCheckoutNone
	case reviewKinds[req.Trigger.Kind]:
		return ReviewByTrigger
	case len(s.OutputSchema) > 0:
		return ReviewByOutputSchema
	}
	return ""
}
