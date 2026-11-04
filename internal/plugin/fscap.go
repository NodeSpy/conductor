package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/confdir"
	"github.com/NodeSpy/conductor/internal/config"
)

// The cap on what a plugin may declare under fs:. A manifest is written by
// the plugin's author, so a declared path is a request, not a grant the
// operator reviewed line by line: a plugin that declares / or the
// operator's home would read everything the jail is meant to hide. These
// are refused, at install and at every launch, unless the operator's own
// fs: on the entry names the same path:
//
//   - / ;
//   - the operator's home directory, or any directory containing it;
//   - a path inside conductor's config or state directory, or containing
//     one (the daemon's secrets, tokens and other plugins' state live
//     there).
//
// A path inside the home directory is not refused (a plugin may declare
// ~/.config/gh); it is shown in the install prompt and plugin list --caps.

// ProtectedDirs are the directories a declared fs: path may not reach:
// conductor's state and config directories, plus extra (the daemon's mask
// paths).
func ProtectedDirs(extra ...string) []string {
	out := []string{config.StateDir(), confdir.Dir()}
	return append(out, extra...)
}

// CheckDeclaredFS refuses a declared fs: entry that is /, the home
// directory or a parent of it, or that contains or sits inside a protected
// directory — unless operatorFS (the entry's own fs:, any instance) names
// the same path. Paths are compared as written (after ~) and with symlinks
// resolved, so a link to / is refused as / is.
func CheckDeclaredFS(name string, declared, operatorFS, protected []string) error {
	granted := map[string]bool{}
	for _, e := range operatorFS {
		if p := fsPath(e); p != "" {
			granted[p] = true
		}
	}
	home, _ := os.UserHomeDir()
	homes := resolvedForms(home)
	var prot []string
	for _, d := range protected {
		prot = append(prot, resolvedForms(d)...)
	}
	for _, e := range declared {
		p := fsPath(e)
		if p == "" || granted[p] {
			continue
		}
		for _, c := range resolvedForms(p) {
			if why := refusedFS(c, homes, prot); why != "" {
				return fmt.Errorf("plugin %s declares fs: %s, and %s — refused; if it really needs that, name the same path in the entry's own fs:", name, e, why)
			}
		}
	}
	return nil
}

// fsPath is a fs: entry's path: ~ expanded, cleaned, "" when not absolute.
func fsPath(entry string) string {
	p, _ := config.ParsePluginFS(strings.TrimSpace(entry))
	p = config.ExpandHome(p)
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

// resolvedForms is p as written and, when different, with symlinks
// resolved.
func resolvedForms(p string) []string {
	if p == "" {
		return nil
	}
	p = filepath.Clean(p)
	out := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		out = append(out, r)
	}
	return out
}

func refusedFS(p string, homes, protected []string) string {
	if p == "/" {
		return "that is the root directory"
	}
	for _, h := range homes {
		if within(h, p) {
			return "that is the home directory (" + h + ") or contains it"
		}
	}
	for _, d := range protected {
		if within(d, p) {
			return "that contains conductor's own directory " + d
		}
		if within(p, d) {
			return "that is inside conductor's own directory " + d
		}
	}
	return ""
}

// within reports whether p is dir or below it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}
