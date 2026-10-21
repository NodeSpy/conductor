//go:build unix

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The browser's workspace files are opened component by component with
// O_NOFOLLOW, so NO symlink anywhere in the path is ever followed. checkRel
// rejects the literal .git / carve-out / "../" names; this ensures the path the
// kernel actually opens is that same literal path — a symlinked component (even
// one swapped in to race checkRel) cannot redirect the open into .git, a
// read-only carve-out, or outside the workspace. It is the race-free equivalent
// of os.Root's escape protection, extended to the intra-workspace carve-outs.

// wsComps splits a cleaned relative path into components (no "", ".", "..").
func wsComps(rel string) []string {
	var out []string
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c != "" && c != "." && c != ".." {
			out = append(out, c)
		}
	}
	return out
}

// openWorkspaceFile opens ws/rel with flag/perm, refusing every symlink in the
// path. rel must already have passed checkRel.
func openWorkspaceFile(ws, rel string, flag int, perm os.FileMode) (*os.File, error) {
	comps := wsComps(rel)
	if len(comps) == 0 {
		return nil, fmt.Errorf("%s %w", rel, errOutside)
	}
	dfd, err := unix.Open(ws, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for i, c := range comps {
		if i == len(comps)-1 {
			fd, err := unix.Openat(dfd, c, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm))
			unix.Close(dfd)
			if err != nil {
				return nil, wsMapErr(rel, err)
			}
			return os.NewFile(uintptr(fd), filepath.Join(ws, rel)), nil
		}
		fd, err := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dfd)
		if err != nil {
			return nil, wsMapErr(rel, err)
		}
		dfd = fd
	}
	return nil, fmt.Errorf("%s %w", rel, errOutside) // unreachable
}

// mkdirAllWorkspace makes each component of relDir under ws, refusing symlinks.
func mkdirAllWorkspace(ws, relDir string) error {
	comps := wsComps(relDir)
	if len(comps) == 0 {
		return nil
	}
	dfd, err := unix.Open(ws, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	for _, c := range comps {
		if err := unix.Mkdirat(dfd, c, 0o755); err != nil && err != unix.EEXIST {
			unix.Close(dfd)
			return wsMapErr(relDir, err)
		}
		fd, err := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dfd)
		if err != nil {
			return wsMapErr(relDir, err)
		}
		dfd = fd
	}
	unix.Close(dfd)
	return nil
}

// wsMapErr turns a symlink refusal (ELOOP / ENOTDIR via O_NOFOLLOW) into the
// workspace-escape error, so a planted symlink reads the same as any other
// out-of-workspace attempt.
func wsMapErr(rel string, err error) error {
	if err == unix.ELOOP || err == unix.ENOTDIR {
		return fmt.Errorf("%s %w (a symlinked path component)", rel, errOutside)
	}
	return fmt.Errorf("%s: %w", rel, err)
}

// removeWorkspaceFile unlinks ws/rel (cleanup of a failed write), refusing
// symlinked components.
func removeWorkspaceFile(ws, rel string) error {
	comps := wsComps(rel)
	if len(comps) == 0 {
		return nil
	}
	dfd, err := unix.Open(ws, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	for i, c := range comps {
		if i == len(comps)-1 {
			err := unix.Unlinkat(dfd, c, 0)
			unix.Close(dfd)
			return err
		}
		fd, err := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dfd)
		if err != nil {
			return err
		}
		dfd = fd
	}
	return nil
}
