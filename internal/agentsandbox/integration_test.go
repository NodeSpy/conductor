//go:build linux || darwin

package agentsandbox

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The probe the sandboxed child runs (this test binary, re-executed inside
// the real sandbox): each line is "ok <what>" or "FAIL <what>".
func TestSandboxProbeChild(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_PROBE") != "1" {
		t.Skip("runs only inside the sandbox")
	}
	say := func(ok bool, what string) {
		if ok {
			fmt.Println("ok " + what)
		} else {
			fmt.Println("FAIL " + what)
		}
	}
	_, err := os.ReadFile(os.Getenv("PROBE_SECRET"))
	say(err != nil, "operator secret unreadable")
	_, err = os.ReadDir(os.Getenv("PROBE_STATE"))
	say(err != nil || dirOnlyHas(os.Getenv("PROBE_STATE"), os.Getenv("PROBE_SOCK_NAME")), "conductor state hidden")
	say(os.WriteFile(filepath.Join(os.Getenv("PROBE_WS"), "out.txt"), []byte("x"), 0o600) == nil, "workspace writable")
	say(os.WriteFile(filepath.Join(os.Getenv("PROBE_HOME"), "state.txt"), []byte("x"), 0o600) == nil, "identity home writable")
	say(os.WriteFile(filepath.Join(os.Getenv("PROBE_RO"), "x"), []byte("x"), 0o600) != nil, "read-only path not writable")
	c, err := net.DialTimeout("unix", os.Getenv("PROBE_SOCK"), 3*time.Second)
	if err == nil {
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		say(strings.TrimSpace(line) == "CONDUCTOR-OK", "conductor socket reachable")
	} else {
		say(false, "conductor socket reachable: "+err.Error())
	}
	c, err = net.DialTimeout("tcp", os.Getenv("PROBE_MODEL"), 3*time.Second)
	if err == nil {
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		say(strings.TrimSpace(line) == "MODEL-OK", "model endpoint reachable (relayed / allowed)")
	} else {
		say(false, "model endpoint reachable: "+err.Error())
	}
	_, err = net.DialTimeout("tcp", os.Getenv("PROBE_OTHER"), 2*time.Second)
	say(err != nil, "another host-loopback port unreachable")
	_, err = net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
	say(err != nil, "the network is unreachable directly")
	say(os.Getenv("GH_TOKEN") == "" && os.Getenv("SSH_AUTH_SOCK") == "", "credentials not in env")
}

func dirOnlyHas(dir, name string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.Name() != name && e.Name() != "agent-state" {
			return false
		}
	}
	return true
}

func serveLine(t *testing.T, network, addr, line string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(line + "\n"))
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// TestSandboxConfinesARealAgent runs the probe under the real backend —
// Linux user namespaces + pivot_root, or macOS Seatbelt — with a policy
// shaped like a dispatch's.
func TestSandboxConfinesARealAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := SocketDir("csi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws, state, ro, secretDir := filepath.Join(root, "ws"), filepath.Join(root, "st"), filepath.Join(root, "ro"), filepath.Join(root, "sec")
	for _, d := range []string{ws, state, ro, secretDir} {
		os.MkdirAll(d, 0o700)
	}
	secret := filepath.Join(secretDir, "id_ed25519")
	os.WriteFile(secret, []byte("DECOY-KEY"), 0o600)
	os.WriteFile(filepath.Join(state, "state.json"), []byte("DECOY-STATE"), 0o600)
	sock := filepath.Join(state, "m.sock")
	serveLine(t, "unix", sock, "CONDUCTOR-OK")
	model := serveLine(t, "tcp", "127.0.0.1:0", "MODEL-OK")
	other := serveLine(t, "tcp", "127.0.0.1:0", "LEAK")

	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(state, "agent-state"), Key: "agent:probe", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	pol := Policy{
		Identity: "agent:probe", Tool: "claude-code", Workspace: ws, Home: home, Logins: logins,
		Egress:   []string{"example.org"},
		Loopback: []string{model.Addr().String()},
		Socket:   sock, Token: "t",
		// The test binary (the probe) and its directory, read-only.
		ReadOnly: []string{filepath.Dir(self), ro},
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	l, err := Wrap(pol, []string{self, "-test.run=^TestSandboxProbeChild$", "-test.v=false"}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint, Relay: rel.Relay,
		LookPath: exec.LookPath, RealHome: secretDir,
		Inherited: append(os.Environ(), "GH_TOKEN=ghp_leak", "SSH_AUTH_SOCK=/tmp/agent.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, "AGENTSANDBOX_PROBE=1", "PROBE_SECRET="+secret, "PROBE_STATE="+state, "PROBE_SOCK_NAME=m.sock",
		"PROBE_WS="+ws, "PROBE_HOME="+home, "PROBE_RO="+ro, "PROBE_SOCK="+sock, "PROBE_MODEL="+model.Addr().String(), "PROBE_OTHER="+other.Addr().String())
	out, err := cmd.CombinedOutput()
	t.Logf("probe (%s):\n%s", runtime.GOOS, out)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if strings.Contains(string(out), "FAIL") || strings.Count(string(out), "\nok ")+boolInt(strings.HasPrefix(string(out), "ok ")) < 10 {
		t.Fatalf("confinement probe:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "out.txt")); err != nil || string(b) != "x" {
		t.Fatal("the workspace write reached the host")
	}
	if _, err := os.Stat(filepath.Join(home, "state.txt")); err != nil {
		t.Fatal("the identity home write persisted on the host (the next launch sees it)")
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// selfConductor builds conductor (the in-sandbox forwarder on Linux).
func selfConductor(t *testing.T) func() (string, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return func() (string, error) { return "/usr/bin/true", nil }
	}
	bin := filepath.Join(t.TempDir(), "conductor")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}
	return func() (string, error) { return bin, nil }
}
