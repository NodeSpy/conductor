//go:build linux || darwin

package memory

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The memory socket names the process on its other end, on Linux and
// macOS alike: a sandboxed launch's session token is bound to its process
// tree, and a connection with no peer credentials can never spend it.
func TestPeerInfoNamesTheConnectingProcess(t *testing.T) {
	dir, err := os.MkdirTemp("", "mp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan Peer, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- Peer{}
			return
		}
		defer c.Close()
		got <- peerInfo(c)
	}()
	c, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := <-got
	if !p.Valid || p.PID != os.Getpid() || p.UID != uint32(os.Getuid()) || p.StartTime == 0 {
		t.Fatalf("peer %+v, want this process (pid %d, uid %d) with a start time", p, os.Getpid(), os.Getuid())
	}
	if p.StartTime != procStartTime(os.Getpid()) {
		t.Fatalf("start time %d is not the process's own %d", p.StartTime, procStartTime(os.Getpid()))
	}
}
