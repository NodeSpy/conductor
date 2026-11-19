//go:build darwin

package agentsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// (macOS only: on Linux the jail is a pid namespace, which ends with its
// launch, and a pid the agent sees is not one the host has.)
//
// A launch with caches leaves nothing running once it is cleaned up, so
// nothing writes its home while its step's end publishes what it filled —
// even with detection and limits off, and on macOS, where there is no pid
// namespace and a process an agent backgrounds (nohup … &) is reparented
// to launchd with its environment hidden from ps: the launch runs in a
// process group of its own (RunPgroup), and the group's leftovers are
// killed at cleanup.
func TestCachedLaunchLeavesNothingRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := SocketDir("cll")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	bin := filepath.Join(root, "conductor")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}
	ws, state, ledger := filepath.Join(root, "ws"), filepath.Join(root, "st"), filepath.Join(root, "ledger", "1-x")
	for _, d := range []string{ws, state, ledger} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lock := filepath.Join(ledger, "lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(state, "agent-state"), Key: "agent:leftover", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	pol := Policy{
		Identity: "agent:leftover", Tool: "claude-code", Workspace: ws, Home: home, Logins: logins,
		CacheLock: lock, Detect: DetectSpec{Action: DetectOff}, Limits: LimitSpec{Off: true},
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	l, err := Wrap(pol, []string{"/bin/sh", "-c", `nohup /bin/sleep 300 >/dev/null 2>&1 & echo $! >"$WS/pid"`}, Deps{
		SelfExe: func() (string, error) { return bin, nil }, EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint, Relay: rel.Relay,
		LookPath: exec.LookPath, RealHome: state, Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, "WS="+ws)
	var outb strings.Builder
	cmd.Stdout, cmd.Stderr = &outb, &outb
	if err := cmd.Start(); err != nil {
		l.Cleanup()
		t.Fatal(err)
	}
	LaunchStarted(cmd.Env, cmd.Process.Pid) // as every launcher does
	_ = cmd.Wait()
	out := outb.String()
	b, err := os.ReadFile(filepath.Join(ws, "pid"))
	pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || perr != nil {
		l.Cleanup()
		t.Fatalf("the launch did not start its leftover (%v %v): %s", err, perr, out)
	}
	start, ok := procStart(pid)
	alive := func() bool { st, ok2 := procStart(pid); return ok2 && st == start }
	t.Cleanup(func() {
		if ok && alive() { // never a process that reused the pid
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !ok || !alive() {
		t.Fatal("the leftover died with its parent: the test proves nothing")
	}
	l.Cleanup()
	deadline := time.Now().Add(5 * time.Second)
	for alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive() {
		t.Fatalf("a process the cached launch left running (pid %d) outlived its cleanup", pid)
	}
}
