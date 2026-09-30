//go:build linux

package jail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// runHost runs an approved host command on Linux: conductor re-executes
// itself as `conductor host-exec` in a fresh user + mount + pid namespace
// (plus an empty network namespace when the command's network is
// restricted), where the helper builds the copy-on-write home view and execs
// the real binary as the operator's real uid. Every write the command makes
// under $HOME lands in overlay upper dirs under the scratch dir and is
// discarded (and reported) when it exits; only the profile's persist paths
// are copied back.
func runHost(ctx context.Context, m *Manager, hr hostRun, stdout, stderr io.Writer) (hostResult, error) {
	self, err := m.SelfExe()
	if err != nil {
		return hostResult{}, err
	}
	scratch, err := os.MkdirTemp(filepath.Join(m.Root), "cow-")
	if err != nil {
		return hostResult{}, err
	}
	defer removeScratch(scratch)
	hr.Scratch = scratch
	hr.UID, hr.GID = os.Getuid(), os.Getgid()
	spec := filepath.Join(scratch, "spec.json")
	b, err := json.Marshal(hr)
	if err != nil {
		return hostResult{}, err
	}
	if err := os.WriteFile(spec, b, 0o600); err != nil {
		return hostResult{}, err
	}
	cmd := exec.CommandContext(ctx, self, "host-exec", spec)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if len(hr.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(hr.Stdin)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	flags := uintptr(unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID)
	if hr.EgressSock != "" {
		flags |= unix.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  flags,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		Setsid:      true, // no controlling terminal: interactive flows cannot complete
		Pdeathsig:   syscall.SIGKILL,
	}
	runErr := cmd.Run()
	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return hostResult{}, runErr
		}
		exit = ee.ExitCode()
		if exit == cowSetupFailed {
			return hostResult{}, fmt.Errorf("copy-on-write home could not be built (see stderr)")
		}
	}
	entries := homeEntries(hr)
	res := hostResult{Exit: exit, Discarded: scanDiscarded(scratch, hr.Home, entries, hr.Persist)}
	writeBack(scratch, hr.Home, entries, hr.Persist)
	return res, nil
}

// cowSetupFailed is the helper's exit code when it could not build the view
// (the command never ran).
const cowSetupFailed = 213

// RunHostExec is the hidden `conductor host-exec <spec>` entry point, running
// as root of a fresh user namespace: build the copy-on-write view, then run
// the real binary in a nested namespace as the operator's real uid.
func RunHostExec(specPath string) int {
	b, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor host-exec: %v\n", err)
		return cowSetupFailed
	}
	var hr hostRun
	if err := json.Unmarshal(b, &hr); err != nil {
		fmt.Fprintf(os.Stderr, "conductor host-exec: %v\n", err)
		return cowSetupFailed
	}
	build := buildCOW
	if hr.Confine {
		build = buildConfined
	}
	fwd, err := build(hr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor host-exec: copy-on-write home: %v\n", err)
		return cowSetupFailed
	}
	if fwd != "" {
		if err := startForwarder(fwd); err != nil {
			fmt.Fprintf(os.Stderr, "conductor host-exec: network: %v\n", err)
			return cowSetupFailed
		}
	}
	cmd := exec.Command(hr.Bin, hr.Args...)
	cmd.Dir = hr.Cwd
	cmd.Env = hr.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A nested namespace mapping the operator's real uid onto ours: the tool
	// sees who it runs as (ssh checks key-file ownership against getuid) and
	// holds no capability over the view's mounts.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Unshareflags: syscall.CLONE_NEWUSER,
		UidMappings:  []syscall.SysProcIDMap{{ContainerID: hr.UID, HostID: 0, Size: 1}},
		GidMappings:  []syscall.SysProcIDMap{{ContainerID: hr.GID, HostID: 0, Size: 1}},
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "conductor host-exec: %v\n", err)
		return 127
	}
	return 0
}

// fdPath is a path that reaches a directory through an O_PATH descriptor,
// so it survives the directory being hidden (the scratch dir and the
// workspace live under $HOME, which the view replaces).
func fdPath(fd int, rel string) string {
	p := "/proc/self/fd/" + strconv.Itoa(fd)
	if rel != "" {
		p += "/" + rel
	}
	return p
}

func openPath(p string) (int, error) {
	return unix.Open(p, unix.O_PATH|unix.O_CLOEXEC, 0)
}

// buildCOW builds the host command's mount view:
//
//	$HOME      a scratch dir holding copies of top-level files and, for each
//	           visible directory, an overlay whose lower layer is the real
//	           directory and whose upper layer is under the scratch dir —
//	           reads see the real config, writes are discarded
//	workspace  the real workspace, read-write
//	/tmp       the dispatch's scratch dir (the same /tmp the jail sees)
//	/proc      this pid namespace's own
//
// The daemon's state/config dirs are hidden. It returns the egress socket
// path (reachable after /tmp moves) when the network is restricted.
func buildCOW(hr hostRun) (string, error) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return "", fmt.Errorf("make-rprivate: %w", err)
	}
	sfd, err := openPath(hr.Scratch)
	if err != nil {
		return "", err
	}
	wsfd := -1
	if hr.Workspace != "" {
		if wsfd, err = openPath(hr.Workspace); err != nil {
			return "", fmt.Errorf("workspace: %w", err)
		}
	}
	tfd, err := openPath(hr.TmpDir)
	if err != nil {
		return "", fmt.Errorf("tmp: %w", err)
	}
	efd := -1
	if hr.EgressSock != "" {
		if efd, err = openPath(filepath.Dir(hr.EgressSock)); err != nil {
			return "", fmt.Errorf("egress socket: %w", err)
		}
	}
	var binFDs []int
	var binRoots []string
	for _, p := range hr.BinRoots {
		// Only roots the view is about to hide need a handle kept.
		if !within(p, hr.Home) && !within(p, "/tmp") {
			continue
		}
		fd, err := openPath(p)
		if err != nil {
			continue
		}
		binFDs = append(binFDs, fd)
		binRoots = append(binRoots, p)
	}

	shome, layers, err := buildHomeLayers(hr)
	if err != nil {
		return "", err
	}
	// Swap the view in. From here on hr.Home shows the scratch home, and the
	// scratch dir is reached only through its descriptor.
	if err := unix.Mount(shome, hr.Home, "", unix.MS_BIND, ""); err != nil {
		return "", fmt.Errorf("bind home: %w", err)
	}
	for _, l := range layers {
		if err := unix.Mount(fdPath(sfd, l.merged), filepath.Join(hr.Home, l.rel), "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return "", fmt.Errorf("bind %s: %w", l.rel, err)
		}
	}
	return finishCOW(hr, tfd, wsfd, efd, binFDs, binRoots)
}

type homeLayer struct{ rel, merged string }

// buildHomeLayers builds the copy-on-write home in the scratch dir: copies of
// the view's top-level files and, for each directory, an overlay whose lower
// layer is the real directory (merged under scratch/merged/<n>).
func buildHomeLayers(hr hostRun) (string, []homeLayer, error) {
	shome := filepath.Join(hr.Scratch, "home")
	if err := os.MkdirAll(shome, 0o700); err != nil {
		return "", nil, err
	}
	entries := homeEntries(hr)
	type layer = homeLayer
	var layers []layer
	for i, rel := range entries {
		real := filepath.Join(hr.Home, rel)
		fi, err := os.Lstat(real)
		if err != nil {
			continue
		}
		dst := filepath.Join(shome, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", nil, err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, _ := os.Readlink(real)
			_ = os.Symlink(t, dst)
		case fi.IsDir():
			n := itoa(i)
			up, wk, merged := filepath.Join(hr.Scratch, "up", n), filepath.Join(hr.Scratch, "wk", n), filepath.Join(hr.Scratch, "merged", n)
			for _, d := range []string{up, wk, merged, dst} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return "", nil, err
				}
			}
			opts := "lowerdir=" + ovlEscape(real) + ",upperdir=" + ovlEscape(up) + ",workdir=" + ovlEscape(wk)
			if err := unix.Mount("overlay", merged, "overlay", 0, opts); err != nil {
				// A directory with a mount underneath it (an NFS share, a
				// FUSE mount) cannot be an overlay lower layer inside a user
				// namespace: show it read-only instead.
				if berr := unix.Mount(real, merged, "", unix.MS_BIND|unix.MS_REC, ""); berr != nil {
					return "", nil, fmt.Errorf("overlay %s: %v; bind: %w", rel, err, berr)
				}
				_ = unix.Mount("", merged, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|lockedFlags(real), "")
			}
			layers = append(layers, layer{rel: rel, merged: filepath.Join("merged", n)})
		case fi.Mode().IsRegular():
			if err := copyFile(real, dst, fi.Mode().Perm()); err != nil {
				return "", nil, err
			}
		}
	}
	return shome, layers, nil
}

// finishCOW completes the unconfined host-command view once the home is in
// place: the daemon's dirs hidden, /tmp, the binary, the workspace, /proc.
func finishCOW(hr hostRun, tfd, wsfd, efd int, binFDs []int, binRoots []string) (string, error) {
	for _, s := range hr.Sensitive {
		if fi, err := os.Stat(s); err == nil && fi.IsDir() {
			if err := unix.Mount("tmpfs", s, "tmpfs", 0, "size=64k,mode=0700"); err != nil {
				return "", fmt.Errorf("hide %s: %w", s, err)
			}
		}
	}
	// /tmp first: the binary or the workspace may themselves live under it.
	if err := unix.Mount(fdPath(tfd, ""), "/tmp", "", unix.MS_BIND, ""); err != nil {
		return "", fmt.Errorf("tmp: %w", err)
	}
	for i, p := range binRoots {
		if err := bindAt(fdPath(binFDs[i], ""), p, true); err != nil {
			return "", fmt.Errorf("binary %s: %w", p, err)
		}
	}
	if wsfd >= 0 {
		if err := bindAt(fdPath(wsfd, ""), hr.Workspace, false); err != nil {
			return "", fmt.Errorf("workspace: %w", err)
		}
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return "", fmt.Errorf("proc: %w", err)
	}
	if efd >= 0 {
		return fdPath(efd, filepath.Base(hr.EgressSock)), nil
	}
	return "", nil
}

// bindAt binds src over p, creating the mountpoint (inside the view) first.
func bindAt(src, p string, ro bool) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if f, err := os.OpenFile(p, os.O_CREATE, 0o600); err == nil {
			f.Close()
		}
	}
	if err := unix.Mount(src, p, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	if ro {
		return unix.Mount("", p, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|lockedFlags(p), "")
	}
	return nil
}

// lockedFlags: see sandbox's — an inherited mount's nosuid/nodev/noexec are
// locked in a user namespace and must be repeated on a remount.
func lockedFlags(p string) uintptr {
	var st unix.Statfs_t
	if unix.Statfs(p, &st) != nil {
		return 0
	}
	var f uintptr
	if int64(st.Flags)&unix.ST_NOSUID != 0 {
		f |= unix.MS_NOSUID
	}
	if int64(st.Flags)&unix.ST_NODEV != 0 {
		f |= unix.MS_NODEV
	}
	if int64(st.Flags)&unix.ST_NOEXEC != 0 {
		f |= unix.MS_NOEXEC
	}
	return f
}

// ovlEscape escapes the characters overlayfs treats specially in a layer
// path.
func ovlEscape(p string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`, `:`, `\:`).Replace(p)
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// startForwarder brings loopback up in the command's empty network namespace
// and forwards the proxy address into conductor's egress proxy.
func startForwarder(sock string) error {
	if err := loopbackUp(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("unix", sock)
				if err != nil {
					return
				}
				defer u.Close()
				go func() { _, _ = io.Copy(u, c) }()
				_, _ = io.Copy(c, u)
			}()
		}
	}()
	return nil
}

func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}
