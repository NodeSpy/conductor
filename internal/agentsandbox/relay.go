package agentsandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Relays: an agent whose model endpoint is a HOST loopback service (a local
// router at 127.0.0.1:3456) has no route to it from a sandbox with no network
// of its own. The in-sandbox forwarder listens on the same address and pipes
// each connection to a unix socket served here, on the host, which dials the
// target. Only loopback targets are relayed.

// SocketDir makes a private directory for unix sockets with a SHORT path: a
// socket path is capped at 104 bytes on macOS (108 on Linux), and a temp dir
// under a deep worktree or macOS's /var/folders/… can exceed it.
func SocketDir(prefix string) (string, error) {
	base := os.TempDir()
	if len(base) > 32 {
		base = "/tmp"
	}
	return os.MkdirTemp(base, prefix)
}

// Relayer serves relay sockets from one private directory.
type Relayer struct {
	dir string

	mu  sync.Mutex
	n   int
	lns []net.Listener
}

// NewRelayer makes the relay socket directory.
func NewRelayer() (*Relayer, error) {
	dir, err := SocketDir("csr")
	if err != nil {
		return nil, fmt.Errorf("agentsandbox: relay dir: %w", err)
	}
	return &Relayer{dir: dir}, nil
}

// Relay opens a socket piped to target and returns its path and a close func.
func (r *Relayer) Relay(target string) (string, func(), error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", nil, err
	}
	if !isLoopbackHost(host) {
		return "", nil, fmt.Errorf("relay: %s is not a loopback address", target)
	}
	r.mu.Lock()
	r.n++
	name := fmt.Sprintf("r%d-%s.sock", r.n, strings.NewReplacer(":", "_", ".", "_", "[", "", "]", "").Replace(host+"_"+port))
	r.mu.Unlock()
	sock := filepath.Join(r.dir, name)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", nil, err
	}
	_ = os.Chmod(sock, 0o600)
	r.mu.Lock()
	r.lns = append(r.lns, ln)
	r.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go pipe(c, target)
		}
	}()
	var once sync.Once
	return sock, func() { once.Do(func() { ln.Close(); _ = os.Remove(sock) }) }, nil
}

// Close stops every relay and removes the directory.
func (r *Relayer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ln := range r.lns {
		ln.Close()
	}
	r.lns = nil
	_ = os.RemoveAll(r.dir)
}

func pipe(c net.Conn, target string) {
	defer c.Close()
	u, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer u.Close()
	go func() {
		_, _ = io.Copy(u, c)
		if cw, ok := u.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	// The target's side ending ends the connection (both closed on return).
	_, _ = io.Copy(c, u)
}
