package flow

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// agentStore is the fake Store plus the agent-session surface.
type agentStore struct {
	*fakeStore
	amu    sync.Mutex
	agents map[string]store.RunAgent // runID\x00key
	puts   []string                  // keys recorded, in order
}

func newAgentStore(fs *fakeStore) *agentStore {
	return &agentStore{fakeStore: fs, agents: map[string]store.RunAgent{}}
}

func (s *agentStore) RunAgent(runID, key string) (store.RunAgent, bool) {
	s.amu.Lock()
	defer s.amu.Unlock()
	a, ok := s.agents[runID+"\x00"+key]
	return a, ok
}

func (s *agentStore) PutRunAgent(runID, key string, a store.RunAgent) error {
	s.amu.Lock()
	defer s.amu.Unlock()
	s.agents[runID+"\x00"+key] = a
	s.puts = append(s.puts, key)
	return nil
}

func (s *agentStore) DeleteRunAgent(runID, key string) error {
	s.amu.Lock()
	defer s.amu.Unlock()
	delete(s.agents, runID+"\x00"+key)
	return nil
}

func (s *agentStore) keys() []string {
	s.amu.Lock()
	defer s.amu.Unlock()
	out := append([]string(nil), s.puts...)
	sort.Strings(out)
	return out
}

// reportStart is a dispatch that reports its agent's start like a runtime that
// records sessions does.
func reportStart(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	if req.OnAgentStart != nil {
		req.OnAgentStart(dispatch.AgentStart{AgentSession: dispatch.AgentSession{
			Runtime: "rt", SessionID: "sess-" + req.Step.Name, Workspace: "/wt", CanResume: true}})
	}
	return dispatch.RunRef{Output: "{}"}, nil
}

// Every agent step in a run — nested in a called workflow, in a parallel
// branch, a for_each item — gets its own stable key, and a step that finishes
// drops its record.
func TestAgentStepKeysAreDistinctAcrossNesting(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
workflows:
  inner:
    steps:
      - { id: fix, type: agent, name: inner-fix, prompt: p, model: x }
`)
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: top, prompt: p, model: x }
  - { id: call, workflow: inner }
  - id: par
    parallel:
      - [ { id: fix, type: agent, name: b0, prompt: p, model: x } ]
      - [ { id: fix, type: agent, name: b1, prompt: p, model: x } ]
  - { id: each, for_each: "a,b", type: agent, name: each, prompt: p, model: x }
`)
	rig := newTestRunner(t, cfg, reg)
	st := newAgentStore(rig.Store)
	rig.Runner.Store = st
	rig.Agents.dispatchFunc = reportStart
	run := emptyRun()
	run.ID = "run1"
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	want := []string{"call/fix", "each[0]", "each[1]", "fix", "par#0/fix", "par#1/fix"}
	got := st.keys()
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
	if len(st.agents) != 0 {
		t.Fatalf("finished steps keep no agent record: %v", st.agents)
	}
}

// A recorded agent is handed back to the step's dispatch to resume, and the
// resume is counted; past MaxAgentResumes the step starts fresh.
func TestAgentStepResumesRecordedAgentBounded(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: fixer, prompt: p, model: x }
`)
	for _, tc := range []struct {
		resumes    int
		wantResume bool
	}{{0, true}, {MaxAgentResumes - 1, true}, {MaxAgentResumes, false}} {
		rig := newTestRunner(t, cfg, reg)
		st := newAgentStore(rig.Store)
		rig.Runner.Store = st
		_ = st.PutRunAgent("run1", "fix", store.RunAgent{Runtime: config.BuiltinPaseoRuntime, SessionID: "old", Workspace: "/kept", CanResume: true, Resumes: tc.resumes})
		var seen *dispatch.AgentSession
		var counted int
		rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			seen = req.Resume
			a, _ := st.RunAgent("run1", "fix")
			counted = a.Resumes
			return dispatch.RunRef{Output: "{}"}, nil
		}
		run := emptyRun()
		run.ID = "run1"
		runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
		if (seen != nil) != tc.wantResume {
			t.Fatalf("resumes=%d: Resume = %+v, want resume %v", tc.resumes, seen, tc.wantResume)
		}
		if tc.wantResume && (seen.SessionID != "old" || seen.Workspace != "/kept" || counted != tc.resumes+1) {
			t.Fatalf("resumes=%d: Resume = %+v, counted %d", tc.resumes, seen, counted)
		}
	}
}

// A step interrupted by shutdown keeps its agent's record for the restart; a
// step that fails for any other reason drops it.
func TestInterruptedAgentStepKeepsItsRecord(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: fixer, prompt: p, model: x }
`)
	for _, shutdown := range []bool{true, false} {
		rig := newTestRunner(t, cfg, reg)
		st := newAgentStore(rig.Store)
		rig.Runner.Store = st
		ctx, cancel := context.WithCancel(context.Background())
		rig.Agents.dispatchFunc = func(c context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			_, _ = reportStart(c, req)
			if shutdown {
				cancel()
				return dispatch.RunRef{}, c.Err()
			}
			return dispatch.RunRef{}, errors.New("agent failed")
		}
		run := emptyRun()
		run.ID = "run1"
		rig.Runner.Run(ctx, run, newTrigger("ping", nil), spec, rig.Runner.IndexOf(spec), nil, false)
		cancel()
		a, kept := st.RunAgent("run1", "fix")
		if kept != shutdown {
			t.Fatalf("shutdown=%v: record kept=%v", shutdown, kept)
		}
		if kept && (a.SessionID != "sess-fixer" || a.Workspace != "/wt" || !a.CanResume) {
			t.Fatalf("kept record = %+v", a)
		}
	}
}

// A runtime that does not name itself in what it reports is recorded under the
// step's runtime; a step that has since moved to another runtime never gets
// its old agent back.
func TestAgentStepRecordCarriesItsRuntime(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: fixer, prompt: p, model: x }
`)
	for _, prior := range []string{config.BuiltinPaseoRuntime, "elsewhere"} {
		rig := newTestRunner(t, cfg, reg)
		st := newAgentStore(rig.Store)
		rig.Runner.Store = st
		_ = st.PutRunAgent("run1", "fix", store.RunAgent{Runtime: prior, SessionID: "old", CanResume: true})
		var seen *dispatch.AgentSession
		var recorded store.RunAgent
		ctx, cancel := context.WithCancel(context.Background())
		rig.Agents.dispatchFunc = func(c context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			seen = req.Resume
			req.OnAgentStart(dispatch.AgentStart{AgentSession: dispatch.AgentSession{SessionID: "new", CanResume: true}})
			recorded, _ = st.RunAgent("run1", "fix")
			cancel()
			return dispatch.RunRef{}, c.Err()
		}
		run := emptyRun()
		run.ID = "run1"
		rig.Runner.Run(ctx, run, newTrigger("ping", nil), spec, rig.Runner.IndexOf(spec), nil, false)
		cancel()
		if moved := prior != config.BuiltinPaseoRuntime; (seen == nil) != moved {
			t.Fatalf("prior runtime %q: Resume = %+v", prior, seen)
		}
		if recorded.Runtime != config.BuiltinPaseoRuntime || recorded.SessionID != "new" {
			t.Fatalf("prior runtime %q: recorded %+v, want it under the step's runtime", prior, recorded)
		}
	}
}

// Two runs of one trigger on one target share a run record (its id is the
// flow, kind and target), so a step of the second found the first's live
// agent recorded under its own key and "resumed" it: the second run adopted
// the first's workspace, and the first's teardown pulled it from under the
// second's agent. Only an agent no dispatch of this process is running —
// one a previous daemon life left — is resumed.
func TestConcurrentRunDoesNotResumeAnotherRunsLiveAgent(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: fixer, prompt: p, model: x }
`)
	rig := newTestRunner(t, cfg, reg)
	st := newAgentStore(rig.Store)
	rig.Runner.Store = st

	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var resumes []*dispatch.AgentSession
	var blocked atomic.Bool
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		mu.Lock()
		resumes = append(resumes, req.Resume)
		mu.Unlock()
		if req.OnAgentStart != nil {
			req.OnAgentStart(dispatch.AgentStart{AgentSession: dispatch.AgentSession{
				Runtime: config.BuiltinPaseoRuntime, SessionID: "sess", Workspace: "/wt-of-a", CanResume: true}})
		}
		if blocked.CompareAndSwap(false, true) {
			close(started)
			<-release // run A's agent is still working
		}
		return dispatch.RunRef{Output: "{}"}, nil
	}
	run := emptyRun()
	run.ID = "flow:ping:shared"
	done := make(chan struct{})
	go func() { runTriggerWithRun(rig, run, newTrigger("ping", nil), spec); close(done) }()
	<-started
	runB := emptyRun()
	runB.ID = run.ID
	runTriggerWithRun(rig, runB, newTrigger("ping", nil), spec)
	close(release)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(resumes) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(resumes))
	}
	if resumes[1] != nil {
		t.Fatalf("run B resumed run A's live agent (workspace %q)", resumes[1].Workspace)
	}

	// After a restart nothing of this process runs the step: its recorded
	// agent is resumed as before.
	_ = st.PutRunAgent(run.ID, "fix", store.RunAgent{Runtime: config.BuiltinPaseoRuntime, SessionID: "old", Workspace: "/kept", CanResume: true})
	resumes = nil
	mu.Unlock()
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	mu.Lock()
	if len(resumes) != 1 || resumes[0] == nil || resumes[0].Workspace != "/kept" {
		t.Fatalf("a recorded agent no live dispatch runs must still be resumed, got %+v", resumes)
	}
}

// A step resumed after a restart runs under the dispatch id its agent started
// with, not the new execution's. Everything the dispatch owns is keyed by that
// id — above all the sandbox's per-dispatch home, where a CLI runtime keeps
// the conversation it is asked to resume — so under a new id the runtime
// found no conversation and every restart threw the agent's work away.
func TestResumedAgentStepKeepsItsDispatchID(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, name: fixer, prompt: p, model: x }
`)
	rig := newTestRunner(t, cfg, reg)
	st := newAgentStore(rig.Store)
	rig.Runner.Store = st

	// Life 1: the agent starts, the daemon stops under it.
	var first string
	ctx1, cancel1 := context.WithCancel(context.Background())
	rig.Agents.dispatchFunc = func(c context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		first = req.DispatchID
		req.OnAgentStart(dispatch.AgentStart{AgentSession: dispatch.AgentSession{SessionID: "sess", Workspace: "/wt", CanResume: true}})
		cancel1()
		return dispatch.RunRef{}, c.Err()
	}
	run := emptyRun()
	run.ID = "run1"
	rig.Runner.Run(ctx1, run, newTrigger("ping", nil), spec, rig.Runner.IndexOf(spec), nil, false)
	cancel1()
	if rec, ok := st.RunAgent("run1", "fix"); !ok || first == "" || rec.DispatchID != first {
		t.Fatalf("the kept record does not carry the dispatch id %q: %+v", first, rec)
	}

	// Life 2: a new execution (a new history id) resumes the step.
	var got, resumeID string
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.DispatchID
		if req.Resume != nil {
			resumeID = req.Resume.DispatchID
		}
		return dispatch.RunRef{Output: "{}"}, nil
	}
	rig.Runner.Run(context.Background(), run, newTrigger("ping", nil), spec, rig.Runner.IndexOf(spec), nil, false)
	if got != first || resumeID != first {
		t.Fatalf("resumed dispatch id = %q (Resume %q), want the original %q", got, resumeID, first)
	}
}
