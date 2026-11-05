package flow

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// treeTar writes a small tree as a tar archive file and returns its path.
func treeTar(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "build.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range []struct{ name, body string }{{"bin/", ""}, {"bin/app", "app v1\n"}, {"README.md", "built\n"}} {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	f.Close()
	return p
}

// inputsRig is an artifact rig whose runner makes step inputs under a
// directory the test can watch.
func inputsRig(t *testing.T) (*testRig, *blob.Store, string) {
	t.Helper()
	rig, bs, _ := artifactRig(t, "connectors:\n  svc: { use: fake }\n")
	newFakeState(t, "svc")
	rig.Runner.InputsDir = filepath.Join(t.TempDir(), "step-inputs")
	return rig, bs, rig.Runner.InputsDir
}

func emptyDir(t *testing.T, what, dir string) {
	t.Helper()
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("%s holds %d entr(ies) after the step", what, len(ents))
	}
}

// An agent step with inputs and no `from:`: a normal workspace, plus a fresh
// copy of each artifact in a directory of the step's own — an archive
// unpacked, a file copied as itself — that the agent is told about, can
// template, and that is gone when the step ends.
func TestWorkspaceInputsReachAnAgentBesideItsWorkspace(t *testing.T) {
	rig, _, inputsDir := inputsRig(t)
	archive := treeTar(t)
	var seen []dispatch.Input
	var files = map[string]string{}
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		seen = req.Inputs
		for _, in := range req.Inputs {
			p := in.Path
			if in.Kind == "dir" {
				p = filepath.Join(p, "bin", "app")
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return dispatch.RunRef{}, err
			}
			files[in.Name] = string(b)
			if filepath.Dir(filepath.Dir(in.Path)) != req.InputsDir && filepath.Dir(in.Path) != req.InputsDir {
				t.Errorf("input %s at %s is not in the step's inputs dir %s", in.Name, in.Path, req.InputsDir)
			}
		}
		if filepath.Dir(req.InputsDir) != inputsDir {
			t.Errorf("InputsDir %q is not a child of %s", req.InputsDir, inputsDir)
		}
		if !strings.Contains(req.Action.Prompt, "- build (dir): "+filepath.Join(req.InputsDir, "build")) {
			t.Errorf("the prompt does not name the build input:\n%s", req.Action.Prompt)
		}
		// The prompt renders over req.Data at dispatch: the inputs are in it.
		if got, err := render("{{.workspace_inputs.build.path}}", req.Data); err != nil || got != filepath.Join(req.InputsDir, "build") {
			t.Errorf("{{.workspace_inputs.build.path}} renders %q, %v", got, err)
		}
		// A copy of its own: changing it touches nothing else.
		os.WriteFile(filepath.Join(req.InputsDir, "build", "README.md"), []byte("changed"), 0o600)
		return dispatch.RunRef{Output: "{}"}, nil
	}
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: build, uses: blob.put, options: { path: %q } }
  - { id: notes, uses: blob.put, options: { text: "look at bin/app", name: "notes.txt", media_type: "text/plain" } }
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix it; path={{.workspace_inputs.build.path}}"
    workspace:
      inputs: { build: "{{.build.blob}}", notes: "{{.notes.blob}}" }
`, archive))
	runTriggerWithRun(rig, store.WorkflowRun{ID: "run-in-1", Outputs: map[string]map[string]any{}}, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	if len(seen) != 2 || seen[0].Name != "build" || seen[0].Kind != "dir" || seen[1].Name != "notes" || seen[1].Kind != "file" {
		t.Fatalf("inputs %+v; want build (dir), notes (file)", seen)
	}
	if filepath.Base(seen[1].Path) != "notes.txt" {
		t.Errorf("the file input is at %s, want it under its own name", seen[1].Path)
	}
	if files["build"] != "app v1\n" || files["notes"] != "look at bin/app" {
		t.Errorf("input contents %q", files)
	}
	if req := rig.Agents.requests()[0]; req.WorkspaceArchive != "" {
		t.Errorf("inputs without from: started the agent from an artifact: %q", req.WorkspaceArchive)
	}
	if b, _ := os.ReadFile(archive); !strings.Contains(string(b), "built\n") {
		t.Error("the agent's change reached the source archive")
	}
	emptyDir(t, "the inputs dir", inputsDir)
}

// Inputs beside `from:`: the workspace is the one artifact, the inputs the
// others, and every parallel agent gets copies of its own.
func TestWorkspaceInputsAreFreshPerParallelAgent(t *testing.T) {
	rig, _, inputsDir := inputsRig(t)
	archive := treeTar(t)
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: build, uses: blob.put, options: { path: %q } }
  - id: lens
    type: agent
    agent: reviewer
    prompt: "review"
    for_each: "{{.lenses}}"
    parallel: true
    workspace: { from: "{{.build.blob}}", inputs: { build: "{{.build.blob}}" } }
`, archive))
	runTriggerWithRun(rig, store.WorkflowRun{ID: "run-in-2", Outputs: map[string]map[string]any{}}, newTrigger("ping", map[string]any{"lenses": []any{"a", "b", "c"}}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	dirs := map[string]bool{}
	for _, req := range rig.Agents.requests() {
		if req.WorkspaceArchive == "" || len(req.Inputs) != 1 {
			t.Fatalf("dispatch %s: archive %q inputs %+v", req.DispatchID, req.WorkspaceArchive, req.Inputs)
		}
		dirs[req.InputsDir] = true
	}
	if len(dirs) != 3 {
		t.Errorf("%d distinct input dirs for 3 agents, want one each", len(dirs))
	}
	emptyDir(t, "the inputs dir", inputsDir)
}

// A code step gets the same inputs: CONDUCTOR_INPUT_<NAME> and
// {{.workspace_inputs.<name>.path}} in its scope.
func TestWorkspaceInputsReachACodeStep(t *testing.T) {
	rig, _, inputsDir := inputsRig(t)
	archive := treeTar(t)
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: build, uses: blob.put, options: { path: %q } }
  - { id: notes, uses: blob.put, options: { text: "hello", name: "notes.txt" } }
  - id: c
    run: sh
    env: { TREE: "{{.workspace_inputs.build.path}}" }
    workspace: { inputs: { build: "{{.build.blob}}", notes: "{{.notes.blob}}" } }
    code: 'printf "{\"app\":\"%%s\",\"notes\":\"%%s\",\"same\":%%s}" "$(cat "$CONDUCTOR_INPUT_BUILD/bin/app" | tr -d "\n")" "$(cat "$CONDUCTOR_INPUT_NOTES")" "$([ "$TREE" = "$CONDUCTOR_INPUT_BUILD" ] && echo true || echo false)"'
`, archive))
	run := store.WorkflowRun{ID: "run-in-3", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	out := run.Outputs["c"]
	if out["app"] != "app v1" || out["notes"] != "hello" || out["same"] != true {
		t.Errorf("code step outputs %#v", out)
	}
	emptyDir(t, "the inputs dir", inputsDir)
}

// An input must be a blob this run holds; a failed copy leaves nothing and
// dispatches nothing.
func TestWorkspaceInputsRefuseAnotherRunsArtifact(t *testing.T) {
	rig, bs, inputsDir := inputsRig(t)
	h, err := bs.PutBytes("someone-elses-run", []byte("not yours"), blob.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", workspace: { inputs: { theirs: %q } } }
`, h.Digest))
	runTriggerWithRun(rig, store.WorkflowRun{ID: "run-in-4", Outputs: map[string]map[string]any{}}, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "workspace.inputs.theirs") || !strings.Contains(errStr, "not referenced by run") {
		t.Fatalf("an input from another run's artifact: failed=%v %q", failed, errStr)
	}
	if n := len(rig.Agents.requests()); n != 0 {
		t.Fatalf("%d agent(s) dispatched", n)
	}
	emptyDir(t, "the inputs dir", inputsDir)
}

// A hostile archive fails the step under the same unpack rules as from:,
// and the half-made copy is removed.
func TestWorkspaceInputsUnpackSafely(t *testing.T) {
	rig, _, inputsDir := inputsRig(t)
	p := filepath.Join(t.TempDir(), "evil.tar")
	f, _ := os.Create(p)
	tw := tar.NewWriter(f)
	tw.WriteHeader(&tar.Header{Name: "ok.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("ok"))
	tw.WriteHeader(&tar.Header{Name: "esc", Typeflag: tar.TypeSymlink, Linkname: "../../../etc"})
	tw.Close()
	f.Close()
	spec := mustSpec(t, fmt.Sprintf(`
on: svc.ping
steps:
  - { id: evil, uses: blob.put, options: { path: %q } }
  - { id: fix, type: agent, agent: fixer, prompt: "fix", workspace: { inputs: { evil: "{{.evil.blob}}" } } }
`, p))
	runTriggerWithRun(rig, store.WorkflowRun{ID: "run-in-5", Outputs: map[string]map[string]any{}}, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "outside the archive") {
		t.Fatalf("a hostile input archive: failed=%v %q", failed, errStr)
	}
	if n := len(rig.Agents.requests()); n != 0 {
		t.Fatalf("%d agent(s) dispatched", n)
	}
	emptyDir(t, "the inputs dir", inputsDir)
}

func TestSweepInputsRemovesOnlyStaleStepDirs(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"step-old", "step-new", "other"} {
		os.Mkdir(filepath.Join(dir, n), 0o700)
	}
	old := filepath.Join(dir, "step-old")
	stale := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, stale, stale)
	os.Chtimes(filepath.Join(dir, "other"), stale, stale)
	if n := SweepInputs(dir, 24*time.Hour); n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	for n, want := range map[string]bool{"step-old": false, "step-new": true, "other": true} {
		if _, err := os.Stat(filepath.Join(dir, n)); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", n, err == nil, want)
		}
	}
}
