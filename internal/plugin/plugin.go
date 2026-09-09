// Package plugin runs EXTERNAL conductor plugins out-of-process (#54): a
// subprocess binary the daemon spawns and talks to over newline-delimited
// JSON-RPC 2.0 on the child's stdin/stdout (the same transport ACP uses,
// internal/acp/jsonrpc.go). A plugin acquires a connector `type:` or a
// `runtime:` name without recompiling the daemon.
//
// A plugin is arbitrary code the daemon executes, so every plugin passes a
// security gate before and around execution:
//
//   - verify-before-execute: the binary's SHA-256 is checked against the
//     configured pin, from a path with safe permissions, BEFORE it is ever
//     run (verify.go).
//   - least-privilege spawn: the child inherits a minimal env allowlist — never
//     the daemon's full environment (which carries credentials) — and a
//     credential is delivered per-call over the RPC transport, never in argv or
//     env (spawn.go, and the connector builder).
//   - enforced sandbox: when an isolation: block is configured the launch is
//     wrapped through internal/sandbox (the #36 §15 layer) — process/mount/pid
//     isolation, daemon-file masking, deny-by-default egress (spawn.go).
//   - untrusted output: every RPC response is size-bounded (bounded.go); the
//     connector/runtime layer additionally schema-validates it against the
//     plugin's declared Decl.
//   - supervision: every call has a timeout; a crashed or hung plugin degrades
//     to "that plugin is down" and never takes the daemon with it, with a
//     restart backoff that cannot crash-loop (manager.go).
package plugin

import "github.com/NodeSpy/conductor/internal/config"

// ProtocolVersion is the plugin wire-protocol version the daemon speaks. A
// plugin reports its own in Describe; the daemon refuses a plugin whose major
// version it does not understand (graceful degradation, not a crash).
const ProtocolVersion = 1

// Wire method names.
const (
	MethodDescribe = "plugin.describe"
	MethodInvoke   = "plugin.invoke"
)

// Kind is what a plugin provides.
type Kind string

const (
	KindConnector Kind = "connector"
	KindRuntime   Kind = "runtime"
)

// Spec is a resolved plugin ready to run: config.PluginRef with its source
// resolved to an absolute path. Construction (and path resolution) is in
// manager.go.
type Spec struct {
	Name            string
	Kind            Kind
	Provides        string
	Version         string
	BinPath         string // absolute path to the executable
	Args            []string
	Sha256          string
	AllowUnverified bool
	Isolation       *config.IsolationConfig
	AllowSecrets    []string
}

// Ref is the `plugin@version` attribution string carried on audit records and
// shown in `plugin list`.
func (s Spec) Ref() string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "@" + s.Version
}

// --- wire schema (maps 1:1 to connector.TypeDecl on the connector side) ---

// Field mirrors connector.Field on the wire.
type Field struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Desc     string   `json:"desc,omitempty"`
}

// Schema is a set of named fields.
type Schema map[string]Field

// Verb is one action verb the plugin exposes.
type Verb struct {
	Name    string `json:"name"`
	Desc    string `json:"desc,omitempty"`
	Options Schema `json:"options,omitempty"`
	Outputs Schema `json:"outputs,omitempty"`
	Ask     bool   `json:"ask,omitempty"`
}

// Event is one source event the plugin exposes (declared; live streaming of
// events — StartSource — is a documented follow-up, see docs/wiki/Plugins.md).
type Event struct {
	Name    string `json:"name"`
	Desc    string `json:"desc,omitempty"`
	Filters Schema `json:"filters,omitempty"`
	Context Schema `json:"context,omitempty"`
	Options Schema `json:"options,omitempty"`
	Dynamic bool   `json:"dynamic,omitempty"`
}

// Capabilities is the plugin's DECLARED privilege manifest (§8.3): what it says
// it needs. The operator GRANTS these via the isolation: block; `plugin show`
// prints declared-vs-granted so a mismatch is visible before install.
type Capabilities struct {
	Egress []string `json:"egress,omitempty"` // network hosts the plugin says it needs
	FS     []string `json:"fs,omitempty"`     // filesystem paths it says it needs
	Spawns bool     `json:"spawns,omitempty"` // whether it spawns child processes
}

// Decl is a plugin's full self-description, returned by Describe.
type Decl struct {
	ProtocolVersion int          `json:"protocol_version"`
	Type            string       `json:"type"`
	Desc            string       `json:"desc,omitempty"`
	Connection      Schema       `json:"connection,omitempty"`
	Verbs           []Verb       `json:"verbs,omitempty"`
	Events          []Event      `json:"events,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
}

// InvokeRequest is the daemon→plugin verb call. Connection carries ONLY the
// calling instance's resolved credentials (least privilege, own-type-only);
// the plugin holds no cross-instance state by protocol design.
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
