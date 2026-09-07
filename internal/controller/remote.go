package controller

import (
	"context"
	"net"
)

// HostArgvPrefix resolves the ssh argv prefix for a named `hosts:` entry:
// append(prefix, hosts.ShellJoin(argv)) (or the output of
// hosts.RemoteCommand/RemoteCommandEnv in place of ShellJoin(argv)) runs argv
// on that host. It is a package-level injectable seam, matching this
// package's existing DI pattern (cliLauncher, deckRunner, acpDialer,
// opencodeDialer): set once by cmd/conductor's wiring from the loaded
// config's `hosts:` block before any controller launches, and stubbed in
// tests. A nil value or an unknown host name is a launch-time error — there
// is no silent local fallback for a controller configured with `host:`.
var HostArgvPrefix func(hostName string) ([]string, error)

// resolveHost picks the host a controller launch runs on: the dispatched
// profile's host wins over the controller's own configured host (a per-agent
// override) — see AgentProfile.Host's doc comment. Only reachable where a
// Spec (and so a profile) is in scope, i.e. NewSession; ResumeSession has no
// profile to consult and uses the controller's host alone.
func resolveHost(controllerHost, profileHost string) string {
	if profileHost != "" {
		return profileHost
	}
	return controllerHost
}

// HostDial opens a net.Conn to addr AS SEEN FROM the named host (an
// `ssh -W` stdio forward — see hosts.Client.DialVia). The opencode
// controller's HTTP client uses it to reach a server the remote runtime
// bound to its own 127.0.0.1. Wired by cmd/conductor alongside
// HostArgvPrefix; nil or an unknown host is a launch-time error.
var HostDial func(ctx context.Context, hostName, addr string) (net.Conn, error)
