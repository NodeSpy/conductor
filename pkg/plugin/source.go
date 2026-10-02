package plugin

import "encoding/json"

// THE SOURCE EXTENSION (connector ABI 1) — what a source plugin needs to be a
// full event source rather than a payload forwarder: its own triggers, its
// own filter evaluation, a catch-up sweep the daemon can nudge, manual
// injection (`conductor force`), and App-token re-mint for a resumed run. The
// design and its security reasoning are in docs/design/plugin-source-abi.md.
//
// Negotiation is Decl.ABI, as the protocol's version note prescribes: a
// connector plugin that sets ABI >= ConnectorABI speaks all of it; one that
// does not (every connector plugin before this extension) is driven exactly
// as before — no triggers on start_source, events matched by the daemon,
// none of the methods below ever called. A new method a plugin does not
// implement answers method-not-found, which the daemon treats as "not
// supported", so a plugin may implement any subset of the handlers.
//
//	daemon → plugin   plugin.start_source  + Triggers (the instance's triggers)
//	plugin → daemon   plugin.event         + Trigger / CatchUp / Instance
//	daemon → plugin   plugin.nudge         run the catch-up sweep now
//	daemon → plugin   plugin.force         build the events for one target, now
//	daemon → plugin   plugin.app_token     re-mint an App installation token
//	daemon → plugin   plugin.target_head   a target's head commit + state, now

// ConnectorABI is the connector-kind ABI revision this SDK speaks. A connector
// plugin reports it in Decl.ABI to receive the source extension.
const ConnectorABI = 1

// Source-extension methods (daemon → plugin requests).
const (
	MethodNudge      = "plugin.nudge"
	MethodForce      = "plugin.force"
	MethodAppToken   = "plugin.app_token"
	MethodTargetHead = "plugin.target_head"
)

// VerbSweep is a CONDUCTOR-DEFINED verb name. A connector plugin speaking
// ConnectorABI that declares a verb by this name is declaring "run the
// catch-up sweep now", and the DAEMON answers it — it nudges every source in
// the daemon that has a sweep (this plugin's instances through plugin.nudge,
// and any other), exactly as `conductor sweep --now` does. The call is never
// forwarded to the plugin, so the verb means the same thing whichever
// implementation backs the connector. Outputs: {nudged: <int>}.
const VerbSweep = "sweep"

// SourceTrigger is one configured trigger on a source instance, as the daemon
// hands it to a ConnectorABI plugin in StartSourceRequest.Triggers.
//
// The plugin evaluates it — routing, identity gates, its declared match keys,
// the trigger's `filter:` — and names it by ID on every event it fires for it.
// The daemon still owns what a trigger DOES (its workflow, its engine options):
// none of that crosses the wire.
type SourceTrigger struct {
	// ID is the daemon's stable handle for the trigger, unique per config.
	// Echo it in SourceEvent.Trigger; it means nothing else.
	ID string `json:"id"`
	// Name is the trigger's configured name ("" when unnamed) — the variant a
	// run is labelled with.
	Name string `json:"name,omitempty"`
	// Event is the declared event the trigger is `on:` (gh.new_comment →
	// "new_comment").
	Event string `json:"event"`
	// Enabled is the trigger's enabled switch (nil = enabled).
	Enabled *bool `json:"enabled,omitempty"`
	// Options are the trigger's event `options:` as the operator wrote them.
	Options map[string]any `json:"options,omitempty"`
	// Filter is the trigger's `filter:` in the STRUCTURAL form — decode it
	// with github.com/NodeSpy/conductor/pkg/sourcekit.Filter. Absent means no
	// filter (the event's intrinsic default applies).
	Filter json.RawMessage `json:"filter,omitempty"`
}

// Target is the object an event concerns. The legacy fields encode under Go
// field names (no tags) — the shape source plugins have always emitted under
// "target"; the daemon derives a key from them when Key is empty. Key, URL
// and Assigned are the generic form (plugin-contract.md §1.5): a target is
// identified by its key within an instance, and an event's
// semantics.target can build Key from its facts instead.
type Target struct {
	Key      string `json:"key,omitempty"`
	URL      string `json:"url,omitempty"`
	Assigned bool   `json:"assigned,omitempty"`

	Repo    string
	Owner   string
	Name    string
	PR      int
	Issue   int
	Number  int
	HeadSHA string
	BaseRef string
	HTMLURL string
	Project string
}

// SourceEvent is one plugin.event payload. The first block is what every
// source plugin has always sent; the second is the ConnectorABI extension.
type SourceEvent struct {
	// Event is the declared event name this is (the `on:` suffix).
	Event string `json:"event"`
	// Kind is the trigger kind the engine sees; empty means Event.
	Kind    string            `json:"kind,omitempty"`
	Title   string            `json:"title,omitempty"`
	Target  Target            `json:"target,omitempty"`
	Context map[string]any    `json:"context,omitempty"`
	Dedup   string            `json:"dedup,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`

	// Instance is the source instance this event belongs to (the
	// StartSourceRequest.Instance it came from). Set it when one plugin
	// process serves several instances; empty routes to the instance that
	// started last, as before.
	Instance string `json:"instance,omitempty"`
	// Trigger routes the event to exactly ONE trigger — the SourceTrigger.ID
	// the plugin evaluated it for. The daemon fires that trigger and no
	// other, and does not re-evaluate its filter: the plugin owns its match
	// keys. An event must name a trigger on its own Event. Empty falls back to
	// the daemon matching every trigger on `on:` with its generic evaluator.
	Trigger string `json:"trigger,omitempty"`
	// CatchUp marks an event the plugin's sweep re-derived rather than a
	// fresh delivery: when an agent already works the target, the engine
	// skips it instead of queueing it.
	CatchUp bool `json:"catch_up,omitempty"`
	// TargetTrusted is the older spelling of Target.Assigned: the platform
	// assigned Target (a signature-verified delivery, or a read with the
	// plugin's own credentials). Either one makes the claim.
	TargetTrusted bool `json:"target_trusted,omitempty"`
}

// NudgeRequest asks a source to run its catch-up sweep now (and reset any
// adaptive cadence). It must not block on the sweep.
type NudgeRequest struct {
	Instance string `json:"instance"`
}

// NudgeResult reports whether the instance has a sweep that was nudged.
type NudgeResult struct {
	Nudged bool `json:"nudged"`
}

// ForceRequest asks a source to build, now, the events kind would fire for
// one target — the `conductor force` path. The daemon marks every returned
// event forced (it bypasses the engine's dedup/backoff gates), which is why
// they come back in the response rather than on the event stream.
type ForceRequest struct {
	Instance string `json:"instance"`
	Kind     string `json:"kind"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
}

// ForceResult is the events a ForceRequest built, each routed to its trigger.
type ForceResult struct {
	Events []SourceEvent `json:"events"`
}

// AppTokenRequest asks for a fresh installation token for a GitHub-App-style
// source — what a persisted run resumed after a restart re-mints, from the
// installation_id its trigger context carried.
type AppTokenRequest struct {
	Instance       string `json:"instance"`
	InstallationID int64  `json:"installation_id"`
}

// AppTokenResult carries the token. The daemon tracks it as a secret.
type AppTokenResult struct {
	Token string `json:"token"`
}

// TargetHeadRequest asks for the CURRENT head commit and state of a target
// this instance's source emitted — what a run's hooks read as
// {{.run.start_sha}} / {{.run.head_sha}} and what names a stop's reason.
type TargetHeadRequest struct {
	Instance string `json:"instance"`
	Target   Target `json:"target"`
}

// TargetHeadResult is the head commit and the target's state: open | closed |
// merged, or "" when unknown.
type TargetHeadResult struct {
	SHA   string `json:"sha"`
	State string `json:"state,omitempty"`
}

// NudgeHandler is implemented by a ConnectorABI source with a catch-up sweep.
type NudgeHandler interface {
	Nudge(NudgeRequest) (NudgeResult, error)
}

// ForceHandler is implemented by a ConnectorABI source that supports manual
// injection.
type ForceHandler interface {
	Force(ForceRequest) (ForceResult, error)
}

// AppTokenHandler is implemented by a ConnectorABI source that mints App
// installation tokens.
type AppTokenHandler interface {
	AppToken(AppTokenRequest) (AppTokenResult, error)
}

// TargetHeadHandler is implemented by a ConnectorABI source whose targets have
// a head (a PR's head commit).
type TargetHeadHandler interface {
	TargetHead(TargetHeadRequest) (TargetHeadResult, error)
}
