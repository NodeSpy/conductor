package dispatch

// reviewKinds are trigger kinds whose agents review rather than fix.
var reviewKinds = map[string]bool{"review_requested": true, "self_review": true}

// ReviewRole reports a dispatch whose agent reviews rather than fixes: a
// decide step, a step with an output schema, a checkout-less step, or a
// review trigger — unless the step says `expect_push:`. conductor pushes a
// sandboxed agent's work (git.push, the end-of-step push) only for a
// dispatch that is not a review: a reviewer's commits stay in its workspace.
func ReviewRole(req Request) bool {
	s := req.Step
	review := s.DecisionLaunch != nil || len(s.OutputSchema) > 0 || s.Checkout == "none" || reviewKinds[req.Trigger.Kind]
	return review && !s.ExpectPush
}
