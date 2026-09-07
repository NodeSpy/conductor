package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
)

func blobFixture(t *testing.T) (blobImpl, context.Context) {
	t.Helper()
	st, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	impl, err := newBlobImpl("blob", config.ConnectorRef{}, Deps{Blobs: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := blob.WithOwner(context.Background(), "run-1")
	return impl.(blobImpl), ctx
}

func TestBlobPutTextAndRead(t *testing.T) {
	b, ctx := blobFixture(t)
	out, err := b.Invoke(ctx, "put", map[string]any{"text": "hello artifact", "name": "note.txt", "media_type": "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := out["blob"].(map[string]any)
	if !ok || handle["name"] != "note.txt" || out["size"] != int64(14) {
		t.Fatalf("put outputs: %+v", out)
	}
	// The scope value is the handle, never the bytes.
	if _, hasText := handle["text"]; hasText {
		t.Fatal("bytes must not ride the scope value")
	}

	read, err := b.Invoke(ctx, "read", map[string]any{"blob": handle})
	if err != nil {
		t.Fatal(err)
	}
	if read["text"] != "hello artifact" {
		t.Fatalf("read: %+v", read)
	}

	stat, err := b.Invoke(ctx, "stat", map[string]any{"blob": handle})
	if err != nil {
		t.Fatal(err)
	}
	if stat["size"] != int64(14) || stat["media_type"] != "text/plain" {
		t.Fatalf("stat: %+v", stat)
	}
}

func TestBlobPutFileAndGet(t *testing.T) {
	b, ctx := blobFixture(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "build.tar")
	if err := os.WriteFile(src, []byte("binary\x00payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := b.Invoke(ctx, "put", map[string]any{"path": src})
	if err != nil {
		t.Fatal(err)
	}
	handle := out["blob"].(map[string]any)
	if handle["name"] != "build.tar" {
		t.Fatalf("default name from base: %+v", handle)
	}

	dst := filepath.Join(dir, "nested", "out.tar")
	got, err := b.Invoke(ctx, "get", map[string]any{"blob": handle, "path": dst})
	if err != nil {
		t.Fatal(err)
	}
	if got["path"] != dst || got["size"] != int64(14) {
		t.Fatalf("get outputs: %+v", got)
	}
	if bts, _ := os.ReadFile(dst); string(bts) != "binary\x00payload" {
		t.Fatalf("round trip: %q", bts)
	}
}

func TestBlobPutValidation(t *testing.T) {
	b, ctx := blobFixture(t)
	if _, err := b.Invoke(ctx, "put", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "exactly one of path or text") {
		t.Fatalf("neither: %v", err)
	}
	if _, err := b.Invoke(ctx, "put", map[string]any{"path": "/x", "text": "y"}); err == nil {
		t.Fatal("both must error")
	}
	if _, err := b.Invoke(ctx, "get", map[string]any{"blob": "junk", "path": "/tmp/x"}); err == nil {
		t.Fatal("junk handle must error")
	}
	// No store configured → plain error.
	impl, _ := newBlobImpl("blob", config.ConnectorRef{}, Deps{})
	if _, err := impl.Invoke(ctx, "put", map[string]any{"text": "x"}); err == nil ||
		!strings.Contains(err.Error(), "no blob store") {
		t.Fatalf("nil store: %v", err)
	}
}

func TestBlobReadCap(t *testing.T) {
	b, ctx := blobFixture(t)
	out, err := b.Invoke(ctx, "put", map[string]any{"text": strings.Repeat("x", 100)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Invoke(ctx, "read", map[string]any{"blob": out["blob"], "max_bytes": 10}); err == nil ||
		!strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("cap: %v", err)
	}
}

// Regression (#36 review H2): the blob verbs enforce run ownership — run B
// cannot get/read/stat run A's blob by digest.
func TestBlobVerbsEnforceRunOwnership(t *testing.T) {
	b, ctxA := blobFixture(t) // ctxA carries run-1
	out, err := b.Invoke(ctxA, "put", map[string]any{"text": "private to run-1"})
	if err != nil {
		t.Fatal(err)
	}
	handle := out["blob"]

	ctxB := blob.WithOwner(context.Background(), "run-B")
	for _, verb := range []string{"read", "stat"} {
		if _, err := b.Invoke(ctxB, verb, map[string]any{"blob": handle}); err == nil ||
			!strings.Contains(err.Error(), "not referenced by run run-B") {
			t.Fatalf("%s across runs must be denied: %v", verb, err)
		}
	}
	if _, err := b.Invoke(ctxB, "get", map[string]any{"blob": handle,
		"path": filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Fatal("get across runs must be denied")
	}
	// The owner still reads.
	if got, err := b.Invoke(ctxA, "read", map[string]any{"blob": handle}); err != nil ||
		got["text"] != "private to run-1" {
		t.Fatalf("owner read: %v %v", got, err)
	}
}

// Regression (#36 review H3): blob.put with `path:` stores the FILE's bytes,
// which the flow layer's option scan never sees — the content itself is
// scanned here, and tracked secret material refuses the put (inline text
// too, for every caller).
func TestBlobPutRefusesTrackedSecrets(t *testing.T) {
	st, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.New()
	sec.Track("s3kr1t-value")
	impl, _ := newBlobImpl("blob", config.ConnectorRef{}, Deps{Blobs: st, Secrets: sec})
	b := impl.(blobImpl)
	ctx := blob.WithOwner(context.Background(), "run-1")

	// A file whose bytes carry the tracked secret (the path string is clean).
	dir := t.TempDir()
	leaky := filepath.Join(dir, "app.env")
	if err := os.WriteFile(leaky, []byte("A=1\nTOKEN=s3kr1t-value\nB=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Invoke(ctx, "put", map[string]any{"path": leaky}); err == nil ||
		!strings.Contains(err.Error(), "tracked secret material") {
		t.Fatalf("path-based put of secret bytes must refuse: %v", err)
	}
	// Inline text with the secret refuses too.
	if _, err := b.Invoke(ctx, "put", map[string]any{"text": "here: s3kr1t-value"}); err == nil ||
		!strings.Contains(err.Error(), "tracked secret material") {
		t.Fatalf("inline put of secret text must refuse: %v", err)
	}
	// Clean content still stores; a secret straddling the chunk boundary is
	// caught by the overlap.
	if _, err := b.Invoke(ctx, "put", map[string]any{"text": "clean"}); err != nil {
		t.Fatalf("clean put: %v", err)
	}
	big := filepath.Join(dir, "big.bin")
	pad := make([]byte, (256<<10)-6) // secret starts 6 bytes before the first chunk ends
	for i := range pad {
		pad[i] = 'x'
	}
	if err := os.WriteFile(big, append(pad, []byte("s3kr1t-value tail")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Invoke(ctx, "put", map[string]any{"path": big}); err == nil ||
		!strings.Contains(err.Error(), "tracked secret material") {
		t.Fatalf("boundary-straddling secret must refuse: %v", err)
	}
}
