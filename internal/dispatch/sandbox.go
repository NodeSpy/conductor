package dispatch

import (
	"context"
	"path/filepath"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/gitpush"
)

// PaseoSandboxed turns the agent sandbox on for paseo dispatches: conductor
// launches its paseo agents with the provider's conductor-owned entry
// (<provider>-sandboxed, written by `conductor paseo setup`), which runs the
// agent through `conductor sandbox exec`, and registers the agent's
// workspace so that wrapper finds the dispatch's identity for it. Set at boot
// when sandbox.agents is on (the default).
var PaseoSandboxed bool

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
	if e, ok := reg.Lookup(dir); ok && e.Identity == identity && e.Workspace == canonPath(dir) {
		return e, nil
	}
	layout, err := agentsandbox.ResolveGitLayout(dir)
	if err != nil {
		return agentsandbox.Entry{}, err
	}
	e := agentsandbox.Entry{
		Workspace: dir, Identity: identity, Tool: tool,
		ReadOnly: layout.ReadOnly, ReadWrite: layout.ReadWrite, Hide: layout.Hide,
	}
	if req != nil {
		e.Dispatch = req.DispatchID
		if id, ok := SkillIdentityFor(*req); ok {
			e.Skill = &id
		}
		if !ReviewRole(*req) && layout.GitDir != "" {
			e.Push = pushTarget(dir, layout, req.Trigger.Target.CheckoutRepo())
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
