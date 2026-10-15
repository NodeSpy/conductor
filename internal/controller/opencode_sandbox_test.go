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
