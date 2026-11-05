package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// storeBlobOutputs stores declared binary-out outputs as run-scoped blobs,
// replacing each with the handle's scope value: raw []byte from a builtin,
// or a path from a plugin — a file it wrote under staging, its instance's
// staging directory, which is removed once stored.
func (r *Runner) storeBlobOutputs(ctx context.Context, decl connector.VerbDecl, outputs map[string]any, staging string) (map[string]any, error) {
	if len(decl.BinaryOut) == 0 || outputs == nil {
		return outputs, nil
	}
	if r.Blobs == nil {
		return nil, fmt.Errorf("blob: verb declares binary outputs but no blob store is configured")
	}
	runID := blob.OwnerFrom(ctx)
	for _, name := range decl.BinaryOut {
		meta := blob.Meta{Name: name}
		if mt, ok := outputs[name+"_media_type"].(string); ok {
			meta.MediaType = mt
		}
		var h blob.Handle
		var err error
		switch v := outputs[name].(type) {
		case []byte:
			h, err = r.Blobs.PutBytes(runID, v, meta)
		case string:
			if v == "" {
				continue
			}
			h, err = r.storeStagedFile(runID, staging, v, meta)
		default:
			continue // the verb returned nothing binary there this time
		}
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		outputs[name] = h.ScopeValue()
	}
	return outputs, nil
}

// storeStagedFile stores the file a plugin returned for a binary output and
// removes it. Only a regular file inside the calling instance's own staging
// directory (plugin-contract.md Q7), symlinks resolved, is accepted: a path
// anywhere else is refused and left alone.
func (r *Runner) storeStagedFile(runID, staging, path string, meta blob.Meta) (blob.Handle, error) {
	if staging == "" {
		return blob.Handle{}, fmt.Errorf("a binary output path from a connector with no staging directory")
	}
	dir, err := filepath.EvalSymlinks(staging)
	if err != nil {
		return blob.Handle{}, fmt.Errorf("staging directory: %w", err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.HasPrefix(real, dir+string(filepath.Separator)) {
		return blob.Handle{}, fmt.Errorf("%q is not a file in the connector's staging directory", path)
	}
	defer os.Remove(real)
	f, err := os.Open(real)
	if err != nil {
		return blob.Handle{}, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return blob.Handle{}, fmt.Errorf("%q is not a regular file", path)
	}
	meta.Name = filepath.Base(real) // the plugin's name for it, e.g. pr-7.tar
	return r.Blobs.Put(runID, f, meta)
}

// instanceStaging is the staging directory a connector instance's plugin
// writes its files to, or "" when it has none.
func instanceStaging(in *connector.Instance) string {
	sd, ok := in.Impl.(stagingDirer)
	if !ok {
		return ""
	}
	dir, err := sd.StagingDir()
	if err != nil {
		return ""
	}
	return dir
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

// keepCallStaging keeps a call's staging directory until its run ends, or
// removes it now outside a run.
func keepCallStaging(ctx context.Context, dir string) {
	if dir == "" {
		return
	}
	ri, ok := ctx.Value(runInstancesKey{}).(*runInstances)
	if !ok {
		removeCallStaging(dir)
		return
	}
	ri.mu.Lock()
	ri.calls = append(ri.calls, dir)
	ri.mu.Unlock()
}

func removeCallStaging(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// removeRunCallStaging removes the call staging directories the run's calls
// left.
func removeRunCallStaging(ctx context.Context) {
	ri, ok := ctx.Value(runInstancesKey{}).(*runInstances)
	if !ok {
		return
	}
	ri.mu.Lock()
	calls := ri.calls
	ri.calls = nil
	ri.mu.Unlock()
	for _, d := range calls {
		removeCallStaging(d)
	}
}
