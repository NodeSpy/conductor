package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
)

// withDeckSandbox wires the agent sandbox for an agent-deck test (state
// under a temp dir, `sandbox exec` as execArgv).
func withDeckSandbox(t *testing.T, execArgv []string) string {
	t.Helper()
	old := AgentSandbox
	t.Cleanup(func() { AgentSandbox = old })
	st := t.TempDir()
	AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{StateRoot: st}, Exec: execArgv}
	return st
}

// deckAddOK answers `add` the way agent-deck does: the session's id and
// the wrapper it stored (wrapperOf: what it kept of the one it was given).
func deckAddOK(id string, wrapperOf func(string) string) func(args []string) ([]byte, error) {
	return func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "add" {
			w := ""
			if i := argIndex(args, "--wrapper"); i >= 0 {
				w = wrapperOf(args[i+1])
			}
			b, _ := json.Marshal(map[string]any{"id": id, "success": true, "wrapper": w})
			return b, nil
		}
		return nil, nil
	}
}

func subcommands(d *fakeDeck) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if len(c.args) > 1 && c.args[0] == "session" {
			out = append(out, c.args[0]+" "+c.args[1])
		} else if len(c.args) > 0 {
			out = append(out, c.args[0])
		}
	}
	return out
}

// Sandboxed, a session is created stopped with conductor's wrapper, checked,
// then started and sent its task — never `launch`ed (which starts it before
// conductor could check anything).
func TestAgentDeckSandboxedAddVerifyStartSend(t *testing.T) {
	withDeckSandbox(t, []string{"/opt/conductor/conductor", "sandbox", "exec", "--socket", "/st/control.sock"})
	deck := &fakeDeck{byArgs: deckAddOK("deck-7", func(w string) string { return w })}
	c := newDeckFor(&fakeProv{}, deck)
	c.args = []string{"-c", "codex --full-auto"}
	ws := t.TempDir()
	req := makeReq("merge_conflict", "fix")
	req.DispatchID = "d-7"
	req.Trigger.TargetTrusted = true
	req.Action.Agent = "fixer"
	sess, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: ws}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(subcommands(deck), ","); got != "add,session start,session send" {
		t.Fatalf("subcommands: %s", got)
	}
	add := deck.call(0)
	if add.dir != ws {
		t.Fatalf("add runs in the workspace: %q", add.dir)
	}
	w := add.args[argIndex(add.args, "--wrapper")+1]
	for _, want := range []string{
		"/opt/conductor/conductor sandbox exec --socket /st/control.sock --tool codex --agent-cwd --bin codex -- /bin/bash -c \"$(cat <<'__CONDUCTOR_",
		"\n{command}\n__CONDUCTOR_",
	} {
		if !strings.Contains(w, want) {
			t.Fatalf("wrapper lacks %q:\n%s", want, w)
		}
	}
	if argIndex(add.args, "--json") < 0 || argIndex(add.args, "-c") != 1 || add.args[len(add.args)-1] != ws {
		t.Fatalf("add args (the workspace named last): %q", add.args)
	}
	send := deck.call(2)
	if send.args[2] != "deck-7" || !strings.Contains(send.args[3], "fix") || !strings.Contains(send.args[3], "Your sandbox") {
		t.Fatalf("send: %q", send.args)
	}
	for _, c := range deck.calls {
		if envHas(c.env, "GH_TOKEN", "utok") {
			t.Fatalf("a forge credential reaches agent-deck: %v", c.args)
		}
	}
	if !envHas(add.env, "GIT_AUTHOR_NAME", "Me") {
		t.Fatal("the agent's git identity still reaches it")
	}
	e, ok := agentsandbox.Default.Lookup(ws)
	if !ok || e.Identity != "agent:fixer@o/r" || e.Tool != "codex" || e.Dispatch != "d-7" {
		t.Fatalf("workspace registered for sandbox exec: %+v %v", e, ok)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := agentsandbox.Default.Lookup(ws); ok {
		t.Fatal("removing the session forgets its workspace")
	}
}

// Every way the sandbox could be lost refuses, and a session agent-deck
// created is removed unstarted.
func TestAgentDeckSandboxedRefusals(t *testing.T) {
	type tc struct {
		exec   []string
		byArgs func([]string) ([]byte, error)
		want   string
		calls  string
	}
	cases := map[string]tc{
		"sandbox exec not wired": {exec: nil, byArgs: deckAddOK("d", func(w string) string { return w }),
			want: "not wired", calls: ""},
		"agent-deck without --wrapper": {exec: []string{"c", "sandbox", "exec"}, byArgs: func(args []string) ([]byte, error) {
			return []byte("flag provided but not defined: -wrapper"), errors.New("exit status 2")
		}, want: "must support --wrapper", calls: "add"},
		"wrapper not kept": {exec: []string{"c", "sandbox", "exec"}, byArgs: deckAddOK("d-1", func(string) string { return "" }),
			want: "did not keep conductor's sandbox wrapper", calls: "add,remove"},
		"wrapper altered": {exec: []string{"c", "sandbox", "exec"}, byArgs: deckAddOK("d-1", func(w string) string { return strings.Replace(w, "{command}", "claude", 1) }),
			want: "did not keep", calls: "add,remove"},
		"no id": {exec: []string{"c", "sandbox", "exec"}, byArgs: func(args []string) ([]byte, error) { return []byte(`{"success":true}`), nil },
			want: "no session id", calls: "add"},
		"start fails": {exec: []string{"c", "sandbox", "exec"}, byArgs: func(args []string) ([]byte, error) {
			if args[0] == "session" && args[1] == "start" {
				return nil, errors.New("boom")
			}
			return deckAddOK("d-1", func(w string) string { return w })(args)
		}, want: "session start", calls: "add,session start,remove"},
		"sandbox refused (send fails)": {exec: []string{"c", "sandbox", "exec"}, byArgs: func(args []string) ([]byte, error) {
			if args[0] == "session" && args[1] == "send" {
				return []byte("exited before it was ready"), errors.New("exit status 1")
			}
			return deckAddOK("d-1", func(w string) string { return w })(args)
		}, want: "send the task", calls: "add,session start,session send,remove"},
	}
	for name, k := range cases {
		t.Run(name, func(t *testing.T) {
			withDeckSandbox(t, k.exec)
			deck := &fakeDeck{byArgs: k.byArgs}
			c := newDeckFor(&fakeProv{}, deck)
			ws := t.TempDir()
			_, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: ws}, nil)
			if err == nil || !strings.Contains(err.Error(), k.want) {
				t.Fatalf("want a refusal containing %q, got %v", k.want, err)
			}
			if got := strings.Join(subcommands(deck), ","); got != k.calls {
				t.Fatalf("calls: %s, want %s", got, k.calls)
			}
			for _, c := range deck.calls {
				if c.args[0] == "remove" && (argIndex(c.args, "--force") < 0 || c.args[1] != "d-1") {
					t.Fatalf("remove: %q", c.args)
				}
			}
			if _, ok := agentsandbox.Default.Lookup(ws); ok {
				t.Fatal("a refused session leaves no registration behind")
			}
		})
	}
}

// Off (sandbox.agents: off, or isolation mode none) the session launches as
// before, with agent-deck's own message flag.
func TestAgentDeckUnsandboxedLaunches(t *testing.T) {
	old := AgentSandbox
	t.Cleanup(func() { AgentSandbox = old })
	AgentSandbox = nil
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) { return []byte(`{"id":"x"}`), nil }}
	c := newDeckFor(&fakeProv{}, deck)
	if _, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: "/wt"}, nil); err != nil {
		t.Fatal(err)
	}
	l := deck.call(0)
	if l.args[0] != "launch" || argIndex(l.args, "--message") < 0 || argIndex(l.args, "--prompt") >= 0 || argIndex(l.args, "--wrapper") >= 0 || l.args[len(l.args)-1] != "/wt" {
		t.Fatalf("launch: %q", l.args)
	}
	withDeckSandbox(t, []string{"c"})
	c.iso = &config.IsolationConfig{Mode: "none"}
	deck.calls = nil
	if _, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: "/wt"}, nil); err != nil {
		t.Fatal(err)
	}
	if deck.call(0).args[0] != "launch" {
		t.Fatal("isolation mode none opts out")
	}
}

func TestAgentDeckTool(t *testing.T) {
	t.Setenv("AGENT_DECK_HOME", t.TempDir())
	for args, want := range map[string]string{
		"":                "claude",
		"-c codex":        "codex",
		"--cmd gemini":    "gemini",
		"-cmd=opencode":   "opencode",
		"-c=codex --yolo": "codex",
		"-g work -c pi":   "pi",
		"-c":              "claude",
		"--channel x":     "claude",
	} {
		c := &agentDeckController{args: strings.Fields(args)}
		if strings.HasPrefix(args, "-c=") {
			c.args = []string{args}
		}
		if got := c.deckTool(); got != want {
			t.Errorf("deckTool(%q) = %q, want %q", args, got, want)
		}
	}
	os.WriteFile(filepath.Join(os.Getenv("AGENT_DECK_HOME"), "config.toml"), []byte("# x\ndefault_tool = \"codex\"\n[claude]\ndefault_tool = \"no\"\n"), 0o600)
	if got := (&agentDeckController{}).deckTool(); got != "codex" {
		t.Fatalf("agent-deck's default_tool: %q", got)
	}
	os.WriteFile(filepath.Join(os.Getenv("AGENT_DECK_HOME"), "config.toml"), []byte("[claude]\ndefault_tool = \"no\"\n"), 0o600)
	if got := (&agentDeckController{}).deckTool(); got != "claude" {
		t.Fatalf("a section's key is not the default: %q", got)
	}
}

// The wrapper, run the way agent-deck runs it (its {command} substituted,
// through bash), hands the WHOLE compound command to one shell inside
// `sandbox exec` — nothing of it runs before or after, outside.
func TestDeckWrapperConfinesTheWholeCommand(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "sandbox-exec-stub")
	log := filepath.Join(dir, "argv")
	// The stand-in for `conductor sandbox exec`: records its argv, then runs
	// what follows -- (as the real one does, inside the sandbox).
	os.WriteFile(stub, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > "+log+"\nwhile [ \"$1\" != -- ]; do shift; done; shift\nexec \"$@\"\n"), 0o755)
	outside := filepath.Join(dir, "last-line")
	compound := "export COLORFGBG='15;0' && export X=\"a b\"; touch " + filepath.Join(dir, "first") + "\n__CONDUCTOR_fake__\ntouch " + outside + "; exec env -u Y sh -c 'echo $X > " + filepath.Join(dir, "second") + "'"
	w := deckWrapper([]string{stub}, "claude-code", "claude", "__CONDUCTOR_0123456789abcdef__")
	cmd := exec.Command("bash", "-c", strings.ReplaceAll(w, "{command}", compound))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	want := []string{"--tool", "claude-code", "--agent-cwd", "--bin", "claude", "--", "/bin/bash", "-c", compound}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("sandbox exec got %q\nwant %q", argv, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "second")); strings.TrimSpace(string(b)) != "a b" {
		t.Fatalf("the compound command ran whole inside: %q", b)
	}
	// A line of the command that looks like a delimiter (not this
	// session's) and what follows it stayed inside the one argument above,
	// and ran in the inner shell.
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the command's last line ran: %v", err)
	}
	if w2 := deckWrapper([]string{"/p a/conductor", "sandbox", "exec"}, "", "it's", "D"); !strings.HasPrefix(w2, "'/p a/conductor' sandbox exec --agent-cwd --bin 'it'\\''s' -- ") {
		t.Fatalf("quoting: %s", w2)
	}
}

// A session whose process died is over: the wait ends (it polled forever).
func TestAgentDeckErrorStatusEndsTheWait(t *testing.T) {
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) {
		if args[0] == "session" && args[1] == "show" {
			return []byte(`{"status":"error"}`), nil
		}
		return nil, nil
	}}
	c := newDeckFor(&fakeProv{}, deck)
	s := &agentDeckSession{id: "x", c: c}
	if !s.idle(context.Background()) {
		t.Fatal("error is terminal")
	}
	deck.byArgs = func(args []string) ([]byte, error) { return []byte(`{"status":"running"}`), nil }
	if s.idle(context.Background()) {
		t.Fatal("running is not")
	}
}
