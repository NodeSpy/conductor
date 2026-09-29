//go:build darwin

package jail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// runConfined runs a content-executing host command in the macOS host-side
// jail (see buildConfined for Linux's). There is no mount namespace, so the
// workspace is an APFS clone in the scratch dir with the dispatch's
// copy-on-write layer for this tool (WsUpper) laid over it, and the command
// runs there — its cwd and any argument naming the workspace translated.
// What it writes into the clone is kept in WsUpper for the dispatch's next
// confined run of the tool, never in the workspace. Its Seatbelt profile
// denies every write but the scratch dir and the dispatch's TMPDIR, every
// read of the real home, conductor's state, the Keychain services, every
// network destination but the egress proxy's loopback port (and the Docker
// daemon's socket, for docker); it runs with no stdin and no terminal.
func runConfined(ctx context.Context, m *Manager, hr hostRun, stdout, stderr io.Writer) (hostResult, error) {
	port := strings.TrimPrefix(hr.EgressSock, "port:")
	if _, err := strconv.Atoi(port); err != nil {
		return hostResult{}, fmt.Errorf("a confined host command needs its network restricted")
	}
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
		dst := filepath.Join(shome, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		if err := cloneTree(filepath.Join(hr.Home, rel), dst); err != nil {
			return hostResult{}, fmt.Errorf("clone %s: %w", rel, err)
		}
	}
	ws := filepath.Join(scratch, "ws")
	if hr.Workspace != "" {
		if err := cloneTree(hr.Workspace, ws); err != nil {
			return hostResult{}, fmt.Errorf("clone workspace: %w", err)
		}
		if err := overlayTree(hr.WsUpper, ws); err != nil {
			return hostResult{}, fmt.Errorf("workspace layer: %w", err)
		}
	}
	tr := func(p string) string {
		if hr.Workspace == "" {
			return p
		}
		if p == hr.Workspace || strings.HasPrefix(p, hr.Workspace+"/") {
			return ws + strings.TrimPrefix(p, hr.Workspace)
		}
		if k, v, ok := strings.Cut(p, "="); ok && (v == hr.Workspace || strings.HasPrefix(v, hr.Workspace+"/")) {
			return k + "=" + ws + strings.TrimPrefix(v, hr.Workspace)
		}
		return p
	}
	args := make([]string, len(hr.Args))
	for i, a := range hr.Args {
		args[i] = tr(a)
	}
	env := make([]string, 0, len(hr.Env)+2)
	for _, kv := range hr.Env {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "PWD=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+shome, "TMPDIR="+hr.TmpDir)
	cmd := exec.CommandContext(ctx, "sandbox-exec", append([]string{"-p", confinedSeatbelt(hr, scratch, entries, port), hr.Bin}, args...)...)
	cmd.Dir = tr(hr.Cwd)
	cmd.Env = env
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
	if hr.Workspace != "" {
		keepChanges(ws, hr.Workspace, hr.WsUpper)
	}
	res := hostResult{Exit: exit, Discarded: scanDiscarded(scratch, hr.Home, nil, hr.Persist)}
	writeBack(scratch, hr.Home, nil, hr.Persist)
	return res, nil
}

// overlayTree lays the saved layer's files over the fresh workspace clone.
func overlayTree(upper, ws string) error {
	return filepath.WalkDir(upper, func(p string, de fs.DirEntry, err error) error {
		if err != nil || p == upper {
			return nil
		}
		rel, _ := filepath.Rel(upper, p)
		dst := filepath.Join(ws, rel)
		if de.IsDir() {
			if fi, err := os.Lstat(dst); err == nil && !fi.IsDir() {
				_ = os.RemoveAll(dst)
			}
			return os.MkdirAll(dst, 0o755)
		}
		_ = os.RemoveAll(dst)
		return cloneTree(p, dst)
	})
}

// keepChanges saves what a confined run created or changed in the clone
// into the dispatch's layer (so a later run sees it), comparing with the
// workspace; deletions are not carried.
func keepChanges(ws, workspace, upper string) {
	_ = filepath.WalkDir(ws, func(p string, de fs.DirEntry, err error) error {
		if err != nil || p == ws {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		if de.IsDir() {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		if oi, err := os.Lstat(filepath.Join(workspace, rel)); err == nil && oi.Mode() == fi.Mode() &&
			oi.Size() == fi.Size() && oi.ModTime().Equal(fi.ModTime()) {
			return nil
		}
		dst := filepath.Join(upper, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		_ = os.RemoveAll(dst)
		_ = cloneTree(p, dst)
		return nil
	})
}

// confinedSeatbelt is the confined run's profile (see runConfined).
func confinedSeatbelt(hr hostRun, scratch string, entries []string, port string) string {
	q := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return strconvQuote(p)
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	// Read-only everywhere…
	b.WriteString("(deny file-write*)\n")
	// …the real home unreadable but for the binary's install; conductor's
	// state and config unreadable outright.
	b.WriteString("(deny file-read-data (subpath " + q(hr.Home) + "))\n")
	for _, p := range hr.BinRoots {
		b.WriteString("(allow file-read-data (subpath " + q(p) + "))\n")
	}
	// Contents, not metadata: a tool lstat()s each component of a path it
	// resolves (terraform its temp files, which sit under the dispatch dir
	// inside conductor's state), as the agent jail allows everywhere.
	for _, s := range hr.Sensitive {
		b.WriteString("(deny file-read-data file-read-xattr file-write* (subpath " + q(s) + "))\n")
	}
	// The scratch dir (the home clones, the workspace clone) and the
	// dispatch's TMPDIR are the only writable places (last rule wins; name
	// file-read-data: a wildcard allow does not override a specific deny).
	for _, p := range []string{scratch, hr.TmpDir} {
		b.WriteString("(allow file-read* file-read-data file-write* (subpath " + q(p) + "))\n")
	}
	b.WriteString("(allow file-write-data (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/dtracehelper\") (literal \"/dev/tty\"))\n")
	// No Keychain.
	b.WriteString("(deny mach-lookup (global-name \"com.apple.SecurityServer\") (global-name \"com.apple.securityd.xpc\") (global-name \"com.apple.security.agent\") (global-name \"com.apple.dnssd.service\"))\n")
	// Network: the egress proxy's loopback port only (and the Docker daemon).
	b.WriteString("(deny network*)\n")
	b.WriteString("(allow network-outbound (remote ip \"localhost:" + port + "\"))\n")
	// A tool's own processes talk over unix sockets in its temp dir
	// (terraform and its provider plugins: go-plugin's handshake socket).
	for _, p := range []string{hr.TmpDir, scratch} {
		re := "#\"^" + regexp.QuoteMeta(realPath(p)) + "/\""
		b.WriteString("(allow network-bind network-inbound (local unix-socket (path-regex " + re + ")))\n")
		b.WriteString("(allow network-outbound (remote unix-socket (path-regex " + re + ")))\n")
	}
	for _, s := range hr.Sockets {
		b.WriteString("(allow network-outbound (remote unix-socket (path-literal " + strconvQuote(s) + ")))\n")
		if r, err := filepath.EvalSymlinks(s); err == nil && r != s {
			b.WriteString("(allow network-outbound (remote unix-socket (path-literal " + strconvQuote(r) + ")))\n")
		}
	}
	return b.String()
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
