package dispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// daemonBackend is an in-process paseo daemon for the resume path: agents with
// labels and workspaces that outlive any one dispatch, as the real daemon's do
// across a conductor restart.
type daemonBackend struct {
	mu         sync.Mutex
	agents     map[string]map[string]string // id → labels
	workspaces map[string]string            // id → cwd
	runs       [][]string                   // RunAgent argv
	sends      []SendOptions
	waits      []string
	archived   []string // agents
	wsArchived []string
	seq        int
	hang       bool   // RunAgent registers its agent, then blocks until ctx ends
	sendErr    error  // Send fails
	sendOut    string // Send's output
	log        string // AgentLog's text
}

func newDaemonBackend() *daemonBackend {
	return &daemonBackend{agents: map[string]map[string]string{}, workspaces: map[string]string{}}
}

func (b *daemonBackend) RunAgent(ctx context.Context, opts RunAgentOptions) (RunAgentResult, error) {
	b.mu.Lock()
	b.runs = append(b.runs, opts.Args)
	b.seq++
	id := "ag-" + itoa(b.seq)
	labels := map[string]string{}
	for i, a := range opts.Args {
		if a == "--label" && i+1 < len(opts.Args) {
			k, v, _ := strings.Cut(opts.Args[i+1], "=")
			labels[k] = v
		}
	}
	b.agents[id] = labels
	hang := b.hang
	b.mu.Unlock()
	if hang {
		<-ctx.Done()
		return RunAgentResult{}, ctx.Err()
	}
	return RunAgentResult{AgentID: id, Output: `{"agentId":"` + id + `"}`}, nil
}

func (b *daemonBackend) ListAgents(_ context.Context, want map[string]string) ([]AgentInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []AgentInfo
	for id, labels := range b.agents {
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
			}
		}
		if match {
			out = append(out, AgentInfo{ID: id})
		}
	}
	return out, nil
}

func (b *daemonBackend) ArchiveAgent(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.archived = append(b.archived, id)
	delete(b.agents, id)
	return nil
}

func (b *daemonBackend) ArchiveWorkspace(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wsArchived = append(b.wsArchived, id)
	delete(b.workspaces, id)
	return nil
}

func (b *daemonBackend) CreateWorkspace(_ context.Context, opts CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	id := "ws-" + itoa(b.seq)
	b.workspaces[id] = opts.Path
	return CreateWorkspaceResult{WorkspaceID: id}, nil
}

func (b *daemonBackend) ListWorkspaces(context.Context) ([]WorkspaceInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []WorkspaceInfo
	for id, cwd := range b.workspaces {
		out = append(out, WorkspaceInfo{WorkspaceID: id, Cwd: cwd, Name: runWorkspacePrefix + id})
	}
	return out, nil
}

func (b *daemonBackend) Send(_ context.Context, opts SendOptions) (SendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sends = append(b.sends, opts)
	if b.sendErr != nil {
		return SendResult{}, b.sendErr
	}
	return SendResult{Output: b.sendOut}, nil
}

func (b *daemonBackend) Wait(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.waits = append(b.waits, id)
	return nil
}

func (b *daemonBackend) AgentLog(context.Context, string, int) (string, error) { return b.log, nil }
func (b *daemonBackend) Inspect(context.Context, string) (AgentDetail, error) {
	return AgentDetail{}, nil
}
func (b *daemonBackend) CreateWorktree(context.Context, CreateWorktreeOptions) (CreateWorktreeResult, error) {
	return CreateWorktreeResult{}, errors.New("not used")
}
func (b *daemonBackend) ListProjects(context.Context) ([]ProjectInfo, error) { return nil, nil }
func (b *daemonBackend) RemoveProject(context.Context, string) error         { return nil }
func (b *daemonBackend) Clone(context.Context, CloneOptions) error           { return nil }

// resumeStep is a foreground checkout:none agent step that records its agent
// for a restart; starts collects what it reports.
func resumeStep(starts *[]AgentStart) Request {
	return Request{
		Wait:       true,
		Trigger:    core.Trigger{Source: "gh", Kind: "fix", TargetTrusted: true, Target: core.Target{Repo: "acme/web", Number: 7}},
		Action:     config.Action{Type: "agent", Prompt: "fix the build", Checkout: "none"},
		DispatchID: "disp-2",
		OnAgentStart: func(s AgentStart) {
			*starts = append(*starts, s)
		},
	}
}

func newResumeDispatcher(t *testing.T, b Backend) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // run workspaces' directories
	d := &Dispatcher{PaseoBin: "paseo", Owned: NewOwnedSet(""), repoDirs: map[string]string{}}
	d.SetBackend(b)
	return d
}

func argAfter(argv []string, flag string) []string {
	var out []string
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			out = append(out, argv[i+1])
		}
	}
	return out
}

// A step's agent is named for a restart before it starts: labelled with a
// fresh session token that is reported, with its workspace, ahead of the turn
// a restart may cut short. That cut keeps the workspace; a real launch
// failure reclaims it.
func TestPaseoStepAgentIsRecordedBeforeItsTurn(t *testing.T) {
	b := newDaemonBackend()
	b.hang = true
	d := newResumeDispatcher(t, b)
	var starts []AgentStart
	ctx, cancel := context.WithCancel(context.Background())
	req := resumeStep(&starts)
	req.OnAgentStart = func(s AgentStart) {
		starts = append(starts, s)
		cancel() // the restart, mid-turn
	}
	if _, err := d.Dispatch(ctx, req); err == nil {
		t.Fatal("an interrupted launch must report the interruption")
	}
	if len(starts) != 1 || len(b.runs) != 1 {
		t.Fatalf("starts=%+v runs=%d", starts, len(b.runs))
	}
	s := starts[0]
	labels := argAfter(b.runs[0], "--label")
	if s.SessionID == "" || !s.CanResume || s.Workspace == "" || s.Cwd == "" || s.Resumed {
		t.Fatalf("reported %+v", s)
	}
	if !containsStr(labels, stepSessionLabel+"="+s.SessionID) {
		t.Fatalf("the agent is not labelled with its session token %q: %v", s.SessionID, labels)
	}
	if ws := argAfter(b.runs[0], "--workspace"); len(ws) != 1 || ws[0] != s.Workspace {
		t.Fatalf("launched in %v, reported workspace %q", ws, s.Workspace)
	}
	if len(b.wsArchived) != 0 {
		t.Fatalf("a launch cut by shutdown archived its workspace (and the agent in it): %v", b.wsArchived)
	}

	// The same launch failing on its own does reclaim the workspace.
	b2 := newDaemonBackend()
	d2 := newResumeDispatcher(t, &failingRun{b2})
	starts = nil
	if _, err := d2.Dispatch(context.Background(), resumeStep(&starts)); err == nil {
		t.Fatal("want the launch failure")
	}
	if len(b2.wsArchived) != 1 {
		t.Fatalf("a failed launch must reclaim its run workspace, archived %v", b2.wsArchived)
	}
}

type failingRun struct{ *daemonBackend }

func (f *failingRun) RunAgent(context.Context, RunAgentOptions) (RunAgentResult, error) {
	return RunAgentResult{}, errors.New("paseo run: boom")
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// After a restart the step's agent, still in the paseo daemon, is found by its
// session label and continued: its turn is waited out, then it gets the
// restart note, and that turn's output is the step's. No new agent starts.
func TestPaseoStepResumeContinuesTheLiveAgent(t *testing.T) {
	b := newDaemonBackend()
	b.agents["ag-7"] = map[string]string{"conductor": "1", stepSessionLabel: "tok"}
	b.workspaces["ws-3"] = "/runs/x"
	b.sendOut = `{"agentId":"ag-7","output":"done"}`
	d := newResumeDispatcher(t, b)
	d.Owned.BindDispatch("disp-1", "ag-7")
	var starts []AgentStart
	req := resumeStep(&starts)
	req.Resume = &AgentSession{SessionID: "tok", Workspace: "ws-3", Cwd: "/runs/x", CanResume: true}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.runs) != 0 {
		t.Fatalf("a fresh agent was launched: %v", b.runs)
	}
	if ref.AgentID != "ag-7" || ref.Output != b.sendOut || ref.Workdir != "/runs/x" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(b.waits) != 1 || b.waits[0] != "ag-7" {
		t.Fatalf("the interrupted turn was not waited out: %v", b.waits)
	}
	if len(b.sends) != 1 || b.sends[0].ID != "ag-7" || b.sends[0].Prompt != ResumeNote || !b.sends[0].JSON {
		t.Fatalf("sends = %+v", b.sends)
	}
	if len(starts) != 1 || !starts[0].Resumed || starts[0].SessionID != "tok" || starts[0].Workspace != "ws-3" {
		t.Fatalf("starts = %+v", starts)
	}
	if !d.Owned.HasAgent("ag-7") || d.AgentForDispatch("disp-2") != "ag-7" {
		t.Fatal("the resumed agent must be conductor's, bound to this dispatch")
	}
}

// The resumed turn of an output_schema step answers in its reply, read back
// from the agent's log.
func TestPaseoStepResumeCapturesSchemaAnswer(t *testing.T) {
	b := newDaemonBackend()
	b.agents["ag-7"] = map[string]string{"conductor": "1", stepSessionLabel: "tok"}
	b.log = "[User] ...\n{\"verdict\":\"ok\"}\n"
	d := newResumeDispatcher(t, b)
	var starts []AgentStart
	req := resumeStep(&starts)
	req.Action.OutputSchema = map[string]any{"type": "object", "required": []any{"verdict"},
		"properties": map[string]any{"verdict": map[string]any{"type": "string"}}}
	req.Resume = &AgentSession{SessionID: "tok", CanResume: true}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Output != `{"verdict":"ok"}` {
		t.Fatalf("output = %q", ref.Output)
	}
	if len(b.sends) == 0 || !strings.HasPrefix(b.sends[0].Prompt, ResumeNote) || !strings.Contains(b.sends[0].Prompt, "verdict") {
		t.Fatalf("the resume note must carry the schema: %q", b.sends[0].Prompt)
	}
}

// When the agent is gone, or will not take the note, a fresh agent starts in
// the kept workspace, told about the restart; with the workspace gone too, it
// starts fresh everywhere.
func TestPaseoStepResumeFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name               string
		agent, ws, sendErr bool
		wantKept           bool
		wantFallback       string
	}{
		{"agent gone", false, true, false, true, "its paseo agent is gone"},
		{"send fails", true, true, true, true, "resumed agent ag-7 failed"},
		{"workspace gone", false, false, false, false, "its workspace is gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newDaemonBackend()
			if tc.agent {
				b.agents["ag-7"] = map[string]string{"conductor": "1", stepSessionLabel: "tok"}
			}
			if tc.ws {
				b.workspaces["ws-3"] = "/runs/x"
			}
			if tc.sendErr {
				b.sendErr = errors.New("agent not found")
			}
			d := newResumeDispatcher(t, b)
			var starts []AgentStart
			req := resumeStep(&starts)
			req.Resume = &AgentSession{SessionID: "tok", Workspace: "ws-3", Cwd: "/runs/x", CanResume: true}
			if _, err := d.Dispatch(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if len(b.runs) != 1 {
				t.Fatalf("want one fresh agent, runs=%v", b.runs)
			}
			argv := b.runs[0]
			ws := argAfter(argv, "--workspace")
			if kept := len(ws) == 1 && ws[0] == "ws-3"; kept != tc.wantKept {
				t.Fatalf("launched in %v, want kept=%v", ws, tc.wantKept)
			}
			if noted := strings.HasPrefix(argv[1], RestartNote); noted != tc.wantKept {
				t.Fatalf("prompt %q: restart note=%v, want %v", argv[1], noted, tc.wantKept)
			}
			s := starts[len(starts)-1]
			if s.KeptWorkspace != tc.wantKept || !strings.Contains(s.Fallback, tc.wantFallback) || s.Resumed {
				t.Fatalf("reported %+v", s)
			}
			if tc.sendErr {
				// The old agent goes; its workspace, where the fresh one works, stays.
				if !containsStr(b.archived, "ag-7") || len(b.wsArchived) != 0 {
					t.Fatalf("archived agents %v workspaces %v", b.archived, b.wsArchived)
				}
			}
		})
	}
}

// AdoptWorktree hands another runtime its paseo workspace back while paseo
// still has it.
func TestDispatcherAdoptsAKeptWorkspace(t *testing.T) {
	b := newDaemonBackend()
	b.workspaces["ws-3"] = "/wt/a"
	d := newResumeDispatcher(t, b)
	if cwd, err := d.AdoptWorktree(context.Background(), Request{}, "ws-3"); err != nil || cwd != "/wt/a" {
		t.Fatalf("adopt = %q %v", cwd, err)
	}
	if _, err := d.AdoptWorktree(context.Background(), Request{}, "ws-9"); err == nil {
		t.Fatal("a gone workspace cannot be adopted")
	}
}
