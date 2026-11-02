package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// applyCheckoutPR resolves an agent step's `checkout_pr:` (config.Step.CheckoutPR)
// onto the trigger this dispatch carries: the PR it names becomes the checkout
// (Trigger.CheckoutPR, Target.Project) while forge authority stays on the
// trigger's own Target.Repo. t is the dispatch's own copy.
//
// Who may point a checkout somewhere else is the point of this function. A
// checkout clones a repository and lands an agent in it, so the value must
// come from someone the operator answers for:
//
//   - a value the CONFIG wrote (no template) — the operator or a pack author
//     chose it;
//   - a templated value on a run the operator started locally (`conductor run`,
//     Trigger.Operator) or a cron schedule started — its inputs are the
//     operator's own, and what its steps derive from them is the workflow the
//     config wrote;
//   - an override that names the trigger's own PR — it changes nothing.
//
// Anything else — a templated value on an event (its fields are the sender's)
// or on a remote invocation (Operator is false there) — is refused, loudly: a
// webhook payload must never pick what conductor clones. An agent-authored
// step never gets here with one (forbiddenAgentAuthoredField refuses it at
// admission; sanitizeAgentAuthoredStep strips an inherited one), and is
// ignored here as well.
func applyCheckoutPR(ctx context.Context, t core.Trigger, step config.Step, data map[string]any) (core.Trigger, error) {
	c := step.CheckoutPR
	if c == nil || agentAuthored(ctx) || step.Checkout != "checkout-pr" {
		// checkout: none (a consumer's override, say) checks nothing out
		return t, nil
	}
	templated := strings.Contains(c.Repo, "{{") || strings.Contains(c.PR, "{{")
	repo, pr := c.Repo, c.PR
	var err error
	if strings.Contains(repo, "{{") {
		if repo, err = render(repo, data); err != nil {
			return t, fmt.Errorf("checkout_pr.repo: %w", err)
		}
	}
	if strings.Contains(pr, "{{") {
		if pr, err = render(pr, data); err != nil {
			return t, fmt.Errorf("checkout_pr.pr: %w", err)
		}
	}
	name, n, err := config.ParseCheckoutPR(repo, pr)
	if err != nil {
		return t, dispatch.Unrecoverable(err)
	}
	if n == t.Target.PR && (strings.EqualFold(name, t.Target.Repo) || strings.EqualFold(name, t.Target.CheckoutRepo())) {
		// the trigger's own PR (by its forge name or its mapped checkout
		// project): nothing is redirected, and the checkout stays as it was
		return t, nil
	}
	if templated && !checkoutPRTrusted(t) {
		return t, dispatch.Unrecoverable(fmt.Errorf(
			"checkout_pr: %s#%d is not this %s trigger's own PR, and only a run the operator or a cron schedule started may check out a PR its data names (a config value may always)",
			name, n, t.Source))
	}
	t.Target.Project = name
	t.CheckoutPR = &core.PRRef{Repo: name, Number: n}
	return t, nil
}

// checkoutPRTrusted reports a run whose data the operator answers for: one the
// operator fired locally, or the builtin cron source (its context is config).
func checkoutPRTrusted(t core.Trigger) bool {
	return t.Operator || t.Source == "cron"
}
