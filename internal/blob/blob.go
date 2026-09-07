// Package blob is the binary/artifact data type (#36 §21): content-addressed
// blobs in a disk store, referenced from the template/step scope by an opaque
// JSON-friendly handle instead of base64-in-JSON, streamed to the verbs that
// declare binary in/out, and GC'd with the run that produced them.
//
// A blob's bytes live once, at <dir>/sha256/<hex> (content addressing dedups
// identical artifacts across runs); each run holds references, persisted in
// <dir>/refs.json, and ReleaseRun drops a run's references and deletes any
// blob nothing references anymore. Files no run references (a crash between
// write and ref, an out-of-run put) are swept by age at boot.
//
// In scope, a blob is the map {"$blob": "sha256:<hex>", name, media_type,
// size} — metadata only, template-addressable ({{.steps.build.outputs
// .artifact.name}}), and subject to the same redaction as any other step
// output. The BYTES never enter the scope; redaction/egress rules apply to
// the metadata, not the content (see the wiki page).
package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Meta is a blob's scope-visible metadata.
type Meta struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size"`
}

// Handle references one stored blob.
type Handle struct {
	Digest string // "sha256:<hex>"
	Meta   Meta
}

// scopeKey marks a scope map as a blob handle.
const scopeKey = "$blob"

// ScopeValue renders the handle as the JSON-friendly scope map.
func (h Handle) ScopeValue() map[string]any {
	m := map[string]any{scopeKey: h.Digest, "size": h.Meta.Size}
	if h.Meta.Name != "" {
		m["name"] = h.Meta.Name
	}
	if h.Meta.MediaType != "" {
		m["media_type"] = h.Meta.MediaType
	}
	return m
}

// FromScope parses a scope value back into a handle: the {"$blob": …} map,
// or a bare "sha256:<hex>" string (a template that rendered just the digest).
func FromScope(v any) (Handle, bool) {
	switch x := v.(type) {
	case map[string]any:
		d, _ := x[scopeKey].(string)
		if !strings.HasPrefix(d, "sha256:") {
			return Handle{}, false
		}
		h := Handle{Digest: d}
		h.Meta.Name, _ = x["name"].(string)
		h.Meta.MediaType, _ = x["media_type"].(string)
		switch s := x["size"].(type) {
		case float64:
			h.Meta.Size = int64(s)
		case int64:
			h.Meta.Size = s
		case int:
			h.Meta.Size = int64(s)
		}
		return h, true
	case string:
		if strings.HasPrefix(x, "sha256:") && len(x) == len("sha256:")+64 {
			return Handle{Digest: x}, true
		}
	}
	return Handle{}, false
}

// Store is the content-addressed blob store.
type Store struct {
	dir string

	mu   sync.Mutex
	refs map[string]map[string]Meta // runID -> digest -> meta

	// released tombstones recently-released run IDs so Put can refuse a
	// reference under a run whose ReleaseRun already ran (#140 F3). Bounded by
	// a fixed-size ring because the store is long-lived (opened once at boot):
	// growing this with total run count would itself leak.
	released map[string]struct{}
	relRing  []string // FIFO ring backing `released`
	relNext  int      // next write slot in relRing
}

// maxReleasedTombstones bounds the recently-released run-ID set. Once
// ReleaseRun has run for a run, a later Put re-registering that run ID would
// create a blob no ReleaseRun will ever free (the run is gone) and that
// SweepOrphans will never reclaim (it still looks referenced) — a permanent
// leak. The tombstone set lets Put refuse such a put. The window that matters
// is a Put racing with, or shortly following, its run's teardown; a fixed cap
// covers that without the set growing unbounded over the daemon's lifetime.
const maxReleasedTombstones = 4096

// Open opens (creating if needed) a store rooted at dir and loads its refs.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sha256"), 0o700); err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	s := &Store{dir: dir, refs: map[string]map[string]Meta{}}
	b, err := os.ReadFile(s.refsPath())
	if err == nil {
		_ = json.Unmarshal(b, &s.refs) // a corrupt refs file degrades to age-based GC
	}
	return s, nil
}

func (s *Store) refsPath() string { return filepath.Join(s.dir, "refs.json") }

// pathFor maps a digest to its file ("sha256:<hex>" → <dir>/sha256/<hex>).
func (s *Store) pathFor(digest string) (string, error) {
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 || strings.ContainsAny(hex, "/\\.") {
		return "", fmt.Errorf("blob: bad digest %q", digest)
	}
	return filepath.Join(s.dir, "sha256", hex), nil
}

// Put streams r into the store, returning the content-addressed handle. A
// non-empty runID registers the run's reference (ReleaseRun frees it); an
// empty runID stores unreferenced — the age sweep is its only GC.
func (s *Store) Put(runID string, r io.Reader, meta Meta) (Handle, error) {
	tmp, err := os.CreateTemp(s.dir, "put-*")
	if err != nil {
		return Handle{}, fmt.Errorf("blob: %w", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Handle{}, fmt.Errorf("blob: put: %w", err)
	}
	meta.Size = size
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	path, _ := s.pathFor(digest)
	if _, statErr := os.Stat(path); statErr != nil {
		if err := os.Rename(tmp.Name(), path); err != nil {
			return Handle{}, fmt.Errorf("blob: put: %w", err)
		}
	}
	hd := Handle{Digest: digest, Meta: meta}
	if runID != "" {
		s.mu.Lock()
		if _, gone := s.released[runID]; gone {
			s.mu.Unlock()
			// The run has already been released (#140 F3): registering a
			// reference now would create a blob no ReleaseRun will ever free —
			// the run is gone — and that SweepOrphans will never reclaim because
			// it still looks referenced. Refuse the put. The already-written
			// file is left unreferenced for the age sweep to reclaim; we must
			// NOT delete it here — content addressing means a concurrent run may
			// share this exact digest. On the normal background path this is
			// unreachable (verb-steps run in the flow loop, which finishes before
			// finishRun → releaseRunBlobs); the guard is defense-in-depth.
			return Handle{}, fmt.Errorf("blob: run %s already released — refusing put that would orphan a permanently-referenced blob", runID)
		}
		if s.refs[runID] == nil {
			s.refs[runID] = map[string]Meta{}
		}
		s.refs[runID][digest] = meta
		err = s.saveRefsLocked()
		s.mu.Unlock()
		if err != nil {
			return hd, err
		}
	}
	return hd, nil
}

// PutBytes is Put over an in-memory value.
func (s *Store) PutBytes(runID string, b []byte, meta Meta) (Handle, error) {
	return s.Put(runID, bytes.NewReader(b), meta)
}

// authorize enforces blob ownership (#36 review H2): a digest alone is not a
// capability — knowing one (from `conductor runs`, the audit, the event
// stream) must not read another run's artifact. A caller may access a blob
// only when its OWN run holds a reference (the same-run/workflow scope —
// child workflows share the run id), or, for out-of-run callers (runID ""),
// when the blob is unreferenced by any run (an adhoc put).
func (s *Store) authorize(runID, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID != "" {
		if _, ok := s.refs[runID][digest]; ok {
			return nil
		}
		return fmt.Errorf("blob: %s is not referenced by run %s — a run may only read artifacts it owns", digest, runID)
	}
	if s.referencedLocked(digest) {
		return fmt.Errorf("blob: %s belongs to another run — out-of-run access denied", digest)
	}
	return nil
}

// Open returns a reader over a blob's bytes, authorized against the calling
// run (see authorize).
func (s *Store) Open(runID, digest string) (io.ReadCloser, error) {
	path, err := s.pathFor(digest)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(runID, digest); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("blob: %s: %w (GC'd with its run, or never stored here)", digest, err)
	}
	return f, nil
}

// Path returns the canonical on-disk path of a blob — the zero-copy way to
// hand its bytes to a verb or write them somewhere — authorized against the
// calling run (see authorize). The file is immutable (content-addressed);
// consumers must treat it read-only.
func (s *Store) Path(runID, digest string) (string, error) {
	path, err := s.pathFor(digest)
	if err != nil {
		return "", err
	}
	if err := s.authorize(runID, digest); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("blob: %s: %w (GC'd with its run, or never stored here)", digest, err)
	}
	return path, nil
}

// ReleaseRun drops a run's references (#36 §21 "GC'd with the run") and
// deletes every blob no other run references anymore.
func (s *Store) ReleaseRun(runID string) error {
	if runID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Tombstone the run even if it held no blobs, so a later Put under this run
	// ID is refused rather than silently creating a permanently-orphaned ref
	// (#140 F3) — ReleaseRun runs once per run and will not run again.
	s.markReleasedLocked(runID)
	released := s.refs[runID]
	if released == nil {
		return nil
	}
	delete(s.refs, runID)
	for digest := range released {
		if s.referencedLocked(digest) {
			continue
		}
		if path, err := s.pathFor(digest); err == nil {
			_ = os.Remove(path)
		}
	}
	return s.saveRefsLocked()
}

// markReleasedLocked records runID in the bounded released-tombstone ring,
// evicting the oldest entry when the ring is full. Caller holds s.mu.
func (s *Store) markReleasedLocked(runID string) {
	if s.released == nil {
		s.released = make(map[string]struct{}, maxReleasedTombstones)
		s.relRing = make([]string, maxReleasedTombstones)
	}
	if _, ok := s.released[runID]; ok {
		return
	}
	if old := s.relRing[s.relNext]; old != "" {
		delete(s.released, old)
	}
	s.relRing[s.relNext] = runID
	s.relNext = (s.relNext + 1) % maxReleasedTombstones
	s.released[runID] = struct{}{}
}

// referencedLocked reports whether any run still references digest.
func (s *Store) referencedLocked(digest string) bool {
	for _, m := range s.refs {
		if _, ok := m[digest]; ok {
			return true
		}
	}
	return false
}

// SweepOrphans deletes unreferenced blob files older than maxAge — the
// backstop for out-of-run puts and crashes between write and ref. Returns
// how many files it removed.
func (s *Store) SweepOrphans(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "sha256"))
	if err != nil {
		return 0, err
	}
	cut := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		digest := "sha256:" + e.Name()
		if s.referencedLocked(digest) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cut) {
			continue
		}
		if os.Remove(filepath.Join(s.dir, "sha256", e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// saveRefsLocked persists the reference index (temp+rename).
func (s *Store) saveRefsLocked() error {
	b, err := json.MarshalIndent(s.refs, "", " ")
	if err != nil {
		return err
	}
	tmp := s.refsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.refsPath())
}
