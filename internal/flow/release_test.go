package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// liveAgents stands in for a runtime with conductor-made worktrees: every
// dispatch opens a session with a worktree of its own, and only archiving
// the session releases both.
type liveAgents struct {
	mu   sync.Mutex
	root string
	n    int
	live map[string]string // agent id → its worktree
}

func newLiveAgents(t *testing.T, rig *testRig) *liveAgents {
	l := &liveAgents{root: t.TempDir(), live: map[string]string{}}
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.n++
		id := fmt.Sprintf("ag-%d", l.n)
		wt := filepath.Join(l.root, id)
		if err := os.Mkdir(wt, 0o755); err != nil {
			return dispatch.RunRef{}, err
		}
		l.live[id] = wt
		return dispatch.RunRef{AgentID: id, Workdir: wt, Output: `{"verdict":"ok"}`}, nil
	}
	rig.Runner.Agents.Archive = func(id string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if wt, ok := l.live[id]; ok {
			os.RemoveAll(wt)
			delete(l.live, id)
		}
	}
	return l
}

func (l *liveAgents) state(t *testing.T) (sessions []string, worktrees int) {
	t.Helper()
	l.mu.Lock()
	for id := range l.live {
		sessions = append(sessions, id)
	}
	l.mu.Unlock()
	sort.Strings(sessions)
	ents, err := os.ReadDir(l.root)
	if err != nil {
		t.Fatal(err)
	}
	return sessions, len(ents)
}

func runRelease(t *testing.T, y string) (*testRig, *liveAgents) {
	t.Helper()
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	l := newLiveAgents(t, rig)
	runTrigger(rig, newTrigger("ping", map[string]any{"lenses": []any{"a", "b", "c", "d"}}), mustSpec(t, y))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	return rig, l
}

// A review's lenses: a parallel for_each of foreground agent steps that
// answer through output_schema and never call step.done. Each one's session
// and worktree are gone once the step is.
func TestFinishedForEachAgentStepsReleaseTheirWorktrees(t *testing.T) {
	_, l := runRelease(t, `
on: svc.ping
steps:
  - id: review
    for_each: "{{.lenses}}"
    parallel: true
    type: agent
    prompt: "review as {{.item}}"
    output_schema: { type: object, properties: { verdict: { type: string } } }
  - id: after
    uses: svc.post
    options: { text: "{{.review.count}}" }
`)
	if l.n != 4 {
		t.Fatalf("dispatched %d lenses, want 4", l.n)
	}
	if sessions, wts := l.state(t); len(sessions) != 0 || wts != 0 {
		t.Fatalf("after the step: live sessions %v, worktrees %d — want none", sessions, wts)
	}
}

// archive_when_done: false keeps them, as it says.
func TestExplicitKeepHoldsTheAgents(t *testing.T) {
	_, l := runRelease(t, `
on: svc.ping
steps:
  - id: review
    for_each: "{{.lenses}}"
    parallel: true
    type: agent
    prompt: "review as {{.item}}"
    output_schema: { type: object, properties: { verdict: { type: string } } }
    archive_when_done: false
`)
	if sessions, wts := l.state(t); len(sessions) != 4 || wts != 4 {
		t.Fatalf("archive_when_done: false released them: live %v, worktrees %d", sessions, wts)
	}
}

// A background step hands its agent to a person: not released, and handed
// to Background as before.
func TestBackgroundStepIsUntouched(t *testing.T) {
	rig, l := runRelease(t, `
on: svc.ping
steps:
  - { id: talk, type: agent, prompt: "over to you", background: true }
`)
	if sessions, wts := l.state(t); len(sessions) != 1 || wts != 1 {
		t.Fatalf("a background step's agent was released: live %v, worktrees %d", sessions, wts)
	}
	if len(rig.Agents.backgroundCalls) != 1 {
		t.Fatalf("background calls: %d", len(rig.Agents.backgroundCalls))
	}
}

// A later step that reads a step's worktree or agent keeps that agent for
// the run; the other steps are released at their step's end. (When the run
// ends it goes too: TestLaterReadAgentReleasedAtRunEnd.)
func TestLaterReadKeepsTheAgent(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	l := newLiveAgents(t, rig)
	dispatchLive := rig.Agents.dispatchFunc
	var live []string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if strings.HasPrefix(req.Action.Prompt, "look") {
			live, _ = l.state(t)
		}
		return dispatchLive(ctx, req)
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, prompt: "fix it" }
  - { id: other, type: agent, prompt: "something else" }
  - { id: check, type: agent, prompt: "look", if: "{{.fix.workdir}}" }
`))
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if len(live) != 1 || live[0] != "ag-1" {
		t.Fatalf("want only fix's agent (ag-1) live when check runs: %v", live)
	}
}

// A read that can't be tied to one step (a range over a for_each's items)
// keeps every agent of the run, for the run.
func TestUnattributedReadKeepsEveryAgent(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	l := newLiveAgents(t, rig)
	dispatchLive := rig.Agents.dispatchFunc
	var live []string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if strings.HasPrefix(req.Action.Prompt, "list") {
			live, _ = l.state(t)
		}
		return dispatchLive(ctx, req)
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"lenses": []any{"a", "b", "c", "d"}}), mustSpec(t, `
on: svc.ping
steps:
  - id: review
    for_each: "{{.lenses}}"
    type: agent
    prompt: "review as {{.item}}"
  - { id: list, type: agent, prompt: "list {{range .review.items}}{{.workdir}} {{end}}" }
`))
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if len(live) != 4 {
		t.Fatalf("want every lens live when list runs: %v", live)
	}
	if sessions, _ := l.state(t); len(sessions) != 0 {
		t.Fatalf("after the run: live %v — want none", sessions)
	}
}

func TestScanLaterReads(t *testing.T) {
	for _, tc := range []struct {
		text string
		ids  []string
		all  bool
	}{
		{"{{.fix.workdir}}", []string{"fix"}, false},
		{"{{.steps.fix.outputs.workdir}} {{.steps.b.agent_id}}", []string{"b", "fix"}, false},
		{"{{.fix.outputs.agent_id}}", []string{"fix"}, false},
		{"Review the change in {{.gate.workdir}}", nil, false},
		{"workdir: /srv/x\nwork_dir: y", nil, false},
		{"{{range .review.items}}{{.workdir}}{{end}}", nil, true},
		{"{{.team.workers.a.workdir}}", nil, true},
	} {
		r := laterReads{ids: map[string]bool{}}
		scanLaterReads(&r, tc.text)
		var ids []string
		for id := range r.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if fmt.Sprint(ids) != fmt.Sprint(tc.ids) || r.all != tc.all {
			t.Errorf("%q: ids %v all %v, want %v %v", tc.text, ids, r.all, tc.ids, tc.all)
		}
	}
}

// Only an agent step the run executes as a step releases by default — not a
// session a team, a decide or a gate runs through execAgent itself.
func TestReleaseOnlyForARunStep(t *testing.T) {
	step := config.Step{ID: "w", Type: "agent"}
	ctx := withLaterReads(context.Background(), []config.Step{step})
	if releasesByDefault(ctx, step, "w") {
		t.Fatal("released without the step mark")
	}
	on := context.WithValue(ctx, stepReleaseKey{}, true)
	if !releasesByDefault(on, step, "w") {
		t.Fatal("a run step is not released")
	}
	for name, s := range map[string]config.Step{
		"explicit false": {ID: "w", ArchiveWhenDone: config.Bool(false)},
		"background":     {ID: "w", Background: true},
		"session":        {ID: "w", Session: &config.SessionSpec{}},
		"watch":          {ID: "w", Watch: &config.WatchSpec{}},
	} {
		if releasesByDefault(on, s, "w") {
			t.Errorf("%s: released by default", name)
		}
	}
}

// Every trigger of a real pack marshals for the scan: a spec that failed to
// would release nothing by default, without a word.
func TestPackSpecsScanForLaterReads(t *testing.T) {
	_, cfg := autopilotRig(t)
	if len(cfg.Triggers) == 0 {
		t.Fatal("no triggers")
	}
	for _, spec := range cfg.Triggers {
		if _, err := yaml.Marshal(spec); err != nil {
			t.Fatalf("%s: %v", spec.Name, err)
		}
		reads, _ := withLaterReads(context.Background(), spec).Value(laterReadsKey{}).(laterReads)
		if reads.all {
			t.Errorf("%s: a read no step can be tied to — nothing in it is released by default", spec.Name)
		}
	}
}
