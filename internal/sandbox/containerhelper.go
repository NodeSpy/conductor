package sandbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// The in-container half of an agent container, run as `conductor
// sandbox-net --init | --mux | --seed | --run | --signal` from the helper
// conductor binds read-only at ContainerHelperDir.

// RunContainerInit is the container's PID 1: it reaps orphans and exits on
// SIGTERM/SIGINT (`docker stop`). It needs nothing from the image.
func RunContainerInit() int {
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for s := range sigs {
		switch s {
		case syscall.SIGCHLD:
			for {
				var ws syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
		default:
			return 0
		}
	}
	return 0
}

// SeedHome fills dst (a fresh HOME volume) from src (the identity home
// conductor prepared), copying what dst does not have yet, and gives dst and
// everything copied to uid:gid. Symlinks are copied as links, never
// followed.
func SeedHome(src, dst string, uid, gid int) error {
	if uid <= 0 {
		return fmt.Errorf("seed: refusing to give a HOME to uid %d", uid)
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if rel == "." {
			return os.Lchown(dst, uid, gid)
		}
		if _, err := os.Lstat(to); err == nil {
			if d.IsDir() {
				return nil
			}
			return nil // the volume's own copy stands
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.Mkdir(to, info.Mode().Perm()|0o700); err != nil {
				return err
			}
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, to); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyRegular(p, to, info.Mode().Perm()); err != nil {
				return err
			}
		default:
			return nil // no device, fifo or socket is seeded
		}
		return os.Lchown(to, uid, gid)
	})
	if err != nil {
		return fmt.Errorf("seed %s: %w", dst, err)
	}
	return nil
}

func copyRegular(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var launchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func launchPidFile(id string) (string, error) {
	if !launchIDRe.MatchString(id) {
		return "", fmt.Errorf("launch id %q", id)
	}
	return filepath.Join(ContainerSockDir, "run-"+id+".pid"), nil
}

// RunContainerLaunch runs argv as one launch in the container: its own
// process group, recorded under id so the host can signal it (`docker exec`
// forwards no signal), and the launch's exit status.
func RunContainerLaunch(id string, argv []string) int {
	pidf, err := launchPidFile(id)
	if err != nil || len(argv) == 0 {
		fmt.Fprintf(os.Stderr, "sandbox-net: --run: %v\n", err)
		return 2
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox-net: %s: %v\n", argv[0], err)
		return 127
	}
	_ = os.WriteFile(pidf, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	defer os.Remove(pidf)
	go func() {
		for s := range sigs {
			_ = syscall.Kill(-cmd.Process.Pid, s.(syscall.Signal))
		}
	}()
	err = cmd.Wait()
	// The launch is over: nothing it started outlives it.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

// SignalContainerLaunch sends sig (a name: TERM, KILL, …) to launch id's
// process group.
func SignalContainerLaunch(id, sig string) int {
	pidf, err := launchPidFile(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox-net: --signal: %v\n", err)
		return 2
	}
	b, err := os.ReadFile(pidf)
	if err != nil {
		return 0 // already gone
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return 2
	}
	s := map[string]syscall.Signal{"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP, "QUIT": syscall.SIGQUIT}[strings.TrimPrefix(strings.ToUpper(sig), "SIG")]
	if s == 0 {
		fmt.Fprintf(os.Stderr, "sandbox-net: --signal: unknown signal %q\n", sig)
		return 2
	}
	_ = syscall.Kill(-pid, s)
	return 0
}
