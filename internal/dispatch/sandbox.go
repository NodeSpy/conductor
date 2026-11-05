package dispatch

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/gitpush"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// PaseoSandboxed turns the agent sandbox on for paseo dispatches: conductor
// launches its paseo agents with the provider's conductor-owned entry
// (<provider>-sandboxed, written by `conductor paseo setup`), which runs the
// agent through `conductor sandbox exec`, and registers the agent's
// workspace so that wrapper finds the dispatch's identity for it. Set at boot
// when sandbox.agents is on (the default).
var PaseoSandboxed bool

// PaseoSandboxLog reports each local paseo dispatch's sandbox verdict
// (nil: not logged). Wired at boot with the daemon's log.
var PaseoSandboxLog func(format string, a ...any)

// paseoSandboxDecision says whether a paseo dispatch launches through the
// conductor-owned sandboxed entry: with the sandbox on, every local one —
// a hand-off included — unless its isolation: is `{mode: none}`, the
// operator's explicit opt-out (a step's own, or its trigger's / workflow's /
// paseo runtime's, resolved onto the step by flow; validation keeps a pack
// from setting it). optedOut: the sandbox is on and this dispatch opted out.
func paseoSandboxDecision(req Request, remote bool) (sandboxed, optedOut bool) {
	if !PaseoSandboxed || remote {
		return false, false
	}
	if req.Step.Isolation.OptsOut() {
		return false, true
	}
	return true, false
}

// sandboxTitleMark tells the driver of a hand-off or a detached agent, in
// paseo's sidebar, whether it runs sandboxed ("" when the sandbox is off,
// or for a dispatch nobody drives).
func sandboxTitleMark(req Request, sandboxed, optedOut bool) string {
	switch {
	case !req.Interactive && !req.Step.Detach:
		return ""
	case sandboxed:
		return " [sandboxed]"
	case optedOut:
		return " [UNSANDBOXED]"
	}
	return ""
}

// sandboxLabel records the verdict on the paseo agent (sandbox=on|off)
// when the sandbox decides it (nothing when off, or for a remote paseo).
func sandboxLabel(sandboxed, optedOut bool) []string {
	switch {
	case sandboxed:
		return []string{"sandbox=on"}
	case optedOut:
		return []string{"sandbox=off"}
	}
	return nil
}

// logPaseoSandbox logs a dispatch's verdict.
func logPaseoSandbox(req Request, sandboxed, optedOut bool, provider string) {
	what := "dispatch"
	if req.Interactive {
		what = "hand-off"
	}
	logPaseoSandboxAs(req, what, agentTitle(req), sandboxed, optedOut, provider)
}

// logPaseoSandboxAs logs the verdict for a launch of kind what titled title.
func logPaseoSandboxAs(req Request, what, title string, sandboxed, optedOut bool, provider string) {
	if PaseoSandboxLog == nil || req.Shadow {
		return
	}
	switch {
	case sandboxed:
		PaseoSandboxLog("paseo %s %s: sandboxed (provider %s)", what, title, provider)
	case optedOut:
		p := provider
		if p == "" {
			p = "paseo's default"
		}
		PaseoSandboxLog("paseo %s %s: UNSANDBOXED — isolation: {mode: none} (the operator's opt-out); plain provider %s", what, title, p)
	}
}

// PaseoDefaultProvider is the provider a sandboxed paseo dispatch uses when
// its model resolution named none (paseo's own default would launch it
// unsandboxed).
const PaseoDefaultProvider = "claude"

// SandboxIdentity names who a dispatch's agent is: its profile, per
// repository — the tool state (HOME) it keeps across its launches and
// resumes, shared with no other profile and no other repository. The
// repository is the TRUSTED one: a dispatch whose target the event's sender
// chose gets a tool state of its own (keyed by the dispatch), never the
// real repository's.
func SandboxIdentity(req Request, tool string) string {
	who := req.Action.Agent
	if who == "" {
		who = "runtime-" + tool
	}
	if !req.Trigger.TargetTrusted {
		return "agent:" + who + "@dispatch-" + req.DispatchID
	}
	if repo := req.Trigger.OwnRepo(); repo != "" {
		return "agent:" + who + "@" + repo
	}
	return "agent:" + who
}

// RegisterSandboxWorkspace records dir in the sandbox registry on the first
// launch into it — while it is still conductor's own checkout — and returns
// the entry: the git layout read now is what every later launch (a resume)
// and conductor's own push use, never re-read from the agent-writable
// checkout. req nil: a profile-less relaunch, registered under identity.
func RegisterSandboxWorkspace(dir, identity, tool string, req *Request) (agentsandbox.Entry, error) {
	reg := agentsandbox.Default
	var prev *agentsandbox.Entry
	if e, ok := reg.Lookup(dir); ok && e.Workspace == canonPath(dir) {
		// This launch's own earlier registration (a resume: no request, or
		// the same dispatch), or the workspace's owner's — a member
		// registers its workspace under its own identity before launching
		// workers into it: that identity stands, a dispatch never re-keys
		// it. Another dispatch of the same identity re-registers: its push
		// and its step-end check find the workspace by its own dispatch id.
		if e.Dispatch == "" || (e.Identity == identity && (req == nil || req.DispatchID == e.Dispatch)) {
			return e, nil
		}
		prev = &e
	}
	var layout agentsandbox.GitLayout
	if prev != nil {
		// A follow-up dispatch into a workspace an agent has already worked
		// in: its git layout and push target are what conductor recorded
		// when the workspace was still its own — never re-read from the .git
		// pointer, alternates or HEAD the earlier agent could rewrite.
		if err := agentsandbox.ValidateRecorded(*prev); err != nil {
			return agentsandbox.Entry{}, err
		}
		layout = agentsandbox.GitLayout{ReadOnly: prev.ReadOnly, ReadWrite: prev.ReadWrite, Hide: prev.Hide, Common: prev.GitCommon, HeadDir: prev.GitWorktree}
		if prev.Push != nil {
			layout.GitDir, layout.HeadDir = prev.Push.GitDir, prev.Push.HeadDir
		}
	} else {
		var err error
		if layout, err = agentsandbox.ResolveGitLayout(dir); err != nil {
			return agentsandbox.Entry{}, err
		}
	}
	e := agentsandbox.Entry{
		Workspace: dir, Identity: identity, Tool: tool,
		ReadOnly: layout.ReadOnly, ReadWrite: layout.ReadWrite, Hide: layout.Hide,
	}
	if layout.Common != "" {
		e.GitCommon, e.GitWorktree = layout.Common, layout.HeadDir
	}
	if req != nil {
		e.Dispatch = req.DispatchID
		e.Model = sandboxModel(*req)
		e.Scope = SandboxScope(*req)
		if id, ok := SkillIdentityFor(*req); ok {
			e.Skill = &id
		}
		switch {
		case ReviewRole(*req):
		case req.WorkspaceArchive != "":
			// An artifact workspace: its .git (if any) is data the artifact
			// carried, not a checkout conductor made, so the remote it names
			// is never a push destination.
		case prev != nil && prev.Push != nil:
			pt := *prev.Push
			e.Push = &pt
		case prev == nil && layout.GitDir != "":
			// Only a repo conductor may work in gets a push target: never
			// one the event's sender named.
			if repo, err := CheckoutRepo(*req); err == nil {
				e.Push = pushTarget(dir, layout, repo)
			}
		}
		if layout.GitDir != "" || prev == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			e.StartHead, _ = gitpush.HeadCommit(ctx, dir, layout.GitDir, layout.HeadDir)
			cancel()
		}
	}
	reg.Register(e)
	got, _ := reg.Lookup(dir)
	return got, nil
}

// pushTarget is where conductor pushes dir's work: the branch the checkout
// is on (as conductor checked it out), to the remote its trusted git dir
// names. nil when the checkout is detached or has no remote.
func pushTarget(dir string, l agentsandbox.GitLayout, repo string) *agentsandbox.PushTarget {
	branch, err := gitpush.CheckedOutBranch(dir)
	if err != nil || branch == "" || gitpush.CheckBranch(branch) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remote, err := gitpush.RemoteURL(ctx, l.GitDir)
	if err != nil || remote == "" {
		return nil
	}
	return &agentsandbox.PushTarget{Repo: repo, Branch: branch, GitDir: l.GitDir, Remote: remote, HeadDir: l.HeadDir}
}

func canonPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// sandboxModel is the model a dispatch's agent runs, as a multi-provider
// tool names it ("provider/model"): its allowlist follows the provider.
func sandboxModel(req Request) string {
	if req.Model == "" || strings.Contains(req.Model, "/") || req.Provider == "" {
		return req.Model
	}
	return req.Provider + "/" + req.Model
}

// SandboxScope is what a dispatch's own scopes change of its agent
// sandbox: the allowance and detection / egress-limit overrides of the
// isolation: it resolved to (its step's, else its trigger's or workflow's,
// else its runtime's), and any allowance the operator granted its pack
// step. An agent-authored dispatch gets none: only the operator's own
// config loosens a sandbox. nil: nothing changes.
func SandboxScope(req Request) *agentsandbox.Scope {
	if req.AgentAuthored {
		return nil
	}
	iso := req.Step.Isolation
	if iso == nil {
		return scopeOf(req.Step.GrantedAllow, nil, nil)
	}
	s := scopeOf(config.UnionAllow(iso.Allow, req.Step.GrantedAllow), iso.Detect, iso.EgressLimits)
	k := knobsOf(iso)
	if req.Step.PackInstance != "" {
		// A pack's step: its egress_only, from whichever layer, only
		// narrows. Decided from provenance here, not carried as a flag.
		k.EgressOnlyNarrow = k.EgressOnlySet
	}
	return withKnobs(s, k)
}

// knobsOf is the part of a scope that names no origin — its caps, egress
// narrowing, protocol check and persistence — as a Scope.
func knobsOf(iso *config.IsolationConfig) *agentsandbox.Scope {
	var k agentsandbox.Scope
	if l := iso.Limits; l != nil {
		k.Memory, k.Pids, k.CPUWeight = l.Memory, max(l.Pids, 0), max(l.CPUWeight, 0)
		if iso.Mode == "" {
			// With a mode, cpu is the wrapper's own (--cpus for a container).
			k.CPU = l.CPU
		}
		if l.DiskFreeMin != "" {
			k.DiskSet = true
			k.DiskFreeFrac, k.DiskFreeBytes, k.DiskOff, _ = config.ParseDiskFreeMin(l.DiskFreeMin)
		}
	}
	if iso.EgressOnly != nil {
		k.EgressOnlySet, k.EgressOnly, k.EgressOnlyNarrow = true, append([]string{}, *iso.EgressOnly...), iso.EgressOnlyNarrow
	}
	k.ProtocolCheck, k.Persist = iso.EgressProtocolCheck, iso.Persist
	for _, h := range iso.Inspect {
		k.Inspect = append(k.Inspect, sandbox.InspectHost{Host: h.Host, CA: h.CA})
	}
	return &k
}

// withKnobs adds k's origin-less knobs to s (nil when neither sets any).
func withKnobs(s, k *agentsandbox.Scope) *agentsandbox.Scope {
	if k == nil || (k.Memory == "" && k.Pids == 0 && k.CPU == "" && k.CPUWeight == 0 && !k.DiskSet &&
		!k.EgressOnlySet && k.ProtocolCheck == nil && k.Persist == "" && len(k.Inspect) == 0) {
		return s
	}
	if s == nil {
		s = &agentsandbox.Scope{}
	}
	s.Memory, s.Pids, s.CPU, s.CPUWeight = k.Memory, k.Pids, k.CPU, k.CPUWeight
	s.DiskSet, s.DiskFreeFrac, s.DiskFreeBytes, s.DiskOff = k.DiskSet, k.DiskFreeFrac, k.DiskFreeBytes, k.DiskOff
	s.EgressOnlySet, s.EgressOnly, s.EgressOnlyNarrow = k.EgressOnlySet, k.EgressOnly, k.EgressOnlyNarrow
	s.ProtocolCheck, s.Persist, s.Inspect = k.ProtocolCheck, k.Persist, k.Inspect
	return s
}

// Rescope is a recorded scope as the live config has it now: its
// allowance rebuilt from the scopes and grants it names (one no longer in
// the config gives nothing, so revoking a grant or removing an allow:
// tightens every recorded workspace at once), and its detection and limit
// overrides re-read from theirs. nil: nothing changes.
func Rescope(s *agentsandbox.Scope, src config.ScopeSources) *agentsandbox.Scope {
	if s == nil {
		return nil
	}
	var allow *config.SandboxAllow
	if s.Allow != nil {
		for _, by := range strings.Split(s.Allow.By, " + ") {
			if iso := src.Sites[by]; iso != nil && iso.Allow != nil && iso.Allow.Origin == by {
				allow = config.UnionAllow(allow, iso.Allow)
			} else if g := src.Grants[by]; g != nil {
				allow = config.UnionAllow(allow, g)
			}
		}
	}
	var d *config.IsolationDetect
	if iso := src.Sites[s.DetectBy]; s.DetectBy != "" && iso != nil && iso.Detect != nil && iso.Detect.Origin == s.DetectBy {
		d = iso.Detect
	}
	var l *config.SandboxEgressLimits
	if iso := src.Sites[s.LimitsBy]; s.LimitsBy != "" && iso != nil && iso.EgressLimits != nil && iso.EgressLimits.Origin == s.LimitsBy {
		l = iso.EgressLimits
	}
	// The origin-less knobs (caps, egress_only, protocol check, persist)
	// were the dispatch's own scope's when it was recorded: kept as
	// recorded (a new dispatch records the live ones).
	return withKnobs(scopeOf(allow, d, l), s)
}

// scopeOf is the sandbox scope of an allowance and detection / egress-limit
// overrides (nil: nothing changes).
func scopeOf(allow *config.SandboxAllow, d *config.IsolationDetect, l *config.SandboxEgressLimits) *agentsandbox.Scope {
	var s agentsandbox.Scope
	if d != nil {
		s.DetectAction, s.Tripwires, s.DetectBy = d.Action, d.Tripwires, d.Origin
		if v, err := time.ParseDuration(d.Interval); err == nil && d.Interval != "" {
			s.DetectInterval = int64(v)
		}
		s.Connections = d.Connections
	}
	if l != nil {
		idle, _ := config.ParseLimitDuration(l.Idle)
		s.EgressIdle = int64(idle)
		s.EgressUpload, _ = config.ParseByteSize(l.Upload)
		s.EgressDownload, _ = config.ParseByteSize(l.Download)
		s.ModelUpload, _ = config.ParseByteSize(l.ModelUpload)
		s.ModelDownload, _ = config.ParseByteSize(l.ModelDownload)
		s.LimitsBy = l.Origin
	}
	s.Allow = AllowanceOf(allow)
	if reflect.DeepEqual(s, agentsandbox.Scope{}) {
		return nil
	}
	return &s
}

// AllowanceOf is a config allowance as the sandbox takes it (nil: none).
func AllowanceOf(a *config.SandboxAllow) *agentsandbox.Allowance {
	if a.Empty() {
		return nil
	}
	return &agentsandbox.Allowance{
		Tunnels: a.TunnelTools(), TunnelHosts: a.TunnelHosts(), TunnelAnyHost: a.TunnelAnyHost(), NonTLS: a.RawTargets(),
		ReverseShell: a.ReverseShell, Pty: a.Pty, By: a.Origin,
	}
}
