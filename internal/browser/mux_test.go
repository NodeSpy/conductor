//go:build linux || darwin

package browser

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMuxWithTheShim runs the real shim natively, a shell standing in for
// Chromium (it echoes the DevTools pipe), and drives every channel.
func TestMuxWithTheShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "cbshim")
	if out, err := exec.Command("go", "build", "-o", shim, "./containershim").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The proxy the host side dials.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				l, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "proxy saw: "+l)
			}(c)
		}
	}()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	listen := l.Addr().String()
	l.Close()
	xfer := filepath.Join(dir, "xfer")
	cmd := exec.Command(shim, "--listen", listen, "--xfer", xfer, "--", "sh", "-c", "cat <&3 >&4")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	m := NewMux(stdout, stdin, func() (net.Conn, error) { return net.Dial("tcp", up.Addr().String()) })
	w, r := m.CDP()
	if _, err := w.Write([]byte("{\"id\":1}\x00")); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(r).ReadString(0)
	if err != nil || got != "{\"id\":1}\x00" {
		t.Fatalf("cdp echo: %q %v", got, err)
	}
	// A proxy connection, as Chromium makes one.
	var c net.Conn
	for i := 0; i < 50; i++ {
		if c, err = net.Dial("tcp", listen); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(c, "CONNECT x\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if line != "proxy saw: CONNECT x\n" {
		t.Fatalf("proxy channel: %q", line)
	}
	// Files in and out of the transfer directory, and only there.
	if err := m.Put(filepath.Join(xfer, "up", "a.txt"), strings.NewReader("upload body")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(xfer, "up", "a.txt")); string(b) != "upload body" {
		t.Fatalf("put: %q", b)
	}
	if err := m.Put(filepath.Join(xfer, "up", "a.txt"), strings.NewReader("again")); err == nil {
		t.Fatal("a put over an existing file")
	}
	var buf bytes.Buffer
	if n, err := m.Get(filepath.Join(xfer, "up", "a.txt"), 100, &buf); err != nil || n != 11 || buf.String() != "upload body" {
		t.Fatalf("get: %d %v %q", n, err, buf.String())
	}
	if _, err := m.Get(filepath.Join(xfer, "up", "a.txt"), 5, io.Discard); err == nil {
		t.Fatal("a get over the limit")
	}
	os.WriteFile(filepath.Join(dir, "secret"), []byte("S"), 0o600)
	os.Symlink(filepath.Join(dir, "secret"), filepath.Join(xfer, "link"))
	for _, p := range []string{filepath.Join(dir, "secret"), filepath.Join(xfer, "..", "secret"), filepath.Join(xfer, "link")} {
		if _, err := m.Get(p, 100, io.Discard); err == nil {
			t.Fatalf("get %s", p)
		}
	}
	if err := m.Put(filepath.Join(xfer, "..", "planted"), strings.NewReader("x")); err == nil {
		t.Fatal("a put outside")
	}
	// The stream ends: so does the shim (and its browser).
	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the shim outlived its stream")
	}
}
