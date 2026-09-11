// Package plugin is the PUBLIC SDK for building external conductor plugins
// (#59). A plugin is a standalone binary the conductor daemon spawns and talks
// to over newline-delimited JSON-RPC 2.0 on stdin/stdout; this package gives
// plugin authors the wire types and a Serve loop so they never hand-roll the
// protocol. It has ZERO dependencies beyond the standard library, so an external
// plugin module stays small.
//
// The daemon side (internal/plugin) aliases these same types, so there is a
// SINGLE source of truth for the wire schema — a plugin built against this SDK
// is byte-compatible with the daemon by construction, and the daemon's own
// integration tests drive an SDK-built reference plugin to prove it.
//
// Minimal connector plugin:
//
//	package main
//
//	import "github.com/NodeSpy/conductor/pkg/plugin"
//
//	func main() {
//		plugin.Serve(plugin.ConnectorFunc(
//			func() plugin.Decl {
//				return plugin.Decl{
//					Type:  "acme",
//					Verbs: []plugin.Verb{{Name: "echo", Options: plugin.Schema{"message": {Type: "string", Required: true}}}},
//				}
//			},
//			func(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
//				return plugin.InvokeResult{Outputs: map[string]any{"message": req.Options["message"]}}, nil
//			},
//		))
//	}
package plugin

// ProtocolVersion is the plugin wire-protocol version this SDK speaks. The
// daemon refuses a plugin whose major version it does not understand, so Serve
// stamps this into every Decl that leaves it with a zero ProtocolVersion.
const ProtocolVersion = 1

// Wire method names.
const (
	MethodDescribe = "plugin.describe"
	MethodInvoke   = "plugin.invoke"
	// MethodStartSource (daemon→plugin request) tells a source plugin to begin
	// streaming events for one instance. The plugin acknowledges immediately and
	// then emits events as MethodEvent notifications until stdin closes.
	MethodStartSource = "plugin.start_source"
	// MethodEvent (plugin→daemon notification) carries one source event: a
	// serialized trigger the daemon feeds into its engine. One-way; no response.
	MethodEvent = "plugin.event"
)

// StartSourceRequest is the daemon→plugin start_source params: the instance name
// and its resolved config/credentials (a source plugin owns the listener/poller,
// so it needs the instance's full config, delivered per-call like a verb's
// connection — never via env).
type StartSourceRequest struct {
	Instance string         `json:"instance"`
	Config   map[string]any `json:"config,omitempty"`
}

// Kind is what a plugin provides.
type Kind string

const (
	KindConnector Kind = "connector"
	KindRuntime   Kind = "runtime"
)

// Field mirrors a connector option/output/connection field on the wire.
type Field struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	// Scope, on a VERB OPTION, declares that the option names a RESOURCE
	// rather than content, and names its dimension — "channel", "repo",
	// "store", "path", or one the plugin invents ("project", "bucket").
	// Conductor gates the VALUE of every scoped option on both agent-facing
	// surfaces: a dispatch may name the resource its own trigger points at,
	// plus whatever the operator allow-listed, and nothing else. Tagging an
	// option is the whole opt-in — there is no other wiring to do, and an
	// untagged option (text, body) is never gated.
	Scope string `json:"scope,omitempty"`
}

// Schema is a set of named fields.
type Schema map[string]Field

// Verb is one action verb the plugin exposes.
type Verb struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
	// Usage is an optional one-line WHAT/WHEN hint — "submit a pull-request
	// review; use after the review is reconciled". It is rendered into the
	// capability card conductor injects into a skill-enabled agent's prompt
	// and into the MCP tool description, so a verb describes itself ONCE
	// instead of every workflow prompt re-explaining it. Absent → the card
	// falls back to Desc.
	Usage   string `json:"usage,omitempty"`
	Options Schema `json:"options,omitempty"`
	Outputs Schema `json:"outputs,omitempty"`
	Ask     bool   `json:"ask,omitempty"`
}

// Event is one source event the plugin exposes. Declaring events is supported;
// live streaming of them (Source plugins) is delivered by the Source handler.
type Event struct {
	Name    string `json:"name"`
	Desc    string `json:"desc,omitempty"`
	Filters Schema `json:"filters,omitempty"`
	Context Schema `json:"context,omitempty"`
	Options Schema `json:"options,omitempty"`
	Dynamic bool   `json:"dynamic,omitempty"`
}

// Capabilities is the plugin's DECLARED PERMISSION MANIFEST: what it says it
// needs. Conductor records it at install, SURFACES it (so the operator sees what
// they accept when they add the plugin), and confines the plugin to it — a
// visible manifest with can't-exceed-declaration, not an OS jail. See
// docs/design/use-unification.md §D for exactly what that does and does not
// enforce.
type Capabilities struct {
	// Egress are the "host[:port]" targets the plugin calls. A connector
	// instance's `network:` may narrow this, never widen it.
	Egress []string `json:"egress,omitempty"`
	// Commands are the commands the plugin spawns, by name. Declaring them lets
	// conductor confine the subprocess's PATH to exactly these; leaving this
	// empty while setting Spawns declares "I spawn things I am not naming",
	// which is surfaced as such.
	Commands []string `json:"commands,omitempty"`
	// FS are the filesystem paths it needs.
	FS []string `json:"fs,omitempty"`
	// Spawns reports that the plugin spawns child processes. Implied by a
	// non-empty Commands.
	Spawns bool `json:"spawns,omitempty"`
}

// Decl is a plugin's full self-description, returned by Describe.
type Decl struct {
	ProtocolVersion int `json:"protocol_version"`
	// Kind is what this plugin PROVIDES: connector or runtime. It is the
	// authoritative answer — conductor derives the kind from the config block
	// the plugin was referenced from and refuses the plugin when the two
	// disagree, so a connector can never be wired as a runtime. Empty (an older
	// plugin) is treated as unspecified and trusted to its block.
	Kind         Kind         `json:"kind,omitempty"`
	Type         string       `json:"type"`
	Desc         string       `json:"desc,omitempty"`
	Connection   Schema       `json:"connection,omitempty"`
	Verbs        []Verb       `json:"verbs,omitempty"`
	Events       []Event      `json:"events,omitempty"`
	Capabilities Capabilities `json:"capabilities,omitempty"`
}

// InvokeRequest is the daemon→plugin verb call. Connection carries ONLY the
// calling instance's resolved credentials.
type InvokeRequest struct {
	Instance   string         `json:"instance"`
	Verb       string         `json:"verb"`
	Options    map[string]any `json:"options,omitempty"`
	Connection map[string]any `json:"connection,omitempty"`
}

// InvokeResult is the plugin→daemon verb response.
type InvokeResult struct {
	Outputs map[string]any `json:"outputs,omitempty"`
}
