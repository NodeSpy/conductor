package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// mcpServerName is the key our injected MCP server is registered under; the
// matching tool-permission prefix is mcp__<name>.
const mcpServerName = "conductor"

// InjectClaudeMCP makes a paseo-launched Claude Code agent connect to the
// conductor tool server by dropping its own MCP config into the run's isolated
// worktree — the workaround for paseo exposing no MCP surface of its own
// (#123): paseo just launches Claude, and Claude auto-discovers a project
// `.mcp.json` + `.claude/settings.local.json` in its working directory.
//
// It writes (never overwriting a repo-provided config) a `.mcp.json` defining
// the `conductor` server, and a `.claude/settings.local.json` that enables ONLY
// that server (never the target repo's own) and pre-approves its tools — so the
// connection is non-interactive. Both files are appended to
// `.git/info/exclude` so the agent's own commits never carry them into the PR.
//
// Guardrails: dir must be a git worktree we created (ephemeral, reaped after the
// run); if the repo ALREADY has a `.mcp.json`, we skip rather than clobber or
// pollute its diff — the operator gets no skill tools on that dispatch, which is
// safe. Returns whether it injected.
func InjectClaudeMCP(dir string, ts *ToolServerSpec) (bool, error) {
	if dir == "" || ts == nil {
		return false, nil
	}
	mcpPath := filepath.Join(dir, ".mcp.json")
	if _, err := os.Stat(mcpPath); err == nil {
		return false, nil // repo ships its own — don't touch it
	}
	server := map[string]any{"command": ts.Command, "args": ts.Args}
	if len(ts.Env) > 0 {
		server["env"] = ts.Env
	}
	mcp := map[string]any{"mcpServers": map[string]any{mcpServerName: server}}
	b, err := json.MarshalIndent(mcp, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(mcpPath, append(b, '\n'), 0o600); err != nil {
		return false, err
	}

	// Enable ONLY our server (not any the checked-out repo defines) and
	// pre-approve its tools so the launch is non-interactive.
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return false, err
	}
	settings := map[string]any{
		"enabledMcpjsonServers": []string{mcpServerName},
		"permissions":           map[string]any{"allow": []string{"mcp__" + mcpServerName}},
	}
	sb, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), append(sb, '\n'), 0o600); err != nil {
		return false, err
	}

	gitExclude(dir, ".mcp.json", ".claude/settings.local.json")
	return true, nil
}

// gitExclude appends paths to the worktree's .git/info/exclude so injected
// files never show up in `git status`/`git add -A` (kept out of the agent's
// commits). Best-effort: a non-worktree dir or a missing .git just no-ops.
func gitExclude(dir string, paths ...string) {
	// A linked worktree's .git is a file pointing at the real gitdir; resolve
	// it so info/exclude lands in the right place.
	excludeDir := filepath.Join(dir, ".git", "info")
	if data, err := os.ReadFile(filepath.Join(dir, ".git")); err == nil {
		if line := strings.TrimSpace(string(data)); strings.HasPrefix(line, "gitdir:") {
			excludeDir = filepath.Join(strings.TrimSpace(strings.TrimPrefix(line, "gitdir:")), "info")
		}
	}
	if err := os.MkdirAll(excludeDir, 0o755); err != nil {
		return
	}
	p := filepath.Join(excludeDir, "exclude")
	existing, _ := os.ReadFile(p)
	var add strings.Builder
	for _, path := range paths {
		if !strings.Contains(string(existing), "\n"+path+"\n") && !strings.HasPrefix(string(existing), path+"\n") {
			add.WriteString(path + "\n")
		}
	}
	if add.Len() == 0 {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		f.WriteString("\n")
	}
	f.WriteString(add.String())
}
