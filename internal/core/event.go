// Package core holds the integration-agnostic types that flow through
// paseo-conductor: the normalized Trigger emitted by every integration, and the
// Integration interface + type registry the engine uses to start them.
package core

import "context"

// KindClosed is a reserved kind an integration emits when the underlying object
// (e.g. a PR) reaches a terminal state, so the engine drops its dedup state. It
// never dispatches an action.
const KindClosed = "_closed"

// Target identifies the GitHub (or future-source) object a Trigger concerns.
// Fields are populated best-effort from the webhook payload; zero values mean
// "not applicable" (e.g. PR == 0 for an issue-only trigger).
type Target struct {
	Repo    string // "owner/name"
	Owner   string
	Name    string
	PR      int
	Issue   int
	Number  int // the PR or issue number, whichever applies
	HeadSHA string
	BaseRef string
	HTMLURL string
	// Project is the paseo project/workspace to check out, when it differs from
	// Repo (e.g. the forge repo and the registered paseo project differ in org or
	// casing). Set by an integration's project_map; empty means "use Repo".
	// Only affects checkout resolution — forge operations still use Repo.
	Project string
}

// CheckoutRepo returns the paseo project to check out: Project when set, else
// Repo. Forge operations should use Repo directly, not this.
func (t Target) CheckoutRepo() string {
	if t.Project != "" {
		return t.Project
	}
	return t.Repo
}

// Trigger is the normalized unit of work. Integrations translate raw provider
// events into Triggers and hand them to the engine via an EmitFunc.
type Trigger struct {
	Source   string            // e.g. "github"
	Instance string            // integration instance name (for labels/logs)
	Kind     string            // e.g. "merge_conflict", "review_requested"
	Target   Target            //
	Title    string            // human-readable summary for titles/logs
	Context  map[string]any    // template data for prompts/commands
	Dedup    string            // dedup signature; empty => always act
	Labels   map[string]string // extra labels to attach to dispatched work
	Action   any               // integration-resolved action (engine asserts to config.Action)
	// CatchUp marks a trigger emitted by the periodic sweep (re-derived state)
	// rather than a fresh webhook event. When an agent is already working the PR,
	// catch-up triggers are skipped (don't re-nudge) while fresh events are queued
	// to that agent.
	CatchUp bool
}

// Key returns the stable per-object key used by the dedup store.
func (t Trigger) Key() string {
	if t.Target.Repo == "" {
		return t.Source + ":" + t.Instance
	}
	return t.Target.Repo + "#" + itoa(t.Target.Number)
}

// EmitFunc receives Triggers from an integration. It must be safe for
// concurrent use; the engine's implementation enqueues onto its work channel.
type EmitFunc func(context.Context, Trigger)

// Integration is a source of Triggers (GitHub today; Slack/Discord later).
// Each configured instance is one Integration value.
type Integration interface {
	// Name is the instance name (unique across the config).
	Name() string
	// Validate checks the instance's configuration up front.
	Validate() error
	// Start runs until ctx is cancelled, emitting Triggers as events arrive.
	Start(ctx context.Context, emit EmitFunc) error
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
