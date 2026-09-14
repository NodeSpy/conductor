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

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The wire schema is defined once in the PUBLIC SDK (pkg/plugin) and aliased
// here, so a plugin built against the SDK is byte-identical to what the daemon
// expects — there is no second copy to drift. The daemon's own integration
// tests drive an SDK-built reference plugin over the real transport to prove it.

// ProtocolVersion is the plugin wire-protocol version the daemon speaks. A
// plugin reports its own in Describe; the daemon refuses a plugin whose major
// version it does not understand (graceful degradation, not a crash).
const ProtocolVersion = sdk.ProtocolVersion

// Wire method names.
const (
	MethodDescribe    = sdk.MethodDescribe
	MethodInvoke      = sdk.MethodInvoke
	MethodStartSource = sdk.MethodStartSource
	MethodEvent       = sdk.MethodEvent
)

// Kind is what a plugin provides.
type Kind = sdk.Kind

const (
	KindConnector = sdk.KindConnector
	KindRuntime   = sdk.KindRuntime
)

// Spec is a resolved plugin ready to run: a derived config.PluginRef joined
// with local install state, its binary resolved to an absolute path.
// Construction is in manager.go.
type Spec struct {
	// Name is the implementation name (connector type / runtime name).
	Name string
	// Kind is what it provides, derived from the block that referenced it.
	Kind Kind
	// Provides is the registered name — the same as Name.
	Provides string
	// Version is the `@…` constraint from the reference, if any.
	Version string
	// Resolved is the concrete release tag the installed build came from.
	Resolved string
	// Use is the parsed reference, for origin display and re-resolution.
	Use config.Use
	// BinPath is the absolute path to the executable. Empty means "referenced
	// but not installed" — Start says so rather than exec'ing nothing.
	BinPath string
	// Args are extra argv appended at spawn. NOT settable from config: the old
	// `plugins:` block had an `args:` key, and the `use:` surface deliberately
	// does not — a plugin's configuration arrives over the RPC transport, per
	// instance, not as process arguments shared by all of them. Retained for
	// internal callers and tests that drive a reference plugin's modes.
	Args []string
	// Local marks a development binary the operator pointed at directly. There
	// is no sha to pin (it changes on every build); safe-permissions still applies.
	Local bool
	// Sha256 is the verified sha recorded at install, checked before every exec.
	Sha256 string
	// Manifest is the permission manifest recorded at install.
	Manifest Manifest
	// Network is the referencing connector's declared egress.
	Network []string
	// Isolation is OPTIONAL OS hardening. nil is the normal case.
	Isolation *config.IsolationConfig
	// AllowSecrets optionally tightens which secret refs may cross the boundary.
	AllowSecrets []string
}

// Ref is the `plugin@version` attribution string carried on audit records and
// shown in `plugin list`.
func (s Spec) Ref() string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "@" + s.Version
}

// Key is the plugin's identity in install state: "<kind-dir>/<name>".
func (s Spec) Key() string {
	if s.Kind == KindRuntime {
		return "runtimes/" + s.Name
	}
	return "connectors/" + s.Name
}

// Installed reports whether a binary is available to run.
func (s Spec) Installed() bool { return s.BinPath != "" }

// NotInstalledError is the error a not-yet-fetched plugin produces — a
// direction, not a stack trace.
func (s Spec) NotInstalledError() error {
	return fmt.Errorf("plugin %s (%s) is referenced by your config but not installed — run `conductor init` (or `conductor plugin update %s`) to fetch it", s.Name, s.Use.String(), s.Name)
}

// --- wire schema (aliased from pkg/plugin; maps 1:1 to connector.TypeDecl) ---

type (
	Field              = sdk.Field
	Schema             = sdk.Schema
	Verb               = sdk.Verb
	Event              = sdk.Event
	Capabilities       = sdk.Capabilities
	Decl               = sdk.Decl
	InvokeRequest      = sdk.InvokeRequest
	InvokeResult       = sdk.InvokeResult
	StartSourceRequest = sdk.StartSourceRequest
)
