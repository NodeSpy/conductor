package blob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPutOpenRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.PutBytes("run-1", []byte("artifact bytes"), Meta{Name: "a.bin", MediaType: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.Digest, "sha256:") || h.Meta.Size != 14 {
		t.Fatalf("handle: %+v", h)
	}
	rc, err := s.Open("run-1", h.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := make([]byte, 64)
	n, _ := rc.Read(buf)
	if string(buf[:n]) != "artifact bytes" {
		t.Fatalf("content: %q", buf[:n])
	}
	// Path serves the same file.
	p, err := s.Path("run-1", h.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "artifact bytes" {
		t.Fatalf("path content: %q", b)
	}
}

func TestContentAddressingDedups(t *testing.T) {
	s, _ := Open(t.TempDir())
	h1, _ := s.PutBytes("run-1", []byte("same"), Meta{Name: "a"})
	h2, _ := s.PutBytes("run-2", []byte("same"), Meta{Name: "b"})
	if h1.Digest != h2.Digest {
		t.Fatalf("same content, different digests: %s vs %s", h1.Digest, h2.Digest)
	}
	// One file on disk.
	entries, _ := os.ReadDir(filepath.Join(s.dir, "sha256"))
	if len(entries) != 1 {
		t.Fatalf("files: %d", len(entries))
	}
	// Each handle keeps its own metadata.
	if h1.Meta.Name != "a" || h2.Meta.Name != "b" {
		t.Fatalf("metas: %+v %+v", h1.Meta, h2.Meta)
	}
}

func TestReleaseRunGC(t *testing.T) {
	s, _ := Open(t.TempDir())
	shared, _ := s.PutBytes("run-1", []byte("shared"), Meta{})
	_, _ = s.PutBytes("run-2", []byte("shared"), Meta{}) // second ref, same digest
	only1, _ := s.PutBytes("run-1", []byte("only run 1"), Meta{})

	if err := s.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	// run-1's exclusive blob is gone; the shared one survives via run-2.
	if _, err := s.Open("run-1", only1.Digest); err == nil {
		t.Fatal("run-1's exclusive blob must be GC'd with the run")
	}
	if _, err := s.Open("run-2", shared.Digest); err != nil {
		t.Fatalf("shared blob must survive run-2's reference: %v", err)
	}
	if err := s.ReleaseRun("run-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("run-2", shared.Digest); err == nil {
		t.Fatal("last reference released — blob must be deleted")
	}
	// Releasing an unknown run is a no-op.
	if err := s.ReleaseRun("ghost"); err != nil {
		t.Fatal(err)
	}
}

// A Put that lands after its run was released must not create a blob that is
// permanently referenced by a dead run — nothing would ever free it (#140 F3).
// The put is refused and the already-written file is left for the age sweep.
func TestPutAfterReleaseRefused(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.PutBytes("run-1", []byte("first"), Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	// A later put re-registering the released run must be refused.
	late, err := s.PutBytes("run-1", []byte("late artifact"), Meta{})
	if err == nil {
		t.Fatal("put under an already-released run must be refused")
	}
	if late != (Handle{}) {
		t.Fatalf("refused put must not return a handle: %+v", late)
	}
	// The file the refused put wrote is unreferenced, so the age sweep reclaims
	// it — it is not left permanently referenced by the dead run.
	n, err := s.SweepOrphans(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("refused put's file must be sweepable as an orphan, swept %d", n)
	}
	// A never-used run is tombstoned by ReleaseRun too: a put after it is
	// refused, not silently orphaned.
	if err := s.ReleaseRun("run-empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutBytes("run-empty", []byte("x"), Meta{}); err == nil {
		t.Fatal("put under a released never-used run must be refused")
	}
	// A live, never-released run is unaffected — the guard is per-run.
	if _, err := s.PutBytes("run-2", []byte("live"), Meta{}); err != nil {
		t.Fatalf("put under a live run must still work: %v", err)
	}
}

func TestRefsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	h, _ := s.PutBytes("run-1", []byte("persisted"), Meta{})

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The reopened store still knows run-1's reference: sweep spares it...
	if n, _ := s2.SweepOrphans(0); n != 0 {
		t.Fatalf("sweep removed referenced blobs: %d", n)
	}
	// ...and ReleaseRun still GCs it.
	if err := s2.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Open("run-1", h.Digest); err == nil {
		t.Fatal("release after reopen must GC")
	}
}

func TestSweepOrphans(t *testing.T) {
	s, _ := Open(t.TempDir())
	// An out-of-run put is unreferenced.
	h, _ := s.PutBytes("", []byte("orphan"), Meta{})
	ref, _ := s.PutBytes("run-1", []byte("referenced"), Meta{})

	// Young orphans survive an age-bounded sweep.
	if n, _ := s.SweepOrphans(time.Hour); n != 0 {
		t.Fatalf("young orphan swept: %d", n)
	}
	// A zero-age sweep takes the orphan but not the referenced blob.
	n, err := s.SweepOrphans(0)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, err := s.Open("", h.Digest); err == nil {
		t.Fatal("orphan must be swept")
	}
	if _, err := s.Open("run-1", ref.Digest); err != nil {
		t.Fatalf("referenced blob must survive: %v", err)
	}
}

func TestScopeValueRoundTrip(t *testing.T) {
	h := Handle{Digest: "sha256:" + strings.Repeat("ab", 32),
		Meta: Meta{Name: "r.pdf", MediaType: "application/pdf", Size: 42}}
	v := h.ScopeValue()
	if v["$blob"] != h.Digest || v["name"] != "r.pdf" || v["size"] != int64(42) {
		t.Fatalf("scope value: %v", v)
	}
	back, ok := FromScope(v)
	if !ok || back.Digest != h.Digest || back.Meta != h.Meta {
		t.Fatalf("round trip: %+v %v", back, ok)
	}
	// JSON numbers come back as float64 — still parses.
	v["size"] = float64(42)
	if back, ok := FromScope(v); !ok || back.Meta.Size != 42 {
		t.Fatalf("float size: %+v", back)
	}
	// A bare digest string parses; junk doesn't.
	if _, ok := FromScope(h.Digest); !ok {
		t.Fatal("bare digest must parse")
	}
	for _, junk := range []any{"not a digest", map[string]any{"x": 1}, 42, nil,
		map[string]any{"$blob": "md5:zzz"}} {
		if _, ok := FromScope(junk); ok {
			t.Fatalf("junk parsed: %v", junk)
		}
	}
}

func TestBadDigestRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, d := range []string{"sha256:../../../etc/passwd", "sha256:short", "plain", "sha256:" + strings.Repeat("a", 63) + "/"} {
		if _, err := s.Open("", d); err == nil {
			t.Fatalf("bad digest accepted: %q", d)
		}
	}
}

// Regression (#36 review H2): a digest is NOT a capability. A run may only
// read blobs its own run references; another run — or an out-of-run caller —
// that learned the digest (audit, event stream, `conductor runs`) is denied.
func TestBlobOwnershipEnforced(t *testing.T) {
	s, _ := Open(t.TempDir())
	h, err := s.PutBytes("run-A", []byte("run A's artifact"), Meta{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// The owner reads fine.
	if _, err := s.Open("run-A", h.Digest); err != nil {
		t.Fatalf("owner read: %v", err)
	}
	// Run B, armed with the digest, is denied — Open and Path both.
	if _, err := s.Open("run-B", h.Digest); err == nil ||
		!strings.Contains(err.Error(), "not referenced by run run-B") {
		t.Fatalf("cross-run Open must be denied: %v", err)
	}
	if _, err := s.Path("run-B", h.Digest); err == nil {
		t.Fatal("cross-run Path must be denied")
	}
	// An out-of-run caller is denied a run-owned blob too…
	if _, err := s.Open("", h.Digest); err == nil ||
		!strings.Contains(err.Error(), "out-of-run access denied") {
		t.Fatalf("out-of-run read of a run's blob: %v", err)
	}
	// …but may read a truly adhoc (unreferenced) blob.
	adhoc, _ := s.PutBytes("", []byte("adhoc"), Meta{})
	if _, err := s.Open("", adhoc.Digest); err != nil {
		t.Fatalf("adhoc read: %v", err)
	}
	// Run B putting the SAME content earns its own reference (content
	// addressing shares the file; ownership is per run).
	h2, _ := s.PutBytes("run-B", []byte("run A's artifact"), Meta{})
	if _, err := s.Open("run-B", h2.Digest); err != nil {
		t.Fatalf("run B's own put must be readable: %v", err)
	}
}
