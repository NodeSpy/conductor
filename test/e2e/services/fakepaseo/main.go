// Command fakepaseo is a hermetic stand-in for the real `paseo` CLI, used by the
// e2e harness (test/e2e/) as conductor's native controller runtime. Conductor
// execs it via the config `paseo_bin:` override, exactly as it would the real
// paseo. It speaks the exact subcommand/JSON contract conductor depends on
// (run / ls / inspect / send / archive / wait / clone / workspace …) and does
// REAL git under the hood — cloning from and pushing to the local bare-git forge,
// committing as the acts-as-the-user identity conductor passes via `--env` — so
// groups B (each controller runs a fixer), H (webhook fixers), and I (identity &
// isolation) are genuinely end-to-end, with NO LLM and NO secrets.
//
// It keeps a small JSON state store (agents + workspaces) under $FAKE_PASEO_STATE,
// guarded by a cross-process flock so a burst of dispatches (group D1) is safe.
// Fault injection for the failure/escalation groups (J) is env-driven:
//
//	FAKE_PASEO_FAIL_WORKSPACE=1  → `workspace create` errors (J2: worktree fails)
//	FAKE_PASEO_FAIL_RUN=1        → `run` exits non-zero with a paseo-shaped error
//
// This binary is NOT part of the shipped product; it lives only in the harness.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: fakepaseo <subcommand> …")
	}
	logEvent(os.Args[1:])
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "ls":
		cmdLs(os.Args[2:])
	case "inspect":
		cmdInspect(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "archive":
		cmdArchive(os.Args[2:])
	case "wait":
		// The agent is synchronous in the fake — it's already idle. Exit 0.
		os.Exit(0)
	case "clone":
		cmdClone(os.Args[2:])
	case "workspace":
		cmdWorkspace(os.Args[2:])
	case "version":
		fmt.Println("fakepaseo 0.0.0")
	default:
		// Unknown subcommands are a no-op success: conductor probes a few we don't
		// model, and an error would spuriously fail a dispatch.
		os.Exit(0)
	}
}

// ---- state ------------------------------------------------------------------

type agent struct {
	ID        string            `json:"id"`
	Cwd       string            `json:"cwd"`
	Status    string            `json:"status"` // idle when finished
	Title     string            `json:"title"`
	Labels    map[string]string `json:"labels"`
	Archived  bool              `json:"archived"`
	CreatedAt time.Time         `json:"created_at"`
	LastUsage time.Time         `json:"last_usage"`
	Sends     []string          `json:"sends"`
}

type workspace struct {
	ID        string `json:"workspaceId"`
	Cwd       string `json:"cwd"`
	Project   string `json:"project"`
	Isolation string `json:"isolation"`
	Archived  bool   `json:"archived"`
}

type state struct {
	Seq        int                   `json:"seq"`
	Agents     map[string]*agent     `json:"agents"`
	Workspaces map[string]*workspace `json:"workspaces"`
}

func stateDir() string {
	if d := os.Getenv("FAKE_PASEO_STATE"); d != "" {
		return d
	}
	return "/data/fakepaseo"
}

// withState runs fn under an exclusive cross-process lock, then persists.
func withState(fn func(s *state)) {
	dir := stateDir()
	must(os.MkdirAll(dir, 0o755))
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	must(err)
	defer lock.Close()
	must(syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	s := &state{Agents: map[string]*agent{}, Workspaces: map[string]*workspace{}}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Agents == nil {
			s.Agents = map[string]*agent{}
		}
		if s.Workspaces == nil {
			s.Workspaces = map[string]*workspace{}
		}
	}
	fn(s)
	b, _ := json.MarshalIndent(s, "", "  ")
	must(os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644))
}

// readState reads a snapshot without holding the lock across the whole command.
func readState() *state {
	s := &state{Agents: map[string]*agent{}, Workspaces: map[string]*workspace{}}
	if b, err := os.ReadFile(filepath.Join(stateDir(), "state.json")); err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Agents == nil {
		s.Agents = map[string]*agent{}
	}
	if s.Workspaces == nil {
		s.Workspaces = map[string]*workspace{}
	}
	return s
}

// ---- flag parsing -----------------------------------------------------------

// parsed holds the flags conductor passes that the fake cares about. Repeated
// --label/--env are collected; unknown value-flags are skipped generically.
type parsed struct {
	positionals []string
	workspace   string
	cwd         string
	title       string
	mode        string // --mode / --worktree-mode strategy
	prNumber    string
	newBranch   string
	base        string
	path        string // --path (workspace create base dir)
	isolation   string
	dir         string // --clone dir
	background  bool
	labels      map[string]string
	env         map[string]string
}

// valueFlags are flags that take a following value we want to capture.
var valueFlags = map[string]bool{
	"--workspace": true, "--cwd": true, "--title": true, "--mode": true,
	"--worktree-mode": true, "--pr-number": true, "--new-branch": true,
	"--base": true, "--path": true, "--isolation": true, "--dir": true,
	"--provider": true, "--model": true, "--thinking": true, "--wait-timeout": true,
	"--output-schema": true, "--forge": true, "--protocol": true, "--new-workspace": true,
}

func parseFlags(args []string) parsed {
	p := parsed{labels: map[string]string{}, env: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			// Bare flag; ignore (we always emit JSON where conductor expects it).
		case a == "--background":
			p.background = true
		case a == "--label" && i+1 < len(args):
			i++
			k, v := splitKV(args[i])
			p.labels[k] = v
		case a == "--env" && i+1 < len(args):
			i++
			k, v := splitKV(args[i])
			p.env[k] = v
		case valueFlags[a] && i+1 < len(args):
			v := args[i+1]
			i++
			switch a {
			case "--workspace":
				p.workspace = v
			case "--cwd":
				p.cwd = v
			case "--title":
				p.title = v
			case "--mode", "--worktree-mode":
				p.mode = v
			case "--pr-number":
				p.prNumber = v
			case "--new-branch":
				p.newBranch = v
			case "--base":
				p.base = v
			case "--path":
				p.path = v
			case "--isolation":
				p.isolation = v
			case "--dir":
				p.dir = v
			}
		case strings.HasPrefix(a, "--"):
			// Unknown bare flag; ignore.
		default:
			p.positionals = append(p.positionals, a)
		}
	}
	return p
}

func splitKV(s string) (string, string) {
	if i := strings.IndexByte(s, '='); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// ---- run --------------------------------------------------------------------

func cmdRun(args []string) {
	if os.Getenv("FAKE_PASEO_FAIL_RUN") != "" {
		// Emit paseo's structured error shape on stdout and exit non-zero, so
		// conductor surfaces a dispatch failure → escalate (group J1).
		fmt.Println(`{"error":{"code":"RUN_FAILED","message":"fakepaseo: forced run failure"}}`)
		os.Exit(1)
	}
	p := parseFlags(args)
	prompt := ""
	if len(p.positionals) > 0 {
		prompt = p.positionals[0]
	}

	// Resolve the working directory: a pre-created worktree workspace id wins,
	// else an explicit --cwd.
	cwd := p.cwd
	if p.workspace != "" {
		s := readState()
		if ws, ok := s.Workspaces[p.workspace]; ok {
			cwd = ws.Cwd
		} else {
			cwd = p.workspace // treat as a path fallback
		}
	}
	if cwd == "" {
		// checkout:none with no workspace — run in a scratch dir so we still record
		// an agent (assess-style steps).
		cwd = filepath.Join(stateDir(), "scratch")
		_ = os.MkdirAll(cwd, 0o755)
	}

	// If it's a git repo, make the scripted edit + commit + push as the user.
	if isGitRepo(cwd) {
		applyEdit(cwd, prompt, p)
	}

	// Optionally simulate an acts-as-the-user WRITE against the mock GitHub API so
	// group H can assert a captured comment attributed to the user token.
	if os.Getenv("FAKE_PASEO_POST_COMMENT") != "" {
		postComment(p)
	}

	id := ""
	withState(func(s *state) {
		s.Seq++
		id = fmt.Sprintf("agent-%d", s.Seq)
		now := time.Now()
		s.Agents[id] = &agent{
			ID: id, Cwd: cwd, Status: "idle", Title: p.title,
			Labels: p.labels, CreatedAt: now, LastUsage: now,
		}
	})
	// Conductor parses the launched agent id off stdout JSON.
	fmt.Printf("{\"id\":%q}\n", id)
}

// applyEdit makes a deterministic edit, commits it as the acts-as-the-user
// identity (GIT_AUTHOR_*/GIT_COMMITTER_* from conductor's --env), and pushes the
// current branch to the forge (origin). This is what proves, per controller, that
// a fixer edited/pushed and the commit is attributed to the user, not the bot.
func applyEdit(cwd, prompt string, p parsed) {
	name := p.env["GIT_AUTHOR_NAME"]
	email := p.env["GIT_AUTHOR_EMAIL"]
	if name == "" {
		name = "fakepaseo"
	}
	if email == "" {
		email = "fakepaseo@example.test"
	}
	marker := filepath.Join(cwd, "CONDUCTOR_FIX.md")
	line := fmt.Sprintf("conductor fix: %s\n", firstLine(prompt))
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
	)
	git(cwd, env, "add", "-A")
	// -c user.* covers the case where only committer env is honored.
	git(cwd, env, "-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", "conductor: "+firstLine(prompt), "--allow-empty")
	branch := gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" || branch == "HEAD" {
		branch = "conductor-work"
	}
	git(cwd, env, "push", "origin", "HEAD:refs/heads/"+branch)
}

// postComment simulates the agent posting a PR comment as the user (GH_TOKEN)
// against the mock GitHub API. The repo/number come from conductor's labels.
func postComment(p parsed) {
	base := strings.TrimRight(os.Getenv("PC_GITHUB_API_BASE"), "/")
	if base == "" {
		return
	}
	repo, num := p.labels["repo"], prNumberFromKey(p.labels["pr"])
	if repo == "" || num == "" {
		return
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%s/comments", base, repo, num)
	body := fmt.Sprintf(`{"body":"conductor handled %s"}`, p.labels["kind"])
	// Use curl to keep the fake dependency-free; token proves identity to the mock.
	c := exec.Command("curl", "-s", "-X", "POST",
		"-H", "Authorization: token "+p.env["GH_TOKEN"],
		"-H", "Content-Type: application/json",
		"-d", body, url)
	_ = c.Run()
}

// ---- ls / inspect / send / archive ------------------------------------------

func cmdLs(args []string) {
	p := parseFlags(args)
	s := readState()
	out := []map[string]any{}
	for _, a := range s.Agents {
		if a.Archived {
			continue
		}
		if !matchLabels(a.Labels, p.labels) {
			continue
		}
		out = append(out, map[string]any{
			"id": a.ID, "cwd": a.Cwd, "status": a.Status,
			"title": a.Title, "labels": a.Labels,
		})
	}
	emitJSON(out)
}

func cmdInspect(args []string) {
	p := parseFlags(args)
	id := firstPositional(p)
	s := readState()
	a := s.Agents[id]
	if a == nil {
		// Report a benign, non-home cwd so verifyWorktree never wrongly fails.
		emitJSON(map[string]any{"Cwd": filepath.Join(stateDir(), "unknown"),
			"PendingPermissions": []any{}, "CreatedAt": time.Now(), "LastUsage": time.Now()})
		return
	}
	emitJSON(map[string]any{
		"Cwd": a.Cwd, "Worktree": nil, "PendingPermissions": []any{},
		"CreatedAt": a.CreatedAt, "LastUsage": a.LastUsage, "UpdatedAt": a.LastUsage,
		"status": a.Status,
	})
}

func cmdSend(args []string) {
	p := parseFlags(args)
	if len(p.positionals) < 1 {
		fail("send: need <id> <prompt>")
	}
	id := p.positionals[0]
	prompt := ""
	if len(p.positionals) > 1 {
		prompt = strings.Join(p.positionals[1:], " ")
	}
	withState(func(s *state) {
		if a := s.Agents[id]; a != nil {
			a.Sends = append(a.Sends, prompt)
			a.LastUsage = time.Now()
		}
	})
}

func cmdArchive(args []string) {
	p := parseFlags(args)
	id := firstPositional(p)
	withState(func(s *state) {
		if a := s.Agents[id]; a != nil {
			a.Archived = true
		}
	})
}

// ---- clone / workspace ------------------------------------------------------

func cmdClone(args []string) {
	p := parseFlags(args)
	repo := firstPositional(p)
	if repo == "" || p.dir == "" {
		fail("clone: need <owner/repo> --dir <dir>")
	}
	// Real paseo clones `owner/repo` into <dir>/<basename> — conductor passes the
	// parent checkouts dir as --dir and expects the checkout at <dir>/<name>.
	target := filepath.Join(p.dir, filepath.Base(repo))
	url := forgeURL(repo)
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		emitJSON(map[string]any{"cwd": target}) // already cloned
		return
	}
	must(os.MkdirAll(p.dir, 0o755))
	if out, err := run("", "git", "clone", url, target); err != nil {
		fail("clone %s: %v: %s", url, err, out)
	}
	emitJSON(map[string]any{"cwd": target})
}

func cmdWorkspace(args []string) {
	if len(args) < 1 {
		fail("workspace: need a subcommand")
	}
	switch args[0] {
	case "create":
		wsCreate(args[1:])
	case "ls":
		// Always report an empty set: conductor's reuse-check then clones fresh
		// (deterministic), and the reaper finds nothing to reap.
		emitJSON([]any{})
	case "archive":
		p := parseFlags(args[1:])
		id := firstPositional(p)
		withState(func(s *state) {
			if w := s.Workspaces[id]; w != nil {
				w.Archived = true
			}
		})
	default:
		os.Exit(0)
	}
}

func wsCreate(args []string) {
	if os.Getenv("FAKE_PASEO_FAIL_WORKSPACE") != "" {
		// A hard worktree-creation failure → conductor escalates loudly (group J2).
		fmt.Fprintln(os.Stderr, "fakepaseo: WORKSPACE_CREATE_FAILED (forced)")
		os.Exit(1)
	}
	p := parseFlags(args)
	base := p.path
	if base == "" || !isGitRepo(base) {
		fail("workspace create: --path must be a git checkout, got %q", base)
	}
	url := gitOut(base, "config", "--get", "remote.origin.url")
	if url == "" {
		fail("workspace create: base %s has no origin", base)
	}
	var id, dir string
	withState(func(s *state) {
		s.Seq++
		id = fmt.Sprintf("ws-%d", s.Seq)
		dir = filepath.Join(stateDir(), "ws", id)
	})
	must(os.MkdirAll(filepath.Dir(dir), 0o755))
	if out, err := run("", "git", "clone", url, dir); err != nil {
		fail("workspace create: clone %s: %v: %s", url, err, out)
	}
	branch := "conductor-work"
	switch p.mode {
	case "checkout-pr":
		branch = "pr-" + p.prNumber
		// Prefer the real PR head ref if the forge has it, else the PR branch.
		if _, err := run(dir, "git", "fetch", "origin",
			fmt.Sprintf("refs/pull/%s/head:%s", p.prNumber, branch)); err != nil {
			_, _ = run(dir, "git", "checkout", "-B", branch, "origin/"+branch)
		} else {
			_, _ = run(dir, "git", "checkout", branch)
		}
	case "branch-off":
		branch = p.newBranch
		if branch == "" {
			branch = "conductor-branch"
		}
		start := "origin/HEAD"
		if p.base != "" {
			start = "origin/" + p.base
		}
		_, _ = run(dir, "git", "checkout", "-B", branch, start)
	}
	withState(func(s *state) {
		s.Workspaces[id] = &workspace{ID: id, Cwd: dir, Project: base, Isolation: "worktree"}
	})
	emitJSON(map[string]any{"workspaceId": id, "cwd": dir})
}

// ---- helpers ----------------------------------------------------------------

func forgeURL(repo string) string {
	base := os.Getenv("FORGE_BASE")
	if base == "" {
		base = "git://forge/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + repo + ".git"
}

func prNumberFromKey(key string) string {
	if i := strings.LastIndexByte(key, '#'); i >= 0 {
		return key[i+1:]
	}
	return ""
}

func matchLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func isGitRepo(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func git(dir string, env []string, args ...string) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	if out, err := c.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "git %s: %v: %s\n", strings.Join(args, " "), err, out)
	}
}

func gitOut(dir string, args ...string) string {
	out, _ := run(dir, "git", args...)
	return strings.TrimSpace(out)
}

func run(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	if dir != "" {
		c.Dir = dir
	}
	out, err := c.CombinedOutput()
	return string(out), err
}

func firstPositional(p parsed) string {
	if len(p.positionals) > 0 {
		return p.positionals[0]
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.TrimSpace(s)
}

func emitJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

// logEvent appends the invocation to a debug log for post-hoc assertion.
func logEvent(args []string) {
	dir := stateDir()
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "events.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		_ = f.Close()
	}
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "fakepaseo: "+format+"\n", a...)
	os.Exit(1)
}
