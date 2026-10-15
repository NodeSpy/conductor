// Command fakeagentdeck is a hermetic stand-in for the agent-deck CLI — installed
// on PATH as `agent-deck`, driven by conductor's agent-deck controller
// (internal/controller/agentdeck.go) via launch / list --json / session send /
// session show --json / remove. Conductor execs `agent-deck launch …` IN the
// conductor-provisioned PR worktree (cmd.Dir) with the acts-as-the-user identity in
// its env; on launch the fake performs the shared fixer edit+commit+push (package
// fixer) in that worktree, so the agent-deck row lands a real commit on the forge —
// with NO LLM and NO secrets.
//
// It keeps a tiny JSON session store under $AGENTDECK_STATE and reports launched
// sessions as idle (so the controller's `session show --json` poll terminates).
//
// The sandboxed path (conductor's agent sandbox on): `add --wrapper W` stores
// the session stopped; `session send` then runs it the way agent-deck does —
// W with {command} replaced by agent-deck's own compound command line for the
// session's -c tool, through bash, the message on stdin. Installed as
// `deck-agent` this binary IS that tool: the fixer, run wherever the wrapper
// put it (inside `conductor sandbox exec`).
//
// NOT part of the shipped product; harness-only.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/test/e2e/services/fixer"
)

func stateFile() string {
	d := os.Getenv("AGENTDECK_STATE")
	if d == "" {
		d = "/data/agentdeck"
	}
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, "sessions.json")
}

type session struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Group   string   `json:"group"`
	Cwd     string   `json:"cwd"`
	Status  string   `json:"status"`
	Sends   []string `json:"sends"`
	Cmd     string   `json:"cmd,omitempty"`
	Wrapper string   `json:"wrapper,omitempty"`
}

func load() map[string]*session {
	m := map[string]*session{}
	if b, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func save(m map[string]*session) {
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(stateFile(), b, 0o644)
}

func main() {
	if filepath.Base(os.Args[0]) == "deck-agent" {
		// The agent a session runs: its task arrives on stdin.
		cwd, _ := os.Getwd()
		prompt, _ := io.ReadAll(os.Stdin)
		if err := fixer.Apply(cwd, "agent-deck", string(prompt)); err != nil {
			fmt.Fprintln(os.Stderr, "deck-agent:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent-deck <launch|list|session|remove> …")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "add":
		cwd := lastPositional()
		m := load()
		id := "deck-" + strconv.Itoa(len(m)+1)
		cmd := flagVal("-c")
		if cmd == "" {
			cmd = "claude"
		}
		m[id] = &session{ID: id, Title: flagVal("--title"), Group: flagVal("--group"),
			Cwd: cwd, Status: "stopped", Cmd: cmd, Wrapper: flagVal("--wrapper")}
		save(m)
		emit(map[string]any{"id": id, "success": true, "wrapper": flagVal("--wrapper"), "command": cmd})
	case "launch":
		// conductor names the worktree (and runs us in it); do the fixer's
		// work there.
		cwd := lastPositional()
		msg := flagVal("--message")
		if msg == "" {
			msg = flagVal("--prompt")
		}
		_ = fixer.Apply(cwd, "agent-deck", msg)
		m := load()
		id := "deck-" + strconv.Itoa(len(m)+1)
		m[id] = &session{ID: id, Title: flagVal("--title"), Group: flagVal("--group"),
			Cwd: cwd, Status: "idle"}
		save(m)
		emit(map[string]any{"id": id})
	case "list":
		m := load()
		out := []map[string]any{}
		for _, s := range m {
			out = append(out, map[string]any{"id": s.ID, "title": s.Title,
				"group": s.Group, "cwd": s.Cwd, "status": s.Status})
		}
		emit(out)
	case "session":
		if len(os.Args) < 3 {
			os.Exit(0)
		}
		switch os.Args[2] {
		case "start":
			m := load()
			if s := m[posAfter("start")]; s != nil {
				s.Status = "waiting"
				save(m)
			}
		case "send":
			m := load()
			s := m[posAfter("send")]
			if s == nil {
				break
			}
			s.Sends = append(s.Sends, "sent")
			if s.Wrapper != "" && len(s.Sends) == 1 {
				// agent-deck's own command line for the tool — compound,
				// as the real one is — run through the session's wrapper.
				line := "export AGENTDECK_INSTANCE_ID=" + s.ID + "; exec env AGENTDECK_TITLE=\"" + s.Title + "\" " + s.Cmd
				c := exec.Command("bash", "-c", strings.ReplaceAll(s.Wrapper, "{command}", line))
				c.Dir = s.Cwd
				c.Stdin = strings.NewReader(os.Args[len(os.Args)-1])
				c.Stdout, c.Stderr = os.Stderr, os.Stderr
				if err := c.Run(); err != nil {
					s.Status = "error"
					save(m)
					fmt.Fprintf(os.Stderr, "agent-deck: session %s exited before it was ready: %v\n", s.ID, err)
					os.Exit(1)
				}
			}
			s.Status = "idle"
			save(m)
		case "show":
			m := load()
			if s := m[posAfter("show")]; s != nil {
				emit(s)
			} else {
				emit(map[string]any{"status": "idle"})
			}
		}
	case "remove":
		m := load()
		delete(m, posAfter("remove"))
		save(m)
	case "--version", "version":
		fmt.Println("Agent Deck v0.0.0-fake")
	default:
		os.Exit(0)
	}
}

func flagVal(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

// posAfter returns the first non-flag arg following the given subcommand token.
func posAfter(sub string) string {
	seen := false
	for _, a := range os.Args {
		if a == sub {
			seen = true
			continue
		}
		if seen && len(a) > 0 && a[0] != '-' {
			return a
		}
	}
	return ""
}

// lastPositional is a subcommand's [path]: the last argument that is not a
// flag or a flag's value, else the working directory (agent-deck's default).
func lastPositional() string {
	takesValue := map[string]bool{"-c": true, "--cmd": true, "--title": true, "--group": true, "--message": true,
		"--prompt": true, "--model": true, "--wrapper": true, "-t": true, "-g": true, "-m": true}
	p := ""
	for i := 2; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case takesValue[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			p = a
		}
	}
	if p == "" {
		p, _ = os.Getwd()
	}
	return p
}

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
