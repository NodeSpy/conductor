package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Run facts: what a workflow-level hook knows about the run it brackets,
// under {{.run.*}} (docs/wiki/Workflows.md, "Run facts"):
//
//	run.start_sha   the target's head when the run started — read fresh as
//	                the run begins (after any wait for an agent slot), not
//	                the head the event saw
//	run.head_sha    the target's head when the hook fires (= start_sha at
//	                start), read fresh
//	run.head_short  head_sha's first 7 characters
//	run.pushed      the head moved during the run (both ends known, unequal)
//	run.reason      at fail / stop: a short, public-safe phrase for why
//	                (never the error text); "" otherwise
//
// Heads come from the event connector's HeadReader (a PR's head commit); a
// target with none, or a read that fails, leaves the shas "" and pushed
// false. They are read only when the trigger declares a hook for the phase,
// so a hookless trigger costs nothing.
type runFacts struct {
	startSHA, headSHA, reason string
	// state is the target's state as of the last head read (open | closed |
	// accepted | ""), and stopWords the connector's phrase for a stop in it.
	state, stopWords string
}

func (f runFacts) data() map[string]any {
	return map[string]any{
		"start_sha":  f.startSHA,
		"head_sha":   f.headSHA,
		"head_short": shortSHA(f.headSHA),
		"pushed":     f.startSHA != "" && f.headSHA != "" && f.headSHA != f.startSHA,
		"reason":     f.reason,
	}
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// headReadTimeout bounds one head read: run facts must never hold up a run.
const headReadTimeout = 10 * time.Second

// readHead reads the trigger target's current head and state through the
// connector that emitted it. "" when there is none or the read fails (logged
// and audited, never fatal). Dry runs read nothing.
func (r *Runner) readHead(ctx context.Context, t core.Trigger) (sha, state, reason string) {
	if r.DryRun || r.Conns == nil || t.Instance == "" {
		return "", "", ""
	}
	in, ok := r.Conns.Get(t.Instance)
	if !ok {
		return "", "", ""
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), headReadTimeout)
	defer cancel()
	h, err := in.TargetHead(c, t)
	if err != nil {
		msg := r.redactErr(err)
		r.Log("%s run facts: read head: %s", flowTag(t), msg)
		r.audit(map[string]any{"event": "run_head", "outcome": "failed", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "error": msg})
		return "", "", ""
	}
	return h.SHA, h.State, h.StopReason
}

// stopReason is run.reason for a stop: the run's target went away under it.
// The words are the connector's (its reads_revision reasons, carried on the
// head read the stop hooks take anyway); a connector that declares none
// gets a neutral phrase.
func stopReason(declared string) string {
	if declared != "" {
		return declared
	}
	return "its target closed"
}

// hasPhase reports whether hooks declares any hook at phase.
func hasPhase(hooks []config.Hook, phase string) bool {
	for _, h := range hooks {
		if h.At == phase {
			return true
		}
	}
	return false
}

// withRun is the run scope plus {{.run.*}}, for a workflow-level hook phase.
// A copy: the facts never leak into the steps' own scope.
func withRun(data map[string]any, f runFacts) map[string]any {
	d := cloneData(data)
	d["run"] = f.data()
	return d
}

// publicReason classifies a run failure into the short phrase run.reason
// carries. It never includes the error text — that can name paths, hosts, or
// a provider's response, and a hook may post it somewhere public (a commit
// status on a PR).
func publicReason(err error) string {
	var np *noProgressError
	switch {
	case dispatch.IsUnrecoverable(err):
		return "the agent couldn't be started"
	case errors.As(err, &np):
		return "the change was never pushed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	if id := failedStepID(err); id != "" {
		return fmt.Sprintf("step %q failed", id)
	}
	return "the run failed"
}

// ParkedReason is run.reason for an engine park (see FireParkedHooks).
const ParkedReason = "parked after repeated tries — needs a human or new commits"

// FireParkedHooks fires a trigger's workflow-level `at: fail` hooks for an
// event the ENGINE parked — a (target, kind, head) that kept failing and will
// not be retried until new commits — which never reaches a run of its own.
// The hooks get the same contract as a run's fail hooks: hook.failure with
// kind "parked" (gave_up true), and run facts with start_sha = the head it
// parked at, head_sha = the head now, reason = ParkedReason. No start hooks
// fire: nothing started.
func (r *Runner) FireParkedHooks(ctx context.Context, t core.Trigger, flowRef, parkedAt string, attempts int) {
	spec, idx, ok := r.SpecFor(flowRef)
	if !ok || !hasPhase(spec.Hooks, "fail") {
		return
	}
	ctx = withIdentityScope(ctx, config.ScopeForTrigger(spec, idx))
	ctx = context.WithValue(ctx, policyKey{}, r.resolvePolicy(spec))
	ctx = context.WithValue(ctx, botReplyKey{}, r.resolveBotReply(t, spec))
	data := baseData(t, r.SecretVals)
	addVaultData(data, r.VaultVals)
	head, _, _ := r.readHead(ctx, t)
	facts := runFacts{startSHA: parkedAt, headSHA: head, reason: ParkedReason}
	failure := map[string]any{"kind": "parked", "gave_up": true, "step": "",
		"error": fmt.Sprintf("parked after %d tries at %s", attempts, shortSHA(parkedAt))}
	r.fireHooks(ctx, t, spec.Hooks, "fail", "failed", "", "", withRun(data, facts), failure, "workflow")
}
