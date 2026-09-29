//go:build darwin

package jail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// runHost runs an approved host command on macOS (#154 §10). There is no
// mount namespace, so the copy-on-write home is built from APFS clones: the
// command's $HOME is a scratch dir holding clonefile copies of the paths its
// profile reads (writes land in the clones and are discarded, persist paths
// copied back), and a Seatbelt profile denies writes to the real home —
// except the persist paths and the workspace — for tools that find their
// home through getpwuid rather than $HOME (ssh). Reads of the real home are
// limited to the profile's own paths; conductor's state and config are
// unreadable either way.
func runHost(ctx context.Context, m *Manager, hr hostRun, stdout, stderr io.Writer) (hostResult, error) {
	scratch, err := os.MkdirTemp(m.Root, "cow-")
	if err != nil {
		return hostResult{}, err
	}
	defer removeScratch(scratch)
	shome := filepath.Join(scratch, "home")
	if err := os.MkdirAll(shome, 0o700); err != nil {
		return hostResult{}, err
	}
	entries := homeEntries(hr)
	for _, rel := range entries {
		src := filepath.Join(hr.Home, rel)
		dst := filepath.Join(shome, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		if err := cloneTree(src, dst); err != nil {
			return hostResult{}, fmt.Errorf("clone %s: %w", rel, err)
		}
	}
	prof := hostSeatbelt(hr, scratch, entries)
	env := make([]string, 0, len(hr.Env)+1)
	for _, kv := range hr.Env {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "TMPDIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+shome, "TMPDIR="+hr.TmpDir)
	if hr.EgressSock != "" {
		// The proxy env was added by the caller; the profile confines the
		// command to it (see hostSeatbelt).
	}
	cmd := exec.CommandContext(ctx, "sandbox-exec", append([]string{"-p", prof, hr.Bin}, hr.Args...)...)
	cmd.Dir = hr.Cwd
	cmd.Env = env
	if len(hr.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(hr.Stdin)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return hostResult{}, err
		}
		exit = ee.ExitCode()
	}
	// The clones sit in scratch/home: the same shape scanDiscarded and
	// writeBack read on Linux (no overlay layers here).
	res := hostResult{Exit: exit, Discarded: scanDiscarded(scratch, hr.Home, nil, hr.Persist)}
	writeBack(scratch, hr.Home, nil, hr.Persist)
	return res, nil
}

// cloneTree copies src to dst with clonefile(2) — an APFS copy-on-write
// clone, instant and space-free — falling back to a plain copy off APFS.
func cloneTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, _ := os.Readlink(src)
		return os.Symlink(t, dst)
	}
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err == nil {
		return nil
	}
	if fi.IsDir() {
		return exec.Command("cp", "-R", src, dst).Run()
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, fi.Mode().Perm())
}

// hostSeatbelt is the host command's profile: everything the operator's
// shell could do, except writing the real home (bar persist paths and the
// workspace) and reading the parts of it the tool's profile does not name.
func hostSeatbelt(hr hostRun, scratch string, entries []string) string {
	q := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return strconvQuote(p)
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	home := hr.Home
	b.WriteString("(deny file-write* (subpath " + q(home) + "))\n")
	if !hr.FullHome {
		b.WriteString("(deny file-read-data (subpath " + q(home) + "))\n")
		for _, e := range entries {
			b.WriteString("(allow file-read-data (subpath " + q(filepath.Join(home, e)) + "))\n")
		}
		for _, p := range hr.BinRoots {
			b.WriteString("(allow file-read-data (subpath " + q(p) + "))\n")
		}
		// Library/Keychains: a Keychain-backed login (gh's `gh:github.com`)
		// is read through securityd, which needs the keychain file.
		b.WriteString("(allow file-read-data (subpath " + q(filepath.Join(home, "Library", "Keychains")) + "))\n")
		b.WriteString("(allow file-read-data (subpath " + q(filepath.Join(home, "Library", "Preferences")) + "))\n")
	}
	// conductor's own state/config are unreadable — then the dispatch's own
	// paths inside them (the scratch home, the workspace, the dispatch tmp
	// all live under the state dir) are carved back out: in SBPL the last
	// matching rule wins.
	for _, s := range hr.Sensitive {
		b.WriteString("(deny file-read* file-write* (subpath " + q(s) + "))\n")
	}
	// Name file-read-data explicitly: Seatbelt does not let a wildcard
	// (file-read*) allow override a deny on the specific operation above,
	// whatever the order (verified on macOS 26).
	for _, p := range []string{scratch, hr.Workspace, hr.TmpDir} {
		if p != "" {
			b.WriteString("(allow file-read* file-read-data file-write* (subpath " + q(p) + "))\n")
		}
	}
	for _, p := range hr.Persist {
		b.WriteString("(allow file-write* (subpath " + q(filepath.Join(home, p)) + "))\n")
	}
	if hr.EgressSock != "" {
		// A restricted network: outbound only to the proxy's loopback port
		// (EgressSock carries "port:<n>" on macOS), no name resolution.
		port := strings.TrimPrefix(hr.EgressSock, "port:")
		if _, err := strconv.Atoi(port); err == nil {
			b.WriteString("(deny network-outbound)\n(deny mach-lookup (global-name \"com.apple.dnssd.service\"))\n")
			b.WriteString("(allow network-outbound (remote ip \"localhost:" + port + "\"))\n")
		}
	}
	return b.String()
}

func strconvQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// RunHostExec is Linux-only (macOS host commands run under sandbox-exec
// directly).
func RunHostExec(string) int { return 213 }
