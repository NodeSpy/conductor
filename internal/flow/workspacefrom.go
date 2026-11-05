package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
)

// workspaceArchive resolves an agent step's `workspace: { from: … }` to the
// on-disk path of the directory archive the agent starts in, or "" when the
// step names none. The reference renders over the step's scope (a sole
// {{.x.blob}} keeps the handle as it is), and the blob must be one this run
// holds — the store authorizes it against the run's own blob owner, so a
// step can never start in another run's artifact.
func (r *Runner) workspaceArchive(ctx context.Context, step config.Step, data map[string]any) (string, error) {
	from := step.Workspace.From
	if from == "" {
		return "", nil
	}
	if agentAuthored(ctx) {
		return "", fmt.Errorf("workspace.from: refused on an agent-authored step")
	}
	path, _, err := r.resolveBlobRef(ctx, "workspace.from", from, data)
	return path, err
}

// resolveBlobRef resolves a step's blob reference (field names it for the
// error) over the step's scope — a sole {{.x.blob}} keeps the handle as it
// is — to the blob's on-disk path and handle. The blob must be one this run
// holds: the store authorizes it against the run's own blob owner.
func (r *Runner) resolveBlobRef(ctx context.Context, field, ref string, data map[string]any) (string, blob.Handle, error) {
	var v any = ref
	if p, ok := soleFieldRef(ref); ok {
		v, _ = lookupPath(data, p)
	} else if strings.Contains(ref, "{{") {
		s, err := render(ref, data)
		if err != nil {
			return "", blob.Handle{}, fmt.Errorf("%s: %w", field, err)
		}
		v = strings.TrimSpace(s)
	}
	h, ok := blob.FromScope(v)
	if !ok {
		return "", blob.Handle{}, fmt.Errorf("%s: %q did not resolve to a blob handle (a blob.put step's .blob output, or a verb's binary output)", field, ref)
	}
	if r.Blobs == nil {
		return "", blob.Handle{}, fmt.Errorf("%s: no blob store is configured", field)
	}
	path, err := r.Blobs.Path(blob.OwnerFrom(ctx), h.Digest)
	if err != nil {
		return "", blob.Handle{}, fmt.Errorf("%s: %w", field, err)
	}
	return path, h, nil
}
