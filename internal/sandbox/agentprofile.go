package sandbox

// AgentProfile is the macOS side of the agent workspace jail (#154 §10): what
// the Seatbelt profile adds for an agent launch beyond the plain allow-list.
// Linux expresses the same things as mounts (a tmpfs $HOME, shims bound over
// the real binaries), so it ignores this.
type AgentProfile struct {
	// Home is the operator's real home (for the Keychain path).
	Home string
	// ReadOnly / ReadWrite are extra paths (the tool-state paths in the real
	// home, the shim dir, the broker dir, the scratch home and TMPDIR).
	ReadOnly  []string
	ReadWrite []string
	// WriteDeny are read-only carve-outs inside read-write paths (a git
	// common dir's config, hooks/, objects/info).
	WriteDeny []string
	// Hide are paths inside allowed ones that are denied outright (sibling
	// worktrees' metadata); Unhide re-allows paths inside them (this
	// dispatch's own gitdir).
	Hide   []string
	Unhide []string
	// ExecDeny are the real binaries of the host set: running `/opt/homebrew/bin/gh`
	// by absolute path is refused, only the shim on PATH works.
	ExecDeny []string
	// UnixSockets are the unix sockets the agent may connect to (its broker).
	UnixSockets []string
	// LoopbackPorts are host loopback ports the agent may reach when the
	// network is restricted (its own model endpoint on a local router).
	LoopbackPorts []int
	// Keychain allows the Security framework services a Keychain-held login
	// needs (see seatbelt_agent.go). Off by default: see docs/wiki/Isolation.md.
	Keychain bool
}
