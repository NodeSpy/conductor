package dispatch

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/memory"
)

// The remote skill back-channel (#36 §12). A skill-enabled agent dispatched to
// a remote paseo runtime (host:) runs on another box and can't see the daemon's
// local tool socket. Rather than expose conductor on a public URL, we ride the
// SAME SSH trust conductor already uses to launch paseo there: a persistent
// `ssh -R <remote.sock>:<daemon.sock>` reverse tunnel forwards the daemon socket
// onto the remote box, bound to a 0600 unix socket. The remote agent's
// `conductor` CLI dials that forwarded socket exactly as a local agent dials the
// real one — internal wiring over a channel only conductor can open, nothing
// world-facing.
//
// paseo `run --background` returns immediately (the agent keeps running under
// paseo's own remote daemon), so the tunnel can't ride the run invocation — it
// is a daemon-lifetime process this manager supervises and restarts on drop.

const (
	tunnelReadyMarker  = "__CONDUCTOR_TUNNEL_READY__"
	tunnelReadyCmd     = "echo " + tunnelReadyMarker + "; exec sleep 2147483647"
	tunnelReadyTimeout = 10 * time.Second
	tunnelMaxBackoff   = 30 * time.Second
)

var skillTunnels = &tunnelMgr{
	tunnels: map[string]*skillTunnel{},
	newCmd: func(ctx context.Context, argv []string) *exec.Cmd {
		return exec.CommandContext(ctx, argv[0], argv[1:]...)
	},
}

type tunnelMgr struct {
	mu      sync.Mutex
	ctx     context.Context // daemon lifetime; governs every tunnel's supervisor
	log     func(string, ...any)
	tunnels map[string]*skillTunnel
	newCmd  func(ctx context.Context, argv []string) *exec.Cmd // exec seam for tests
}

type skillTunnel struct {
	remoteSock string
	readyOnce  sync.Once
	ready      chan struct{}
}

func (t *skillTunnel) signalReady() { t.readyOnce.Do(func() { close(t.ready) }) }

// InitSkillTunnels wires the daemon lifetime + logger (boot). Until called,
// ensure() supervises under context.Background() and logs nowhere.
func InitSkillTunnels(ctx context.Context, log func(string, ...any)) {
	skillTunnels.mu.Lock()
	skillTunnels.ctx = ctx
	skillTunnels.log = log
	skillTunnels.mu.Unlock()
}

func (m *tunnelMgr) logf(format string, a ...any) {
	m.mu.Lock()
	log := m.log
	m.mu.Unlock()
	if log != nil {
		log(format, a...)
	}
}

// ensure returns the remote-side socket path of a live reverse tunnel to
// target, forwarding localSock, starting and supervising one on first use. It
// blocks until the tunnel reports ready (a marker on the far side's stdout) or
// a short timeout elapses; ok=false means no usable tunnel right now (the
// supervisor keeps retrying, so a later dispatch may succeed).
func (m *tunnelMgr) ensure(target hosts.Target, localSock string) (remoteSock string, ok bool) {
	m.mu.Lock()
	dctx := m.ctx
	if dctx == nil {
		dctx = context.Background()
	}
	key := hostKey(target)
	t := m.tunnels[key]
	fresh := t == nil
	if fresh {
		t = &skillTunnel{remoteSock: remoteSockPath(localSock), ready: make(chan struct{})}
		m.tunnels[key] = t
	}
	m.mu.Unlock()

	if fresh {
		go m.supervise(dctx, target, localSock, t)
	}
	select {
	case <-t.ready:
		return t.remoteSock, true
	case <-time.After(tunnelReadyTimeout):
		return "", false
	case <-dctx.Done():
		return "", false
	}
}

// supervise runs the reverse-tunnel ssh process for the daemon's lifetime,
// signalling ready when the far side prints the marker and restarting the
// process (with capped backoff) whenever it drops.
func (m *tunnelMgr) supervise(ctx context.Context, target hosts.Target, localSock string, t *skillTunnel) {
	backoff := time.Second
	for ctx.Err() == nil {
		argv := (&hosts.Client{}).ReverseForwardArgs(target, t.remoteSock, localSock, tunnelReadyCmd)
		cmd := m.newCmd(ctx, argv)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.logf("skill: reverse tunnel to %s: stdout pipe: %v", hostLabel(target), err)
			return
		}
		if err := cmd.Start(); err != nil {
			m.logf("skill: reverse tunnel to %s: start: %v", hostLabel(target), err)
		} else {
			go func() {
				sc := bufio.NewScanner(stdout)
				for sc.Scan() {
					if strings.Contains(sc.Text(), tunnelReadyMarker) {
						t.signalReady()
						return
					}
				}
			}()
			_ = cmd.Wait()
		}
		if ctx.Err() != nil {
			return
		}
		m.logf("skill: reverse tunnel to %s dropped — restarting", hostLabel(target))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < tunnelMaxBackoff {
			backoff *= 2
		}
	}
}

// remoteSkillEndpoint returns the `unix://` endpoint a remote agent should use
// to reach conductor — an SSH-reverse-forwarded copy of the daemon socket on the
// agent's box — or "" when there's no daemon socket to forward or the tunnel
// isn't up. The tunnel outlives this dispatch (it's supervised under the daemon
// ctx), so the per-request ctx only bounds how long we wait for readiness.
func (d *Dispatcher) remoteSkillEndpoint(_ context.Context, _ Request) string {
	if d.Remote == nil {
		return ""
	}
	local := socketFromToolCommand(memory.ToolCommand())
	if local == "" {
		return ""
	}
	sock, ok := skillTunnels.ensure(*d.Remote, local)
	if !ok {
		return ""
	}
	return "unix://" + sock
}

// hostKey identifies a remote target for tunnel reuse (one tunnel per host).
func hostKey(t hosts.Target) string {
	return fmt.Sprintf("%s@%s:%d", t.Cfg.User, t.Cfg.Host, t.Cfg.Port)
}

// hostLabel is a human name for logs (the configured name, else the address).
func hostLabel(t hosts.Target) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Cfg.Host
}

// remoteSockPath is a stable remote socket path per daemon (derived from the
// local socket path), so a restarted tunnel rebinds the same path and
// StreamLocalBindUnlink clears any stale one.
func remoteSockPath(localSock string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(localSock))
	return fmt.Sprintf("/tmp/conductor-skill-%08x.sock", h.Sum32())
}
