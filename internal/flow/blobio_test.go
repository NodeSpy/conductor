package flow

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

const blobFlowCfg = `
connectors:
  svc: { type: fake }
`

// blobRig wires a flow rig with a real blob store.
func blobRig(t *testing.T) (*testRig, *fakeState, *blob.Store) {
	t.Helper()
	cfg := loadConfig(t, blobFlowCfg)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	bs, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rig.Runner.Blobs = bs
	return rig, fake, bs
}

func TestBinaryOutBecomesHandleAndGCsWithRun(t *testing.T) {
	rig, fake, bs := blobRig(t)
	fake.outputs["download"] = map[string]any{"body": []byte("PDF-ish bytes"), "body_media_type": "application/pdf"}
	var handle map[string]any
	fake.failIf["post"] = func(opts map[string]any) bool {
		// Capture what the later step saw for "meta" (the handle relayed
		// through scope) — never the bytes.
		handle, _ = opts["meta"].(map[string]any)
		return false
	}

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: dl, uses: svc.download, options: { url: "http://x/report.pdf" } }
  - { id: send, uses: svc.post, options: { text: "got {{.dl.body.size}} bytes", meta: "{{.dl.body}}" } }
`)
	run := store.WorkflowRun{ID: "run-blob-1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	// The later step received the handle map (digest + metadata), not bytes.
	digest, _ := handle["$blob"].(string)
	if !strings.HasPrefix(digest, "sha256:") || handle["media_type"] != "application/pdf" {
		t.Fatalf("relayed handle: %+v", handle)
	}
	if handle["size"] != int64(13) {
		t.Fatalf("handle size: %+v", handle)
	}
	// The templated size rendered off the handle's metadata.
	calls := fake.snapshot()
	post := calls[len(calls)-1]
	if post.Opts["text"] != "got 13 bytes" {
		t.Fatalf("templated metadata: %+v", post.Opts)
	}
	// The run finished → its artifacts are GC'd with it.
	if _, err := bs.Open("run-blob-1", digest); err == nil {
		t.Fatal("blob must be GC'd with the run")
	}
}

// REGRESSION (H2): blob ownership keyed on the STABLE run-dedup id (run.ID =
// kind:key, unchanged across every trigger of a target) let the first
// execution's ReleaseRun tombstone that id, so the SECOND trigger of the same
// target could never Put again — a re-trigger DoS. Blobs now own a
// per-EXECUTION namespace: two sequential Run()s sharing one run.ID both Put.
func TestReTriggerSameRunIDCanStillPutBlobs(t *testing.T) {
	rig, fake, _ := blobRig(t)
	fake.outputs["download"] = map[string]any{"body": []byte("PDF-ish bytes"), "body_media_type": "application/pdf"}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: dl, uses: svc.download, options: { url: "http://x/report.pdf" } }
`)
	// Both executions share the SAME run-dedup id — pre-fix the second put hit
	// the first execution's release tombstone ("already released") and failed.
	for _, tag := range []string{"first", "second"} {
		run := store.WorkflowRun{ID: "gh:o/r#1", Outputs: map[string]map[string]any{}}
		runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
		if failed, errStr := rig.workflowFailed(); failed {
			t.Fatalf("%s execution of the same run-dedup id must Put its blob, got: %s", tag, errStr)
		}
	}
}

// REGRESSION (H2, preserves #140 F3): a late Put AFTER this execution's own
// ReleaseRun is still refused — the per-execution namespace is one-shot, so a
// backgrounded agent racing its run's teardown can't orphan a ref.
func TestPostReleasePutWithinExecutionRefused(t *testing.T) {
	bs, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const owner = "r-exec-1" // a single execution's blob owner id
	if _, err := bs.PutBytes(owner, []byte("a"), blob.Meta{}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := bs.ReleaseRun(owner); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := bs.PutBytes(owner, []byte("b"), blob.Meta{}); err == nil || !strings.Contains(err.Error(), "already released") {
		t.Fatalf("post-release put within one execution must be refused, got: %v", err)
	}
}

func TestBinaryInStagesPath(t *testing.T) {
	rig, fake, _ := blobRig(t)
	// A blob produced earlier IN THIS execution (owner = the execution's blob
	// id, post-H2) is staged to a real on-disk path for a binary-in verb.
	fake.outputs["download"] = map[string]any{"body": []byte("upload me")}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: dl, uses: svc.download, options: { url: "http://x/u.bin" } }
  - { id: up, uses: svc.upload, options: { file: "{{.dl.body}}" } }
`)
	run := store.WorkflowRun{ID: "run-blob-2", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// The upload verb received a real on-disk path carrying the blob's bytes.
	calls := fake.snapshot()
	up := calls[len(calls)-1]
	path, _ := up.Opts["file"].(string)
	if path == "" || strings.Contains(path, "$blob") {
		t.Fatalf("staged input: %v", up.Opts["file"])
	}
	if b, rerr := os.ReadFile(path); rerr == nil {
		if string(b) != "upload me" {
			t.Fatalf("staged content: %q", b)
		}
	}
	// (The path was read DURING the run; the run's end GC'd it — expected.)
}

func TestBinaryInPassesPlainStringsThrough(t *testing.T) {
	rig, fake, _ := blobRig(t)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: up, uses: svc.upload, options: { file: "/tmp/ordinary-local-path" } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if got := fake.snapshot()[0].Opts["file"]; got != "/tmp/ordinary-local-path" {
		t.Fatalf("plain path must pass through: %v", got)
	}
}

func TestBinaryIOWithoutStoreFailsPlainly(t *testing.T) {
	rig, fake, _ := blobRig(t)
	rig.Runner.Blobs = nil
	fake.outputs["download"] = map[string]any{"body": []byte("x")}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: dl, uses: svc.download }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "no blob store") {
		t.Fatalf("binary out without a store: %v %q", failed, errStr)
	}
}

func TestPlanBarrierGatesBlobPut(t *testing.T) {
	// blob.put is an internal value write: an unapproved agent plan may not
	// park tracked secret material in the artifact store.
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
policy:
  agent_authored:
    allow: [ blob.put, agent ]
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	bs, _ := blob.Open(t.TempDir())
	rig.Runner.Blobs = bs
	rig.Runner.SecretVals = map[string]string{"tok": "s3kr1t-value"}
	rig.Runner.Secrets.Track("s3kr1t-value")

	// The value arrives laundered through the trigger context (the static
	// guard can't see it) — the runtime write barrier must still refuse.
	out := "```plan\n- id: park\n  uses: blob.put\n  options: { text: \"stash {{.msg}}\" }\n```"
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: out}, nil
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "s3kr1t-value"}), mustSpec(t, planSpec))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("plan write barrier must gate blob.put: %v %q", failed, errStr)
	}
}
