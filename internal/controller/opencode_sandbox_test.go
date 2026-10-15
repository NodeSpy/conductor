package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// sandboxRig wires the real agent sandbox for a controller test: identity
// homes under root, an operator home holding a decoy key, conductor's own
// binary as the Linux forwarder, the allowlist proxy.
type sandboxRig struct {
	root, realHome, secret, bin string
}

func newSandboxRig(t *testing.T) *sandboxRig {
	t.Helper()
	if testing.Short() {
		t.Skip("short")
	}
	if why := agentsandbox.Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := agentsandbox.SocketDir("cct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	r := &sandboxRig{root: root, realHome: filepath.Join(root, "home"), bin: filepath.Join(root, "bin")}
	r.secret = filepath.Join(r.realHome, ".ssh", "id_decoy")
	for _, d := range []string{filepath.Dir(r.secret), r.bin, filepath.Join(root, "st")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(r.secret, []byte("DECOY-KEY"), 0o600)
	t.Setenv("PATH", r.bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pm := sandbox.NewProxyManager(nil)
	self := buildConductor(t)
	oldSelf, oldSB, oldU, oldA := launchSelfExe, AgentSandbox, EgressProxyUnix, EgressProxyFor
	t.Cleanup(func() {
		launchSelfExe, AgentSandbox, EgressProxyUnix, EgressProxyFor = oldSelf, oldSB, oldU, oldA
		pm.Close()
	})
	launchSelfExe = func() (string, error) { return self, nil }
	EgressProxyUnix, EgressProxyFor = pm.UnixEndpoint, pm.Endpoint
	AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{
		StateRoot: filepath.Join(root, "st"), RealHome: r.realHome, GOOS: runtime.GOOS,
	}}
	return r
}

// build compiles a harness fake into the rig's bin dir under name.
func (r *sandboxRig) build(t *testing.T, name, pkg string) {
	t.Helper()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(r.bin, name), pkg).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
}

func (r *sandboxRig) workspace(t *testing.T, name string) string {
	t.Helper()
	ws := filepath.Join(r.root, name)
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws, "OWN"), []byte(name), 0o600)
	return ws
}

type ocProbe struct {
	Home      string            `json:"home"`
	Read      map[string]string `json:"read"`
	Conductor bool              `json:"conductor"`
	Creds     bool              `json:"creds"`
	Prompt    string            `json:"prompt"`
}

func probeOpencode(t *testing.T, s *opencodeSession, paths ...string) ocProbe {
	t.Helper()
	q := url.Values{}
	for _, p := range paths {
		q.Add("read", p)
	}
	req, _ := http.NewRequest(http.MethodGet, s.cl.baseURL+"/x-probe?"+q.Encode(), nil)
	resp, err := s.cl.hc.Do(req)
	if err != nil {
		t.Fatalf("probe the sandboxed server: %v", err)
	}
	defer resp.Body.Close()
	var p ocProbe
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return p
}

// Every opencode dispatch runs its own `opencode serve` inside the agent
// sandbox, under that dispatch's identity: conductor drives it through the
// exposed port (and the session's password), the operator's key is absent,
// HOME is the identity's own, and two concurrent dispatches see neither
// each other's workspace nor each other's tool state.
func TestOpencodeSandboxedServerPerDispatch(t *testing.T) {
	r := newSandboxRig(t)
	r.build(t, "opencode", "github.com/NodeSpy/conductor/test/e2e/services/fakeopencode")
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, nil)

	type run struct {
		ws   string
		sess *opencodeSession
	}
	start := func(name, repo, id string) run {
		ws := r.workspace(t, name)
		req := makeReq("merge_conflict", "fix it")
		req.DispatchID = id
		req.Action.Agent = "fixer"
		req.Trigger.Target = core.Target{Repo: repo, Number: 1}
		req.Trigger.TargetTrusted = true
		s, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: ws}, nil)
		if err != nil {
			t.Fatalf("NewSession %s: %v", name, err)
		}
		t.Cleanup(func() { s.Close(context.Background()); forgetWorkspace(ws) })
		return run{ws, s.(*opencodeSession)}
	}
	a := start("wsA", "acme/a", "dA")
	b := start("wsB", "acme/b", "dB")
	for _, x := range []run{a, b} {
		waitSession(t, x.sess)
	}
	// The turn went through: conductor reached each server over its
	// exposed port with its password.
	ch, err := a.sess.Prompt(context.Background(), Message{Text: "again"})
	if err != nil {
		t.Fatal(err)
	}
	var last Update
	for u := range ch {
		last = u
	}
	if last.Err != nil || last.Output != "opencode: done" {
		t.Fatalf("follow-up turn on the sandboxed server: %+v", last)
	}

	pa := probeOpencode(t, a.sess, r.secret, filepath.Join(a.ws, "OWN"), filepath.Join(b.ws, "OWN"))
	pb := probeOpencode(t, b.sess, filepath.Join(b.ws, "OWN"), filepath.Join(a.ws, "OWN"), pa.Home)
	if pa.Read[r.secret] != "denied" {
		t.Fatalf("the operator's key is readable from the sandboxed server: %v", pa.Read)
	}
	if pa.Read[filepath.Join(a.ws, "OWN")] != "ok" || pb.Read[filepath.Join(b.ws, "OWN")] != "ok" {
		t.Fatalf("a server cannot read its own workspace: %v / %v", pa.Read, pb.Read)
	}
	if pa.Read[filepath.Join(b.ws, "OWN")] != "denied" || pb.Read[filepath.Join(a.ws, "OWN")] != "denied" {
		t.Fatalf("one dispatch sees the other's workspace: %v / %v", pa.Read, pb.Read)
	}
	if pb.Read[pa.Home] != "denied" {
		t.Fatalf("dispatch B reads dispatch A's tool state %s", pa.Home)
	}
	st := AgentSandbox.Resolver.StateRoot
	if !strings.HasPrefix(pa.Home, st) || !strings.HasPrefix(pb.Home, st) || pa.Home == pb.Home {
		t.Fatalf("HOME is not each identity's own: %q / %q", pa.Home, pb.Home)
	}
	if pa.Creds {
		t.Fatal("a forge credential reached the sandboxed server's env")
	}
	if !strings.Contains(pb.Prompt, "Your sandbox") {
		t.Fatalf("the sandboxed agent is told about its sandbox: %q", pb.Prompt)
	}
	// A resume restarts the server sandboxed, under the identity the
	// workspace was registered with: the same tool state, where the
	// session is.
	rs, err := c.ResumeSession(context.Background(), a.sess.ID(), false, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer rs.Close(context.Background())
	if pr := probeOpencode(t, rs.(*opencodeSession), r.secret); pr.Home != pa.Home || pr.Read[r.secret] != "denied" {
		t.Fatalf("the resumed server runs as %q (want %q), secret %v", pr.Home, pa.Home, pr.Read)
	}
	// The server's conductor password is what reaches it: the transport
	// without it is refused.
	bare := &http.Client{Transport: a.sess.cl.hc.Transport.(basicAuth).rt}
	resp, err := bare.Get(a.sess.cl.baseURL + "/x-probe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the server answered without its password: %s", resp.Status)
	}
	if runtime.GOOS == "linux" {
		// Its port is the sandbox's own: nothing listens there on the host.
		if c, err := (&http.Client{}).Get(a.sess.cl.baseURL + "/x-probe"); err == nil {
			c.Body.Close()
			if c.StatusCode != http.StatusUnauthorized {
				t.Fatalf("the sandboxed server answers on the host's loopback")
			}
		}
	}
}

// A sandboxed opencode launch that cannot be built is not run bare.
func TestOpencodeSandboxedRefusesWhenTheSandboxCannotBeBuilt(t *testing.T) {
	r := newSandboxRig(t)
	r.build(t, "opencode", "github.com/NodeSpy/conductor/test/e2e/services/fakeopencode")
	EgressProxyUnix, EgressProxyFor = nil, nil
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, nil)
	ws := r.workspace(t, "ws")
	req := makeReq("merge_conflict", "fix it")
	req.DispatchID = "d1"
	_, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: ws}, nil)
	forgetWorkspace(ws)
	if err == nil || !strings.Contains(err.Error(), "not launched unsandboxed") {
		t.Fatalf("an unbuildable sandbox must refuse the launch, got %v", err)
	}
}

// Off, the server is launched as before: a port of its own choosing on the
// host, reached by the controller's own client, no password.
func TestOpencodeUnsandboxedSpawnIsUnchanged(t *testing.T) {
	r := newSandboxRig(t)
	r.build(t, "opencode", "github.com/NodeSpy/conductor/test/e2e/services/fakeopencode")
	AgentSandbox = nil
	u, hc, cleanup, err := spawnOpencode(context.Background(), "", r.workspace(t, "ws"), nil, withAgent(launchOpts{}, "opencode", nil, true))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if hc != nil {
		t.Fatal("an unsandboxed server is reached by the controller's own client")
	}
	if strings.HasSuffix(u, ":4096") {
		t.Fatalf("an unsandboxed server picks its own port: %s", u)
	}
	resp, err := http.Get(u + "/x-probe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no password when unsandboxed: %s", resp.Status)
	}
}

// Inside a Linux sandbox the server's port is fixed — its network namespace
// is its own; on macOS it takes a free host port.
func TestOpencodeServePort(t *testing.T) {
	old := sandbox.CheckGOOS
	t.Cleanup(func() { sandbox.CheckGOOS = old })
	sandbox.CheckGOOS = "linux"
	if p, err := opencodeServePort(); err != nil || p != 4096 {
		t.Fatalf("linux: %d %v", p, err)
	}
	sandbox.CheckGOOS = "darwin"
	p, err := opencodeServePort()
	if err != nil || p == 4096 || p <= 0 {
		t.Fatalf("darwin: a free port, got %d %v", p, err)
	}
}

// What an agent launch adds for its launcher reaches the wrap: files shown
// read-only, its own variables (past the credential filter), the exposed
// address — and how that address is reached comes back.
func TestPrepareAgentSandboxCarriesTheLaunchersAdditions(t *testing.T) {
	oldGOOS, oldLook, oldEuid := sandbox.CheckGOOS, sandbox.CheckLookPath, sandbox.CheckGeteuid
	oldSB, oldSelf, oldU := AgentSandbox, launchSelfExe, EgressProxyUnix
	t.Cleanup(func() {
		sandbox.CheckGOOS, sandbox.CheckLookPath, sandbox.CheckGeteuid = oldGOOS, oldLook, oldEuid
		AgentSandbox, launchSelfExe, EgressProxyUnix = oldSB, oldSelf, oldU
	})
	sandbox.CheckGOOS = "linux"
	sandbox.CheckLookPath = func(n string) (string, error) { return "/usr/bin/" + n, nil }
	sandbox.CheckGeteuid = func() int { return 1000 }
	launchSelfExe = func() (string, error) { return "/opt/conductor/conductor", nil }
	EgressProxyUnix = func([]string) (string, string, func(), error) { return "/tmp/eg/e.sock", "c", func() {}, nil }
	root := t.TempDir()
	AgentSandbox = &SandboxWiring{Resolver: &agentsandbox.Resolver{StateRoot: filepath.Join(root, "st"), GOOS: "linux"}}
	os.MkdirAll(AgentSandbox.Resolver.StateRoot, 0o700)
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o700)
	t.Cleanup(func() { forgetWorkspace(ws) })
	cfg := filepath.Join(t.TempDir(), "tool.json")
	os.WriteFile(cfg, []byte("{}"), 0o600)
	req := makeReq("merge_conflict", "x")
	req.DispatchID = "d1"
	opt := withAgent(launchOpts{}, "opencode", &req, false)
	opt.agent.files = []string{cfg}
	opt.agent.env = []string{"OPENCODE_SERVER_PASSWORD=pw"}
	opt.agent.expose = []string{"127.0.0.1:4096"}
	argv, _, env, _, err := prepareLaunch("", ws, nil, []string{"/bin/true"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--bind-ro "+cfg+" ") && !strings.Contains(joined, `{"path":"`+cfg+`","ro":true}`) {
		t.Fatalf("the launcher's file is shown read-only:\n%s", joined)
	}
	if !envHas(env, "OPENCODE_SERVER_PASSWORD", "pw") {
		t.Fatalf("the launcher's own variable reaches the agent: %v", env)
	}
	sock := opt.agent.exposed["127.0.0.1:4096"]
	if sock == "" || !strings.Contains(joined, "--expose "+sock+"=127.0.0.1:4096") {
		t.Fatalf("exposed %v, argv:\n%s", opt.agent.exposed, joined)
	}
	os.RemoveAll(filepath.Dir(sock))
}
