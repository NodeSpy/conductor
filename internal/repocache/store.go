package repocache

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Store is the on-disk cache store:
//
//	<dir>/<repo slug>/<name>/<key>/meta.json   what was published, when, from where
//	<dir>/<repo slug>/<name>/<key>/data/home/<rel>, data/workspace/<rel>
//	<dir>/<repo slug>/<name>/<key>/.used       mtime = last restore
//
// A published key directory is immutable: it is built under a dot name and
// renamed into place, and only ever removed whole (eviction, clear). A
// restore reads it as an overlay's read-only lower layer or clones it.
type Store struct {
	Dir string
}

// Meta is a published cache's record.
type Meta struct {
	Repo    string    `json:"repo"`
	Name    string    `json:"name"`
	Key     string    `json:"key"`
	Origin  string    `json:"origin,omitempty"`
	Paths   []string  `json:"paths"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	// Commit is the default-branch commit the warm ran on.
	Commit string `json:"commit,omitempty"`
}

// Entry is one published cache.
type Entry struct {
	Meta
	Dir      string
	LastUsed time.Time
}

// Lower is where a cache path's published content is ("" = not published).
func (e Entry) Lower(p Path) string {
	d := filepath.Join(e.Dir, "data", string(p.Area), filepath.FromSlash(p.Rel))
	if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
		return d
	}
	return ""
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

// CheckRepo refuses a repository name that is not owner/name.
func CheckRepo(repo string) error {
	if !repoRE.MatchString(repo) || strings.Contains(repo, "..") {
		return fmt.Errorf("repo %q is not owner/name", repo)
	}
	return nil
}

// repoSlug is the store directory of a repository: "/" becomes "~", which
// a repository name (CheckRepo) never contains, so no two names share one.
func repoSlug(repo string) string { return strings.ReplaceAll(repo, "/", "~") }

func (s *Store) nameDir(repo, name string) string {
	return filepath.Join(s.Dir, repoSlug(repo), name)
}

// Hit kinds.
const (
	Exact   = "hit"
	Partial = "partial"
	Miss    = "miss"
)

// Lookup finds the cache to restore: the exact key, else the newest whose
// key starts with a restore prefix (in order).
func (s *Store) Lookup(repo, name, key string, prefixes []string) (Entry, string, bool) {
	if CheckRepo(repo) != nil || !nameRE.MatchString(name) {
		return Entry{}, Miss, false
	}
	if keyRE.MatchString(key) {
		if e, err := s.read(filepath.Join(s.nameDir(repo, name), key)); err == nil {
			return e, Exact, true
		}
	}
	all := s.entries(repo, name)
	for _, p := range prefixes {
		var best *Entry
		for i := range all {
			if strings.HasPrefix(all[i].Key, p) && (best == nil || all[i].Created.After(best.Created)) {
				best = &all[i]
			}
		}
		if best != nil {
			return *best, Partial, true
		}
	}
	return Entry{}, Miss, false
}

// Has reports an exact key published.
func (s *Store) Has(repo, name, key string) bool {
	_, kind, ok := s.Lookup(repo, name, key, nil)
	return ok && kind == Exact
}

func (s *Store) entries(repo, name string) []Entry {
	d := s.nameDir(repo, name)
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, ent := range ents {
		if strings.HasPrefix(ent.Name(), ".") || !ent.IsDir() {
			continue
		}
		if e, err := s.read(filepath.Join(d, ent.Name())); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// read loads one key directory.
func (s *Store) read(dir string) (Entry, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return Entry{}, err
	}
	if !fi.IsDir() {
		return Entry{}, fmt.Errorf("%s is not a directory", dir)
	}
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Entry{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Entry{}, err
	}
	if m.Key != filepath.Base(dir) {
		return Entry{}, fmt.Errorf("%s: meta names key %q", dir, m.Key)
	}
	e := Entry{Meta: m, Dir: dir, LastUsed: m.Created}
	if u, err := os.Stat(filepath.Join(dir, ".used")); err == nil {
		e.LastUsed = u.ModTime()
	}
	return e, nil
}

// List is every published cache, by repo, name, then newest first.
func (s *Store) List() []Entry {
	var out []Entry
	repos, _ := os.ReadDir(s.Dir)
	for _, r := range repos {
		if strings.HasPrefix(r.Name(), ".") || !r.IsDir() {
			continue
		}
		names, _ := os.ReadDir(filepath.Join(s.Dir, r.Name()))
		for _, n := range names {
			if strings.HasPrefix(n.Name(), ".") || !n.IsDir() {
				continue
			}
			ents, _ := os.ReadDir(filepath.Join(s.Dir, r.Name(), n.Name()))
			for _, k := range ents {
				if strings.HasPrefix(k.Name(), ".") || !k.IsDir() {
					continue
				}
				if e, err := s.read(filepath.Join(s.Dir, r.Name(), n.Name(), k.Name())); err == nil {
					out = append(out, e)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Created.After(b.Created)
	})
	return out
}

// Touch records a restore of e.
func (s *Store) Touch(e Entry) {
	p := filepath.Join(e.Dir, ".used")
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, nil, 0o600)
	}
}

// ErrExists is Publish finding the key already published.
var ErrExists = errors.New("cache key already published")

// Publish builds a cache under a dot name — fill puts each path's content
// at data/<area>/<rel> under the directory it is given — and renames it
// into place. A key already published is left as is (ErrExists): a key
// directory is never replaced or written into.
func (s *Store) Publish(m Meta, fill func(dataDir string) error) (Entry, error) {
	if err := CheckRepo(m.Repo); err != nil {
		return Entry{}, err
	}
	if !nameRE.MatchString(m.Name) || !keyRE.MatchString(m.Key) {
		return Entry{}, fmt.Errorf("publish: bad cache name %q or key %q", m.Name, m.Key)
	}
	nd := s.nameDir(m.Repo, m.Name)
	final := filepath.Join(nd, m.Key)
	if _, err := os.Lstat(final); err == nil {
		return Entry{}, ErrExists
	}
	if err := os.MkdirAll(nd, 0o700); err != nil {
		return Entry{}, err
	}
	tmp := filepath.Join(nd, ".tmp-"+randHex())
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return Entry{}, err
	}
	fail := func(err error) (Entry, error) {
		_ = RemoveAll(tmp)
		return Entry{}, err
	}
	data := filepath.Join(tmp, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		return fail(err)
	}
	if err := fill(data); err != nil {
		return fail(err)
	}
	m.Size = DiskUsage(data)
	if m.Created.IsZero() {
		m.Created = time.Now().UTC()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "meta.json"), b, 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".used"), nil, 0o600); err != nil {
		return fail(err)
	}
	// rename(2) onto an existing directory fails unless it is empty, and a
	// published key directory never is: two warms racing on one key keep
	// the first.
	if err := os.Rename(tmp, final); err != nil {
		if _, serr := os.Lstat(final); serr == nil {
			_, _ = fail(nil)
			return Entry{}, ErrExists
		}
		return fail(err)
	}
	return s.read(final)
}

// Remove deletes a published cache: renamed out of sight first, so a
// lookup never finds it half-removed.
func (s *Store) Remove(e Entry) error {
	if !within(e.Dir, s.Dir) {
		return fmt.Errorf("%s is not in the store", e.Dir)
	}
	gone := filepath.Join(filepath.Dir(e.Dir), ".gone-"+randHex())
	if err := os.Rename(e.Dir, gone); err != nil {
		return err
	}
	return RemoveAll(gone)
}

// Size is the store's total size.
func (s *Store) Size() int64 {
	var n int64
	for _, e := range s.List() {
		n += e.Size
	}
	return n
}

// Evict removes least-recently-used caches until the store is within max
// bytes, never one inUse reports. It returns what it removed.
func (s *Store) Evict(max int64, inUse func(dir string) bool) []Entry {
	if max <= 0 {
		return nil
	}
	all := s.List()
	var total int64
	for _, e := range all {
		total += e.Size
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].LastUsed.Before(all[j].LastUsed) })
	var out []Entry
	for _, e := range all {
		if total <= max {
			break
		}
		if inUse != nil && inUse(e.Dir) {
			continue
		}
		if s.Remove(e) == nil {
			total -= e.Size
			out = append(out, e)
		}
	}
	s.sweepTemp()
	return out
}

// sweepTemp removes build and removal leftovers a crash left (an hour old).
func (s *Store) sweepTemp() {
	repos, _ := os.ReadDir(s.Dir)
	for _, r := range repos {
		if strings.HasPrefix(r.Name(), ".") {
			continue
		}
		names, _ := os.ReadDir(filepath.Join(s.Dir, r.Name()))
		for _, n := range names {
			ents, _ := os.ReadDir(filepath.Join(s.Dir, r.Name(), n.Name()))
			for _, k := range ents {
				if !strings.HasPrefix(k.Name(), ".tmp-") && !strings.HasPrefix(k.Name(), ".gone-") {
					continue
				}
				p := filepath.Join(s.Dir, r.Name(), n.Name(), k.Name())
				if fi, err := os.Lstat(p); err == nil && time.Since(fi.ModTime()) > time.Hour {
					_ = RemoveAll(p)
				}
			}
		}
	}
}

// DiskUsage is the disk a tree takes (allocated blocks where the platform
// reports them), never following a symlink.
func DiskUsage(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			n += allocated(fi)
		}
		return nil
	})
	return n
}

// RemoveAll removes a tree a sandboxed tool or an overlay left: a read-only
// directory (a Go module cache) or an overlay's mode-0 work directory is
// made writable first. It never follows a symlink.
func RemoveAll(p string) error {
	err := os.RemoveAll(p)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		if err != nil && d == nil {
			// A directory unreadable before its chmod: retry it.
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	_ = os.Chmod(p, 0o700)
	err = os.RemoveAll(p)
	return err
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
