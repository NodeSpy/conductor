package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitpush"
)

// SandboxWiring is the daemon's agent sandbox (docs/design/agent-sandbox.md),
// wired by cmd/conductor when sandbox.agents is on — the default. Every local
// cli/acp agent launch then runs under it, and fails closed: a launch whose
// sandbox cannot be built does not run. nil: agent launches are not
// sandboxed (sandbox.agents: off, and tests that do not opt in).
type SandboxWiring struct {
	Resolver *agentsandbox.Resolver
	// Relay pipes a host-loopback model endpoint into the sandbox.
	Relay func(target string) (string, func(), error)
}

// AgentSandbox is the wiring (nil = off).
var AgentSandbox *SandboxWiring

// agentLaunch marks a launch as an agent runtime's own (cli/acp), the
// launches the agent sandbox covers.
type agentLaunch struct {
	tool string
	// req is the dispatch (nil for a profile-less relaunch after a restart:
	// the identity is then the runtime's).
	req *dispatch.Request
	// skillEnv: mint the dispatch's conductor token into the agent's env (a
	// cli worker's `conductor call step.done`); an acp agent gets its tools
	// through the injected MCP server instead.
	skillEnv bool
}

// withAgent marks opt as an agent launch of tool for req.
func withAgent(opt launchOpts, tool string, req *dispatch.Request, skillEnv bool) launchOpts {
	opt.agent = &agentLaunch{tool: tool, req: req, skillEnv: skillEnv}
	return opt
}

// sandboxApplies reports whether a launch runs under the agent sandbox: a
// local agent launch with the sandbox on, unless its isolation block opts
// out (`mode: none`) or chose another wrapper (mode user or container, or
// namespace with `privileged: true`). A remote (host:) launch runs on
// another box, under that box's own isolation.
func sandboxApplies(host string, opt launchOpts) bool {
	if AgentSandbox == nil || opt.agent == nil || host != "" {
		return false
	}
	if iso := opt.iso; iso != nil {
		switch iso.Mode {
		case "none", "user", "container":
			return false
		case "namespace":
			return !iso.Privileged
		}
	}
	return true
}

// DispatchIdentity names who a dispatch's agent is: its profile, per
// repository — the tool state (HOME) it keeps across its launches and
// resumes, shared with no other profile and no other repository. The
// repository is the TRUSTED one: a dispatch whose target the event's sender
// chose gets a tool state of its own (keyed by the dispatch), never the
// real repository's.
func DispatchIdentity(req dispatch.Request, tool string) string {
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

// prepareAgentSandbox builds a cli/acp launch's sandbox and wraps argv in
// it. The returned env is COMPLETE: the caller runs the launch with exactly
// it, never on top of the daemon's own environment.
func prepareAgentSandbox(dir string, env, argv []string, opt launchOpts) (wrapped []string, localDir string, localEnv []string, cleanup func(), err error) {
	w := AgentSandbox
	if w == nil || w.Resolver == nil {
		return nil, "", nil, nil, fmt.Errorf("agent sandbox: not wired")
	}
	if len(argv) == 0 {
		return nil, "", nil, nil, fmt.Errorf("agent sandbox: nothing to launch")
	}
	a := opt.agent
	tool := agentsandbox.CanonicalTool(a.tool)
	if tool == "" {
		tool = agentsandbox.CanonicalTool(argv[0])
	}
	var identity string
	switch {
	case a.req != nil:
		identity = DispatchIdentity(*a.req, firstNonEmptyStr(tool, a.tool))
	default:
		// A profile-less relaunch (a resume): the identity the workspace was
		// registered with, so the session finds its own tool state.
		identity = "agent:runtime-" + firstNonEmptyStr(tool, a.tool)
		if dir != "" {
			if e, ok := agentsandbox.Default.Lookup(dir); ok {
				identity = e.Identity
			}
		}
	}
	if dir == "" {
		// A checkout-less agent (a cron trigger, a judge) works in a scratch
		// directory of its identity's own — never the daemon's cwd.
		dir = filepath.Join(w.Resolver.StateRoot, agentsandbox.IdentityKey(identity), "work")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", nil, nil, fmt.Errorf("agent sandbox: scratch workspace: %w", err)
		}
	}
	entry, err := registerWorkspace(dir, identity, tool, a.req)
	if err != nil {
		return nil, "", nil, nil, err
	}
	if iso := opt.iso; iso != nil {
		// An explicit namespace block shapes the sandbox further: its
		// network allowlist joins the model endpoints, its fs: paths are
		// shown read-write.
		if iso.Network != nil {
			entry.Egress = append(append([]string(nil), entry.Egress...), iso.Network.Egress...)
		}
		for _, p := range iso.FS {
			entry.ReadWrite = append(append([]string(nil), entry.ReadWrite...), p)
		}
	}
	// The token is minted below with the dispatch's own rules (an
	// interactive hand-off's durable session), not the resolver's.
	mintless := entry
	mintless.Skill = nil
	pol, err := w.Resolver.PolicyForEntry(mintless, tool, env)
	if err != nil {
		return nil, "", nil, nil, err
	}
	pol.Socket = w.Resolver.Socket
	if a.skillEnv && a.req != nil && pol.Socket != "" {
		if se := dispatch.SkillEnv(*a.req, "unix://"+pol.Socket); se != nil {
			pol.Token = se["CONDUCTOR_SKILL_TOKEN"]
		}
	}
	pol.Env = append(pol.Env, nonCredentialEnv(env)...)
	launch, err := agentsandbox.Wrap(pol, argv, agentsandbox.Deps{
		SelfExe:    launchSelfExe,
		EgressUnix: EgressProxyUnix,
		EgressAddr: EgressProxyFor,
		Relay:      w.Relay,
		LookPath:   exec.LookPath,
		RealHome:   w.Resolver.RealHome,
		// The model credentials and endpoints come from the daemon's own
		// environment or the dispatch's; everything else is dropped.
		Inherited: append(os.Environ(), env...),
	})
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("agent sandbox: %w — the agent is not launched unsandboxed", err)
	}
	return launch.Argv, launch.Dir, launch.Env, launch.Cleanup, nil
}

// registerWorkspace records dir in the sandbox registry on the first launch
// into it — while it is still conductor's own checkout — and returns the
// entry: the git layout read then is what every later launch (a resume) and
// conductor's own push use, never re-read from the agent-writable checkout.
func registerWorkspace(dir, identity, tool string, req *dispatch.Request) (agentsandbox.Entry, error) {
	reg := agentsandbox.Default
	if e, ok := reg.Lookup(dir); ok && e.Identity == identity && filepath.Clean(e.Workspace) == canonDir(dir) {
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
		if id, ok := dispatch.SkillIdentityFor(*req); ok {
			e.Skill = &id
		}
		if !dispatch.ReviewRole(*req) && layout.GitDir != "" {
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

// forgetWorkspace drops dir from the sandbox registry (its session ended).
func forgetWorkspace(dir string) {
	if dir != "" {
		agentsandbox.Default.Forget(dir)
	}
}

func canonDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// nonCredentialEnv is the part of a dispatch's environment a sandboxed agent
// keeps: its git identity and the operator's non-secret action variables.
// Credentials (a forge token, cloud keys, anything named like one) never
// reach it — conductor performs the writes they were for.
func nonCredentialEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || credentialName(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// credentialName reports a variable that is, or carries, a credential.
func credentialName(k string) bool {
	switch k {
	case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO", "KUBECONFIG", "DOCKER_HOST", "DOCKER_CONFIG",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"CONDUCTOR_ENDPOINT", "CONDUCTOR_SKILL_TOKEN", "CONDUCTOR_SKILL_CLAIM", "CONDUCTOR_VAULT_KEY",
		"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "HOME", "TMPDIR", "PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES":
		return true
	}
	for _, pre := range []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_", "VAULT_", "OP_", "TF_TOKEN_", "PC_", "GH_", "GITHUB_", "HCLOUD_", "DIGITALOCEAN_", "CF_", "HEROKU_", "NPM_", "NODE_AUTH"} {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	u := strings.ToUpper(k)
	for _, frag := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE_KEY", "API_KEY", "APIKEY", "ACCESS_KEY", "SESSION_KEY"} {
		if strings.Contains(u, frag) {
			return true
		}
	}
	for _, part := range strings.Split(u, "_") {
		if part == "AUTH" {
			return true
		}
	}
	return false
}

// sandboxNote is what a sandboxed agent is told about its sandbox, appended
// to its task: what it can reach, and that conductor — not it — pushes.
func sandboxNote(req dispatch.Request) string {
	b := "\n\n## Your sandbox\n\nYou run in conductor's agent sandbox: your workspace is writable; the network is limited to your model; no git, cloud or forge credential is available here, so `git push` and `gh` writes will fail — do not retry them or look for credentials."
	if dispatch.ReviewRole(req) {
		return b + " This is a review: conductor does not push anything from this workspace.\n"
	}
	return b + " Commit your work; conductor pushes your committed HEAD to this dispatch's branch (signed, fast-forward only) when your step ends. To push earlier, run `conductor call git.push`.\n"
}
