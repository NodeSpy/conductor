package sandbox

import (
	"path/filepath"
	"sort"
	"strings"
)

// orderBinds sorts a jail allow-list parent-first (by path depth, stable),
// with a tmpfs ahead of anything else at the same path, so a mount never
// lands underneath one that would hide it: the scratch $HOME tmpfs goes in
// before ~/.claude is bound into it, the read-write git common dir before
// its read-only config and hooks.
func orderBinds(binds []BindMount) []BindMount {
	out := append([]BindMount(nil), binds...)
	depth := func(p string) int {
		return strings.Count(strings.Trim(filepath.Clean(p), "/"), "/")
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := depth(out[i].Path), depth(out[j].Path)
		if di != dj {
			return di < dj
		}
		return out[i].Tmpfs && !out[j].Tmpfs
	})
	return out
}
