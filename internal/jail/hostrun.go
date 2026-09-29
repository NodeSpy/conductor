package jail

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/hostcmd"
)

// hostRun is one approved host command, ready to run on the operator's
// machine (#154 §3.3): the real binary, its copy-on-write home view, its
// environment, and the dispatch paths it may write through.
type hostRun struct {
	Tool  string   `json:"tool"`
	Bin   string   `json:"bin"`
	Args  []string `json:"args"`
	Cwd   string   `json:"cwd"`
	Env   []string `json:"env"`
	Stdin []byte   `json:"-"`

	Home string `json:"home"`
	// HomePaths are the home-relative entries the command may see (copy-on-
	// write); FullHome shows every top-level entry instead.
	HomePaths []string `json:"home_paths"`
	FullHome  bool     `json:"full_home"`
	// Persist are home-relative paths whose writes are copied back.
	Persist []string `json:"persist"`

	Workspace string   `json:"workspace"`
	TmpDir    string   `json:"tmp"`
	Sensitive []string `json:"sensitive"`
	// BinRoots are host paths the binary needs readable (its install under
	// $HOME, e.g. ~/.local/bin/gh).
	BinRoots []string `json:"bin_roots"`
	// EgressSock, when set, confines the command's network to the egress
	// proxy (the command gets an empty network namespace + forwarder).
	EgressSock string `json:"egress_sock,omitempty"`

	// Confine runs a content-executing command in the host-side jail (see
	// hostcmd/content.go): an allow-list root (Linux) or a write-deny
	// profile (macOS), the workspace read-only with its writes landing in
	// WsUpper — kept for the dispatch's later confined runs of the same
	// tool (terraform init, then plan), never in the real workspace — no
	// stdin, and the network always restricted.
	Confine bool   `json:"confine,omitempty"`
	WsUpper string `json:"ws_upper,omitempty"`
	WsWork  string `json:"ws_work,omitempty"`
	// Sockets are host unix sockets a confined run may reach (the Docker
	// daemon's, for docker).
	Sockets []string `json:"sockets,omitempty"`

	// Scratch is the per-command copy-on-write dir (set by runHost).
	Scratch string `json:"scratch"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}

// hostResult is how a host command ended.
type hostResult struct {
	Exit      int
	Discarded []string // home-relative paths whose writes were thrown away
}

// maxDiscardReport caps how many discarded paths an event lists.
const maxDiscardReport = 20

// scanDiscarded lists what a finished host command wrote into its
// copy-on-write home, as home-relative paths: the overlay upper dirs
// (Linux; entries[i] is the home entry up/<i> was layered over) and the
// top-level files copied into the scratch home, compared with the originals.
func scanDiscarded(scratch, home string, entries []string, persist []string) []string {
	var out []string
	seen := map[string]bool{}
	note := func(rel string) {
		if seen[rel] {
			return
		}
		for _, p := range persist {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return // written through, not discarded
			}
		}
		seen[rel] = true
		out = append(out, rel)
	}
	for i, e := range entries {
		up := filepath.Join(scratch, "up", itoa(i))
		_ = filepath.WalkDir(up, func(p string, de fs.DirEntry, err error) error {
			if err != nil || p == up {
				return nil
			}
			if de.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(up, p)
			note(filepath.Join(e, rel))
			return nil
		})
	}
	sh := filepath.Join(scratch, "home")
	_ = filepath.WalkDir(sh, func(p string, de fs.DirEntry, err error) error {
		if err != nil || p == sh || de.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(sh, p)
		orig := filepath.Join(home, rel)
		if de.Type()&fs.ModeSymlink != 0 {
			a, _ := os.Readlink(p)
			b, _ := os.Readlink(orig)
			if a != b {
				note(rel)
			}
			return nil
		}
		if !sameContent(p, orig) {
			note(rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// writeBack copies each persist path's copy-on-write content back into the
// real home — the rotating-token caches a tool must keep. Only regular files
// are copied (atomically, via rename), nothing is deleted.
func writeBack(scratch, home string, entries []string, persist []string) {
	for _, p := range persist {
		// Which layer holds it: an overlay upper (dir entries) or the scratch
		// home (top-level files).
		for i, e := range entries {
			if p != e && !strings.HasPrefix(p, e+"/") {
				continue
			}
			src := filepath.Join(scratch, "up", itoa(i), strings.TrimPrefix(strings.TrimPrefix(p, e), "/"))
			copyTree(src, filepath.Join(home, p))
		}
		copyTree(filepath.Join(scratch, "home", p), filepath.Join(home, p))
	}
}

func copyTree(src, dst string) {
	_ = filepath.WalkDir(src, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if de.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !de.Type().IsRegular() {
			return nil
		}
		if sameContent(p, target) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fi, _ := de.Info()
		mode := os.FileMode(0o600)
		if fi != nil {
			mode = fi.Mode().Perm()
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o700)
		tmp := target + ".conductor-tmp"
		if os.WriteFile(tmp, b, mode) == nil {
			_ = os.Rename(tmp, target)
		}
		return nil
	})
}

func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	sa, _ := fa.Stat()
	sb, _ := fb.Stat()
	if sa == nil || sb == nil || sa.Size() != sb.Size() {
		return false
	}
	ba, _ := io.ReadAll(io.LimitReader(fa, 64<<20))
	bb, _ := io.ReadAll(io.LimitReader(fb, 64<<20))
	return bytes.Equal(ba, bb)
}

// removeScratch deletes a copy-on-write dir, first making every directory
// writable (overlayfs leaves its work dir 0000).
func removeScratch(dir string) {
	_ = filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err == nil && de.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// homeEntries resolves the entries a host command's home view shows.
func homeEntries(hr hostRun) []string {
	if !hr.FullHome {
		// Include the persist paths' own entries (they must be visible to be
		// written), then keep only what exists.
		seen := map[string]bool{}
		var out []string
		cands := append(append([]string(nil), hr.HomePaths...), hr.Persist...)
		sort.SliceStable(cands, func(i, j int) bool {
			return strings.Count(cands[i], "/") < strings.Count(cands[j], "/")
		})
		for _, p := range cands {
			p = filepath.Clean(p)
			if seen[p] || p == "." || strings.HasPrefix(p, "..") {
				continue
			}
			covered := false
			for _, q := range out {
				if p == q || strings.HasPrefix(p, q+"/") {
					covered = true
				}
			}
			if covered {
				continue
			}
			if _, err := os.Lstat(filepath.Join(hr.Home, p)); err != nil {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
		return out
	}
	des, err := os.ReadDir(hr.Home)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// homeViewFor is hostcmd.HomeView with its env as a list (tests).
func homeViewFor(tool string) ([]string, []string, []string, bool) {
	p, per, env, full := hostcmd.HomeView(tool, nil)
	var e []string
	for k, v := range env {
		e = append(e, k+"="+v)
	}
	return p, per, e, full
}
