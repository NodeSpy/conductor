package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
)

// agentContainers builds the daemon's container backend (isolation `mode:
// container`). The engine is found on first use, so a daemon whose config
// runs no agent in a container never touches one; a config that does gets
// it found — and this daemon's containers reconciled — at boot, with a
// warning when there is none (each such launch is then refused).
func agentContainers(cfg *config.Config, stateRoot string, logf func(string, ...any)) *agentsandbox.Containers {
	sc := &config.SandboxContainer{}
	if cfg.Sandbox != nil && cfg.Sandbox.Container != nil {
		sc = cfg.Sandbox.Container
	}
	sum := sha256.Sum256([]byte(stateRoot))
	c := &agentsandbox.Containers{
		Root:     filepath.Join(stateRoot, ".containers"),
		DaemonID: hex.EncodeToString(sum[:])[:12],
		UID:      os.Getuid(), GID: os.Getgid(),
		IdleStop: 30 * time.Minute,
		Proxy: func(allow []string) (string, string, func(), error) {
			if controller.EgressProxyUnix == nil {
				return "", "", nil, fmt.Errorf("no egress proxy in this daemon")
			}
			return controller.EgressProxyUnix(allow)
		},
		Defaults: agentsandbox.Container{Image: sc.Image, ReadOnlyRoot: sc.ReadOnly, IdleStop: sc.IdleStop},
		Logf:     logf,
	}
	if sc.IdleStop != "" {
		if d, err := time.ParseDuration(sc.IdleStop); err == nil {
			c.IdleStop = d
		}
	}
	if l := sc.Limits; l != nil {
		c.Defaults.Memory, c.Defaults.CPU, c.Defaults.Pids = l.Memory, l.CPU, l.Pids
	}
	engine, helper := sc.Engine, sc.Helper
	c.Resolve = func() (agentsandbox.Engine, error) {
		e, err := agentsandbox.ResolveEngine(engine, runtime.GOOS, exec.LookPath, os.Environ())
		if err != nil {
			return nil, err
		}
		if e.Kind() == "podman" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			out, _ := e.Output(ctx, "info", "--format", "{{.Host.Security.Rootless}}")
			cancel()
			c.KeepID = strings.TrimSpace(string(out)) == "true"
		}
		dir, inImage, err := containerHelper(c.Root, helper)
		if err != nil {
			return nil, err
		}
		c.Helper, c.HelperInImage = dir, inImage
		return e, nil
	}
	if usesAgentContainers(cfg) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := c.Start(ctx); err != nil {
				logf("WARNING: agent containers: %v — every agent launch configured for a container will be REFUSED until one is installed", err)
				return
			}
			logf("agent containers: engine %s (%s), this daemon's containers labelled %s=%s", c.Engine.Bin(), c.Engine.Kind(), agentsandbox.LabelDaemon, c.DaemonID)
		}()
	}
	return c
}

// usesAgentContainers reports a config that runs some agent in a container
// (by default, or on a runtime): the backend is readied at boot.
func usesAgentContainers(cfg *config.Config) bool {
	if s := cfg.Sandbox; s != nil && (s.Backend == "container" || s.Container != nil) {
		return true
	}
	for _, cc := range cfg.MergedControllers() {
		if cc.Isolation != nil && cc.Isolation.Mode == "container" {
			return true
		}
	}
	return false
}

// containerHelper is where a container gets conductor's binary: a Linux
// build — this one on Linux, sandbox.container.helper where conductor is
// not one (macOS) — copied into dir (bound read-only, so a conductor update
// reaches a running container's next mux), else the image's own copy.
func containerHelper(root, configured string) (dir, inImage string, err error) {
	src := configured
	if src == "" {
		if runtime.GOOS != "linux" {
			return "", "/usr/local/bin/conductor", nil
		}
		if src, err = os.Executable(); err != nil {
			return "", "", err
		}
	}
	if err := checkStaticLinux(src); err != nil {
		return "", "", err
	}
	dir = filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.Chmod(root, 0o755); err != nil {
		return "", "", err
	}
	dst := filepath.Join(dir, "conductor")
	want, err := os.ReadFile(src)
	if err != nil {
		return "", "", fmt.Errorf("agent containers: read conductor's binary %s: %w", src, err)
	}
	if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, want) {
		return dir, "", nil
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, want, 0o755); err != nil {
		return "", "", err
	}
	return dir, "", os.Rename(tmp, dst)
}

// checkStaticLinux refuses a helper an agent image cannot run: not a Linux
// executable, or one linked against the host's C library.
func checkStaticLinux(p string) error {
	f, err := elf.Open(p)
	if err != nil {
		return fmt.Errorf("agent containers: %s is not a Linux executable (%v) — set sandbox.container.helper to a Linux build of this conductor (CGO_ENABLED=0 GOOS=linux go build ./cmd/conductor)", p, err)
	}
	defer f.Close()
	for _, ph := range f.Progs {
		if ph.Type == elf.PT_INTERP {
			return fmt.Errorf("agent containers: %s is dynamically linked — an agent image may not carry its C library; build conductor with CGO_ENABLED=0 (as its releases are), or set sandbox.container.helper", p)
		}
	}
	return nil
}

// sandboxContainersResponse answers sandbox-containers (`conductor sandbox
// ls`) and sandbox-archive (`conductor sandbox archive IDENTITY`).
func sandboxContainersResponse(ctx context.Context, conn net.Conn, req controlRequest, log func(string, ...any)) controlResponse {
	if uid, ok := agentsandbox.PeerUID(conn); ok && int(uid) != os.Getuid() {
		return controlResponse{Error: fmt.Sprintf("%s: refused for uid %d", req.Cmd, uid)}
	}
	w := controller.AgentSandbox
	if w == nil || w.Containers == nil {
		return controlResponse{Error: "the agent sandbox is off in this conductor"}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch req.Cmd {
	case "sandbox-archive":
		if req.Name == "" {
			return controlResponse{Error: "sandbox-archive: no identity"}
		}
		removed, err := w.Containers.Archive(ctx, req.Name)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		log("sandbox-archive %s: removed %s", req.Name, strings.Join(removed, ", "))
		if len(removed) == 0 {
			return controlResponse{OK: true, Msg: "nothing to remove for " + req.Name}
		}
		return controlResponse{OK: true, Msg: "removed " + strings.Join(removed, ", ")}
	default:
		list, err := w.Containers.List(ctx)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		var b strings.Builder
		for _, ci := range list {
			fmt.Fprintf(&b, "%s\t%s\t%s\tpersistent=%v\tlaunches=%d\n", ci.Name, ci.State, ci.Identity, ci.Persistent, ci.Launches)
		}
		return controlResponse{OK: true, Msg: b.String()}
	}
}

// holdContainerLease keeps a container launch's share for as long as the
// `conductor sandbox exec` that asked for it holds the control connection:
// it closes it when the agent ends — or dies, and the kernel closes it.
func holdContainerLease(conn net.Conn, release func()) {
	_ = conn.SetDeadline(time.Time{})
	_, _ = io.Copy(io.Discard, conn)
	release()
}

// cmdSandboxContainers is `conductor sandbox ls` / `archive IDENTITY`.
func cmdSandboxContainers(sub string, args []string) int {
	var o sandboxExecOpts
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "--config", "--state-dir":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "conductor sandbox %s: %s needs a value\n", sub, args[i])
				return 2
			}
			v := args[i+1]
			switch args[i] {
			case "--socket":
				o.socket = v
			case "--config":
				o.config = v
			default:
				o.stateDir = v
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	req := controlRequest{Cmd: "sandbox-containers"}
	if sub == "archive" {
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: conductor sandbox archive IDENTITY")
			return 2
		}
		req = controlRequest{Cmd: "sandbox-archive", Name: rest[0]}
	}
	resp, err := controlCall(sandboxControlSocket(o), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox %s: %v\n", sub, err)
		return 1
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "conductor sandbox %s: %s\n", sub, resp.Error)
		return 1
	}
	fmt.Print(resp.Msg)
	if resp.Msg != "" && !strings.HasSuffix(resp.Msg, "\n") {
		fmt.Println()
	}
	return 0
}
