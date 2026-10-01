package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// A connector type with a progress face, so these tests watch exactly what
// the runner hands a ProgressReporter, and when, without any one
// connector's idea of what progress looks like.
var progDecl = &connector.TypeDecl{
	Type: "fakeprog",
	Desc: "Fake connector with a progress face (flow progress tests).",
	Events: []connector.EventDecl{{
		Name:    "ping",
		Desc:    "a synthetic test event",
		Context: connector.Schema{"msg": {Type: connector.TString}},
		Options: connector.Schema{"progress": {Type: connector.TMap}},
	}},
	Verbs: []connector.VerbDecl{{
		Name:    "post",
		Desc:    "records an invocation",
		Options: connector.Schema{"text": {Type: connector.TString}},
		Outputs: connector.Schema{"id": {Type: connector.TInt}},
	}},
}

func init() {
	connector.RegisterType(progDecl, func(name string, _ config.ConnectorRef, _ connector.Deps) (connector.Impl, error) {
		return &progImpl{name: name}, nil
	})
}

// progLog is the ordered record of everything progress-relevant: progress
// start/finish, verb calls (hooks and steps), and agent dispatches.
type progLog struct {
	mu      sync.Mutex
	entries []string
	runs    []connector.ProgressRun
	finals  []connector.RunOutcome
}

func (l *progLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, s)
}

func (l *progLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

var (
	progLogsMu sync.Mutex
	progLogs   = map[string]*progLog{}
)

func progLogFor(name string) *progLog {
	progLogsMu.Lock()
	defer progLogsMu.Unlock()
	if progLogs[name] == nil {
		progLogs[name] = &progLog{}
	}
	return progLogs[name]
}

type progImpl struct{ name string }

func (p *progImpl) Validate() error          { return nil }
func (p *progImpl) DeclaredEvents() []string { return nil }
func (p *progImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (p *progImpl) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	progLogFor(p.name).add(fmt.Sprintf("verb:%s:%v", verb, opts["text"]))
	return map[string]any{"id": 1}, nil
}

func (p *progImpl) StartProgress(_ context.Context, run connector.ProgressRun) connector.Progress {
	l := progLogFor(p.name)
	l.add("progress:start")
	l.mu.Lock()
	l.runs = append(l.runs, run)
	l.mu.Unlock()
	return &progHandle{l: l}
}

type progHandle struct{ l *progLog }

func (h *progHandle) Finish(_ context.Context, o connector.RunOutcome) {
	h.l.add("progress:finish:" + o.Result)
	h.l.mu.Lock()
	h.l.finals = append(h.l.finals, o)
	h.l.mu.Unlock()
}

// progRig builds a runner over one fakeprog connector named name.
func progRig(t *testing.T, name string) (*testRig, *progLog) {
	t.Helper()
	cfg := loadConfig(t, "connectors: { "+name+": { use: fakeprog } }")
	reg := buildRegistry(t, cfg)
	progLogsMu.Lock()
	progLogs[name] = &progLog{}
	progLogsMu.Unlock()
	rig := newTestRunner(t, cfg, reg)
	l := progLogFor(name)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		l.add("dispatch")
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	return rig, l
}

func progTrigger(conn string) core.Trigger {
	t := newTrigger("ping", map[string]any{"msg": "x"})
	t.Source, t.Instance = conn, conn
	return t
}

const progSpec = `
on: %[1]s.ping
hooks:
  - { at: start, uses: %[1]s.post, options: { text: start-hook } }
steps:
  - id: fix
    type: agent
    prompt: p
`

// TestProgressStartsBeforeAnythingElse: progress starts before the start
// hooks and before the step dispatches its agent — so before any worktree is
// provisioned — and finishes ok after the steps.
func TestProgressStartsBeforeAnythingElse(t *testing.T) {
	rig, l := progRig(t, "pa")
	runTrigger(rig, progTrigger("pa"), mustSpec(t, fmt.Sprintf(progSpec, "pa")))
	got := strings.Join(l.snapshot(), " ")
	want := "progress:start verb:post:start-hook dispatch progress:finish:ok"
	if got != want {
		t.Fatalf("lifecycle order = %q\nwant              %q", got, want)
	}
}

// TestProgressFailureOutcomes: every way a run fails finishes progress as
// failed — the dispatch that never came up (the incident: a worktree error
// that escalated), an agent that ran and failed, a fixer that never pushed —
// with a reason that never carries the error's own text.
func TestProgressFailureOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
	}{
		{"dispatch never came up (escalates)", "the agent couldn't be started",
			dispatch.Unrecoverable(errors.New("gitwt: create worktree /home/x/.wt/pr-7: exit status 128"))},
		{"agent failed", `step "fix" failed`, errors.New("agent exited 1: secret-ish detail")},
		{"timed out", "timed out", fmt.Errorf("wait: %w", context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, l := progRig(t, "pf")
			rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
				l.add("dispatch")
				return dispatch.RunRef{}, tc.err
			}
			runTrigger(rig, progTrigger("pf"), mustSpec(t, fmt.Sprintf(progSpec, "pf")))
			if len(l.finals) != 1 {
				t.Fatalf("finishes = %d, want exactly 1: %v", len(l.finals), l.snapshot())
			}
			o := l.finals[0]
			if o.Result != connector.OutcomeFailed || o.Reason != tc.reason {
				t.Fatalf("outcome = %+v, want failed %q", o, tc.reason)
			}
			for _, leak := range []string{"gitwt", "/home", "secret", "exit"} {
				if strings.Contains(o.Reason, leak) {
					t.Fatalf("reason %q leaks the error text (%q)", o.Reason, leak)
				}
			}
		})
	}
}

// TestProgressNoProgressIsAFailure: an expect_push fixer that left its change
// unpushed is a failure, not a quiet success.
func TestProgressNoProgressIsAFailure(t *testing.T) {
	rig, l := progRig(t, "pn")
	wd := gitWorktree(t, true)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: wd, Output: "done", AgentID: "a1"}, nil
	}
	spec := mustSpec(t, `
on: pn.ping
steps:
  - id: fix
    type: agent
    prompt: p
    expect_push: true
`)
	runTrigger(rig, progTrigger("pn"), spec)
	if len(l.finals) != 1 || l.finals[0].Result != connector.OutcomeFailed || l.finals[0].Reason != "the change was never pushed" {
		t.Fatalf("finals = %+v, want one failed 'the change was never pushed'", l.finals)
	}
}

// TestProgressStoppedWhenTargetCloses: a PR closing under the run is moot,
// not failed.
func TestProgressStoppedWhenTargetCloses(t *testing.T) {
	rig, l := progRig(t, "ps")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, dispatch.ErrTargetClosed
	}
	runTrigger(rig, progTrigger("ps"), mustSpec(t, fmt.Sprintf(progSpec, "ps")))
	if len(l.finals) != 1 || l.finals[0].Result != connector.OutcomeStopped {
		t.Fatalf("finals = %+v, want one stopped", l.finals)
	}
}

// TestProgressShadowShowsNothing: a shadow run posts nothing anywhere, so it
// starts no progress either.
func TestProgressShadowShowsNothing(t *testing.T) {
	rig, l := progRig(t, "psh")
	rig.Runner.Run(context.Background(), emptyRun(), progTrigger("psh"),
		mustSpec(t, fmt.Sprintf(progSpec, "psh")), 0, nil, true)
	for _, e := range l.snapshot() {
		if strings.HasPrefix(e, "progress:") {
			t.Fatalf("shadow run touched progress: %v", l.snapshot())
		}
	}
}

// TestProgressPanicStillFinishes: a run that panics mid-step still records a
// failure (the engine recovers the panic; the subject mustn't stay 👀).
func TestProgressPanicStillFinishes(t *testing.T) {
	rig, l := progRig(t, "pp")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		panic("boom")
	}
	func() {
		defer func() { _ = recover() }()
		runTrigger(rig, progTrigger("pp"), mustSpec(t, fmt.Sprintf(progSpec, "pp")))
	}()
	if len(l.finals) != 1 || l.finals[0].Result != connector.OutcomeFailed {
		t.Fatalf("finals = %+v, want one failed after a panic", l.finals)
	}
}

// TestProgressHandsOverOptionsAndBatch: the trigger's options.progress and a
// grouped run's events reach the reporter untouched.
func TestProgressHandsOverOptionsAndBatch(t *testing.T) {
	rig, l := progRig(t, "po")
	spec := mustSpec(t, `
on: po.ping
options: { progress: { reactions: false } }
steps:
  - { id: s, uses: po.post, options: { text: x } }
`)
	b := &Batch{Key: "k", Events: []core.Trigger{progTrigger("po"), progTrigger("po")}}
	runTriggerBatch(rig, progTrigger("po"), spec, b)
	if len(l.runs) != 1 {
		t.Fatalf("starts = %d, want 1", len(l.runs))
	}
	run := l.runs[0]
	if v, ok := run.Options["reactions"]; !ok || v != false {
		t.Fatalf("options = %v, want the trigger's progress block", run.Options)
	}
	if len(run.Batch) != 2 {
		t.Fatalf("batch = %d events, want 2", len(run.Batch))
	}
}

// TestReportOutcomeStartsAndFinishes: the engine's parked path shows a
// failed outcome on an event that never reached a run.
func TestReportOutcomeStartsAndFinishes(t *testing.T) {
	cfg := loadConfig(t, "connectors: { pr: { use: fakeprog } }\ntriggers:\n  - { on: pr.ping, steps: [ { id: s, uses: pr.post, options: { text: x } } ] }\n")
	reg := buildRegistry(t, cfg)
	progLogsMu.Lock()
	progLogs["pr"] = &progLog{}
	progLogsMu.Unlock()
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.ReportOutcome(context.Background(), progTrigger("pr"), "0:pr.ping", false,
		connector.RunOutcome{Result: connector.OutcomeFailed, Reason: "parked"})
	l := progLogFor("pr")
	if got := strings.Join(l.snapshot(), " "); got != "progress:start progress:finish:failed" {
		t.Fatalf("report = %q", got)
	}
	// Shadow: nothing.
	progLogs["pr"] = &progLog{}
	rig.Runner.ReportOutcome(context.Background(), progTrigger("pr"), "0:pr.ping", true,
		connector.RunOutcome{Result: connector.OutcomeFailed})
	if got := progLogFor("pr").snapshot(); len(got) != 0 {
		t.Fatalf("shadow report touched progress: %v", got)
	}
}
