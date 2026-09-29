package jail

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Relay pipes a unix socket in d's broker dir to a loopback TCP service on
// the host — the agent's own model endpoint when it is a local router
// (ANTHROPIC_BASE_URL=http://127.0.0.1:3456). A jail with an enforced
// network has no route to the host's loopback; the in-jail forwarder listens
// on the same address and pipes to this socket, so the agent's own model
// traffic works while everything else stays behind the proxy. It returns the
// socket's path as seen inside the jail.
func (m *Manager) Relay(d *Dispatch, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", err
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return "", fmt.Errorf("relay: %s is not a loopback address", target)
	}
	name := "relay-" + strings.NewReplacer(":", "_", ".", "_").Replace(host) + "-" + port + ".sock"
	sock := filepath.Join(d.Dir, "broker", name)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	_ = os.Chmod(sock, 0o600)
	d.addCleanup(func() { ln.Close(); _ = os.Remove(sock) })
	m.emit(d, Event{Type: "egress", Status: "ok", Detail: target + " (the agent's model endpoint, relayed from the host loopback)"})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer u.Close()
				go func() { _, _ = io.Copy(u, c) }()
				_, _ = io.Copy(c, u)
			}()
		}
	}()
	if isDarwin {
		return sock, nil
	}
	return filepath.Join(BrokerDir, name), nil
}
