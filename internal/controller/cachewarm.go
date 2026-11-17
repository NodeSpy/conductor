package controller

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/repocache"
)

// RunCacheWarm runs a repository cache's warm command (repocache.WarmRun)
// under the agent sandbox, as an agent of its own: the default-branch
// checkout read-write as its workspace, a fresh home, the operator's
// egress, none of conductor's or the operator's credentials. Conductor then
// publishes what it left at the cache's paths.
func RunCacheWarm(ctx context.Context, w repocache.WarmRun) error {
	sw := AgentSandbox
	if sw == nil || sw.Resolver == nil {
		return fmt.Errorf("cache warm: the agent sandbox is off")
	}
	pol := sw.Resolver.WarmPolicy(w.Workspace, w.Home, "cache-warm:"+w.Repo)
	pol.Env = append(pol.Env, w.Env...)
	launch, err := agentsandbox.Wrap(pol, []string{"sh", "-c", w.Command}, agentsandbox.Deps{
		SelfExe:           launchSelfExe,
		EgressUnix:        EgressProxyUnix,
		EgressAddr:        EgressProxyFor,
		EgressUnixSession: EgressProxyUnixSession,
		EgressAddrSession: EgressProxyForSession,
		Event:             SandboxEvent,
		Relay:             sw.Relay,
		LookPath:          exec.LookPath,
		RealHome:          sw.Resolver.RealHome,
		Inherited:         os.Environ(),
	})
	if err != nil {
		return fmt.Errorf("cache warm: %w", err)
	}
	defer launch.Cleanup()
	cmd := exec.Command(launch.Argv[0], launch.Argv[1:]...)
	cmd.Env, cmd.Dir = launch.Env, launch.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &tailBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cache warm: %w", err)
	}
	agentsandbox.LaunchStarted(launch.Env, cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// The scratch is removed (launch.Cleanup) only once the sandbox
		// that writes it is gone; WaitDelay keeps a held pipe from
		// stalling that past the process's own exit.
		<-done
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	b   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if over := t.b.Len() - t.max; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.b.String() }
