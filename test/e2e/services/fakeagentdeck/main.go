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
// NOT part of the shipped product; harness-only.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/NodeSpy/paseo-conductor/test/e2e/services/fixer"
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
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Group  string   `json:"group"`
	Cwd    string   `json:"cwd"`
	Status string   `json:"status"`
	Sends  []string `json:"sends"`
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
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent-deck <launch|list|session|remove> …")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "launch":
		// conductor sets cmd.Dir to the worktree; do the fixer's work there.
		cwd, _ := os.Getwd()
		_ = fixer.Apply(cwd, "agent-deck", flagVal("--prompt"))
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
		case "send":
			m := load()
			if s := m[posAfter("send")]; s != nil {
				s.Sends = append(s.Sends, "sent")
				save(m)
			}
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

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
