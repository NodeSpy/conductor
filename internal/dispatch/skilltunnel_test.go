package dispatch

import (
	"context"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hosts"
)

func TestReverseForwardArgs(t *testing.T) {
	tgt := hosts.Target{Cfg: config.HostConfig{Host: "build-box", User: "ci", Port: 2222, Key: "/k", KnownHosts: "/kh"}}
	argv := (&hosts.Client{}).ReverseForwardArgs(tgt, "/tmp/r.sock", "/run/d.sock", "echo hi")
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"ExitOnForwardFailure=yes",   // a failed forward must surface, not run silently
		"StreamLocalBindUnlink=yes",  // clear a stale remote socket
		"StreamLocalBindMask=0177",   // remote socket 0600
		"-R /tmp/r.sock:/run/d.sock", // the reverse forward itself
		"-p 2222", "-i /k",           // per-target connection flags
		"UserKnownHostsFile=/kh",  // pinned known_hosts
		"-- ci@build-box echo hi", // user@host then the readiness command
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n got: %s", want, joined)
		}
	}
}

func TestRemoteSockPathStable(t *testing.T) {
	a := remoteSockPath("/run/conductor/memory.sock")
	if a != remoteSockPath("/run/conductor/memory.sock") {
		t.Fatal("remoteSockPath must be deterministic per local socket")
	}
	if a == remoteSockPath("/run/other/memory.sock") {
		t.Fatal("distinct local sockets must map to distinct remote paths")
	}
	if !strings.HasPrefix(a, "/tmp/conductor-skill-") || !strings.HasSuffix(a, ".sock") {
		t.Fatalf("unexpected remote sock path %q", a)
	}
}

func TestTunnelMgrEnsureReuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var started int32
	m := &tunnelMgr{
		ctx:     ctx,
		tunnels: map[string]*skillTunnel{},
		newCmd: func(ctx context.Context, _ []string) *exec.Cmd {
			atomic.AddInt32(&started, 1)
			// A stand-in for the ssh tunnel: print the readiness marker, then
			// stay alive like the real `-R … sleep` session.
			return exec.CommandContext(ctx, "sh", "-c", "echo "+tunnelReadyMarker+"; sleep 30")
		},
	}
	tgt := hosts.Target{Cfg: config.HostConfig{Host: "build-box", User: "ci"}}

	sock, ok := m.ensure(tgt, "/run/d.sock")
	if !ok || sock == "" {
		t.Fatalf("first ensure: ok=%v sock=%q", ok, sock)
	}
	// A second ensure for the same host reuses the live tunnel — no new process.
	sock2, ok2 := m.ensure(tgt, "/run/d.sock")
	if !ok2 || sock2 != sock {
		t.Fatalf("second ensure should reuse: ok=%v sock=%q (want %q)", ok2, sock2, sock)
	}
	if n := atomic.LoadInt32(&started); n != 1 {
		t.Fatalf("expected exactly one tunnel process, started %d", n)
	}

	// A different host gets its own tunnel.
	other := hosts.Target{Cfg: config.HostConfig{Host: "other-box", User: "ci"}}
	if _, ok := m.ensure(other, "/run/d.sock"); !ok {
		t.Fatal("ensure for a second host should start its own tunnel")
	}
	if n := atomic.LoadInt32(&started); n != 2 {
		t.Fatalf("expected two tunnel processes across two hosts, started %d", n)
	}
}
