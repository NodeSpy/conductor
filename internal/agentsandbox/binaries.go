package agentsandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The sandbox shows the base system (/usr, /etc, /opt, …) but none of the
// operator's home, so an agent CLI installed under it (the native
// installer's ~/.local/bin/claude → ~/.local/share/claude/…, an npm global
// under ~/.npm-global) would not start. launchBinds shows exactly what the
// launch binary needs: its install (read-only), the symlinks on the way to
// it (recreated — a bind would follow them), and, for a node script, its
// whole package (its node_modules).

// launchBinds are the jail entries argv0 needs to run. home is the
// operator's real home: never shown whole, nor any directory containing it.
func launchBinds(argv0 string, lookPath func(string) (string, error), home string) []sandbox.BindMount {
	if argv0 == "" {
		return nil
	}
	bin := argv0
	if !filepath.IsAbs(bin) {
		p, err := lookPath(bin)
		if err != nil {
			return nil
		}
		bin = p
	}
	var out []sandbox.BindMount
	for _, p := range binaryRoots(bin, lookPath) {
		if visibleInBase(p) || coversHome(p, home) {
			continue
		}
		out = append(out, sandbox.BindMount{Path: p, RO: true, Optional: true})
	}
	return append(out, symlinkHops(bin)...)
}

// coversHome reports a directory that is, or contains, the operator's home:
// binding it would show everything in it. A binary sitting directly in such
// a directory is shown as the file alone (binaryRoots lists it too).
func coversHome(p, home string) bool {
	if home == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir() && within(home, p)
}

// binaryRoots are the host paths that must be readable for bin to run: the
// PATH hit, each symlink hop, the resolved file and its directory, and (for
// a script) its interpreter's, recursively and bounded. Directories already
// covered by another are dropped.
func binaryRoots(bin string, lookPath func(string) (string, error)) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		if depth > 4 || p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			lp, err := lookPath(p)
			if err != nil {
				return
			}
			p = lp
		}
		for hop := 0; hop < 8; hop++ {
			add(p)
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				break
			}
			t, err := os.Readlink(p)
			if err != nil {
				break
			}
			if !filepath.IsAbs(t) {
				t = filepath.Join(filepath.Dir(p), t)
			}
			p = filepath.Clean(t)
		}
		dir := filepath.Dir(p)
		add(dir)
		if interp := shebang(p); interp != "" {
			fields := strings.Fields(interp)
			run := fields[0]
			if filepath.Base(fields[0]) == "env" && len(fields) > 1 {
				run = fields[len(fields)-1]
			}
			// A node script (an npm-installed CLI or ACP adapter) loads its
			// dependencies from its package's node_modules: the whole
			// package, not just the script's dir.
			if filepath.Base(run) == "node" {
				if root := packageRoot(dir); root != "" {
					add(root)
				}
			}
			walk(run, depth+1)
		}
	}
	walk(bin, 0)
	sort.Strings(out)
	var keep []string
	covered := func(p string) bool {
		for _, k := range keep {
			if within(p, k) {
				return true
			}
		}
		return false
	}
	for _, p := range out {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() && !covered(p) {
			keep = append(keep, p)
		}
	}
	for _, p := range out {
		fi, err := os.Lstat(p)
		if err != nil || fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || covered(p) {
			continue
		}
		keep = append(keep, p)
	}
	return keep
}

// packageRoot is the npm package dir holding dir (the nearest ancestor with
// a package.json inside a node_modules, a few levels up at most), or "".
// Never a whole tree of packages: the package's own dir only.
func packageRoot(dir string) string {
	for i := 0; i < 5 && dir != "/" && dir != "."; i++ {
		if fi, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && fi.Mode().IsRegular() {
			if filepath.Base(filepath.Dir(dir)) == "node_modules" || filepath.Base(filepath.Dir(filepath.Dir(dir))) == "node_modules" {
				return dir
			}
			return ""
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func shebang(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	s := string(buf[:n])
	if !strings.HasPrefix(s, "#!") {
		return ""
	}
	line, _, _ := strings.Cut(s[2:], "\n")
	return strings.TrimSpace(line)
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

// visibleInBase reports a path the sandbox's base system already shows: the
// Linux jail's read-only /usr /etc /opt (and merged-usr links), or the
// Seatbelt base profile's system paths.
func visibleInBase(p string) bool {
	roots := []string{"/usr", "/etc", "/opt", "/bin", "/sbin", "/lib", "/lib64"}
	if sandbox.CheckGOOS == "darwin" {
		roots = []string{"/usr", "/bin", "/sbin", "/System", "/Library", "/opt/homebrew", "/opt/local", "/etc", "/private/etc"}
	}
	for _, root := range roots {
		if within(p, root) {
			return true
		}
	}
	return false
}

func within(p, root string) bool {
	if root == "" || p == "" {
		return false
	}
	p, root = filepath.Clean(p), filepath.Clean(root)
	if root == string(filepath.Separator) {
		return filepath.IsAbs(p)
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
