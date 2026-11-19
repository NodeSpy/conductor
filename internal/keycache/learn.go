package keycache

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// LEARNED PATHS
//
// A cache holds the directories its key's runs fill, learned from the runs
// themselves: no recipe, no ecosystem knowledge.
//
// Which directories are candidates (the granularity rule): a cache root is
// a directory directly inside a CONTAINER, the home itself or one of the
// base directories tools put their caches and data in by convention
// (~/.cache, ~/.local, ~/.local/share, ~/.local/state, ~/.config, and on
// macOS ~/Library/Caches). So ~/go, ~/.npm, ~/.cargo, ~/.m2, ~/.cache/yarn,
// ~/.cache/go-build and ~/.local/share/pnpm are each one candidate — one
// tool's tree, whatever it keeps inside — and the home or ~/.cache as a
// whole never is. A candidate that holds a protected path (a credential
// file inside it, ~/.cargo/credentials.toml) is never taken whole: its
// subdirectories are the candidates instead, down to maxLearnDepth. A
// candidate that is, or is inside, a protected path is never one.
//
// What makes a candidate a learned path: a run of the key wrote at least
// MinSize there, in at least MinRuns successful runs of the key. Once
// learned, a path stays learned for the key (its runs restore it and write
// little more). A learned path inside another is dropped for the outer.

// containers are the home-relative directories whose children are cache
// roots; a non-nil list names the only children that are (on macOS,
// ~/Library holds far more than caches).
var containers = map[string][]string{
	"":               nil,
	".cache":         nil,
	".local":         nil,
	".local/share":   nil,
	".local/state":   nil,
	".config":        nil,
	"Library":        {"Caches"},
	"Library/Caches": nil,
}

// maxLearnDepth bounds how deep a candidate can be (components below the
// home).
const maxLearnDepth = 6

// candidates are the cache-root candidates in home, home-relative
// (protected: the full list, protectedList). The
// walk never follows a symlink (Lstat), and reads nothing but directory
// listings.
func candidates(home string, protected []string) []string {
	var out []string
	var walk func(rel string, container bool, only []string)
	walk = func(rel string, container bool, only []string) {
		if strings.Count(rel, "/")+1 > maxLearnDepth && rel != "" {
			return
		}
		ents, err := os.ReadDir(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			if only != nil && !contains(only, e.Name()) {
				continue
			}
			c := e.Name()
			if rel != "" {
				c = rel + "/" + c
			}
			if !plainRel(c) {
				continue // a name an overlay cannot take, or a git dir
			}
			if sub, ok := containers[c]; ok {
				walk(c, true, sub)
				continue
			}
			switch protection(c, protected) {
			case "inside":
			case "holds":
				walk(c, false, nil)
			default:
				out = append(out, c)
			}
		}
	}
	walk("", true, nil)
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// learnState is what a key's runs taught: how many successful runs filled
// each candidate past the threshold, and the paths that made it.
type learnState struct {
	Seen  map[string]int `json:"seen,omitempty"`
	Paths []string       `json:"paths,omitempty"`
}

func (s *Store) readLearn(id string) learnState {
	var st learnState
	if b, err := os.ReadFile(s.learnFile(id)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Learned is a key's learned paths.
func (s *Store) Learned(id string) []string { return s.readLearn(id).Paths }

// learn records one successful run's writes (home-relative path → bytes)
// and returns the key's learned paths. The caller holds the store's lock.
func (s *Store) learn(id string, written map[string]int64, minSize int64, minRuns int, protected []string) []string {
	st := s.readLearn(id)
	if st.Seen == nil {
		st.Seen = map[string]int{}
	}
	paths := map[string]bool{}
	for _, p := range st.Paths {
		if CheckHomePath(p, protected) {
			paths[p] = true
		}
	}
	for p, n := range written {
		if n < minSize || !CheckHomePath(p, protected) {
			continue
		}
		st.Seen[p]++
		if st.Seen[p] >= minRuns {
			paths[p] = true
		}
	}
	st.Paths = outermost(paths)
	for p := range st.Seen {
		// What a learned path holds is learned with it.
		for _, l := range st.Paths {
			if strings.HasPrefix(p, l+"/") {
				delete(st.Seen, p)
			}
		}
	}
	if b, err := json.Marshal(st); err == nil {
		if os.MkdirAll(s.keyDir(id), 0o700) == nil {
			_ = writeAtomic(s.learnFile(id), b)
		}
	}
	return st.Paths
}

// outermost is the set's paths with any inside another dropped, sorted.
func outermost(set map[string]bool) []string {
	var out []string
	for p := range set {
		inner := false
		for q := range set {
			if q != p && strings.HasPrefix(p, q+"/") {
				inner = true
				break
			}
		}
		if !inner {
			out = append(out, path.Clean(p))
		}
	}
	sort.Strings(out)
	return out
}
