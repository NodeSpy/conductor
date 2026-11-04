package agentsandbox

import (
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Allowance loosens a launch's detection and TLS check: what the operator
// allowed it in sandbox.allow or the isolation: blocks of its scopes (see
// config.SandboxAllow). What it covers is audited as allowed, by By, and
// not killed or refused; everything else still applies.
type Allowance struct {
	// Tunnels are tunnel-tool program names the watcher lets run.
	Tunnels []string `json:"tunnels,omitempty"`
	// TunnelHosts join the launch's egress allowlist.
	TunnelHosts []string `json:"tunnel_hosts,omitempty"`
	// TunnelAnyHost: a tunnel tool is allowed by name alone, to any host
	// the launch reaches.
	TunnelAnyHost bool `json:"tunnel_any_host,omitempty"`
	// NonTLS are targets exempt from the TLS first-bytes check.
	NonTLS       []string `json:"non_tls,omitempty"`
	ReverseShell bool     `json:"reverse_shell,omitempty"`
	Pty          bool     `json:"pty,omitempty"`
	// By names the scopes that granted it.
	By string `json:"by,omitempty"`
}

// Scope is what one dispatch's own scopes change of its sandbox, resolved
// by conductor from the operator's config when the workspace is
// registered: an allowance, and overrides of the detection action,
// tripwires and egress limits.
type Scope struct {
	Allow *Allowance `json:"allow,omitempty"`
	// DetectAction is DetectKill, DetectAlert or DetectOff ("": the
	// global setting).
	DetectAction string `json:"detect_action,omitempty"`
	// Tripwires overrides sandbox.detect.tripwires (nil: the global).
	Tripwires *bool `json:"tripwires,omitempty"`
	// EgressIdle (ns), EgressUpload, EgressDownload override the global
	// limits: 0 keeps it, -1 is off.
	EgressIdle, EgressUpload, EgressDownload int64 `json:",omitempty"`
	// ModelUpload, ModelDownload override the model gateway's caps the
	// same way.
	ModelUpload, ModelDownload int64 `json:",omitempty"`
	// DetectBy and LimitsBy name the scopes the detection and egress-limit
	// overrides came from (Allow.By names the allowance's): what a recorded
	// scope is recomputed from when the config changes.
	DetectBy string `json:"detect_by,omitempty"`
	LimitsBy string `json:"limits_by,omitempty"`
	// Memory and Pids are the scope's launch caps over sandbox.limits
	// ("" / 0: the global); a scope that sets one is capped even where
	// sandbox.limits is off.
	Memory string `json:"memory,omitempty"`
	Pids   int    `json:"pids,omitempty"`
}

// JoinAllowances is every allowance of list in one (nil: none).
func JoinAllowances(list ...*Allowance) *Allowance {
	var u Allowance
	var by []string
	any := false
	for _, a := range list {
		if a == nil {
			continue
		}
		any = true
		for _, t := range a.Tunnels {
			u.Tunnels = appendUnique(u.Tunnels, strings.ToLower(t))
		}
		for _, h := range a.TunnelHosts {
			u.TunnelHosts = appendUnique(u.TunnelHosts, h)
		}
		for _, h := range a.NonTLS {
			u.NonTLS = appendUnique(u.NonTLS, h)
		}
		u.TunnelAnyHost = u.TunnelAnyHost || a.TunnelAnyHost
		u.ReverseShell = u.ReverseShell || a.ReverseShell
		u.Pty = u.Pty || a.Pty
		for _, b := range strings.Split(a.By, " + ") {
			if b != "" {
				by = appendUnique(by, b)
			}
		}
	}
	if !any {
		return nil
	}
	u.By = strings.Join(by, " + ")
	return &u
}

// applyScope puts the base allowance and a dispatch's scope onto its
// policy.
func applyScope(p *Policy, base *Allowance, s *Scope) {
	var own *Allowance
	if s != nil {
		own = s.Allow
		switch s.DetectAction {
		case DetectKill, DetectAlert, DetectOff:
			p.Detect.Action = s.DetectAction
		}
		if s.Tripwires != nil {
			p.Detect.NoTripwires = !*s.Tripwires
		}
		if p.Detect.Action == DetectOff {
			p.Detect.NoTripwires = true
			p.Detect.Connections = sandbox.ShapeOff
		}
		if s.Memory != "" {
			p.Limits.Memory, p.Limits.Off = s.Memory, false
		}
		if s.Pids > 0 {
			p.Limits.Pids, p.Limits.Off = s.Pids, false
		}
		for _, o := range []struct {
			v   int64
			dst *int64
		}{{s.EgressIdle, &p.EgressIdle}, {s.EgressUpload, &p.EgressUpload}, {s.EgressDownload, &p.EgressDownload},
			{s.ModelUpload, &p.ModelUpload}, {s.ModelDownload, &p.ModelDownload}} {
			if o.v != 0 {
				*o.dst = o.v
			}
		}
	}
	a := JoinAllowances(base, own)
	if a == nil {
		return
	}
	p.Detect.AllowTunnels = a.Tunnels
	p.Detect.AllowReverseShell, p.Detect.AllowPty = a.ReverseShell, a.Pty
	p.Detect.AllowedBy = a.By
	for _, h := range a.TunnelHosts {
		p.Egress = appendUnique(p.Egress, h)
	}
	p.NonTLS, p.NonTLSBy = a.NonTLS, a.By
	// A tunnel the allowance covers may be shaped like one.
	p.Detect.ShapeAllowed = append(append([]string(nil), a.TunnelHosts...), a.NonTLS...)
	p.Detect.ShapeAllowAll = a.TunnelAnyHost
}
