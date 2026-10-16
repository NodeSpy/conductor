package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
)

// The agent sandbox for agent-deck. agent-deck runs the agent in a tmux
// session of its own, typing a shell command it builds (env exports, its
// own flags, then the tool) into an interactive shell there — so conductor
// cannot wrap the process it starts, and wrapping agent-deck itself would
// confine the session manager, not the agent. The seam is agent-deck's
// --wrapper: the template it runs that command through. conductor's wrapper
// hands the WHOLE command — compound as it is — to a shell inside
// `conductor sandbox exec`, so every part of it runs sandboxed:
//
//	conductor sandbox exec --socket S --tool T --agent-cwd --bin B -- /bin/bash -c "$(cat <<'__CONDUCTOR_x__'
//	{command}
//	__CONDUCTOR_x__
//	)"
//
// (A plain `sandbox exec -- {command}` would confine only the first word
// of `export COLORFGBG=…; … exec claude …` and run the agent bare.) The
// heredoc is quoted, so agent-deck's shell expands nothing in it; the
// delimiter is fresh per session.
//
// Fail closed: the session is created stopped (`add`), its stored wrapper
// is checked to be exactly conductor's, and only then started and sent the
// task. An agent-deck that does not take --wrapper refuses `add`; one that
// stores another wrapper is removed unstarted. The agent's identity reaches
// `sandbox exec` through the workspace's registration, as for paseo.

// deckSandboxed reports whether a dispatch's agent-deck session runs under
// the agent sandbox (the same rule as a cli/acp launch).
func (c *agentDeckController) deckSandboxed(host string, opt launchOpts) bool {
	return sandboxApplies(host, withAgent(opt, c.deckTool(), nil, false))
}

// deckTool is the agent CLI agent-deck runs for this runtime: the -c/--cmd
// of its `command:` (its first word), else agent-deck's own default_tool,
// else agent-deck's default, claude.
func (c *agentDeckController) deckTool() string {
	for i := 0; i < len(c.args); i++ {
		a := c.args[i]
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || (name != "c" && name != "cmd") {
			continue
		}
		if !hasVal {
			if i+1 >= len(c.args) {
				break
			}
			val = c.args[i+1]
		}
		if f := strings.Fields(val); len(f) > 0 {
			return f[0]
		}
	}
	if t := deckDefaultTool(); t != "" {
		return t
	}
	return "claude"
}

// deckConfigPaths are where agent-deck reads its config (AGENT_DECK_HOME,
// its XDG location, its legacy one).
func deckConfigPaths() []string {
	if h := strings.TrimSpace(os.Getenv("AGENT_DECK_HOME")); h != "" {
		return []string{filepath.Join(h, "config.toml")}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".config", "agent-deck", "config.toml"), filepath.Join(home, ".agent-deck", "config.toml")}
}

var deckDefaultToolRe = regexp.MustCompile(`^\s*default_tool\s*=\s*["']([^"']+)["']`)

// deckDefaultTool reads agent-deck's top-level default_tool ("" = unset).
func deckDefaultTool() string {
	for _, p := range deckConfigPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				break // default_tool is a top-level key
			}
			if m := deckDefaultToolRe.FindStringSubmatch(line); m != nil {
				return strings.TrimSpace(m[1])
			}
		}
	}
	return ""
}

// deckWrapper is the --wrapper template that runs agent-deck's command for
// bin (canonical tool tool) inside `conductor sandbox exec`.
func deckWrapper(exec []string, tool, bin, delim string) string {
	var b strings.Builder
	for i, a := range exec {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(a))
	}
	if tool != "" {
		b.WriteString(" --tool " + shellQuote(tool))
	}
	// The session's directory IS the agent's workspace: one conductor did
	// not register (a session agent-deck restarts after conductor did) is
	// refused, never swapped for a scratch workspace.
	b.WriteString(" --agent-cwd --bin " + shellQuote(bin))
	fmt.Fprintf(&b, " -- /bin/bash -c \"$(cat <<'%s'\n{command}\n%s\n)\"", delim, delim)
	return b.String()
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-=:,+@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newSandboxedDeckSession creates the agent-deck session stopped with
// conductor's wrapper, verifies agent-deck kept it, then starts it and
// sends the task.
func (c *agentDeckController) newSandboxedDeckSession(ctx context.Context, spec Spec, env []string, prompt string, opt launchOpts) (Session, error) {
	w := AgentSandbox
	if w == nil || w.Resolver == nil || len(w.Exec) == 0 {
		return nil, fmt.Errorf("agent-deck: the agent sandbox is on but `conductor sandbox exec` is not wired — the agent is not launched unsandboxed")
	}
	req := spec.Request
	bin := c.deckTool()
	tool := agentsandbox.CanonicalTool(bin)
	identity := DispatchIdentity(req, firstNonEmptyStr(tool, bin))
	dir := spec.Cwd
	if dir == "" {
		// A checkout-less agent works in a scratch directory of its
		// identity's own, never the daemon's cwd.
		dir = filepath.Join(w.Resolver.StateRoot, agentsandbox.IdentityKey(identity), "work")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("agent-deck: scratch workspace: %w", err)
		}
	}
	if _, err := registerWorkspace(dir, identity, tool, &req); err != nil {
		return nil, fmt.Errorf("agent-deck: register the sandbox workspace: %w", err)
	}
	delim, err := randomToken()
	if err != nil {
		forgetWorkspace(dir)
		return nil, err
	}
	wrapper := deckWrapper(w.Exec, tool, bin, "__CONDUCTOR_"+delim[:16]+"__")
	// The agent gets its conductor token from `sandbox exec`; agent-deck
	// itself needs no forge or cloud credential either.
	env = nonCredentialEnv(env)
	host := ""
	title := deckTitle(req)
	args := append([]string{"add"}, c.args...)
	args = append(args, "--title", title, "--group", deckGroup(req), "--wrapper", wrapper, "--json")
	if m := req.Model; m != "" {
		args = append(args, "--model", m)
	}
	// The workspace, named: with no path agent-deck puts a session in its
	// group's existing one — another dispatch's workspace.
	args = append(args, dir)
	fail := func(id string, err error) (Session, error) {
		if id != "" {
			_, _ = c.exec(ctx, host, "", env, opt, "remove", id, "--force")
		}
		forgetWorkspace(dir)
		return nil, err
	}
	out, err := c.exec(ctx, host, dir, env, opt, args...)
	if err != nil {
		return fail("", fmt.Errorf("agent-deck: add (this agent-deck must support --wrapper to run sandboxed; the agent is not launched unsandboxed): %w: %s", err, strings.TrimSpace(string(out))))
	}
	var added struct {
		ID      string `json:"id"`
		Wrapper string `json:"wrapper"`
	}
	_ = json.Unmarshal(out, &added)
	id := added.ID
	if id == "" {
		id = parseDeckID(out)
	}
	if id == "" {
		return fail("", fmt.Errorf("agent-deck: add returned no session id (%s)", strings.TrimSpace(string(out))))
	}
	if added.Wrapper != wrapper {
		return fail(id, fmt.Errorf("agent-deck: session %s did not keep conductor's sandbox wrapper (got %q) — removed unstarted; the agent is not launched unsandboxed", id, added.Wrapper))
	}
	if out, err := c.exec(ctx, host, "", env, opt, "session", "start", id); err != nil {
		return fail(id, fmt.Errorf("agent-deck: session start %s: %w: %s", id, err, strings.TrimSpace(string(out))))
	}
	if out, err := c.exec(ctx, host, "", env, opt, "session", "send", id, prompt); err != nil {
		return fail(id, fmt.Errorf("agent-deck: send the task to %s (did its sandbox start? `conductor sandbox exec` refuses when it cannot be built): %w: %s", id, err, strings.TrimSpace(string(out))))
	}
	return &agentDeckSession{id: id, c: c, env: env, host: host, opt: opt, workspace: dir}, nil
}
