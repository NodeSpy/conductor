package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
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
	ctx := memory.WithSource(context.Background(), memory.Source{Run: "run-1"})
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
