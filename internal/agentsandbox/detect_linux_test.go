//go:build linux

package agentsandbox

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	dir := t.TempDir()
	fakeSSH := filepath.Join(dir, "ssh")
	b, _ := os.ReadFile(sleep)
	if err := os.WriteFile(fakeSSH, b, 0o755); err != nil {
		t.Fatal(err)
	}
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
		list: listProcs(marker), signal: signalProc, reported: map[string]bool{},
		event: func(_ string, f map[string]any) { mu.Lock(); kinds = append(kinds, f["kind"].(string)); mu.Unlock() }}
	time.Sleep(200 * time.Millisecond) // let sh fork its sleep
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
		list: listProcs(marker), signal: signalProc, reported: map[string]bool{},
		event: func(_ string, f map[string]any) { mu.Lock(); kinds = append(kinds, f["kind"].(string)); mu.Unlock() }}
	time.Sleep(200 * time.Millisecond)
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
