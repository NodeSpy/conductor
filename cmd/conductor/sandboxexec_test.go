package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// fakeControl serves one canned control response per connection and
// records the requests.
func fakeControl(t *testing.T, resp func(controlRequest) controlResponse) (string, *[]controlRequest) {
	t.Helper()
	dir, err := agentsandbox.SocketDir("cse")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var reqs []controlRequest
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var r controlRequest
			if json.NewDecoder(c).Decode(&r) == nil {
				reqs = append(reqs, r)
				_ = json.NewEncoder(c).Encode(resp(r))
			}
			c.Close()
		}
	}()
	return sock, &reqs
}

// stderrOf runs f with os.Stderr captured.
func stderrOf(t *testing.T, f func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	rc := f()
	os.Stderr = old
	w.Close()
	b, _ := io.ReadAll(r)
	return rc, string(b)
}

// The refusals: no daemon, a daemon that refuses (even if it also sent a
// policy), a daemon that answers without a policy, a policy that cannot be
// realized — each is exit 125 with its own reason, and the command never runs.
func TestSandboxExecRefuses(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "ran")
	t.Chdir(ws)
	argv := []string{"/bin/sh", "-c", "echo ran > " + marker}
	home := t.TempDir()
	valid := &agentsandbox.Policy{Identity: "x", Workspace: ws, Home: home}

	gone := filepath.Join(t.TempDir(), "control.sock")
	refusing, _ := fakeControl(t, func(controlRequest) controlResponse { return controlResponse{Error: "no policy for you"} })
	both, _ := fakeControl(t, func(controlRequest) controlResponse { return controlResponse{Error: "refused anyway", Policy: valid} })
	empty, _ := fakeControl(t, func(controlRequest) controlResponse { return controlResponse{OK: true} })
	bogus, _ := fakeControl(t, func(controlRequest) controlResponse {
		return controlResponse{OK: true, Policy: &agentsandbox.Policy{Identity: "x", Workspace: ws, Home: filepath.Join(ws, "no-such-home")}}
	})
	for _, c := range []struct{ name, sock, want string }{
		{"daemon unreachable", gone, "the conductor daemon is unreachable"},
		{"daemon refuses", refusing, "no policy for you"},
		{"an error wins over a policy", both, "refused anyway"},
		{"no policy in the answer", empty, "returned no policy"},
		{"an unrealizable policy", bogus, "does not exist"},
	} {
		rc, errOut := stderrOf(t, func() int { return cmdSandboxExec(append([]string{"--socket", c.sock, "--"}, argv...)) })
		if rc != sandboxRefused || !strings.Contains(errOut, "refusing to launch") || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: rc %d, stderr %q (want %q)", c.name, rc, errOut, c.want)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused launch never runs the command")
	}
	for _, bad := range [][]string{nil, {"--"}, {"--tool"}, {"--bogus", "--", "x"}, {"claude"}} {
		if rc, _ := stderrOf(t, func() int { return cmdSandboxExec(bad) }); rc != 2 {
			t.Errorf("usage error %q: rc %d", bad, rc)
		}
	}
}

// What the wrapper asks: its cwd (or paseo's PASEO_AGENT_CWD), the tool, the
// launcher's model endpoints and never a credential; scratch only when no
// agent directory was named.
func TestSandboxExecAsksForItsDirectory(t *testing.T) {
	ws := t.TempDir()
	agentDir := t.TempDir()
	t.Chdir(ws)
	sock, reqs := fakeControl(t, func(controlRequest) controlResponse { return controlResponse{Error: "stop here"} })
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:3456")
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("PASEO_AGENT_CWD", "")
	cmdSandboxExec([]string{"--socket", sock, "--", "claude"})
	t.Setenv("PASEO_AGENT_CWD", agentDir)
	cmdSandboxExec([]string{"--socket", sock, "--tool", "codex", "--", "/usr/bin/codex", "app-server"})
	t.Setenv("PASEO_AGENT_CWD", "relative/dir")
	cmdSandboxExec([]string{"--socket", sock, "--", "claude"})
	if len(*reqs) != 3 {
		t.Fatalf("requests: %+v", *reqs)
	}
	r := (*reqs)[0]
	if r.Cmd != "sandbox-policy" || r.Cwd != ws || r.Tool != "claude" || !r.Scratch {
		t.Fatalf("own cwd: %+v", r)
	}
	if strings.Join(r.Env, ",") != "ANTHROPIC_BASE_URL=http://127.0.0.1:3456" {
		t.Fatalf("only the model endpoint travels, never a key: %v", r.Env)
	}
	if r := (*reqs)[1]; r.Cwd != agentDir || r.Tool != "codex" || r.Scratch {
		t.Fatalf("paseo's agent directory wins, and is no scratch launch: %+v", r)
	}
	if r := (*reqs)[2]; r.Cwd != ws || !r.Scratch {
		t.Fatalf("a relative PASEO_AGENT_CWD is ignored: %+v", r)
	}
	// An agent-deck session's wrapper: its directory is the agent's own.
	t.Setenv("PASEO_AGENT_CWD", "")
	cmdSandboxExec([]string{"--socket", sock, "--tool", "claude-code", "--agent-cwd", "--bin", "claude", "--", "/bin/bash", "-c", "claude"})
	if r := (*reqs)[3]; r.Cwd != ws || r.Scratch || r.Tool != "claude-code" {
		t.Fatalf("--agent-cwd is no scratch launch: %+v", r)
	}
}

func TestParseSandboxExecBins(t *testing.T) {
	o, argv, err := parseSandboxExec([]string{"--bin", "claude", "--agent-cwd", "--bin", "node", "--", "/bin/bash", "-c", "x"})
	if err != nil || strings.Join(o.bins, ",") != "claude,node" || !o.agentCwd || strings.Join(argv, " ") != "/bin/bash -c x" {
		t.Fatalf("%+v %q %v", o, argv, err)
	}
	if _, _, err := parseSandboxExec([]string{"--bin"}); err == nil {
		t.Fatal("--bin needs a value")
	}
}

func TestSandboxControlSocket(t *testing.T) {
	if got := sandboxControlSocket(sandboxExecOpts{socket: "/x/control.sock"}); got != "/x/control.sock" {
		t.Fatal(got)
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfg, []byte("store:\n  state_file: /srv/conductor/state.json\n"), 0o600)
	if got := sandboxControlSocket(sandboxExecOpts{config: cfg}); got != "/srv/conductor/control.sock" {
		t.Fatalf("the config's state dir: %s", got)
	}
	home, _ := os.UserHomeDir()
	os.WriteFile(cfg, []byte("store:\n  state_file: ~/st/state.json\n"), 0o600)
	if got := sandboxControlSocket(sandboxExecOpts{config: cfg}); got != filepath.Join(home, "st", "control.sock") {
		t.Fatalf("~ expands: %s", got)
	}
	os.WriteFile(cfg, []byte("connectors: {}\n"), 0o600)
	if got := sandboxControlSocket(sandboxExecOpts{config: cfg}); !strings.HasSuffix(got, "/control.sock") || filepath.Dir(got) == "/" {
		t.Fatalf("default state dir: %s", got)
	}
}

func TestSandboxPolicyResponse(t *testing.T) {
	old := sandboxResolver.Load()
	t.Cleanup(func() { sandboxResolver.Store(old) })
	sandboxResolver.Store(nil)
	if r := sandboxPolicyResponse(nil, controlRequest{Cwd: "/tmp"}, t.Logf); r.Error == "" || r.Policy != nil {
		t.Fatalf("sandbox off: no policy: %+v", r)
	}
	ws := t.TempDir()
	sandboxResolver.Store(&agentsandbox.Resolver{Registry: &agentsandbox.Registry{}, StateRoot: t.TempDir(), GOOS: sandbox.CheckGOOS})
	r := sandboxPolicyResponse(nil, controlRequest{Cwd: ws, Tool: "claude"}, t.Logf)
	if r.Error != "" || r.Policy == nil || r.Policy.Tool != "claude-code" {
		t.Fatalf("a policy: %+v", r)
	}
	if r := sandboxPolicyResponse(nil, controlRequest{Cwd: "/", Tool: "claude"}, t.Logf); r.Error == "" {
		t.Fatal("refused workspace")
	}
}

func TestModelRouteEnvCarriesNoCredential(t *testing.T) {
	got := modelRouteEnv([]string{"ANTHROPIC_BASE_URL=http://u", "OPENAI_BASE_URL=https://v", "ANTHROPIC_API_KEY=k", "GH_TOKEN=t", "BASE_URL_X=http://y", "_X_BASE_URL=1"})
	if strings.Join(got, ",") != "ANTHROPIC_BASE_URL=http://u,OPENAI_BASE_URL=https://v" {
		t.Fatal(got)
	}
}
