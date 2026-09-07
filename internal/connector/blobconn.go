package connector

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
)

// blobDecl declares the built-in binary/artifact verbs (#36 §21). Like
// kv/sql/memory the connector is always available (the name is reserved) and
// needs no connection block: the daemon owns one content-addressed store
// beside the state file. A blob is referenced in scope by the opaque
// {"$blob": "sha256:…", name, media_type, size} handle — the bytes never
// enter the JSON scope, and the handle's METADATA flows through the same
// redaction as any step output.
//
// blob.put reads a file from conductor's own filesystem (or inline text) —
// treat it like `cli` in policy.agent_authored.allow: an agent-authored plan
// granted blob.put/get can read/write files as the daemon's user.
var blobDecl = &TypeDecl{
	Type: "blob",
	Desc: "Built-in content-addressed artifacts: files pass between steps/agents as opaque handles, GC'd with the run.",
	Verbs: []VerbDecl{
		{
			Name: "put", Desc: "store a file (or inline text) as a run-scoped blob",
			Options: Schema{
				"path":       {Type: TString, Desc: "local file to ingest (mutually exclusive with text)"},
				"text":       {Type: TString, Desc: "inline content to store"},
				"name":       {Type: TString, Desc: "artifact name (defaults to the file's base name)"},
				"media_type": {Type: TString, Desc: "MIME type (informational)"},
			},
			Outputs: Schema{
				"blob":   {Type: TMap, Desc: "the opaque handle: pass it to later steps"},
				"digest": {Type: TString},
				"size":   {Type: TInt},
			},
		},
		{
			Name: "get", Desc: "write a blob's bytes to a local path (e.g. into an agent's worktree)",
			Options: Schema{
				"blob": {Type: TAny, Required: true, Desc: "a blob handle (or its sha256:… digest)"},
				"path": {Type: TString, Required: true, Desc: "destination file (parent dirs created)"},
			},
			Outputs: Schema{
				"path": {Type: TString},
				"size": {Type: TInt},
			},
		},
		{
			Name: "read", Desc: "read a (text) blob's content into the scope",
			Options: Schema{
				"blob":      {Type: TAny, Required: true},
				"max_bytes": {Type: TInt, Desc: "size guard (default 1MiB; a larger blob errors)"},
			},
			Outputs: Schema{
				"text": {Type: TString},
				"size": {Type: TInt},
			},
		},
		{
			Name: "stat", Desc: "a blob's metadata",
			Options: Schema{
				"blob": {Type: TAny, Required: true},
			},
			Outputs: Schema{
				"digest":     {Type: TString},
				"name":       {Type: TString},
				"media_type": {Type: TString},
				"size":       {Type: TInt},
			},
		},
	},
}

func init() { RegisterType(blobDecl, newBlobImpl) }

type blobImpl struct {
	store *blob.Store
}

func newBlobImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return blobImpl{store: deps.Blobs}, nil
}

func (blobImpl) Validate() error          { return nil }
func (blobImpl) DeclaredEvents() []string { return nil }
func (blobImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("blob: the built-in artifact verbs have no events — nothing to put in on:")
}

// blobReadCap is blob.read's default size guard: text going into the JSON
// scope should be prose-sized, not an artifact dump.
const blobReadCap = 1 << 20

func (b blobImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if b.store == nil {
		return nil, fmt.Errorf("blob: no blob store is configured in this context")
	}
	runID := memory.SourceFrom(ctx).Run
	switch verb {
	case "put":
		return b.put(ctx, opts)
	case "get":
		return b.get(runID, opts)
	case "read":
		return b.read(runID, opts)
	case "stat":
		return b.stat(runID, opts)
	}
	return nil, fmt.Errorf("blob: unknown verb %q", verb)
}

func (b blobImpl) put(ctx context.Context, opts map[string]any) (map[string]any, error) {
	path, _ := opts["path"].(string)
	text, hasText := opts["text"].(string)
	if (path != "") == hasText { // both, or neither
		return nil, fmt.Errorf("blob.put: set exactly one of path or text")
	}
	meta := blob.Meta{}
	meta.Name, _ = opts["name"].(string)
	meta.MediaType, _ = opts["media_type"].(string)
	runID := memory.SourceFrom(ctx).Run

	var h blob.Handle
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("blob.put: %w", err)
		}
		defer f.Close()
		if meta.Name == "" {
			meta.Name = filepath.Base(path)
		}
		if h, err = b.store.Put(runID, f, meta); err != nil {
			return nil, err
		}
	} else {
		var err error
		if h, err = b.store.PutBytes(runID, []byte(text), meta); err != nil {
			return nil, err
		}
	}
	return map[string]any{"blob": h.ScopeValue(), "digest": h.Digest, "size": h.Meta.Size}, nil
}

// handleArg parses the "blob" option (a handle map or bare digest string).
func handleArg(opts map[string]any) (blob.Handle, error) {
	h, ok := blob.FromScope(opts["blob"])
	if !ok {
		return blob.Handle{}, fmt.Errorf("blob: the blob option must be a blob handle (a step's .blob output) or a sha256:… digest")
	}
	return h, nil
}

func (b blobImpl) get(runID string, opts map[string]any) (map[string]any, error) {
	h, err := handleArg(opts)
	if err != nil {
		return nil, err
	}
	dst, _ := opts["path"].(string)
	if dst == "" {
		return nil, fmt.Errorf("blob.get: path is required")
	}
	src, err := b.store.Open(runID, h.Digest)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("blob.get: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("blob.get: %w", err)
	}
	n, err := io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("blob.get: %w", err)
	}
	return map[string]any{"path": dst, "size": n}, nil
}

func (b blobImpl) read(runID string, opts map[string]any) (map[string]any, error) {
	h, err := handleArg(opts)
	if err != nil {
		return nil, err
	}
	cap := int64(blobReadCap)
	if v, ok := opts["max_bytes"].(int); ok && v > 0 {
		cap = int64(v)
	} else if v, ok := opts["max_bytes"].(float64); ok && v > 0 {
		cap = int64(v)
	}
	src, err := b.store.Open(runID, h.Digest)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	buf, err := io.ReadAll(io.LimitReader(src, cap+1))
	if err != nil {
		return nil, fmt.Errorf("blob.read: %w", err)
	}
	if int64(len(buf)) > cap {
		return nil, fmt.Errorf("blob.read: blob exceeds max_bytes (%d) — use blob.get to write it to a file instead", cap)
	}
	return map[string]any{"text": string(buf), "size": len(buf)}, nil
}

func (b blobImpl) stat(runID string, opts map[string]any) (map[string]any, error) {
	h, err := handleArg(opts)
	if err != nil {
		return nil, err
	}
	// Size/existence from disk (the handle's meta rides the scope, but the
	// store is the truth for existence after GC).
	path, err := b.store.Path(runID, h.Digest)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"digest": h.Digest, "name": h.Meta.Name,
		"media_type": h.Meta.MediaType, "size": info.Size(),
	}, nil
}
