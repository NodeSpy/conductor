package dispatch

// reviewKinds are trigger kinds whose agents review rather than fix.
var reviewKinds = map[string]bool{"review_requested": true, "self_review": true}

// ReviewRole reports a dispatch whose agent reviews rather than fixes: a
// decide step, a step with an output schema, a checkout-less step, or a
// review trigger — unless the step says `expect_push:`. It is not a policy
// switch of its own: each surface reads it for its own default — the jail's
// gh and git profiles make a review step's gh and git read-only (see
// internal/hostcmd), and a review step's verb grant is whatever its `skill:`
// block says (no write verbs unless it lists them).
func ReviewRole(req Request) bool {
	s := req.Step
	review := s.DecisionLaunch != nil || len(s.OutputSchema) > 0 || s.Checkout == "none" || reviewKinds[req.Trigger.Kind]
	return review && !s.ExpectPush
}
