package agentsandbox

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
)

// A program is judged by what its binary is, not by its name: a copy of
// bash renamed ./esbuild is still bash, a copy of socat renamed ./dep-sync
// still socat. The watcher indexes the host's own shells, interpreters and
// tunnel tools (by file identity, and by size and digest for copies) and
// matches each launch process's executable against them. A binary the host
// does not have — one the agent built or downloaded — is not identified:
// the egress proxy's shape signal is what covers it.

// binary classes.
const (
	classShell  = "shell"       // a shell: any inet socket is a reverse shell
	classInterp = "interpreter" // an interpreter: an inet socket on stdin/stdout is
	classTunnel = "tunnel"      // a tunnel tool
)

// trueShells are shells: none holds a network socket in a build.
var trueShells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ash": true, "ksh": true, "mksh": true, "fish": true,
	"csh": true, "tcsh": true, "busybox": true, "yash": true, "posh": true, "rbash": true,
}

// identity is what a binary is: a known program's name and class.
type identity struct{ name, class string }

// classOf is the class of a program name ("" = none).
func classOf(name string) string {
	switch {
	case tunnelTools[name]:
		return classTunnel
	case trueShells[name]:
		return classShell
	case shells[name]:
		return classInterp
	}
	return ""
}

type fileKey struct{ dev, ino uint64 }

// binaryIndex is the host's known binaries.
type binaryIndex struct {
	byFile map[fileKey]identity
	bySize map[int64][]string // size → paths, hashed on demand
	byPath map[string]identity
	digest map[string]identity
	hashed map[string]bool // paths whose digest is in digest
	seen   map[string]identity
	mu     sync.Mutex
}

// binarySearchDirs are where the index looks besides $PATH.
var binarySearchDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin", "/usr/local/sbin", "/opt/homebrew/bin", "/opt/local/bin", "/snap/bin"}

var hostBinaries = sync.OnceValue(func() *binaryIndex {
	return indexBinaries(append(filepath.SplitList(os.Getenv("PATH")), binarySearchDirs...))
})

func indexBinaries(dirs []string) *binaryIndex {
	ix := &binaryIndex{byFile: map[fileKey]identity{}, bySize: map[int64][]string{}, byPath: map[string]identity{}, digest: map[string]identity{}, hashed: map[string]bool{}, seen: map[string]identity{}}
	var names []string
	for _, m := range []map[string]bool{tunnelTools, trueShells, shells} {
		for n := range m {
			names = append(names, n)
		}
	}
	// openssl is no class of its own (s_client is judged by its arguments),
	// but an allowance for it covers only the host's.
	names = append(names, "openssl")
	sort.Strings(names)
	done := map[string]bool{}
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) || done[d] {
			continue
		}
		done[d] = true
		for _, n := range names {
			p := filepath.Join(d, n)
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
				continue
			}
			k, ok := keyOf(fi)
			if !ok {
				continue
			}
			id := identity{n, classOf(n)}
			if prev, dup := ix.byFile[k]; dup {
				// One file under several names (/bin → /usr/bin, rbash →
				// bash): it is the name the file itself has.
				if real, err := filepath.EvalSymlinks(p); err == nil && filepath.Base(real) == n && prev.name != n {
					ix.byFile[k] = id
					for _, q := range ix.bySize[fi.Size()] {
						if ix.byPath[q] == prev {
							ix.byPath[q] = id
						}
					}
				}
				continue
			}
			ix.byFile[k] = id
			ix.bySize[fi.Size()] = append(ix.bySize[fi.Size()], p)
			ix.byPath[p] = id
		}
	}
	return ix
}

func keyOf(fi os.FileInfo) (fileKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), uint64(st.Ino)}, true
}

// identify says what the binary at exe is (a /proc/<pid>/exe link or a
// path), or nothing.
func (ix *binaryIndex) identify(exe string) (identity, bool) {
	if ix == nil || exe == "" {
		return identity{}, false
	}
	fi, err := os.Stat(exe)
	if err != nil || !fi.Mode().IsRegular() {
		return identity{}, false
	}
	k, ok := keyOf(fi)
	if !ok {
		return identity{}, false
	}
	if id, ok := ix.byFile[k]; ok {
		return id, true
	}
	cands := ix.bySize[fi.Size()]
	if len(cands) == 0 {
		return identity{}, false
	}
	// A copy: same size as a known binary — compare digests, once per
	// file version.
	ver := strconv.FormatUint(k.dev, 10) + ":" + strconv.FormatUint(k.ino, 10) + ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if id, ok := ix.seen[ver]; ok {
		return id, id.name != ""
	}
	sum := fileDigest(exe)
	var found identity
	if sum != "" {
		for _, p := range cands {
			if !ix.hashed[p] {
				ix.hashed[p] = true
				if d := fileDigest(p); d != "" {
					ix.digest[d] = ix.byPath[p]
				}
			}
		}
		found = ix.digest[sum]
	}
	ix.seen[ver] = found
	return found, found.name != ""
}

func fileDigest(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return string(h.Sum(nil))
}
