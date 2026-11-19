package keycache

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store is the on-disk cache store:
//
//	<dir>/keys/<id>/key.json             the key (namespace, template, value)
//	<dir>/keys/<id>/<version>/meta.json  one published copy: when, which paths
//	<dir>/keys/<id>/<version>/data/<rel> its content, home-relative
//	<dir>/keys/<id>/<version>/.used      mtime = last restore
//	<dir>/templates/<tid>/learn.json     what runs of the template's keys filled: learned paths
//	<dir>/.layers/<launch>/lock, home, key, dispatch, uses, slots, ended, <slot>/{upper,work}
//	                                     a launch's overlay layers and what it used
//
// A published version is immutable: built under a dot name and renamed into
// place, only ever removed whole (eviction, clear, or once a newer version
// of its key is published and no launch uses it). A restore reads the newest
// version of a key as an overlay's read-only lower layer, or clones it.
type Store struct {
	Dir string
	// LaunchGrace is how long a launch's ledger entry is live before its
	// launch holds the entry's lock (0: DefaultLaunchGrace).
	LaunchGrace time.Duration
	// KeepEnded is how long an ended launch's layers wait for its step to
	// publish them (0: DefaultKeepEnded).
	KeepEnded time.Duration
	// KeyIdle is how long a key with no published version and no launch
	// keeps its directory (0: DefaultKeyIdle).
	KeyIdle time.Duration

	mu sync.Mutex
	// testHook is called at points tests interleave with.
	testHook func(point string)
}

// Meta is a published version's record.
type Meta struct {
	Key Key `json:"key"`
	// Paths are the home-relative directories the version holds.
	Paths   []string  `json:"paths"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	// Run is the launch that published it (display).
	Run string `json:"run,omitempty"`
}

// Entry is one published version.
type Entry struct {
	Meta
	ID       string
	Version  string
	Dir      string
	LastUsed time.Time
}

// Lower is where a home path's published content is ("" = not in it).
func (e Entry) Lower(rel string) string {
	d := filepath.Join(e.Dir, "data", filepath.FromSlash(rel))
	if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
		return d
	}
	return ""
}

const (
	keysDirName      = "keys"
	templatesDirName = "templates"
)

var idRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
var versionRE = regexp.MustCompile(`^v[0-9]{20}-[0-9a-f]{16}$`)

func (s *Store) keysRoot() string        { return filepath.Join(s.Dir, keysDirName) }
func (s *Store) keyDir(id string) string { return filepath.Join(s.keysRoot(), id) }
func (s *Store) templatesRoot() string   { return filepath.Join(s.Dir, templatesDirName) }
func (s *Store) templateDir(tid string) string {
	return filepath.Join(s.templatesRoot(), tid)
}
func (s *Store) learnFile(tid string) string { return filepath.Join(s.templateDir(tid), "learn.json") }

// Lookup finds the newest published version of a key.
func (s *Store) Lookup(id string) (Entry, bool) {
	vs := s.versions(id)
	if len(vs) == 0 {
		return Entry{}, false
	}
	return vs[0], true
}

// versions are a key's published versions, newest first.
func (s *Store) versions(id string) []Entry {
	if !idRE.MatchString(id) {
		return nil
	}
	ents, err := os.ReadDir(s.keyDir(id))
	if err != nil {
		return nil
	}
	var out []Entry
	for _, ent := range ents {
		if !ent.IsDir() || !versionRE.MatchString(ent.Name()) {
			continue
		}
		if e, err := s.read(id, ent.Name()); err == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// read loads one version directory.
func (s *Store) read(id, version string) (Entry, error) {
	dir := filepath.Join(s.keyDir(id), version)
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
	if m.Key.ID() != id {
		return Entry{}, fmt.Errorf("%s: meta names another key", dir)
	}
	e := Entry{Meta: m, ID: id, Version: version, Dir: dir, LastUsed: m.Created}
	if u, err := os.Stat(filepath.Join(dir, ".used")); err == nil {
		e.LastUsed = u.ModTime()
	}
	return e, nil
}

// List is every published version: by key, newest first.
func (s *Store) List() []Entry {
	var out []Entry
	ids, _ := os.ReadDir(s.keysRoot())
	for _, k := range ids {
		if k.IsDir() && idRE.MatchString(k.Name()) {
			out = append(out, s.versions(k.Name())...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Key.String() != b.Key.String() {
			return a.Key.String() < b.Key.String()
		}
		return a.Version > b.Version
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

// stage is a version being built: fill its data directory, then commit.
type stage struct {
	s    *Store
	key  Key
	tmp  string
	Data string
}

// newStage starts building a version of key under a dot name.
func (s *Store) newStage(key Key) (*stage, error) {
	if err := key.Valid(); err != nil {
		return nil, err
	}
	kd := s.keyDir(key.ID())
	if err := os.MkdirAll(kd, 0o700); err != nil {
		return nil, err
	}
	if err := writeKeyFile(kd, key); err != nil {
		return nil, err
	}
	tmp := filepath.Join(kd, ".tmp-"+randHex())
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return nil, err
	}
	data := filepath.Join(tmp, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		_ = RemoveAll(tmp)
		return nil, err
	}
	return &stage{s: s, key: key, tmp: tmp, Data: data}, nil
}

func writeKeyFile(kd string, key Key) error {
	p := filepath.Join(kd, "key.json")
	if _, err := os.Lstat(p); err == nil {
		return nil
	}
	b, _ := json.Marshal(key)
	return writeAtomic(p, b)
}

// abort drops a stage.
func (st *stage) abort() { _ = RemoveAll(st.tmp) }

// commit renames the stage into place as the key's newest version: one
// rename(2), so a lookup sees the previous version or this one, never a
// part of either. The caller holds the store's lock: the version's name is
// taken there, so of two runs publishing one key the one that commits last
// is the newest.
func (st *stage) commit(m Meta) (Entry, error) {
	m.Key = st.key
	m.Size = DiskUsage(st.Data)
	if m.Created.IsZero() {
		m.Created = time.Now().UTC()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		st.abort()
		return Entry{}, err
	}
	if err := os.WriteFile(filepath.Join(st.tmp, "meta.json"), b, 0o600); err != nil {
		st.abort()
		return Entry{}, err
	}
	if err := os.WriteFile(filepath.Join(st.tmp, ".used"), nil, 0o600); err != nil {
		st.abort()
		return Entry{}, err
	}
	id := st.key.ID()
	n := time.Now().UnixNano()
	if vs := st.s.versions(id); len(vs) > 0 {
		// A clock step back never makes the newest copy older.
		if last, err := strconv.ParseInt(vs[0].Version[1:21], 10, 64); err == nil && last >= n {
			n = last + 1
		}
	}
	version := fmt.Sprintf("v%020d-%s", n, randHex())
	if err := os.Rename(st.tmp, filepath.Join(st.s.keyDir(id), version)); err != nil {
		st.abort()
		return Entry{}, err
	}
	return st.s.read(id, version)
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

// Size is the disk the store's published versions take, each file
// counted once (usage).
func (s *Store) Size() int64 { return usageOf(s.List()).total }

// Evict removes least-recently-used caches until the store is within max
// bytes, never one inUse reports. It returns what it removed.
func (s *Store) Evict(max int64, inUse func(dir string) bool) []Entry {
	if max < 0 {
		max = 0 // the space left once scratch is counted: everything unused goes
	}
	all := s.List()
	u := usageOf(all)
	// A version a newer one of its key superseded goes whatever the cap (no
	// launch restores it again) once no launch uses it; then, over the cap,
	// the least recently restored.
	newest := map[string]string{}
	for _, e := range all {
		if e.Version > newest[e.ID] {
			newest[e.ID] = e.Version
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := all[i].Version != newest[all[i].ID], all[j].Version != newest[all[j].ID]
		if si != sj {
			return si
		}
		return all[i].LastUsed.Before(all[j].LastUsed)
	})
	var out []Entry
	for _, e := range all {
		superseded := e.Version != newest[e.ID]
		if u.total <= max && !superseded {
			break
		}
		if inUse != nil && inUse(e.Dir) {
			continue
		}
		if s.Remove(e) == nil {
			u.remove(e.Dir)
			out = append(out, e)
		}
	}
	s.sweepTemp()
	return out
}

// sweepTemp removes build and removal leftovers a crash left (an hour old).
func (s *Store) sweepTemp() {
	ids, _ := os.ReadDir(s.keysRoot())
	for _, k := range ids {
		ents, _ := os.ReadDir(filepath.Join(s.keysRoot(), k.Name()))
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), ".tmp-") && !strings.HasPrefix(e.Name(), ".gone-") {
				continue
			}
			p := filepath.Join(s.keysRoot(), k.Name(), e.Name())
			if fi, err := os.Lstat(p); err == nil && time.Since(fi.ModTime()) > time.Hour {
				_ = RemoveAll(p)
			}
		}
	}
}

// DefaultKeyIdle is how long an empty key's directory stays.
const DefaultKeyIdle = 24 * time.Hour

// touchKey records a launch of key id (its key.json's mtime): an empty
// key's directory goes KeyIdle after its last launch.
func (s *Store) touchKey(id string) {
	now := time.Now()
	_ = os.Chtimes(filepath.Join(s.keyDir(id), "key.json"), now, now)
}

// sweepKeys removes the directory of every key that has no published
// version, no publish being built, and no launch entry naming it (named),
// and was last launched more than KeyIdle ago: a key new for every run (a
// per-PR key) leaves nothing behind once its versions are evicted. What
// the key's template learned stays (templates/). The caller holds the
// store's lock.
func (s *Store) sweepKeys(named map[string]bool) int {
	idle := s.KeyIdle
	if idle == 0 {
		idle = DefaultKeyIdle
	}
	ids, _ := os.ReadDir(s.keysRoot())
	n := 0
	for _, k := range ids {
		id := k.Name()
		if !k.IsDir() || !idRE.MatchString(id) || named[id] || len(s.versions(id)) > 0 {
			continue
		}
		busy := false
		ents, _ := os.ReadDir(s.keyDir(id))
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".tmp-") || versionRE.MatchString(e.Name()) {
				busy = true // a build in progress, or a version that did not read
				break
			}
		}
		if busy {
			continue
		}
		last := time.Time{}
		for _, f := range []string{filepath.Join(s.keyDir(id), "key.json"), s.keyDir(id)} {
			if fi, err := os.Lstat(f); err == nil && fi.ModTime().After(last) {
				last = fi.ModTime()
			}
		}
		if time.Since(last) < idle {
			continue
		}
		gone := filepath.Join(s.keysRoot(), ".gone-"+randHex())
		if os.Rename(s.keyDir(id), gone) == nil {
			_ = RemoveAll(gone)
			n++
		}
	}
	return n
}

// removeSuperseded removes the versions of a key older than its newest
// that no launch uses (inUse). The caller holds the store's lock.
func (s *Store) removeSuperseded(id string, inUse func(dir string) bool) {
	vs := s.versions(id)
	for _, e := range vs[min(1, len(vs)):] {
		if !inUse(e.Dir) {
			_ = s.Remove(e)
		}
	}
}

// legacyLayout reports a store entry an older conductor left (per-repo
// recipe caches, warm scratch, over-cap marks): never read by this one.
func legacyLayout(name string) bool {
	return strings.Contains(name, "~") || name == ".warm" || name == ".toobig"
}

// removeLegacy removes what an older conductor's recipe caches left in
// the store: this one never reads it, so it would hold disk for good.
func (s *Store) removeLegacy() []string {
	ents, _ := os.ReadDir(s.Dir)
	var out []string
	for _, e := range ents {
		if legacyLayout(e.Name()) {
			if RemoveAll(filepath.Join(s.Dir, e.Name())) == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

// DiskUsage is the disk a tree takes (allocated blocks where the platform
// reports them), never following a symlink: a file hard-linked more than
// once inside the tree counts once.
func DiskUsage(root string) int64 {
	var n int64
	seen := map[fileID]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if id, links, ok := idOf(fi); ok && links > 1 && !fi.IsDir() {
			if seen[id] {
				return nil
			}
			seen[id] = true
		}
		n += allocated(fi)
		return nil
	})
	return n
}

// usage is the disk a set of published versions takes, each file counted
// once however many of them hold it: a version built over another (a
// restore_from key's, or its own key's previous one) hard-links the
// files it kept, and the store's cap is about the disk, not the sum.
type usage struct {
	total int64
	// shared are files more than one link names: their size, and how
	// many of the counted versions hold them.
	shared map[fileID]*sharedFile
	// holds are each version's shared files; own the bytes only it has.
	holds map[string][]fileID
	own   map[string]int64
}

type sharedFile struct {
	size int64
	refs int
}

// usageOf counts entries' disk.
func usageOf(entries []Entry) *usage {
	u := &usage{shared: map[fileID]*sharedFile{}, holds: map[string][]fileID{}, own: map[string]int64{}}
	for _, e := range entries {
		dir := e.Dir
		mine := map[fileID]bool{}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			id, links, ok := idOf(fi)
			if !ok || links <= 1 || fi.IsDir() {
				u.own[dir] += allocated(fi)
				u.total += allocated(fi)
				return nil
			}
			if mine[id] {
				return nil
			}
			mine[id] = true
			u.holds[dir] = append(u.holds[dir], id)
			sf := u.shared[id]
			if sf == nil {
				sf = &sharedFile{size: allocated(fi)}
				u.shared[id] = sf
				u.total += sf.size
			}
			sf.refs++
			return nil
		})
	}
	return u
}

// remove takes a version out of the count and returns the disk that frees.
func (u *usage) remove(dir string) int64 {
	n := u.own[dir]
	delete(u.own, dir)
	for _, id := range u.holds[dir] {
		if sf := u.shared[id]; sf != nil {
			if sf.refs--; sf.refs == 0 {
				n += sf.size
				delete(u.shared, id)
			}
		}
	}
	delete(u.holds, dir)
	u.total -= n
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

// Publish builds a version of key — fill puts each home-relative path's
// content at data/<rel> under the directory it is given — and commits it as
// the key's newest. paths name what it holds.
func (s *Store) Publish(key Key, paths []string, fill func(data string) error) (Entry, error) {
	st, err := s.newStage(key)
	if err != nil {
		return Entry{}, err
	}
	if err := fill(st.Data); err != nil {
		st.abort()
		return Entry{}, err
	}
	var e Entry
	s.Locked(func() { e, err = st.commit(Meta{Paths: paths}) })
	return e, err
}

// KeyEntry is a key the store knows: its identity and what its template
// learned.
type KeyEntry struct {
	ID      string
	Key     Key
	Learned []string
}

// Keys are every key the store has a record of.
func (s *Store) Keys() []KeyEntry {
	ids, _ := os.ReadDir(s.keysRoot())
	var out []KeyEntry
	for _, d := range ids {
		if !d.IsDir() || !idRE.MatchString(d.Name()) {
			continue
		}
		var k Key
		b, err := os.ReadFile(filepath.Join(s.keyDir(d.Name()), "key.json"))
		if err != nil || json.Unmarshal(b, &k) != nil || k.ID() != d.Name() {
			continue
		}
		out = append(out, KeyEntry{ID: d.Name(), Key: k, Learned: s.Learned(k.TemplateID())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// Matches reports whether sel names k: its ID, its rendered value, or its
// value as shown (namespace:value).
func (k KeyEntry) Matches(sel string) bool {
	return sel == k.ID || sel == k.Key.Value || sel == k.Key.String()
}

// ClearKey removes a key whole — its versions and what it learned — when
// no launch uses any of its versions (inUse), or regardless with force.
// The caller holds the store's lock. kept: a version in use was left.
func (s *Store) ClearKey(id string, inUse func(dir string) bool, force bool) (removed []Entry, kept bool, err error) {
	for _, e := range s.versions(id) {
		if !force && inUse(e.Dir) {
			kept = true
			continue
		}
		if err := s.Remove(e); err != nil {
			return removed, kept, err
		}
		removed = append(removed, e)
	}
	if !kept {
		gone := filepath.Join(s.keysRoot(), ".gone-"+randHex())
		if err := os.Rename(s.keyDir(id), gone); err == nil {
			_ = RemoveAll(gone)
		}
	}
	return removed, kept, nil
}
