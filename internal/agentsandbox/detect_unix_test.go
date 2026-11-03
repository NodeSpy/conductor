//go:build linux || darwin

package agentsandbox

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Real processes: a tunnel tool and a shell wired to a TCP socket, both of
// the launch (its marker), are found and killed; a process outside the
// launch is left alone. (Run in a PID namespace: the harness's wrapper.)
func TestDetectorKillsRealProcesses(t *testing.T) {
	fakeSSH := fakeTool(t, "ssh")
	marker := newMarker()
	env := append(os.Environ(), markerEnv+"="+marker)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c)
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sockFile, err := conn.(*net.TCPConn).File()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	start := func(env []string, stdin *os.File, name string, args ...string) *exec.Cmd {
		c := exec.Command(name, args...)
		c.Env = env
		if stdin != nil {
			c.Stdin, c.Stdout = stdin, stdin
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); waitOnce(c) })
		return c
	}
	tunnel := start(env, nil, fakeSSH, "60")
	shell := start(env, sockFile, "sh", "-c", "sleep 60; true")
	bystander := start(os.Environ(), nil, fakeSSH, "60") // not the launch's
	sockFile.Close()

	var mu sync.Mutex
	var kinds []string
	d := &detector{spec: DetectSpec{Action: DetectKill}, run: "r", allow: map[string]bool{}, marker: marker,
		signal: signalProc, reported: map[string]bool{},
		event: func(_ string, f map[string]any) { mu.Lock(); kinds = append(kinds, f["kind"].(string)); mu.Unlock() }}
	d.list = listProcs(marker, d.isRoot)
	time.Sleep(200 * time.Millisecond) // let sh fork its sleep
	if runtime.GOOS == "darwin" {
		// macOS hides a platform binary's environment (sh, a copy of
		// sleep): the launcher names its roots instead.
		d.started(tunnel.Process.Pid)
		d.started(shell.Process.Pid)
	}
	d.scan()

	exited := func(c *exec.Cmd) bool {
		ch := make(chan struct{})
		go func() { waitOnce(c); close(ch) }()
		select {
		case <-ch:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}
	if !exited(tunnel) || !exited(shell) {
		t.Fatalf("the launch's processes were not killed (events %v)", kinds)
	}
	mu.Lock()
	got := strings.Join(kinds, ",")
	mu.Unlock()
	if !strings.Contains(got, "tunnel_tool") {
		t.Fatalf("events %s", got)
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("a process outside the launch was killed")
	}

	// Alert: the reverse shell is reported, nothing killed.
	conn2, _ := net.Dial("tcp", ln.Addr().String())
	f2, _ := conn2.(*net.TCPConn).File()
	conn2.Close()
	sh2 := start(env, f2, "sh", "-c", "sleep 60; true")
	f2.Close()
	kinds = nil
	d2 := &detector{spec: DetectSpec{Action: DetectAlert}, run: "r", allow: map[string]bool{}, marker: marker,
		signal: signalProc, reported: map[string]bool{},
		event: func(_ string, f map[string]any) { mu.Lock(); kinds = append(kinds, f["kind"].(string)); mu.Unlock() }}
	d2.list = listProcs(marker, d2.isRoot)
	time.Sleep(200 * time.Millisecond)
	if runtime.GOOS == "darwin" {
		d2.started(sh2.Process.Pid)
	}
	d2.scan()
	mu.Lock()
	got = strings.Join(kinds, ",")
	mu.Unlock()
	if !strings.Contains(got, "reverse_shell") {
		t.Fatalf("reverse shell not reported: %s", got)
	}
	if err := sh2.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("alert killed")
	}
}

var waits sync.Map

// waitOnce reaps c once, however many callers wait.
func waitOnce(c *exec.Cmd) {
	o, _ := waits.LoadOrStore(c, &sync.Once{})
	o.(*sync.Once).Do(func() { _ = c.Wait() })
}

// A root the launcher names is followed without its marker: its
// descendants are the launch's, and a pid that is no longer that process
// is forgotten.
func TestDetectorFollowsTheRootItsLauncherNames(t *testing.T) {
	fakeSSH := fakeTool(t, "ssh")
	marker, closer, err := startDetection(Policy{Workspace: t.TempDir(), Home: t.TempDir(),
		Detect: DetectSpec{Interval: 50 * time.Millisecond, NoTripwires: true}, Limits: LimitSpec{Off: true}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	root := exec.Command("sh", "-c", fakeSSH+" 60; true") // no marker in its environment
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Process.Kill(); waitOnce(root) })
	LaunchStarted([]string{"PATH=/bin", markerEnv + "=someone-else"}, root.Process.Pid) // another launch's: ignored
	ch := make(chan struct{})
	go func() { waitOnce(root); close(ch) }()
	select {
	case <-ch:
		t.Fatal("killed through another launch's marker")
	case <-time.After(500 * time.Millisecond):
	}
	LaunchStarted([]string{markerEnv + "=" + marker}, root.Process.Pid)
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the named root's tunnel tool was not killed")
	}
}

// fakeTool is a copy of sleep named name (macOS kills a copied platform
// binary unless it is re-signed).
func fakeTool(t *testing.T, name string) string {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	p := filepath.Join(t.TempDir(), name)
	b, _ := os.ReadFile(sleep)
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("codesign", "-s", "-", "-f", p).CombinedOutput(); err != nil {
			t.Skipf("codesign: %v %s", err, out)
		}
	}
	return p
}

// A launch's decoys are gone once it ends: with no watcher to answer, a
// reader of one (a recursive grep over the state dir) would block forever.
func TestTripwiresLeaveWithTheirLaunch(t *testing.T) {
	home := t.TempDir()
	_, closer, err := startDetection(Policy{Workspace: t.TempDir(), Home: home, Limits: LimitSpec{Off: true}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(home, ".ssh", "id_ed25519")
	if fi, err := os.Lstat(decoy); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("no decoy while the launch runs: %v", err)
	}
	closer()
	for _, d := range decoys {
		if _, err := os.Lstat(filepath.Join(home, d.rel)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind after the launch: %v", d.rel, err)
		}
	}
}

// isRoot forgets a root pid once its kernel start time no longer matches
// what started() recorded: a reused pid is not followed as the old root.
func TestIsRootForgetsAPidWhoseStartTimeChanged(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	c := exec.Command(sleep, "5")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); waitOnce(c) })
	pid := c.Process.Pid
	d := &detector{}
	d.started(pid)
	if !d.isRoot(pid) {
		t.Fatal("a freshly started root not recognized")
	}
	// Simulate the pid being reused by a different process (a stale start
	// time): isRoot must forget it, not keep treating it as this root.
	d.mu.Lock()
	d.roots[pid] = d.roots[pid] + 1
	d.mu.Unlock()
	if d.isRoot(pid) {
		t.Fatal("a pid whose start time changed was still treated as the root")
	}
	d.mu.Lock()
	_, stillThere := d.roots[pid]
	d.mu.Unlock()
	if stillThere {
		t.Fatal("the stale root was not forgotten")
	}
}

// killAll never widens its reach to a daemon process sharing the
// launcher's own process group: ownPgid() is excluded even when that
// group is one a launch process leads.
func TestKillAllExcludesItsOwnProcessGroup(t *testing.T) {
	own := ownPgid()
	if own <= 1 {
		t.Skipf("this process's group (%d) is never a launch group; run under setsid", own)
	}
	marked := procInfo{pid: own, ppid: 1, pgid: own, marked: true, start: 1}
	bystander := procInfo{pid: own + 123456, ppid: 1, pgid: own, start: 1}
	var signalled []int
	d := &detector{
		allow: map[string]bool{},
		list:  func() ([]procInfo, error) { return []procInfo{marked, bystander}, nil },
		signal: func(pid int, start uint64) error {
			signalled = append(signalled, pid)
			return nil
		},
	}
	d.killAll()
	for _, pid := range signalled {
		if pid == bystander.pid {
			t.Fatalf("killAll signalled a process sharing its own process group: %v", signalled)
		}
	}
}

// The watcher never answers through a symlink: an agent that replaces a
// decoy with a symlink to a FIFO it controls elsewhere gets no credential
// written into it, and the decoy is re-planted as a genuine pipe at its
// own path.
func TestTripwireRefusesASymlinkedDecoy(t *testing.T) {
	home := t.TempDir()
	ws, err := plantTripwires(home)
	if err != nil {
		t.Fatal(err)
	}
	w := ws[0]
	w.poll = 10 * time.Millisecond

	elsewhere := filepath.Join(t.TempDir(), "evil")
	if err := syscall.Mkfifo(elsewhere, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(w.path)
	if err := os.Symlink(elsewhere, w.path); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	hits := make(chan string, 4)
	go w.watch(stop, func(string) { hits <- w.rel })

	leaked := make(chan []byte, 1)
	go func() {
		f, err := os.OpenFile(elsewhere, os.O_RDONLY, 0)
		if err != nil {
			leaked <- nil
			return
		}
		b := make([]byte, 4096)
		n, _ := f.Read(b)
		leaked <- b[:n]
	}()

	select {
	case b := <-leaked:
		if len(b) > 0 {
			t.Fatalf("the watcher wrote the decoy credential through a symlink: %q", b)
		}
	case <-time.After(1 * time.Second):
		os.Remove(elsewhere) // unblock the reader goroutine, if still waiting
	}
	select {
	case h := <-hits:
		t.Fatalf("a symlinked decoy counted as a read: %s", h)
	default:
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		fi, err := os.Lstat(w.path)
		if err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the symlinked decoy was not re-planted as a pipe: %v %v", fi, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A scoped allowance lets the tool it names run in its own launch only:
// audited as allowed (by its scope), not killed; the same tool in a launch
// without it is killed, and another tool in the allowed launch is too.
func TestScopedAllowanceLetsOnlyItsLaunchRunATunnel(t *testing.T) {
	cf, ssh := fakeTool(t, "cloudflared"), fakeTool(t, "ssh")
	type launch struct {
		d      *detector
		cmd    *exec.Cmd
		events []map[string]any
		mu     sync.Mutex
	}
	run := func(spec DetectSpec, tool string) *launch {
		l := &launch{}
		marker := newMarker()
		l.cmd = exec.Command(tool, "60")
		l.cmd.Env = append(os.Environ(), markerEnv+"="+marker)
		if err := l.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.cmd.Process.Kill(); waitOnce(l.cmd) })
		l.d = &detector{spec: spec, run: "r", allow: map[string]bool{}, marker: marker, signal: signalProc, reported: map[string]bool{},
			event: func(_ string, f map[string]any) { l.mu.Lock(); l.events = append(l.events, f); l.mu.Unlock() }}
		l.d.list = listProcs(marker, l.d.isRoot)
		l.d.started(l.cmd.Process.Pid)
		return l
	}
	allowance := DetectSpec{AllowTunnels: []string{"cloudflared"}, AllowedBy: "workflow deploy"}
	allowed := run(allowance, cf)
	strict := run(DetectSpec{}, cf)
	otherTool := run(allowance, ssh)
	time.Sleep(100 * time.Millisecond)
	for _, l := range []*launch{allowed, strict, otherTool} {
		l.d.scan()
	}
	exited := func(c *exec.Cmd) bool {
		ch := make(chan struct{})
		go func() { waitOnce(c); close(ch) }()
		select {
		case <-ch:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}
	if !exited(strict.cmd) || !exited(otherTool.cmd) {
		t.Fatal("a tunnel tool outside its allowance was not killed")
	}
	if err := allowed.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("the allowed launch's tunnel was killed")
	}
	allowed.mu.Lock()
	defer allowed.mu.Unlock()
	if len(allowed.events) != 1 || allowed.events[0]["action"] != "allowed" || allowed.events[0]["allowed_by"] != "workflow deploy" || allowed.events[0]["kind"] != "tunnel_tool" {
		t.Fatalf("the allowed tunnel's audit: %v", allowed.events)
	}
	strict.mu.Lock()
	defer strict.mu.Unlock()
	if len(strict.events) == 0 || strict.events[0]["action"] != DetectKill {
		t.Fatalf("the strict launch's audit: %v", strict.events)
	}
}
