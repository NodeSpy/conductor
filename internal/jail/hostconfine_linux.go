//go:build linux

package jail

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// buildConfined builds the host-side jail a content-executing host command
// runs in (`terraform plan` running the workspace's providers and data
// sources, `docker build` its Dockerfile). Unlike buildCOW's view — the host
// as the operator sees it, minus the rest of $HOME — this is an allow-list
// root, the same pivot_root construction as the agent's own jail:
//
//	/usr /etc /opt …  the base system, read-only
//	$HOME             the copy-on-write view of the tool's own config paths
//	workspace         an overlay: reads see the workspace, writes land in the
//	                  dispatch's per-tool upper dir (WsUpper) — never in the
//	                  workspace itself
//	/tmp              the dispatch's scratch dir
//	the binary        its install dirs, read-only
//	sockets           only those named (the Docker daemon's, for docker)
//
// Nothing else exists: not the operator's session sockets (/run/user/<uid>:
// ssh-agent, gpg-agent, D-Bus), not another tool's daemon socket, not
// conductor's state. Its network namespace is empty but for the forwarder to
// the egress proxy (the caller always restricts a confined run's network),
// and it runs with no stdin and no controlling terminal.
func buildConfined(hr hostRun) (string, error) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return "", fmt.Errorf("make-rprivate: %w", err)
	}
	if hr.EgressSock == "" {
		return "", fmt.Errorf("a confined host command needs its network restricted")
	}
	efd, err := openPath(filepath.Dir(hr.EgressSock))
	if err != nil {
		return "", fmt.Errorf("egress socket: %w", err)
	}
	shome, layers, err := buildHomeLayers(hr)
	if err != nil {
		return "", err
	}
	binds := []sandbox.BindMount{
		{Path: hr.Home, Src: shome},
		{Path: "/tmp", Src: hr.TmpDir},
	}
	for _, l := range layers {
		binds = append(binds, sandbox.BindMount{Path: filepath.Join(hr.Home, l.rel), Src: filepath.Join(hr.Scratch, l.merged)})
	}
	if hr.Workspace != "" {
		if hr.WsUpper == "" || hr.WsWork == "" {
			return "", fmt.Errorf("no copy-on-write dir for the workspace")
		}
		merged := filepath.Join(hr.Scratch, "ws")
		if err := os.MkdirAll(merged, 0o700); err != nil {
			return "", err
		}
		opts := "lowerdir=" + ovlEscape(hr.Workspace) + ",upperdir=" + ovlEscape(hr.WsUpper) + ",workdir=" + ovlEscape(hr.WsWork)
		if err := unix.Mount("overlay", merged, "overlay", 0, opts); err != nil {
			return "", fmt.Errorf("workspace overlay: %w", err)
		}
		binds = append(binds, sandbox.BindMount{Path: hr.Workspace, Src: merged})
	}
	for _, p := range hr.BinRoots {
		if !visibleInBase(p) {
			binds = append(binds, sandbox.BindMount{Path: p, RO: true, Optional: true})
		}
	}
	// The binary's own path when it is a symlink (~/bin/terraform →
	// ~/.terraform.versions/…): BinRoots holds the resolved install, so
	// recreate each hop, exec'ing it as named (argv[0] intact).
	binds = append(binds, symlinkHops(hr.Bin)...)
	for _, s := range hr.Sockets {
		binds = append(binds, sandbox.BindMount{Path: s, RO: true, Optional: true})
	}
	if err := sandbox.BuildJail(binds); err != nil {
		return "", err
	}
	return fdPath(efd, filepath.Base(hr.EgressSock)), nil
}

// symlinkHops are the symlinks on the way from bin to its real file, as
// jail entries that recreate them (outside the base system).
func symlinkHops(bin string) []sandbox.BindMount {
	var out []sandbox.BindMount
	p := bin
	for hop := 0; hop < 8; hop++ {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			break
		}
		t, err := os.Readlink(p)
		if err != nil {
			break
		}
		if !visibleInBase(p) {
			out = append(out, sandbox.BindMount{Path: p, Link: t})
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		p = filepath.Clean(t)
	}
	return out
}
