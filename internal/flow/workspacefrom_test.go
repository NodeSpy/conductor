package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/store"
)

// artifactRig is a flow rig whose built-in blob verbs and runner share one
// real blob store.
func artifactRig(t *testing.T, cfgYAML string) (*testRig, *blob.Store, string) {
	t.Helper()
	cfg := loadConfig(t, cfgYAML)
	dir := t.TempDir()
	bs, err := blob.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(cfg, connector.Deps{Secrets: testSecrets(nil), Config: cfg, Blobs: bs})
	if err != nil {
		t.Fatal(err)
	}
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Blobs = bs
	return rig, bs, dir
}

// One verb returns the tree as an archive (a staged binary output), and
// every parallel agent after it starts from that artifact: each dispatch
// carries the archive's path and asks for no checkout.
func TestWorkspaceFromHandsEveryAgentTheRunsArchive(t *testing.T) {
	staged := stagedFiles(t, "svc", "tree.tar")["tree.tar"]
	rig, _, blobDir := artifactRig(t, "connectors:\n  svc: { use: fake }\n")
	fake := newFakeState(t, "svc")
	fake.outputs["download"] = map[string]any{"body": staged}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: snap, uses: svc.download, options: { url: "x" } }
  - id: lens
    type: agent
    agent: reviewer
    prompt: "review"
    for_each: "{{.lenses}}"
    parallel: true
    workspace: { from: "{{.snap.body}}" }
`)
	run := store.WorkflowRun{ID: "run-from-1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"lenses": []any{"a", "b", "c"}}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	reqs := rig.Agents.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d dispatches, want 3", len(reqs))
	}
	archive := reqs[0].WorkspaceArchive
	for _, req := range reqs {
		if req.WorkspaceArchive == "" || req.WorkspaceArchive != archive {
			t.Errorf("dispatch %s: WorkspaceArchive %q, want the one stored archive %q", req.DispatchID, req.WorkspaceArchive, archive)
		}
		if req.Action.Checkout != "none" {
			t.Errorf("dispatch %s asks for checkout %q, want none", req.DispatchID, req.Action.Checkout)
		}
	}
	if filepath.Dir(filepath.Dir(archive)) != filepath.Clean(blobDir) {
		t.Errorf("WorkspaceArchive %q is not in the blob store", archive)
	}
	// GC'd with the run, like any of its artifacts.
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("the archive outlived its run: %v", err)
	}
}

// A step can only start from an artifact its own run holds — not another
// run's, even by digest.
func TestWorkspaceFromRefusesAnotherRunsArtifact(t *testing.T) {
	rig, bs, _ := artifactRig(t, "connectors:\n  svc: { use: fake }\n")
	newFakeState(t, "svc")
	h, err := bs.PutBytes("someone-elses-run", []byte("not yours"), blob.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: lens, type: agent, agent: reviewer, prompt: "review", workspace: { from: %q } }
`, h.Digest))
	runTriggerWithRun(rig, store.WorkflowRun{ID: "run-from-2", Outputs: map[string]map[string]any{}}, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "workspace.from") || !strings.Contains(errStr, "not referenced by run") {
		t.Fatalf("starting from another run's artifact: failed=%v %q", failed, errStr)
	}
	if n := len(rig.Agents.requests()); n != 0 {
		t.Fatalf("%d agent(s) dispatched on another run's artifact", n)
	}
}
