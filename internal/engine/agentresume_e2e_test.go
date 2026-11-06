package engine

import (
	"context"
	"encoding/json"
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
  cc: { use: cli, tool: claude-code, bin: BIN }
  ag: { use: acp, agent: fake, command: [ACP] }
triggers:
  - on: eg.ping
    steps:
      - STEP
`

// resumeKind is one runtime's restart case: the step that runs on it, and how
// its fake reports a launch, a resume and a fallback in the tool log.
type resumeKind struct {
	name    string
	step    string
	runtime string // the runtime the step's agent is recorded under
	fresh   func(rec store.RunAgent) string
	resume  func(rec store.RunAgent) string
	kept    string // a fresh agent in the kept workspace
	// failLog is the daemon's log line when the resume fails.
	failLog string
}

var resumeKinds = map[string]resumeKind{
	"cli": {
		name: "cli", runtime: "cc",
		step:    `{ id: fix, type: agent, name: fixer, prompt: "fix the build", runtime: cc, checkout: branch-off }`,
		fresh:   func(r store.RunAgent) string { return "fresh sid=" + r.SessionID + " cwd=" + r.Cwd },
		resume:  func(r store.RunAgent) string { return "resume sid=" + r.SessionID + " cwd=" + r.Cwd + " wip=half done" },
		kept:    "fresh-in-kept sid=",
		failLog: "resume unavailable: resumed session failed",
	},
	"paseo": {
		name: "paseo", runtime: config.BuiltinPaseoRuntime,
		step:    `{ id: fix, type: agent, name: fixer, prompt: "fix the build", checkout: none }`,
		fresh:   func(r store.RunAgent) string { return "fresh label=" + r.SessionID + " cwd=" + r.Cwd },
		resume:  func(r store.RunAgent) string { return " label=" + r.SessionID + " cwd=" + r.Cwd + " wip=half done" },
		kept:    "fresh-in-kept label=",
		failLog: "resume unavailable: resumed agent agent-",
	},
	"acp": {
		name: "acp", runtime: "ag",
		step:    `{ id: fix, type: agent, name: fixer, prompt: "fix the build", runtime: ag, checkout: branch-off }`,
		fresh:   func(r store.RunAgent) string { return "fresh sid=" + r.SessionID + " cwd=" + r.Cwd },
		resume:  func(r store.RunAgent) string { return "resume sid=" + r.SessionID + " cwd=" + r.Cwd + " wip=half done" },
		kept:    "fresh-in-kept sid=",
		failLog: "resume unavailable: resume failed: acp: session/load",
	},
}

// paseoStub stands in for the built-in paseo runtime in the cli case.
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

// resumeRig is what every life of the daemon shares.
type resumeRig struct {
	kind     resumeKind
	root     string
	stateDir string
	origin   string
	cli      string // the scripted claude
	paseo    string // the fake paseo CLI ("" = paseoStub)
	acp      string // the fake ACP agent's launcher
}

func startDaemon(t *testing.T, rig resumeRig) *daemon {
	t.Helper()
	registerGateConn()
	text := strings.NewReplacer("BIN", rig.cli, "ACP", rig.acp, "STEP", rig.kind.step).Replace(resumeE2ECfg)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(rig.stateDir, "state.json"), AuditPath: filepath.Join(rig.stateDir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	git := gitwt.New(rig.stateDir)
	git.RemoteURL = func(string) string { return rig.origin }
	git.Keep = st.KeepsWorkspace
	git.MinAge = time.Nanosecond // every unclaimed worktree is an orphan at once
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	logs := &logSink{}
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif, Log: logs.logf})
	var disp Dispatcher = fakeFlowDispatcher{}
	var paseoRunner controller.Runner = paseoStub{}
	var paseoSender controller.Sender = paseoStub{}
	if rig.paseo != "" {
		// The real paseo runtime over the fake paseo CLI — and the paseo
		// workspaces the acp runtime works in.
		d := dispatch.New(rig.paseo, config.Retry{}, false)
		d.Owned = dispatch.NewOwnedSet(filepath.Join(rig.stateDir, "owned.json"))
		d.CheckoutDir = func(context.Context, string) (string, error) { return rig.origin, nil }
		d.WorktreeCreator = func(ctx context.Context, _ dispatch.Request, _ string) (string, string, error) {
			dir, err := os.MkdirTemp(rig.root, "wt-")
			if err != nil {
				return "", "", err
			}
			out, err := exec.CommandContext(ctx, rig.paseo, "workspace", "create", "--isolation", "worktree",
				"--path", dir, "--title", "conductor/fix", "--json").Output()
			var ws struct{ WorkspaceID string }
			if err == nil {
				err = json.Unmarshal(out, &ws)
			}
			return ws.WorkspaceID, dir, err
		}
		disp, paseoRunner, paseoSender = d, d, d
	}
	ctl := controller.NewRegistry(cfg.MergedControllers(), cfg.DefaultRuntimeName(), paseoRunner, paseoSender,
		controller.WithCLIProvisioner(git))
	eng := New(Options{Config: &cfg, Store: st, Dispatch: disp, Notifier: notif,
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

// settle waits until nothing has written under dir for a moment.
func settle(dir string) {
	snapshot := func() string {
		var b strings.Builder
		_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err == nil {
				fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
			}
			return nil
		})
		return b.String()
	}
	prev := snapshot()
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		cur := snapshot()
		if cur == prev {
			return
		}
		prev = cur
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
// turn, its workspace (with uncommitted work) survives the restart, and the
// resumed step continues the SAME agent session in the SAME workspace through
// the runtime's own resume — the cli tool's --resume, the paseo agent still in
// the paseo daemon, the ACP agent's session/load.
func TestAgentStepResumesSessionAcrossDaemonRestart(t *testing.T) {
	for _, k := range []string{"cli", "paseo", "acp"} {
		t.Run(k, func(t *testing.T) { testAgentResumeAcrossRestart(t, resumeKinds[k], false) })
	}
}

// A resume the runtime cannot carry out (it no longer has the session) falls
// back to a fresh agent in the kept workspace, told about the restart.
func TestAgentStepFailedResumeStartsFreshInKeptWorkspace(t *testing.T) {
	for _, k := range []string{"cli", "paseo", "acp"} {
		t.Run(k, func(t *testing.T) { testAgentResumeAcrossRestart(t, resumeKinds[k], true) })
	}
}

func testAgentResumeAcrossRestart(t *testing.T, kind resumeKind, failResume bool) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	// Runs before TempDir's removal: the engine and the fakes it calls may
	// still be writing (a step's release, the store) when the test is done.
	t.Cleanup(func() { settle(root) })
	rig := resumeRig{kind: kind, root: root, stateDir: filepath.Join(root, "state"), origin: filepath.Join(root, "origin")}
	t.Setenv("HOME", filepath.Join(root, "home")) // paseo run workspaces' directories
	if err := os.MkdirAll(rig.origin, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, rig.origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(rig.origin, "README"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, rig.origin, "add", "README")
	gitIn(t, rig.origin, "commit", "-q", "-m", "init")

	fakes := filepath.Join(root, "fakes")
	if err := os.MkdirAll(fakes, 0o700); err != nil {
		t.Fatal(err)
	}
	toolLog := filepath.Join(fakes, "LOG")
	rig.cli = filepath.Join(root, "claude")
	if err := os.WriteFile(rig.cli, []byte(strings.ReplaceAll(fakeAgentTool, `"LOG`, `"`+toolLog)), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := func(name, role string) string {
		p := filepath.Join(root, name)
		sh := "#!/bin/sh\nAGENTRESUME_FAKE=" + role + " AGENTRESUME_STATE='" + fakes + "' exec '" + self + "' \"$@\"\n"
		if err := os.WriteFile(p, []byte(sh), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rig.acp = launcher("fake-acp", "acp")
	if kind.name != "cli" {
		rig.paseo = launcher("paseo", "paseo")
	}
	readLog := func() string { b, _ := os.ReadFile(toolLog); return string(b) }

	trig := flowTrigger("evt-1")
	trig.TargetTrusted = true
	trig.Target = core.Target{Repo: "acme/web", Number: 1, BaseRef: "main"}

	// Life 1: the step's agent starts, leaves uncommitted work, and the daemon
	// stops under it.
	d1 := startDaemon(t, rig)
	ctx1, stop1 := context.WithCancel(context.Background())
	d1.eng.process(ctx1, trig)
	waitLong(t, "the agent to start", 60*time.Second, func() bool { return strings.Contains(readLog(), "fresh ") })
	stop1()
	waitLong(t, "the run to be interrupted", 60*time.Second, func() bool { return d1.logs.has("interrupted by shutdown") })
	runs := d1.st.PendingRuns()
	if len(runs) != 1 || len(runs[0].Agents) != 1 {
		t.Fatalf("want one interrupted run holding its agent, got %+v", runs)
	}
	runID := runs[0].ID
	rec := runs[0].Agents["fix"]
	if rec.SessionID == "" || rec.Workspace == "" || rec.Cwd == "" || !rec.CanResume || rec.Runtime != kind.runtime {
		t.Fatalf("recorded agent = %+v", rec)
	}
	if !strings.Contains(readLog(), kind.fresh(rec)) {
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
	d2 := startDaemon(t, rig)
	defer d2.st.Close()
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	d2.git.Reap(ctx2)
	if b, err := os.ReadFile(filepath.Join(rec.Cwd, "wip.txt")); err != nil || string(b) != "half done" {
		t.Fatalf("the restart took the kept workspace's uncommitted work: %q %v", b, err)
	}
	d2.eng.ResumeWorkflows(ctx2)
	waitLong(t, "the run to finish", 60*time.Second, func() bool { return len(d2.st.PendingRuns()) == 0 })

	if strings.Count(readLog(), "fresh ") != 1 {
		t.Fatalf("an agent was started in a fresh workspace on resume:\n%s", readLog())
	}
	if failResume {
		if !strings.Contains(readLog(), kind.kept) || !strings.Contains(readLog(), " cwd="+rec.Cwd+" wip=half done\n") {
			t.Fatalf("want a fresh agent in the kept workspace after the failed resume:\n%s", readLog())
		}
		if !d2.logs.has(kind.failLog) || !d2.logs.has("fresh agent in kept workspace") {
			t.Fatalf("no fallback log line: %v", d2.logs.lines)
		}
		return
	}
	if !strings.Contains(readLog(), kind.resume(rec)) {
		t.Fatalf("the agent was not resumed with its session in its workspace; want %q in:\n%s", kind.resume(rec), readLog())
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
