package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitsafe"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
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
	// Exec is the argv that runs a command under this daemon's sandbox from
	// a process conductor does not start itself (agent-deck's tmux
	// session): `conductor sandbox exec --socket <control socket>`.
	Exec []string
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
	// files are launcher-written files the agent reads (a tool config),
	// shown read-only; env are the launcher's own variables for it (set
	// after the credential filter: a server password it is meant to have).
	files []string
	env   []string
	// expose are in-sandbox loopback addresses the agent serves on for
	// conductor (opencode's server); prepareAgentSandbox fills exposed with
	// how each is reached (agentsandbox.Launch.Exposed).
	expose  []string
	exposed map[string]string
}

// sandboxGOOS is the platform the agent sandbox builds for.
func sandboxGOOS() string { return sandbox.CheckGOOS }

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

// DispatchIdentity is dispatch.SandboxIdentity (kept for callers here).
func DispatchIdentity(req dispatch.Request, tool string) string {
	return dispatch.SandboxIdentity(req, tool)
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
			entry.ReadWrite = append(append([]string(nil), entry.ReadWrite...), config.ExpandHome(p))
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
	if a.req != nil && a.req.DispatchID != "" {
		pol.Run = a.req.DispatchID
	}
	if a.skillEnv && a.req != nil && pol.Socket != "" {
		sb := &skill.SandboxBinding{Identity: pol.Identity, Workspace: pol.Workspace, Egress: entry.Egress,
			Deny: append(append([]string(nil), entry.ReadOnly...), entry.Hide...)}
		if se := dispatch.SkillEnvFor(*a.req, "unix://"+pol.Socket, sb); se != nil {
			pol.Token = se["CONDUCTOR_SKILL_TOKEN"]
		}
	}
	pol.Env = append(pol.Env, nonCredentialEnv(env)...)
	pol.Env = append(pol.Env, a.env...)
	pol.ReadOnly = append(pol.ReadOnly, a.files...)
	pol.Expose = append(pol.Expose, a.expose...)
	launch, err := agentsandbox.Wrap(pol, argv, agentsandbox.Deps{
		SelfExe:           launchSelfExe,
		EgressUnix:        EgressProxyUnix,
		EgressAddr:        EgressProxyFor,
		EgressUnixSession: EgressProxyUnixSession,
		EgressAddrSession: EgressProxyForSession,
		Relay:             w.Relay,
		LookPath:          exec.LookPath,
		RealHome:          w.Resolver.RealHome,
		// The model credentials and endpoints come from the daemon's own
		// environment or the dispatch's; everything else is dropped.
		Inherited: append(os.Environ(), env...),
	})
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("agent sandbox: %w — the agent is not launched unsandboxed", err)
	}
	a.exposed = launch.Exposed
	return launch.Argv, launch.Dir, launch.Env, launch.Cleanup, nil
}

// registerWorkspace is dispatch.RegisterSandboxWorkspace.
func registerWorkspace(dir, identity, tool string, req *dispatch.Request) (agentsandbox.Entry, error) {
	return dispatch.RegisterSandboxWorkspace(dir, identity, tool, req)
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
		return b + " This is a review: conductor does not push anything from this workspace, so do not commit changes here — commits left behind are reported as work that never landed.\n"
	}
	return b + " Commit your work; conductor pushes your committed HEAD to this dispatch's branch (signed, fast-forward only) when your step ends. To push earlier, run `conductor call git.push`.\n"
}

// ConfinedGit runs conductor's own read of a workspace — the diff preview's
// `git diff` — inside the agent sandbox: the workspace may be an agent's,
// and so are its .git/config, .gitattributes and hooks, whose filter,
// textconv or fsmonitor commands git would otherwise run as conductor. No
// network, a scratch home, the workspace and its git paths only. A
// directory the registry does not know (a daemon restart, a workspace
// conductor did not create) gets its git layout read on the spot; ok is
// false only when the sandbox is not wired at all.
func ConfinedGit(ctx context.Context, dir string, args []string) (string, bool, error) {
	w := AgentSandbox
	if w == nil || w.Resolver == nil {
		return "", false, nil
	}
	e, ok := agentsandbox.Default.Lookup(dir)
	if !ok {
		var err error
		if e, err = confinedEntry(dir); err != nil {
			return "", true, fmt.Errorf("confined git: %w", err)
		}
	}
	home, err := os.MkdirTemp(w.Resolver.StateRoot, "git-read-")
	if err != nil {
		return "", true, err
	}
	defer os.RemoveAll(home)
	pol := agentsandbox.Policy{
		Identity: "conductor:git-read", Workspace: e.Workspace, Cwd: canonDir(dir), Home: home,
		ReadOnly: e.ReadOnly, ReadWrite: e.ReadWrite, Hide: e.Hide,
	}
	argv := append([]string{"git"}, gitsafe.Args(args...)...)
	l, err := agentsandbox.Wrap(pol, argv, agentsandbox.Deps{
		SelfExe: launchSelfExe, LookPath: exec.LookPath, RealHome: w.Resolver.RealHome,
		Inherited: os.Environ(),
	})
	if err != nil {
		return "", true, fmt.Errorf("confined git: %w", err)
	}
	defer l.Cleanup()
	cmd := exec.CommandContext(ctx, l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, gitsafe.Env(l.Env)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", true, fmt.Errorf("git %s (confined): %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), true, nil
}

// confinedEntry is the sandbox shape of a directory no registration
// describes: its enclosing checkout (the nearest dir with a .git) as the
// workspace, with that checkout's git layout.
func confinedEntry(dir string) (agentsandbox.Entry, error) {
	d := canonDir(dir)
	ws := d
	for p := d; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			ws = p
			break
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if home, _ := os.UserHomeDir(); ws == string(filepath.Separator) || (home != "" && isWithin(canonDir(home), ws)) {
		return agentsandbox.Entry{}, fmt.Errorf("refusing %s as a confined read's workspace: it contains your home directory", ws)
	}
	layout, err := agentsandbox.ResolveGitLayout(ws)
	if err != nil {
		return agentsandbox.Entry{}, err
	}
	return agentsandbox.Entry{Workspace: ws, Identity: "conductor:git-read", ReadOnly: layout.ReadOnly, ReadWrite: layout.ReadWrite, Hide: layout.Hide}, nil
}

// isWithin reports p == dir or p inside dir.
func isWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
