package config

import "fmt"

// ValidateHostPaths is read through a package var (see jailcfg.go) that
// hostcmd sets at init from its real profile registry. This package's tests
// link no such registry, so they declare the two shapes a profile's view
// comes in: gh's (commonHome plus its own config dir) and a tool with no
// profile at all (any narrowing is valid — there is no view to check
// against).
func init() {
	ValidateHostPaths = func(tool string, paths []string) error {
		view := map[string][]string{
			"gh": {".gitconfig", ".config/git", ".config/gh"},
		}[tool]
		if view == nil {
			return nil
		}
		for _, p := range paths {
			if !pathWithinAny(p, view) {
				return fmt.Errorf("%q is outside %s's own view %v — paths can only narrow", p, tool, view)
			}
		}
		return nil
	}
}
