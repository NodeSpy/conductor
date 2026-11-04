//go:build linux || darwin

package agentsandbox

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestSandboxDetectChild runs inside the real sandbox: it talks to its
// model through the gateway, then reads a decoy credential and lingers.
func TestSandboxDetectChild(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_DETECT") != "1" {
		t.Skip("runs only inside the sandbox")
	}
	say := func(ok bool, what string) {
		if ok {
			fmt.Println("ok " + what)
		} else {
			fmt.Println("FAIL " + what)
		}
	}
	leaked := false
	for _, kv := range os.Environ() {
		leaked = leaked || strings.Contains(kv, "sk-operator-real")
	}
	say(!leaked, "the operator's key is not in the environment")
	req, _ := http.NewRequest("POST", os.Getenv("ANTHROPIC_BASE_URL")+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		say(resp.StatusCode == 200 && string(b) == "MODEL-HTTP-OK", "model reached through the gateway")
	} else {
		say(false, "model reached through the gateway: "+err.Error())
	}
	req, _ = http.NewRequest("POST", os.Getenv("ANTHROPIC_BASE_URL")+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Api-Key", "sk-guessed")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		say(resp.StatusCode == 401, "the gateway refuses another key")
	}
	// The router itself, on the host's loopback: only through the gateway.
	direct := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if resp, err := direct.Get(os.Getenv("DETECT_UPSTREAM")); err == nil {
		resp.Body.Close()
		say(false, "the router is unreachable directly")
	} else {
		say(true, "the router is unreachable directly")
	}
	os.Stdout.Sync()
	// (this package's TestMain resets HOME in every test binary)
	home := os.Getenv("DETECT_HOME")
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519"))
	fmt.Printf("read decoy: %v %d bytes\n", err, len(b))
	time.Sleep(30 * time.Second)
	fmt.Println("FAIL still alive after reading a decoy")
}

// TestSandboxDetectsAndGateways: in the real sandbox, the agent's model
// traffic goes through the gateway with the operator's key added host-side,
// and a read of a decoy credential kills the launch.
func TestSandboxDetectsAndGateways(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	var mu sync.Mutex
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("X-Api-Key")+"|"+r.Header.Get("Authorization"))
		mu.Unlock()
		io.WriteString(w, "MODEL-HTTP-OK")
	}))
	defer up.Close()

	root, err := SocketDir("csd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws, state, realHome := filepath.Join(root, "ws"), filepath.Join(root, "st"), filepath.Join(root, "real")
	for _, d := range []string{ws, state, realHome} {
		os.MkdirAll(d, 0o700)
	}
	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(state, "agent-state"), Key: "agent:detect", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	pol := Policy{
		Identity: "agent:detect", Tool: "claude-code", Workspace: ws, Home: home, Logins: logins,
		ReadOnly: []string{filepath.Dir(self)},
		Detect:   DetectSpec{Interval: 200 * time.Millisecond},
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	var events []string
	l, err := Wrap(pol, []string{self, "-test.run=^TestSandboxDetectChild$", "-test.v=false"}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		LookPath: exec.LookPath, RealHome: realHome,
		Inherited: append(os.Environ(), "ANTHROPIC_API_KEY=sk-operator-real", "ANTHROPIC_BASE_URL="+up.URL),
		Event: func(kind string, f map[string]any) {
			mu.Lock()
			events = append(events, fmt.Sprint(kind, " ", f["kind"], " ", f["reason"]))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, append(l.Env, "AGENTSANDBOX_DETECT=1", "DETECT_HOME="+home, "DETECT_UPSTREAM="+up.URL)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	LaunchStarted(cmd.Env, cmd.Process.Pid) // as every launcher does
	err = cmd.Wait()
	out := buf.Bytes()
	t.Logf("child (%s, %v):\n%s", runtime.GOOS, time.Since(start), out)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("events: %v", events)
	if strings.Contains(string(out), "FAIL") || strings.Count(string(out), "ok ") != 4 {
		t.Fatalf("child:\n%s", out)
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); err == nil || !ok || !(ws.Signaled() || ws.ExitStatus() == 137) {
		t.Fatalf("the launch was not killed: %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("killed too late")
	}
	if e := strings.Join(events, "\n"); !strings.Contains(e, "detection tripwire decoy credential .ssh/id_ed25519 read") ||
		!strings.Contains(e, "model_gateway_refused") {
		t.Fatalf("events %v", events)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "sk-operator-real|") {
			t.Fatalf("upstream saw %q", k)
		}
	}
	if len(keys) != 1 {
		t.Fatalf("upstream requests %v (the refused one never forwarded)", keys)
	}
}

// In the real sandbox, a launch whose scope allows a tunnel tool runs it to
// the end (the watcher audits it as allowed, by the scope); the same tool
// in a launch without the allowance is killed.
func TestSandboxAllowsOnlyItsScopesTunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := SocketDir("csa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	tool := fakeTool(t, "cloudflared")
	asHostTools(t, tool)
	launch := func(name string, scope *Scope) (out string, events []map[string]any, err error) {
		ws, state := filepath.Join(root, name, "ws"), filepath.Join(root, name, "st")
		os.MkdirAll(ws, 0o700)
		home, logins, err := PrepareHome(HomeSpec{Root: state, Key: "agent:" + name, Tool: "claude-code", GOOS: runtime.GOOS})
		if err != nil {
			t.Fatal(err)
		}
		pol := Policy{
			Identity: "agent:" + name, Tool: "claude-code", Workspace: ws, Home: home, Logins: logins,
			ReadOnly: []string{filepath.Dir(tool)},
			Detect:   DetectSpec{Interval: 200 * time.Millisecond}, Limits: LimitSpec{Off: true},
		}
		applyScope(&pol, nil, scope)
		pm := sandbox.NewProxyManager(nil)
		defer pm.Close()
		var mu sync.Mutex
		l, err := Wrap(pol, []string{"/bin/sh", "-c", tool + " 3 && echo survived"}, Deps{
			SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
			Event: func(kind string, f map[string]any) {
				if kind == "detection" {
					mu.Lock()
					events = append(events, f)
					mu.Unlock()
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer l.Cleanup()
		cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
		cmd.Dir, cmd.Env = l.Dir, l.Env
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		LaunchStarted(cmd.Env, cmd.Process.Pid)
		err = cmd.Wait()
		mu.Lock()
		defer mu.Unlock()
		t.Logf("%s (%s): err %v, events %v\n%s", name, runtime.GOOS, err, events, buf.Bytes())
		return buf.String(), events, err
	}
	out, events, err := launch("deploy", &Scope{Allow: &Allowance{Tunnels: []string{"cloudflared"}, By: "workflow deploy"}})
	if err != nil || !strings.Contains(out, "survived") {
		t.Fatalf("the allowed launch's tunnel did not run to its end: %v", err)
	}
	if len(events) == 0 || events[0]["action"] != "allowed" || events[0]["allowed_by"] != "workflow deploy" || events[0]["kind"] != "tunnel_tool" {
		t.Fatalf("the allowed tunnel's audit: %v", events)
	}
	out, events, err = launch("review", nil)
	if err == nil || strings.Contains(out, "survived") {
		t.Fatal("the same tool ran to its end in a launch without the allowance")
	}
	if len(events) == 0 || events[0]["action"] != DetectKill {
		t.Fatalf("the strict launch's audit: %v", events)
	}
}
