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
//
// IT DOES NOT MOVE FOR AN ADDITION. The daemon compares it for EXACT equality
// (internal/plugin/client.go, Describe), so bumping it would refuse every
// plugin already in the field — including ones a new daemon understands
// perfectly. New surface is therefore negotiated by Decl.ABI, which is absent
// on every existing plugin and read only where it means something.
const ProtocolVersion = 1

// EngineABI is the STEP-ENGINE ABI revision this SDK speaks, reported in
// Decl.ABI by an engine plugin (see Decl.ABI). It is INDEPENDENT of
// ProtocolVersion: the envelope, the framing and the describe/invoke surface
// are unchanged, so only a plugin that serves plugin.run has an ABI at all.
const EngineABI = 1

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
	// MethodRun (daemon→plugin request) runs ONE code step on a step-engine
	// plugin: RunRequest in, RunResult out. It is the out-of-process form of
	// what `use: js` does in-binary and `use: cli` does in a subprocess.
	MethodRun = "plugin.run"
)

// Host-callback methods — the NEW direction: a request the PLUGIN issues and
// the DAEMON answers, valid only while one of that plugin's plugin.run calls
// is in flight. They are the out-of-process face of ctx.store/ctx.sql/
// ctx.memory, and they carry exactly the internal ctx data-plane request:
// HostRequest/HostResult mirror internal/code's CtxRequest/CtxResponse field
// for field, so the plugin wire and the `cli` engine's unix socket are ONE
// ABI in front of ONE handler (internal/code.CtxHandler).
//
// The method selects the kind; HostRequest.Kind is the same value, and the
// daemon refuses a request whose body names a different kind than its method
// (a host.kv call can never smuggle a sql op past a reader of the log).
const (
	MethodHostKV     = "host.kv"
	MethodHostSQL    = "host.sql"
	MethodHostMemory = "host.memory"
)

// Host kinds — the three data-plane faces, named as they are on the wire and
// as internal/code names them (CtxKindKV/CtxKindSQL/CtxKindMemory).
const (
	HostKindKV     = "kv"
	HostKindSQL    = "sql"
	HostKindMemory = "memory"
)

// HostMethodFor is the host.* method that carries a kind, and the inverse
// HostKindFor reads a kind back off a method. Both return "" for a value they
// do not know, so a caller never invents a method name by concatenation.
func HostMethodFor(kind string) string {
	switch kind {
	case HostKindKV:
		return MethodHostKV
	case HostKindSQL:
		return MethodHostSQL
	case HostKindMemory:
		return MethodHostMemory
	}
	return ""
}

// HostKindFor is HostMethodFor's inverse.
func HostKindFor(method string) string {
	switch method {
	case MethodHostKV:
		return HostKindKV
	case MethodHostSQL:
		return HostKindSQL
	case MethodHostMemory:
		return HostKindMemory
	}
	return ""
}

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
	// KindStep is a STEP ENGINE: what executes a code step's work, reached by
	// a step's `use: <name>`. Its wire value is "engine" — the same word as
	// the config kind (config.UseKindEngine) and the official repo's
	// `engines/` directory — because the daemon cross-checks a plugin's
	// declared Kind against the BLOCK it was referenced from, and those two
	// strings have to be the same string to compare. The Go name says what it
	// serves (a step); the value says where it is declared.
	KindStep Kind = "engine"
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
	Kind Kind `json:"kind,omitempty"`
	// ABI is the KIND-SPECIFIC ABI revision this plugin speaks, and is how
	// surface gets added to the protocol WITHOUT touching ProtocolVersion.
	//
	// Absent/zero means "a plugin from before this field existed" — every
	// connector and runtime in the field today — and is read by nobody: the
	// daemon accepts any ProtocolVersion==1 plugin exactly as it always did,
	// and consults ABI only for KindStep, where it selects which plugin.run /
	// host.* shape both sides speak (EngineABI). A connector that sets it is
	// simply describing something no one asks about.
	//
	// This is the whole negotiation, and it is deliberately boring: a new
	// field with a zero value that means "the old thing" cannot break an old
	// plugin, because an old plugin never emits it and a new daemon never
	// requires it.
	ABI          int          `json:"abi,omitempty"`
	Type         string       `json:"type"`
	Desc         string       `json:"desc,omitempty"`
	Connection   Schema       `json:"connection,omitempty"`
	Verbs        []Verb       `json:"verbs,omitempty"`
	Events       []Event      `json:"events,omitempty"`
	Capabilities Capabilities `json:"capabilities,omitempty"`
	// Auth, when set, declares that this connector authenticates via conductor's
	// MANAGED OAuth2: the plugin bakes in the provider's endpoints + default
	// scopes here, the operator supplies client_id/client_secret + grant +
	// token_vault in the connector's `auth:` config block, and the daemon runs
	// its own OAuth2 authenticator for the connector — so `conductor connector
	// auth <name>` performs the one-time login and the daemon injects a fresh,
	// auto-rotated bearer token into each InvokeRequest.Connection under
	// AccessTokenKey. The plugin never performs the token exchange itself.
	//
	// Zero value (nil) means "no managed auth" — the connector authenticates
	// however its own connection fields say — so this is back-compatible: an old
	// plugin never emits it and an old daemon never reads it.
	Auth *AuthSpec `json:"auth,omitempty"`
	// Protocols, on a runtime plugin, are the DECISION protocols it answers
	// natively (e.g. ProtocolSystemOneV1). A runtime that declares protocols
	// and does not speak the agent-launch verb set is a DECISION-ONLY
	// runtime: conductor sends it decide: steps and never an agent step. It
	// serves two verbs over the ordinary plugin.invoke:
	//
	//   - VerbDecide: options {protocol, model, state, questions} (the
	//     protocol's request body plus its name); outputs {answers, model,
	//     usage}. The daemon validates every answer against the questions
	//     before anything downstream reads it.
	//   - VerbModels: no options; outputs {models: [{id, name, released}]} —
	//     the roster fleets resolve against.
	//
	// Zero value means "no decision protocols" (every runtime before this
	// field existed), so it is back-compatible in both directions.
	Protocols []string `json:"protocols,omitempty"`
}

// ProtocolSystemOneV1 is the system_one/v1 decision protocol — TypeSafe's
// published System One contract, as conductor's decide: step speaks it.
const ProtocolSystemOneV1 = "system_one/v1"

// Decision-runtime verbs (see Decl.Protocols).
const (
	VerbDecide = "decide"
	VerbModels = "models"
)

// AuthSpec is a connector's baked-in OAuth2 provider description (see Decl.Auth).
// Endpoints and default scopes live here so the operator only supplies
// credentials; the shared authConfig grants (client_credentials | refresh_token
// | authorization_code | device) are what Grants lists.
type AuthSpec struct {
	Grants        []string `json:"grants,omitempty" yaml:"grants,omitempty"`                   // supported grants, e.g. ["authorization_code","refresh_token"]
	TokenURL      string   `json:"token_url,omitempty" yaml:"token_url,omitempty"`             // OAuth2 token endpoint
	AuthURL       string   `json:"auth_url,omitempty" yaml:"auth_url,omitempty"`               // consent endpoint (authorization_code)
	DeviceAuthURL string   `json:"device_auth_url,omitempty" yaml:"device_auth_url,omitempty"` // device-authorization endpoint (device grant)
	Scopes        []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`                   // default scopes if the operator sets none
	// AuthParams are extra query parameters the plugin needs appended to the
	// authorization_code CONSENT URL — e.g. Google requires
	// {"access_type":"offline","prompt":"consent"} to return a refresh token.
	// Baked into the plugin so the operator needn't know provider quirks.
	AuthParams map[string]string `json:"auth_params,omitempty" yaml:"auth_params,omitempty"`
}

// AccessTokenKey is the reserved InvokeRequest.Connection key under which the
// daemon injects the managed OAuth2 bearer token for a connector that declares
// Decl.Auth. Plugins read it verbatim (see AccessToken); operators must not use
// it as one of their own connection fields.
const AccessTokenKey = "access_token"

// AccessToken returns the managed OAuth2 bearer token the daemon injected into a
// verb's connection map, or "" if none was injected (the connector declares no
// Auth, or the daemon is older than this field). A convenience for plugins.
func AccessToken(conn map[string]any) string {
	s, _ := conn[AccessTokenKey].(string)
	return s
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

// RunRequest is the daemon→plugin plugin.run params: ONE code step, with the
// three halves of the code-step ABI every engine shares (see internal/code):
//
//	inputs   the rendered ctx document the step's code reads
//	outputs  what the engine hands back (RunResult.Outputs)
//	data     ctx.store/ctx.sql/ctx.memory, over the host.* callbacks
//
// RunID is the capability that makes the third half work: it is a per-RUN
// random token, not an identifier — the daemon mints it, hands it over here,
// and answers a host.* request only while the run it names is in flight. It
// is the plugin wire's spelling of CONDUCTOR_CTX_TOKEN (internal/code's
// ctxsock.go), and it carries the same warning: it is not a name, do not log
// it, do not persist it, present it verbatim on every host.* call.
type RunRequest struct {
	// Instance is the engine's configured name, for the plugin's own logs.
	Instance string `json:"instance,omitempty"`
	// RunID authenticates this run's host.* callbacks. Empty means the daemon
	// granted no data plane, and every host.* call will be refused.
	RunID string `json:"run_id,omitempty"`
	// Code is the step's `code:` body — the script/program text, verbatim.
	Code string `json:"code,omitempty"`
	// Args are the step's `args:`, already templated.
	Args []string `json:"args,omitempty"`
	// Env are the step's `env:`, already templated. They are DATA, delivered
	// per-call over this transport — an engine plugin's own process
	// environment is the scrubbed minimal one every plugin gets, and a step's
	// env: never joins it.
	Env map[string]string `json:"env,omitempty"`
	// Inputs is the rendered ctx document (what `ctx` is to an in-process
	// engine, and what arrives as JSON on stdin for `use: cli`).
	Inputs map[string]any `json:"inputs,omitempty"`
}

// RunResult is the plugin→daemon plugin.run response: the step's outputs,
// which become `{{.steps.<id>.outputs.*}}` exactly as every other engine's do.
type RunResult struct {
	Outputs map[string]any `json:"outputs,omitempty"`
}

// HostRequest is one plugin→daemon data-plane call. It MIRRORS internal/code's
// CtxRequest field for field (run_id here is what token is there — the same
// per-run capability under the name the plugin wire gives it), so the daemon
// converts by copying rather than by translating, and the socket face and the
// plugin face cannot drift into two policies.
//
// Kind/Op/Resource/Args mirror the DataGuard signature one-for-one, and Args
// follows the POSITIONAL convention of the in-process bindings: `kv get ns
// key` is Args:["ns","key"], `kv set ns key v` is Args:["ns","key",v], `sql
// query` is Args:["SELECT …", [bind…]]. memory's ops take their own
// positional args and ignore Resource.
type HostRequest struct {
	// RunID is the capability from the RunRequest being served. A request
	// carrying the wrong one — or none, or one whose run has finished — is
	// refused by the daemon before any policy is consulted.
	RunID string `json:"run_id,omitempty"`
	// Kind is kv|sql|memory, and must equal the kind the method names.
	Kind string `json:"kind"`
	Op   string `json:"op"`
	// Resource is the DEFINED store the op names (kv/sql). Empty for memory,
	// which has no store dimension.
	Resource string `json:"resource,omitempty"`
	Args     []any  `json:"args,omitempty"`
}

// HostResult is the answer to one HostRequest, mirroring internal/code's
// CtxResponse. Exactly one of Value (OK) or Error (not OK) is meaningful.
//
// Refused separates a POLICY denial from a failure: conductor will not let
// this step do that (the no_secret_egress write barrier, the agent-authored
// store/scope allowlist, or an unauthenticated run_id) as opposed to "the
// store is down" or "that op wants three args". An engine should surface a
// refusal to the operator as a refusal and never retry it.
//
// A refusal is IN-BAND — an ok:false result, not a JSON-RPC error — for the
// same reason CtxResponse is: the daemon answering "no" is the protocol
// working. JSON-RPC errors stay for the transport's own failures (an unknown
// method, params that will not decode), matching the codes in serve.go.
type HostResult struct {
	OK      bool   `json:"ok"`
	Value   any    `json:"value,omitempty"`
	Error   string `json:"error,omitempty"`
	Refused bool   `json:"refused,omitempty"`
}
