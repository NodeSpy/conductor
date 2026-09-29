package sandbox

// AgentProfile is the macOS side of the agent workspace jail (#154 §10): what
// the Seatbelt profile adds for an agent launch beyond the plain allow-list.
// Linux expresses the same things as mounts (a tmpfs $HOME, shims bound over
// the real binaries), so it ignores this.
type AgentProfile struct {
	// ReadOnly / ReadWrite are extra paths (the tool-state paths in the real
	// home, the shim dir, the broker dir, the scratch home and TMPDIR).
	ReadOnly  []string
	ReadWrite []string
	// ExecDeny are the real binaries of the host set: running `/usr/local/bin/gh`
	// by absolute path is refused, only the shim on PATH works.
	ExecDeny []string
	// UnixSockets are the unix sockets the agent may connect to (its broker).
	UnixSockets []string
	// ProxyPort is conductor's loopback egress proxy port when the network is
	// restricted (the only outbound route); 0 = no restriction.
	ProxyPort int
	// Keychain allows the Security framework services claude-code's login
	// needs (see seatbelt_agent.go).
	Keychain bool
}
