package dispatch

import (
	"os"
	"strconv"

	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// The conductor tool server (memory + run_step + the skill's verb tools and
// secret broker) is injected per dispatch into whatever the runtime launches.
// This is the SHARED wiring — what to launch, with which provenance flags, and
// the one-shot skill claim (#122 R1) — so every injection path (ACP/opencode
// via session/new mcpServers; paseo via a workspace .mcp.json) assembles the
// same server instead of copy-pasting it. It lives here in dispatch, not
// controller, because the paseo dispatcher can't import controller (controller
// imports dispatch).

// ToolServerSpec is one dispatch's conductor tool server: the subprocess to
// launch, its argv, and the environment to set on it.
type ToolServerSpec struct {
	Command string
	Args    []string
	// Env carries the ONE-SHOT skill claim code (CONDUCTOR_SKILL_CLAIM) for a
	// skill-enabled profile — environment, never argv, which any same-user
	// process can read from a process listing. Empty for profiles without skill:.
	Env map[string]string
}

// BuildToolServer assembles the tool server for one dispatch, or nil when there
// is nothing to inject: no tool command published at boot (no memory: section
// and no skill-enabled profile), or a remote (host:) session — the daemon's
// socket doesn't exist on that box.
//
// When the dispatched profile carries a skill: block (#36 §12), this is where
// the daemon binds identity SERVER-SIDE: it mints a one-shot claim code mapped
// to the real profile and its policy in the skill broker. The subprocess
// exchanges the code (single-use, short TTL) for the session token over the
// socket at startup, and the broker binds the session to that process's kernel
// peer credentials. The --agent/--repo flags remain provenance labels for
// memory writes; the broker never trusts them.
//
// host is the runtime's own host (a hosts: entry); the profile's host wins when
// set. A non-empty effective host means a remote launch → nil (no local socket).
func BuildToolServer(req Request, host string) *ToolServerSpec {
	argv := memory.ToolCommand()
	effHost := host
	if req.Profile.Host != "" {
		effHost = req.Profile.Host
	}
	if len(argv) == 0 || effHost != "" {
		return nil
	}
	args := append([]string(nil), argv[1:]...)
	if a := req.Action.Agent; a != "" {
		args = append(args, "--agent", a)
	}
	if repo := req.Trigger.Target.Repo; repo != "" {
		args = append(args, "--repo", repo)
	}
	if kind := req.Trigger.Kind; kind != "" {
		args = append(args, "--trigger", kind)
	}
	if n := req.Trigger.Target.Number; n > 0 {
		args = append(args, "--number", strconv.Itoa(n))
	}
	out := &ToolServerSpec{Command: argv[0], Args: args}
	if b := skill.Active(); b != nil && req.Profile.Skill != nil {
		claim, err := b.MintClaim(skill.Identity{
			Agent:   req.Action.Agent,
			Repo:    req.Trigger.Target.Repo,
			Trigger: req.Trigger.Kind,
			Number:  req.Trigger.Target.Number,
			Policy:  *req.Profile.Skill,
		})
		if err == nil {
			out.Env = map[string]string{"CONDUCTOR_SKILL_CLAIM": claim}
		}
	}
	return out
}

// SkillEnv returns the environment a LOCAL dispatched agent needs to reach the
// conductor skill surface via the `conductor` CLI: the daemon endpoint and a
// uid-bound session token (broker.MintSession). Delivered through the runtime's
// env (paseo `--env`, etc.), never argv. Returns nil when there is nothing to
// offer — no tool socket published at boot, no skill: on the profile, or a
// remote (host:) launch where the daemon socket isn't reachable (the HTTP
// endpoint is a later increment). This is the paseo/cli counterpart to
// BuildToolServer's MCP injection (ACP/opencode).
func SkillEnv(req Request, host string) map[string]string {
	if req.Profile.Skill == nil {
		return nil
	}
	effHost := host
	if req.Profile.Host != "" {
		effHost = req.Profile.Host
	}
	if effHost != "" {
		return nil // remote: no local socket, no HTTP endpoint yet
	}
	sock := socketFromToolCommand(memory.ToolCommand())
	if sock == "" {
		return nil // no tool server published (no memory: and no skill: at boot)
	}
	b := skill.Active()
	if b == nil {
		return nil
	}
	tok, err := b.MintSession(skill.Identity{
		Agent:   req.Action.Agent,
		Repo:    req.Trigger.Target.Repo,
		Trigger: req.Trigger.Kind,
		Number:  req.Trigger.Target.Number,
		Policy:  *req.Profile.Skill,
	}, uint32(os.Getuid()))
	if err != nil {
		return nil
	}
	return map[string]string{
		"CONDUCTOR_ENDPOINT":    "unix://" + sock,
		"CONDUCTOR_SKILL_TOKEN": tok,
	}
}

// socketFromToolCommand pulls the --socket value out of memory.ToolCommand()
// (the daemon publishes `conductor mcp memory --socket <path> …` at boot).
func socketFromToolCommand(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--socket" {
			return argv[i+1]
		}
	}
	return ""
}
