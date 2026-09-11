// Package core holds the integration-agnostic types that flow through
// conductor: the normalized Trigger emitted by every integration, and the
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
	Variant  string            // action-variant name when a kind has multiple; "" for the sole action
	Target   Target            //
	Title    string            // human-readable summary for titles/logs
	Context  map[string]any    // template data for prompts/commands
	Dedup    string            // dedup signature; empty => always act
	Labels   map[string]string // extra labels to attach to dispatched work
	Action   any               // integration-resolved action (engine asserts to config.Action)
	// TargetTrusted marks a dispatch whose TARGET was assigned by the SOURCE
	// ITSELF — a signature-verified github payload, a slack channel id, a
	// synthetic target derived from the source's own configured name — rather
	// than taken from data the sender of the event supplied.
	//
	// The zero value is UNTRUSTED, and that inversion is the point. Three
	// separate findings in a row were the same shape: a Target built from
	// payload data (a webhook `repo:` templated from the POST body, a plugin
	// source's wire event, a run_step rebuilding its trigger) that nobody
	// remembered to mark. A field whose safe state is the zero value cannot be
	// forgotten — a new source that says nothing gets the safe answer, and
	// claiming trust is a deliberate line of code with a reviewer's question
	// attached: who chose this value?
	//
	// The scope layer reads it and extends "your own target needs no grant"
	// only to a trusted one: a forged target gets no implicit own-repo, no own
	// memory scope, none of the target-derived facts in a `{{ }}` allowlist
	// entry, and no say in an agent-authored step's identity namespace.
	TargetTrusted bool
	// DispatchID is the daemon-assigned id of the dispatch this trigger was
	// RECONSTRUCTED from, set only on the live-tool path (run_step). It is
	// unique per launching dispatch and chosen by conductor — never by the
	// event's sender, never by the agent — which is what makes it usable as a
	// confinement anchor when the target cannot be trusted.
	DispatchID string
	// CatchUp marks a trigger emitted by the periodic sweep (re-derived state)
	// rather than a fresh webhook event. When an agent is already working the PR,
	// catch-up triggers are skipped (don't re-nudge) while fresh events are queued
	// to that agent.
	CatchUp bool
	// Force marks a manually-injected trigger (`conductor force`): the engine
	// bypasses its dedup / liveness / backoff gates so the action runs now, even if
	// it thinks the state is already handled. The kill switch and pause still apply.
	Force bool
	// HistoryID, when non-empty, pins the id of this run's §20 history record
	// instead of the engine minting a fresh one. It exists so a caller that
	// dispatched the trigger (the §13 callable-invoke surface) can hand back a
	// run_id up front and then read the record — GET /runs/<id>, wait, callback —
	// by that exact id. It must be filesystem-safe and unique per run; the
	// invoke surface generates it. Empty (the default) keeps the engine's own
	// id assignment, so nothing else changes.
	HistoryID string
}

// OwnRepo is THE RULE for "which repo may this dispatch treat as its own",
// and it exists because the answer had started being re-derived per struct.
//
// "The trusted dispatch" is represented three times — core.Trigger, the
// provenance a live tool is handed (memory.Source), and the identity a skill
// session stands for (flow.SkillIdentity) — and each carries a repo next to
// the bit that says whether the event's SENDER chose it. Every own-scope
// decision has to combine those two the same way; when the combining lived at
// the call sites instead, one face was fixed per round and the next face kept
// the raw repo. The memory IPC face granted implicit own-scope from a
// webhook-forged repo for exactly that reason.
//
// So the combination is written once, here, and every representation exposes
// it as an accessor rather than handing out its raw fields. "" means the
// dispatch has no own repo — which is the correct, deny-by-default answer for
// a target the sender picked.
func OwnRepo(repo string, targetTrusted bool) string {
	if !targetTrusted {
		return ""
	}
	return repo
}

// OwnRepo is the repo this dispatch may treat as its own. See core.OwnRepo.
func (t Trigger) OwnRepo() string { return OwnRepo(t.Target.Repo, t.TargetTrusted) }

// Key returns the stable per-object key used by the dedup store, the session
// broker (the live interactive review hand-off), the PR labels a dispatch
// carries, and the run id.
//
// It is built from the TRUSTED repo. A dispatch whose target the event's
// SENDER chose — a webhook `repo:` templated from the POST body — gets a key
// in its own namespace instead, so it can never equal the key a real dispatch
// for that repo produces (round-12 #3). Without that, a forged target landed
// on the victim PR's broker binding and was handed its live review session;
// it also shared the victim's dedup entry, which is the same reach wearing a
// different hat.
//
// A trusted dispatch's key is unchanged — "owner/repo#7", the spelling every
// existing store record uses.
func (t Trigger) Key() string {
	if repo := t.OwnRepo(); repo != "" {
		return repo + "#" + itoa(t.Target.Number)
	}
	if t.Target.Repo != "" {
		// An untrusted target still needs a STABLE key — dedup and session
		// reuse are what make a webhook source usable — so it keeps its repo
		// and number, namespaced by the source that produced it. The sender
		// picks what goes after the prefix; they do not pick the prefix.
		return t.Source + ":" + t.Instance + ":" + t.Target.Repo + "#" + itoa(t.Target.Number)
	}
	return t.Source + ":" + t.Instance
}

// EmitFunc receives Triggers from an integration. It must be safe for
// concurrent use; the engine's implementation enqueues onto its work channel.
type EmitFunc func(context.Context, Trigger)

// CompletionHook, when set, is invoked by the engine right after it stamps a
// dispatch's outcome (see the engine's auditDispatch), once per trigger, with
// the final outcome: "ok", "failed", "skipped", "adopted", "queued", or
// "shadow". It lets an integration correlate a completed dispatch back to the
// Trigger it originally emitted (via Trigger.Dedup) — e.g. Slack posting
// on_done/on_fail feedback. nil (default) is a no-op: dispatch behavior is
// unchanged.
var CompletionHook func(t Trigger, outcome string)

// SetCompletionHook installs the completion hook (set once at startup by main
// wiring). Passing nil clears it.
func SetCompletionHook(fn func(t Trigger, outcome string)) { CompletionHook = fn }

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

// Forcer is an optional integration capability: build and emit trigger(s) for a
// specific kind + target on demand (the `force` command), bypassing the usual
// applicability filters. Returns how many triggers it emitted. An integration
// that can't build a trigger for the kind/target returns an error.
type Forcer interface {
	Force(ctx context.Context, kind, repo string, number int, emit EmitFunc) (int, error)
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
