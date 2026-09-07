package flow

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/connector"
)

// blobOwnerNonce disambiguates blob owner ids minted for executions with no
// history record (shadow/dry runs), so two such runs of one dedup key never
// collide on a blob namespace.
var blobOwnerNonce int64

// blobOwnerID returns a per-EXECUTION blob owner id, unique to this Run()
// invocation (H2). The run's history id (r<nano>, minted once per Run in
// beginHistory) already has that property, so reuse it when present; with no
// history record fall back to the stable run id plus a process-unique nonce.
// Threaded on the context via blob.WithOwner so a re-trigger sharing a stable
// run-dedup key gets a fresh, releasable namespace instead of one the first
// execution's ReleaseRun permanently tombstoned.
func blobOwnerID(runID string, hist *histRec) string {
	if hist != nil && hist.rec.ID != "" {
		return hist.rec.ID
	}
	if runID == "" {
		return ""
	}
	return runID + "#" + strconv.FormatInt(atomic.AddInt64(&blobOwnerNonce, 1), 36)
}

// Verb-level binary IO (#36 §21). A verb whose declaration names BinaryIn
// options receives each as the blob's local (immutable, content-addressed)
// file path instead of the scope handle; a verb naming BinaryOut outputs
// returns raw []byte there, which the runner stores as a run-scoped blob and
// replaces with the opaque handle before the value enters the JSON scope.
// Neither direction ever puts artifact bytes into the template scope, the
// checkpoint file, or the audit.

// stageBlobInputs resolves declared binary-in options: a blob handle (the
// {"$blob": …} map or a bare digest string) becomes the blob's on-disk path,
// authorized against the calling run (#36 review H2). A plain string that
// isn't a handle passes through — the option may accept an ordinary local
// path too.
func (r *Runner) stageBlobInputs(ctx context.Context, decl connector.VerbDecl, opts map[string]any) (map[string]any, error) {
	if len(decl.BinaryIn) == 0 {
		return opts, nil
	}
	if r.Blobs == nil {
		return nil, fmt.Errorf("blob: verb declares binary inputs but no blob store is configured")
	}
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		out[k] = v
	}
	for _, name := range decl.BinaryIn {
		v, ok := out[name]
		if !ok {
			continue
		}
		h, isHandle := blob.FromScope(v)
		if !isHandle {
			continue
		}
		path, err := r.Blobs.Path(blob.OwnerFrom(ctx), h.Digest)
		if err != nil {
			return nil, fmt.Errorf("option %q: %w", name, err)
		}
		out[name] = path
	}
	return out, nil
}

// storeBlobOutputs stores declared binary-out outputs ([]byte) as run-scoped
// blobs, replacing the bytes with the handle's scope value.
func (r *Runner) storeBlobOutputs(ctx context.Context, decl connector.VerbDecl, outputs map[string]any) (map[string]any, error) {
	if len(decl.BinaryOut) == 0 || outputs == nil {
		return outputs, nil
	}
	if r.Blobs == nil {
		return nil, fmt.Errorf("blob: verb declares binary outputs but no blob store is configured")
	}
	runID := blob.OwnerFrom(ctx)
	for _, name := range decl.BinaryOut {
		raw, ok := outputs[name].([]byte)
		if !ok {
			continue // the verb returned nothing binary there this time
		}
		meta := blob.Meta{Name: name}
		if mt, ok := outputs[name+"_media_type"].(string); ok {
			meta.MediaType = mt
		}
		h, err := r.Blobs.PutBytes(runID, raw, meta)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		outputs[name] = h.ScopeValue()
	}
	return outputs, nil
}

// releaseRunBlobs GC's the run's artifacts with the run (#36 §21).
func (r *Runner) releaseRunBlobs(runID string) {
	if r.Blobs == nil || runID == "" {
		return
	}
	if err := r.Blobs.ReleaseRun(runID); err != nil {
		r.Log("blob: release run %s: %v", runID, err)
	}
}
