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
	var v any = from
	if p, ok := soleFieldRef(from); ok {
		v, _ = lookupPath(data, p)
	} else if strings.Contains(from, "{{") {
		s, err := render(from, data)
		if err != nil {
			return "", fmt.Errorf("workspace.from: %w", err)
		}
		v = strings.TrimSpace(s)
	}
	h, ok := blob.FromScope(v)
	if !ok {
		return "", fmt.Errorf("workspace.from: %q did not resolve to a blob handle (a blob.put step's .blob output)", from)
	}
	if r.Blobs == nil {
		return "", fmt.Errorf("workspace.from: no blob store is configured")
	}
	path, err := r.Blobs.Path(blob.OwnerFrom(ctx), h.Digest)
	if err != nil {
		return "", fmt.Errorf("workspace.from: %w", err)
	}
	return path, nil
}
