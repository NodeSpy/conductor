package agentsandbox

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// withPlatform renders the sandbox for goos without needing its wrapper
// binaries: the wrapper is computed, never run.
func withPlatform(t *testing.T, goos string) {
	t.Helper()
	og, ol, oe := sandbox.CheckGOOS, sandbox.CheckLookPath, sandbox.CheckGeteuid
	sandbox.CheckGOOS = goos
	sandbox.CheckLookPath = func(b string) (string, error) { return "/usr/bin/" + b, nil }
	sandbox.CheckGeteuid = func() int { return 1000 }
	t.Cleanup(func() { sandbox.CheckGOOS, sandbox.CheckLookPath, sandbox.CheckGeteuid = og, ol, oe })
}

// unixSocket serves a unix socket at a short path and returns it.
func unixSocket(t *testing.T) string {
	t.Helper()
	dir, err := SocketDir("cst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return p
}

type fixture struct {
	pol    Policy
	deps   Deps
	relays []string
	minted []string
}

func newPolicyFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	ws, home := filepath.Join(root, "ws"), filepath.Join(root, "home")
	ro, rw := filepath.Join(root, "objects"), filepath.Join(root, "common")
	for _, d := range []string{ws, home, ro, rw} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{}
	f.pol = Policy{
		Identity: "agent:fixer@acme/app", Tool: "claude-code", Workspace: ws, Home: home,
		Egress: []string{"api.anthropic.com"}, Loopback: []string{"127.0.0.1:3456"},
		Socket: unixSocket(t), Token: "tok-123",
		ReadOnly: []string{ro}, ReadWrite: []string{rw}, Hide: []string{filepath.Join(rw, "worktrees")},
		Logins: []Login{{Path: filepath.Join(home, ".claude", ".credentials.json"), Src: filepath.Join(root, "real", ".credentials.json")}},
		Env:    []string{"GIT_AUTHOR_NAME=Op"},
	}
	f.deps = Deps{
		SelfExe: func() (string, error) { return "/opt/conductor/conductor", nil },
		EgressUnix: func(allow []string) (string, string, func(), error) {
			return "/tmp/egress-x/egress-0.sock", "cred-unix", func() {}, nil
		},
		EgressAddr: func(allow []string) (string, string, func(), error) {
			return "127.0.0.1:40123", "cred-tcp", func() {}, nil
		},
		Relay: func(target string) (string, func(), error) {
			f.relays = append(f.relays, target)
			return "/tmp/csr1/r1.sock", func() {}, nil
		},
		LookPath: func(b string) (string, error) { return "/usr/bin/" + b, nil },
		RealHome: filepath.Join(root, "real"),
		Inherited: []string{"PATH=/usr/bin", "USER=op", "LANG=C", "HOME=/home/op", "TMPDIR=/var/folders/x",
			"GH_TOKEN=ghp_x", "GITHUB_TOKEN=x", "SSH_AUTH_SOCK=/run/ssh", "AWS_SECRET_ACCESS_KEY=x",
			"CLAUDE_CODE_MESSAGING_TOKEN=x", "CONDUCTOR_SKILL_TOKEN=daemon-own", "OPENAI_API_KEY=sk-o",
			"ANTHROPIC_API_KEY=sk-a", "ANTHROPIC_BASE_URL=http://127.0.0.1:3456", "CLAUDE_CODE_SUBAGENT_MODEL=m",
			"DISABLE_TELEMETRY=1", "LC_ALL=C"},
	}
	return f
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// jailOf decodes the --jail allow-list out of a Linux wrap.
func jailOf(t *testing.T, argv []string) []sandbox.BindMount {
	t.Helper()
	for i, a := range argv {
		if a == "--jail" && i+1 < len(argv) {
			var bs []sandbox.BindMount
			if err := json.Unmarshal([]byte(argv[i+1]), &bs); err != nil {
				t.Fatal(err)
			}
			return bs
		}
	}
	t.Fatalf("no --jail in %q", argv)
	return nil
}

func hasBind(bs []sandbox.BindMount, want sandbox.BindMount) bool {
	for _, b := range bs {
		if b == want {
			return true
		}
	}
	return false
}

func TestWrapLinuxPolicy(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	l, err := Wrap(f.pol, []string{"claude", "-p", "hi"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	// No network of its own (--net), re-entered through conductor's forwarder,
	// the allowlist proxy and the relay the only ways out.
	for _, want := range []string{"unshare --user --map-root-user", " --net ", "/opt/conductor/conductor sandbox-net",
		"--listen 127.0.0.1:18080 --unix /tmp/egress-x/egress-0.sock", "--relay 127.0.0.1:3456=/tmp/csr1/r1.sock",
		"--chdir " + f.pol.Workspace, "-- /usr/bin/claude -p hi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("wrap lacks %q:\n%s", want, joined)
		}
	}
	if len(f.relays) != 1 || f.relays[0] != "127.0.0.1:3456" {
		t.Errorf("relays: %v", f.relays)
	}
	bs := jailOf(t, l.Argv)
	for _, want := range []sandbox.BindMount{
		{Path: f.pol.Workspace}, {Path: f.pol.Home},
		{Path: f.pol.Socket},
		{Path: f.pol.ReadOnly[0], RO: true}, {Path: f.pol.ReadWrite[0]},
		{Path: f.pol.Hide[0], Tmpfs: true},
		{Path: f.pol.Logins[0].Path, Src: f.pol.Logins[0].Src},
		{Path: "/opt/conductor/conductor", Src: "/opt/conductor/conductor", RO: true},
		{Path: "/tmp/csr1/r1.sock"}, {Path: "/tmp/egress-x"},
	} {
		if want.Path == "/opt/conductor/conductor" {
			continue // /opt is in the Linux base: no bind needed
		}
		if !hasBind(bs, want) {
			t.Errorf("jail lacks %+v:\n%+v", want, bs)
		}
	}
	for _, b := range bs {
		if within(f.deps.RealHome, b.Path) || b.Path == "/" {
			t.Errorf("the operator's home is never shown whole: %+v", b)
		}
		if strings.Contains(b.Path, "state") && b.Path != f.pol.Socket {
			t.Errorf("unexpected state path bound: %+v", b)
		}
	}
	env := envMap(l.Env)
	for k, want := range map[string]string{
		"HOME": f.pol.Home, "TMPDIR": "/tmp", "PATH": "/usr/bin", "USER": "op", "LC_ALL": "C",
		"ANTHROPIC_API_KEY": "sk-a", "ANTHROPIC_BASE_URL": "http://127.0.0.1:3456",
		"CLAUDE_CODE_SUBAGENT_MODEL": "m", "DISABLE_TELEMETRY": "1", "GIT_AUTHOR_NAME": "Op",
		"CONDUCTOR_ENDPOINT": "unix://" + f.pol.Socket, "CONDUCTOR_SKILL_TOKEN": "tok-123",
	} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	if !strings.Contains(env["HTTPS_PROXY"], "cred-unix@127.0.0.1:18080") {
		t.Errorf("HTTPS_PROXY = %q", env["HTTPS_PROXY"])
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY", "CLAUDE_CODE_MESSAGING_TOKEN", "OPENAI_API_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s must not reach the agent", k)
		}
	}
	if l.Dir != f.pol.Workspace {
		t.Errorf("dir %q", l.Dir)
	}
}

func TestWrapLinuxNoEgressIsAFullCut(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	f.pol.Egress, f.pol.Loopback = nil, nil
	f.deps.EgressUnix = nil
	l, err := Wrap(f.pol, []string{"/bin/sh"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(l.Argv, " ")
	if !strings.Contains(j, " --net ") || strings.Contains(j, "--listen") || strings.Contains(j, "--relay") {
		t.Fatalf("no egress = no network at all:\n%s", j)
	}
	if _, ok := envMap(l.Env)["HTTPS_PROXY"]; ok {
		t.Fatal("no proxy without an allowlist")
	}
}

func TestWrapLinuxCwdInsideWorkspace(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	sub := filepath.Join(f.pol.Workspace, "pkg")
	os.MkdirAll(sub, 0o700)
	f.pol.Cwd = sub
	l, err := Wrap(f.pol, []string{"/bin/sh"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if l.Dir != sub || !strings.Contains(strings.Join(l.Argv, " "), "--chdir "+sub) {
		t.Fatalf("starts in its cwd: %q %q", l.Dir, l.Argv)
	}
	if !hasBind(jailOf(t, l.Argv), sandbox.BindMount{Path: f.pol.Workspace}) {
		t.Fatal("the whole workspace stays bound")
	}
	f.pol.Cwd = t.TempDir()
	if _, err := Wrap(f.pol, []string{"/bin/sh"}, f.deps); err == nil {
		t.Fatal("a cwd outside the workspace is refused")
	}
}

func TestWrapDarwinProfile(t *testing.T) {
	withPlatform(t, "darwin")
	f := newPolicyFixture(t)
	f.pol.ReadOnly = append(f.pol.ReadOnly, filepath.Join(f.pol.ReadWrite[0], "config"))
	os.WriteFile(f.pol.ReadOnly[1], nil, 0o600)
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if l.Argv[0] != "sandbox-exec" || l.Argv[1] != "-p" || strings.Join(l.Argv[3:], " ") != "/usr/bin/claude" {
		t.Fatalf("seatbelt wrap: %q", l.Argv)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "sandbox-net") || len(f.relays) != 0 {
		t.Fatal("no Linux plumbing (forwarder, relays) on macOS")
	}
	p := l.Argv[2]
	r := func(s string) string {
		if x, err := filepath.EvalSymlinks(s); err == nil {
			return x
		}
		return s
	}
	for _, want := range []string{
		"(deny network*)",
		`(allow network-outbound (remote unix-socket (path-literal "` + r(f.pol.Socket) + `")))`,
		`(allow network-outbound (remote ip "localhost:3456"))`,
		`(allow network-outbound (remote ip "localhost:40123"))`,
		`(allow file-read* file-write* (subpath "` + r(f.pol.Workspace) + `"))`,
		`(allow file-read* file-write* (subpath "` + r(f.pol.Home) + `"))`,
		`(allow file-read* (subpath "` + r(f.pol.ReadOnly[0]) + `"))`,
		`(deny file-write* (subpath "` + r(f.pol.ReadOnly[1]) + `"))`,
		`(deny file-read* file-write* (subpath "` + r(f.pol.Hide[0]) + `"))`,
		`(allow file-read* file-write* (subpath "` + r(f.pol.Logins[0].Src) + `"))`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %s\n%s", want, p)
		}
	}
	if strings.Contains(p, "(allow network*)") {
		t.Fatal("the network is never open")
	}
	env := envMap(l.Env)
	if env["TMPDIR"] != filepath.Join(f.pol.Home, ".tmp") || !strings.Contains(env["HTTPS_PROXY"], "cred-tcp@127.0.0.1:40123") {
		t.Fatalf("env: %v", env)
	}
}

func TestWrapRefusesWhatItCannotConfine(t *testing.T) {
	withPlatform(t, "linux")
	cases := map[string]func(f *fixture){
		"no workspace":        func(f *fixture) { f.pol.Workspace = filepath.Join(f.pol.Workspace, "gone") },
		"relative workspace":  func(f *fixture) { f.pol.Workspace = "ws" },
		"home not created":    func(f *fixture) { f.pol.Home = filepath.Join(f.pol.Home, "gone") },
		"relative home":       func(f *fixture) { f.pol.Home = "home" },
		"socket not a socket": func(f *fixture) { f.pol.Socket = f.pol.Home },
		"empty egress":        func(f *fixture) { f.pol.Egress = []string{""} },
		"non-loopback relay":  func(f *fixture) { f.pol.Loopback = []string{"10.0.0.1:3456"} },
		"relay without port":  func(f *fixture) { f.pol.Loopback = []string{"127.0.0.1"} },
		"relative ro path":    func(f *fixture) { f.pol.ReadOnly = []string{"objects"} },
		"no relay wired":      func(f *fixture) { f.deps.Relay = nil },
		"no self":             func(f *fixture) { f.deps.SelfExe = nil },
		"no lookpath":         func(f *fixture) { f.deps.LookPath = nil },
		"unknown binary":      func(f *fixture) { f.deps.LookPath = func(string) (string, error) { return "", os.ErrNotExist } },
		"no proxy wired":      func(f *fixture) { f.deps.EgressUnix = nil },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPolicyFixture(t)
			mut(f)
			if l, err := Wrap(f.pol, []string{"claude"}, f.deps); err == nil {
				t.Fatalf("must refuse, got %q", l.Argv)
			}
		})
	}
	f := newPolicyFixture(t)
	if _, err := Wrap(f.pol, nil, f.deps); err == nil {
		t.Fatal("nothing to launch is refused")
	}
}

func TestAgentEnvKeepsOnlyWhatAnAgentNeeds(t *testing.T) {
	p := Policy{Tool: "codex", Home: "/h"}
	env := envMap(AgentEnv([]string{"OPENAI_API_KEY=o", "OPENAI_BASE_URL=u", "ANTHROPIC_API_KEY=a", "HOME=/real", "GH_TOKEN=g",
		"NPM_TOKEN=n", "CODEX_HOME=/real/.codex", "DISABLE_X=1", "TERM=xterm", "SHELL=/bin/zsh"}, p))
	if env["OPENAI_API_KEY"] != "o" || env["OPENAI_BASE_URL"] != "u" || env["HOME"] != "/h" || env["TERM"] != "xterm" || env["DISABLE_X"] != "1" {
		t.Fatalf("kept: %v", env)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "GH_TOKEN", "NPM_TOKEN", "CODEX_HOME", "CONDUCTOR_ENDPOINT"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s must be dropped (another tool's credential, a credential, or the real home)", k)
		}
	}
	// No socket or no token: no endpoint.
	p.Token = "t"
	if _, ok := envMap(AgentEnv(nil, p))["CONDUCTOR_ENDPOINT"]; ok {
		t.Fatal("a token without a socket is not offered")
	}
	p.Socket, p.Token = "/s", ""
	if _, ok := envMap(AgentEnv(nil, p))["CONDUCTOR_SKILL_TOKEN"]; ok {
		t.Fatal("a socket without a token is not offered")
	}
	// The last value of a key wins (the policy's own over inherited).
	p.Env = []string{"TERM=dumb"}
	if envMap(AgentEnv([]string{"TERM=xterm"}, p))["TERM"] != "dumb" {
		t.Fatal("policy env overrides inherited")
	}
}

func TestModelRoute(t *testing.T) {
	old := lookupIP
	lookupIP = func(h string) ([]net.IP, error) {
		if h == "router.tail" {
			return []net.IP{net.ParseIP("100.64.0.7")}, nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { lookupIP = old })
	hosts, lb := ModelRoute("claude-code", "", nil)
	if len(lb) != 0 || !contains(hosts, "api.anthropic.com") || contains(hosts, "api.openai.com") {
		t.Fatalf("defaults: %v %v", hosts, lb)
	}
	_, lb = ModelRoute("claude-code", "", []string{"ANTHROPIC_BASE_URL=http://localhost:3456/pool"})
	if len(lb) != 1 || lb[0] != "127.0.0.1:3456" {
		t.Fatalf("a loopback router is relayed: %v", lb)
	}
	hosts, lb = ModelRoute("claude-code", "", []string{"ANTHROPIC_BASE_URL=https://router.tail:8443"})
	if len(lb) != 0 || !contains(hosts, "router.tail:8443") || !contains(hosts, "100.64.0.7:8443") {
		t.Fatalf("a remote router is allowlisted, by name and address: %v", hosts)
	}
	hosts, _ = ModelRoute("claude-code", "", []string{"ANTHROPIC_BASE_URL=http://plain.example"})
	if !contains(hosts, "plain.example:80") {
		t.Fatalf("http defaults to :80: %v", hosts)
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:4000"}}`), 0o600)
	if _, lb = ModelRoute("claude-code", home, nil); len(lb) != 1 || lb[0] != "127.0.0.1:4000" {
		t.Fatalf("claude-code's settings.json route: %v", lb)
	}
	if _, lb = ModelRoute("codex", home, nil); len(lb) != 0 {
		t.Fatalf("another tool ignores claude's settings: %v", lb)
	}
	if hosts, _ := ModelRoute("", "", nil); len(hosts) != 0 {
		t.Fatalf("an unknown tool reaches no model: %v", hosts)
	}
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

func TestCanonicalTool(t *testing.T) {
	for in, want := range map[string]string{
		"claude": "claude-code", "/usr/bin/claude": "claude-code", "claude-teamclaude": "claude-code", "claude-code": "claude-code",
		"claude-agent-acp": "claude-code", "codex": "codex", "codex-acp": "codex", "pi": "pi", "gemini": "gemini", "sh": "", "": "",
	} {
		if got := CanonicalTool(in); got != want {
			t.Errorf("CanonicalTool(%q) = %q, want %q", in, got, want)
		}
	}
}
