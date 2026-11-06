package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/gitwt"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// fakeAgentTool is a scripted stand-in for a resumable CLI agent, installed as
// the cli runtime's claude-code bin. A fresh launch leaves uncommitted work in
// its workspace and keeps working until it is killed; a resumed one records
// what it found and finishes. Every launch is appended to LOG.
const fakeAgentTool = `#!/bin/sh
sid=""; resume=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) sid="$2"; shift ;;
    --resume) resume="$2"; shift ;;
  esac
  shift
done
prompt=$(cat)
if [ -z "$resume" ]; then
  case "$prompt" in *"earlier agent was working"*)
    echo "fresh-in-kept sid=$sid cwd=$(pwd) wip=$(cat wip.txt 2>/dev/null)" >> "LOG"
    printf '{"session_id":"%s","result":"done"}\n' "$sid"
    exit 0 ;;
  esac
  printf 'half done' > wip.txt
  echo "fresh sid=$sid cwd=$(pwd)" >> "LOG"
  exec sleep 60
fi
echo "resume sid=$resume cwd=$(pwd) wip=$(cat wip.txt 2>/dev/null)" >> "LOG"
if [ -e "LOG.fail-resume" ]; then
  echo "No conversation found with session ID: $resume"
  exit 1
fi
printf '%s' "$prompt" > "LOG.prompt"
printf '{"session_id":"%s","result":"done"}\n' "$resume"
`

const resumeE2ECfg = `
connectors:
  eg: { use: enginegate }
runtimes:
  cc: { use: cli, tool: claude-code, bin: BIN, default: true }
triggers:
  - on: eg.ping
    steps:
      - { id: fix, type: agent, name: fixer, prompt: "fix the build", runtime: cc, checkout: branch-off }
`

// paseoStub stands in for the built-in paseo runtime, which this test never uses.
type paseoStub struct{ fakeFlowDispatcher }

func (paseoStub) AgentForDispatch(string) string             { return "" }
func (paseoStub) DispatchInFlight(string) bool               { return false }
func (paseoStub) DeliverOutput(string, any) (bool, error)    { return false, nil }
func (paseoStub) Send(context.Context, string, string) error { return nil }

// daemon is one life of the daemon: its store, worktree provisioner and engine
// over a shared state dir.
type daemon struct {
	st   *store.Store
	git  *gitwt.Provisioner
	eng  *Engine
	logs *logSink
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func startDaemon(t *testing.T, stateDir, origin, bin string) *daemon {
	t.Helper()
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(strings.Replace(resumeE2ECfg, "BIN", bin, 1)), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(stateDir, "state.json"), AuditPath: filepath.Join(stateDir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	git := gitwt.New(stateDir)
	git.RemoteURL = func(string) string { return origin }
	git.Keep = st.KeepsWorkspace
	git.MinAge = time.Nanosecond // every unclaimed worktree is an orphan at once
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	logs := &logSink{}
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif, Log: logs.logf})
	ctl := controller.NewRegistry(cfg.MergedControllers(), cfg.DefaultRuntimeName(), paseoStub{}, paseoStub{},
		controller.WithCLIProvisioner(git))
	eng := New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif,
		Flow: runner, Connectors: reg, Controllers: ctl, Log: logs.logf})
	return &daemon{st: st, git: git, eng: eng, logs: logs}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func waitLong(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole path across a daemon restart: a step's agent is interrupted mid
// turn, its workspace (with uncommitted work) survives the restarted daemon's
// worktree reaper, and the resumed step continues the SAME agent session in
// the SAME workspace through the runtime's resume.
func TestAgentStepResumesSessionAcrossDaemonRestart(t *testing.T) {
	testAgentResumeAcrossRestart(t, false)
}

// A resume the runtime cannot carry out (it no longer has the session) falls
// back to a fresh agent in the kept workspace, told about the restart.
func TestAgentStepFailedResumeStartsFreshInKeptWorkspace(t *testing.T) {
	testAgentResumeAcrossRestart(t, true)
}

func testAgentResumeAcrossRestart(t *testing.T, failResume bool) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	origin := filepath.Join(root, "origin")
	if err := os.MkdirAll(origin, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "README"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "add", "README")
	gitIn(t, origin, "commit", "-q", "-m", "init")

	toolLog := filepath.Join(root, "tool.log")
	bin := filepath.Join(root, "claude")
	if err := os.WriteFile(bin, []byte(strings.ReplaceAll(fakeAgentTool, `"LOG`, `"`+toolLog)), 0o700); err != nil {
		t.Fatal(err)
	}
	readLog := func() string { b, _ := os.ReadFile(toolLog); return string(b) }

	trig := flowTrigger("evt-1")
	trig.TargetTrusted = true
	trig.Target = core.Target{Repo: "acme/web", Number: 1, BaseRef: "main"}

	// Life 1: the step's agent starts, leaves uncommitted work, and the daemon
	// stops under it.
	d1 := startDaemon(t, stateDir, origin, bin)
	ctx1, stop1 := context.WithCancel(context.Background())
	d1.eng.process(ctx1, trig)
	waitLong(t, "the agent to start", 10*time.Second, func() bool { return strings.Contains(readLog(), "fresh ") })
	stop1()
	waitLong(t, "the run to be interrupted", 10*time.Second, func() bool { return d1.logs.has("interrupted by shutdown") })
	runs := d1.st.PendingRuns()
	if len(runs) != 1 || len(runs[0].Agents) != 1 {
		t.Fatalf("want one interrupted run holding its agent, got %+v", runs)
	}
	runID := runs[0].ID
	rec := runs[0].Agents["fix"]
	if rec.SessionID == "" || rec.Workspace == "" || !rec.CanResume || rec.Runtime != "cc" {
		t.Fatalf("recorded agent = %+v", rec)
	}
	if !strings.Contains(readLog(), "fresh sid="+rec.SessionID+" cwd="+rec.Cwd) {
		t.Fatalf("the agent was not started on the recorded session/workspace:\n%s\n%+v", readLog(), rec)
	}
	d1.st.Close()

	// Life 2: the reaper runs first, as at boot — the kept worktree stays —
	// then the run resumes.
	if failResume {
		if err := os.WriteFile(toolLog+".fail-resume", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d2 := startDaemon(t, stateDir, origin, bin)
	defer d2.st.Close()
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	d2.git.Reap(ctx2)
	if b, err := os.ReadFile(filepath.Join(rec.Cwd, "wip.txt")); err != nil || string(b) != "half done" {
		t.Fatalf("the reaper took the kept workspace's uncommitted work: %q %v", b, err)
	}
	d2.eng.ResumeWorkflows(ctx2)
	waitLong(t, "the run to finish", 10*time.Second, func() bool { return len(d2.st.PendingRuns()) == 0 })

	want := "resume sid=" + rec.SessionID + " cwd=" + rec.Cwd + " wip=half done"
	if !strings.Contains(readLog(), want) {
		t.Fatalf("the agent was not resumed with its session in its workspace; want %q in:\n%s", want, readLog())
	}
	if strings.Count(readLog(), "fresh ") != 1 {
		t.Fatalf("a fresh workspace's agent was started on resume:\n%s", readLog())
	}
	if failResume {
		if !strings.Contains(readLog(), "fresh-in-kept sid=") || !strings.Contains(readLog(), " cwd="+rec.Cwd+" wip=half done\n") {
			t.Fatalf("want a fresh agent in the kept workspace after the failed resume:\n%s", readLog())
		}
		if !d2.logs.has("resume unavailable: resumed session failed") || !d2.logs.has("fresh agent in kept workspace") {
			t.Fatalf("no fallback log line: %v", d2.logs.lines)
		}
		return
	}
	if strings.Contains(readLog(), "fresh-in-kept") {
		t.Fatalf("a fresh agent was started although the resume worked:\n%s", readLog())
	}
	if p, _ := os.ReadFile(toolLog + ".prompt"); string(p) != dispatch.ResumeNote {
		t.Fatalf("resumed turn prompt = %q, want the restart note", p)
	}
	if !d2.logs.has("resuming agent session " + rec.SessionID + " for step fix") {
		t.Fatalf("no resume log line: %v", d2.logs.lines)
	}
	if d2.st.KeepsWorkspace(rec.Cwd) {
		t.Fatalf("run %s finished but still keeps its workspace", runID)
	}
}
