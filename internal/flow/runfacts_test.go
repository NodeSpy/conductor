package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// A connector with a head face (connector.HeadReader) whose head a test
// scripts, and a recording verb — enough to watch workflow-level hooks and
// their {{.run.*}} facts without any one connector's API.
var headDecl = &connector.TypeDecl{
	Type: "fakehead",
	Desc: "Fake connector with a scriptable target head (run-facts tests).",
	Events: []connector.EventDecl{{
		Name:    "ping",
		Desc:    "a synthetic test event",
		Context: connector.Schema{"msg": {Type: connector.TString}},
	}},
	Verbs: []connector.VerbDecl{
		{Name: "post", Desc: "records its text",
			Options: connector.Schema{"text": {Type: connector.TString}},
			Outputs: connector.Schema{"id": {Type: connector.TInt}}},
		{Name: "fail", Desc: "always errors", Options: connector.Schema{}, Outputs: connector.Schema{}},
	},
}

func init() {
	connector.RegisterType(headDecl, func(name string, _ config.ConnectorRef, _ connector.Deps) (connector.Impl, error) {
		return &headImpl{name: name}, nil
	})
}

// headLog is one fakehead instance's script + record: TargetHead returns
// heads[0], heads[1], … (the last one repeating); every call lands in log.
type headLog struct {
	mu    sync.Mutex
	state string // the target state every read reports
	heads []string
	reads int
	log   []string
}

func (l *headLog) add(s string) {
	l.mu.Lock()
	l.log = append(l.log, s)
	l.mu.Unlock()
}

func (l *headLog) entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.log...)
}

// posts returns the recorded post texts in order.
func (l *headLog) posts() []string {
	var out []string
	for _, e := range l.entries() {
		if s, ok := strings.CutPrefix(e, "post:"); ok {
			out = append(out, s)
		}
	}
	return out
}

var (
	headLogsMu sync.Mutex
	headLogs   = map[string]*headLog{}
)

func headLogFor(name string) *headLog {
	headLogsMu.Lock()
	defer headLogsMu.Unlock()
	if headLogs[name] == nil {
		headLogs[name] = &headLog{}
	}
	return headLogs[name]
}

type headImpl struct{ name string }

func (h *headImpl) Validate() error          { return nil }
func (h *headImpl) DeclaredEvents() []string { return nil }
func (h *headImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (h *headImpl) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	l := headLogFor(h.name)
	if verb == "fail" {
		l.add("fail")
		return nil, errors.New("the hook's API said no")
	}
	l.add(fmt.Sprintf("post:%v", opts["text"]))
	return map[string]any{"id": 1}, nil
}
func (h *headImpl) TargetHead(context.Context, core.Trigger) (connector.TargetHead, error) {
	l := headLogFor(h.name)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log = append(l.log, "head")
	i := l.reads
	l.reads++
	if len(l.heads) == 0 {
		return connector.TargetHead{}, nil
	}
	if i >= len(l.heads) {
		i = len(l.heads) - 1
	}
	return connector.TargetHead{SHA: l.heads[i], State: l.state}, nil
}

// headRig builds a runner over one fakehead connector named name, whose head
// reads return heads in order, and whose agent dispatch is recorded in the
// same log (so ordering against hooks is visible).
func headRig(t *testing.T, name string, heads ...string) (*testRig, *headLog) {
	t.Helper()
	cfg := loadConfig(t, "connectors: { "+name+": { use: fakehead } }")
	reg := buildRegistry(t, cfg)
	headLogsMu.Lock()
	headLogs[name] = &headLog{heads: heads}
	headLogsMu.Unlock()
	rig := newTestRunner(t, cfg, reg)
	l := headLogFor(name)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		l.add("dispatch")
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	return rig, l
}

func headTrigger(conn string) core.Trigger {
	t := newTrigger("ping", map[string]any{"msg": "x"})
	t.Source, t.Instance = conn, conn
	t.Target.HeadSHA = "event-head-0000" // what the EVENT saw; the run reads its own
	return t
}

// factsSpec posts the run facts at every phase around one agent step.
const factsSpec = `
on: %[1]s.ping
hooks:
  - { at: start, uses: %[1]s.post, options: { text: "start start={{.run.start_sha}} head={{.run.head_sha}} pushed={{.run.pushed}}" } }
  - { at: done,  uses: %[1]s.post, options: { text: "done start={{.run.start_sha}} head={{.run.head_sha}} short={{.run.head_short}} pushed={{.run.pushed}} reason={{.run.reason}}" } }
  - { at: fail,  uses: %[1]s.post, options: { text: "fail reason={{.run.reason}} kind={{.hook.failure.kind}} pushed={{.run.pushed}} head={{.run.head_short}}" } }
steps:
  - id: fix
    type: agent
    prompt: p
`

// Start hooks fire before the agent is dispatched — before any worktree is
// provisioned (provisioning happens inside the dispatch) — and the head the
// run starts on is read right before them.
func TestStartHooksFireBeforeProvisioning(t *testing.T) {
	rig, l := headRig(t, "hs", "aaaaaaa1111")
	runTrigger(rig, headTrigger("hs"), mustSpec(t, fmt.Sprintf(factsSpec, "hs")))
	got := strings.Join(l.entries(), " | ")
	want := "head | post:start start=aaaaaaa1111 head=aaaaaaa1111 pushed=false | dispatch | head | post:done start=aaaaaaa1111 head=aaaaaaa1111 short=aaaaaaa pushed=false reason="
	if got != want {
		t.Fatalf("order:\n got  %s\n want %s", got, want)
	}
}

// run.start_sha is the head the run ACTUALLY starts on (read as it begins,
// after any slot wait), not the event's head; a head that moved during the
// run reads pushed, with the new head and its short form.
func TestRunFactsPushed(t *testing.T) {
	rig, l := headRig(t, "hp", "aaaaaaa1111", "bbbbbbb2222")
	runTrigger(rig, headTrigger("hp"), mustSpec(t, fmt.Sprintf(factsSpec, "hp")))
	posts := l.posts()
	if len(posts) != 2 {
		t.Fatalf("posts = %v", posts)
	}
	if posts[0] != "start start=aaaaaaa1111 head=aaaaaaa1111 pushed=false" {
		t.Fatalf("start facts = %q (start_sha must be the head read at run start, not the event's)", posts[0])
	}
	if posts[1] != "done start=aaaaaaa1111 head=bbbbbbb2222 short=bbbbbbb pushed=true reason=" {
		t.Fatalf("done facts = %q", posts[1])
	}
}

// A trigger with no hooks reads no head at all: run facts cost nothing where
// nothing uses them.
func TestRunFactsCostNothingWithoutHooks(t *testing.T) {
	rig, l := headRig(t, "hn", "aaaaaaa1111")
	runTrigger(rig, headTrigger("hn"), mustSpec(t, "on: hn.ping\nsteps:\n  - { id: fix, type: agent, prompt: p }\n"))
	for _, e := range l.entries() {
		if e == "head" {
			t.Fatalf("a hookless run read the head: %v", l.entries())
		}
	}
}

// Fail hooks fire on EVERY failure path, each with a public-safe run.reason
// (never the error text) and the matching hook.failure.kind.
func TestFailHooksOnEveryFailurePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error // the dispatch's error
		spec string
		want string
	}{
		{name: "dispatch never came up (the worktree error; escalates)",
			err:  dispatch.Unrecoverable(errors.New("gitwt: create worktree /home/x/.wt/pr-7: exit status 128")),
			want: "fail reason=the agent couldn't be started kind=gave_up"},
		{name: "agent ran and failed",
			err:  errors.New("agent exited 1: secret-ish detail"),
			want: `fail reason=step "fix" failed kind=ordinary`},
		{name: "timed out",
			err:  fmt.Errorf("wait: %w", context.DeadlineExceeded),
			want: "fail reason=timed out kind=ordinary"},
		{name: "a verb step failed",
			spec: "on: %[1]s.ping\nhooks:\n  - { at: fail, uses: %[1]s.post, options: { text: \"fail reason={{.run.reason}} kind={{.hook.failure.kind}}\" } }\nsteps:\n  - { id: call, uses: %[1]s.fail }\n",
			want: `fail reason=step "call" failed kind=ordinary`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, l := headRig(t, "hf", "aaaaaaa1111")
			if tc.err != nil {
				rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
					l.add("dispatch")
					return dispatch.RunRef{}, tc.err
				}
			}
			spec := tc.spec
			if spec == "" {
				spec = factsSpec
			}
			runTrigger(rig, headTrigger("hf"), mustSpec(t, fmt.Sprintf(spec, "hf")))
			var fails []string
			for _, p := range l.posts() {
				if strings.HasPrefix(p, "fail ") {
					fails = append(fails, p)
				}
			}
			if len(fails) != 1 || !strings.HasPrefix(fails[0], tc.want) {
				t.Fatalf("fail hooks = %v, want one starting %q", fails, tc.want)
			}
			for _, leak := range []string{"gitwt", "/home", "secret", "exit status"} {
				if strings.Contains(fails[0], leak) {
					t.Fatalf("run.reason leaks the error text (%q): %q", leak, fails[0])
				}
			}
		})
	}
}

// expect_push: a fixer that left its change unpushed is a failure, and its
// fail hooks say so.
func TestFailHooksOnNoProgress(t *testing.T) {
	rig, l := headRig(t, "hnp", "aaaaaaa1111")
	wd := gitWorktree(t, true)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: wd, Output: "done", AgentID: "a1"}, nil
	}
	spec := mustSpec(t, `
on: hnp.ping
hooks:
  - { at: fail, uses: hnp.post, options: { text: "fail reason={{.run.reason}} kind={{.hook.failure.kind}}" } }
steps:
  - { id: fix, type: agent, prompt: p, expect_push: true }
`)
	runTrigger(rig, headTrigger("hnp"), spec)
	if p := l.posts(); len(p) != 1 || p[0] != "fail reason=the change was never pushed kind=no_progress" {
		t.Fatalf("posts = %v", p)
	}
}

// A panic mid-run still fires the fail hooks (then carries on to the
// engine's recovery).
func TestFailHooksOnPanic(t *testing.T) {
	rig, l := headRig(t, "hpn", "aaaaaaa1111")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) { panic("boom") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic must carry on to the engine's recovery")
			}
		}()
		runTrigger(rig, headTrigger("hpn"), mustSpec(t, fmt.Sprintf(factsSpec, "hpn")))
	}()
	p := l.posts()
	if len(p) != 2 || !strings.HasPrefix(p[1], "fail reason=internal error kind=internal") {
		t.Fatalf("posts = %v", p)
	}
}

// A hook that fails never fails the run, in any phase.
func TestHookFailureNeverFailsTheRun(t *testing.T) {
	rig, l := headRig(t, "hk", "aaaaaaa1111")
	spec := mustSpec(t, `
on: hk.ping
hooks:
  - { at: start, uses: hk.fail }
  - { at: done,  uses: hk.fail }
  - { at: done,  uses: hk.post, options: { text: after } }
steps:
  - { id: fix, type: agent, prompt: p }
`)
	runTrigger(rig, headTrigger("hk"), spec)
	if failed, why := rig.workflowFailed(); failed {
		t.Fatalf("a failing hook failed the run: %s", why)
	}
	if got := strings.Join(l.entries(), " "); got != "head fail dispatch head fail post:after" {
		t.Fatalf("lifecycle = %q (the run and the later hook must carry on past a failed hook)", got)
	}
}

// FireParkedHooks: an event the engine parked runs the trigger's fail hooks
// — kind "parked", the parked reason, start_sha = the head it parked at — and
// nothing else (no start hooks: nothing started).
func TestFireParkedHooks(t *testing.T) {
	cfg := loadConfig(t, `
connectors: { hpk: { use: fakehead } }
triggers:
  - on: hpk.ping
    hooks:
      - { at: start, uses: hpk.post, options: { text: "start" } }
      - { at: fail,  uses: hpk.post, options: { text: "fail kind={{.hook.failure.kind}} gave_up={{.hook.failure.gave_up}} start={{.run.start_sha}} reason={{.run.reason}}" } }
    steps:
      - { id: fix, type: agent, prompt: p }
`)
	reg := buildRegistry(t, cfg)
	headLogsMu.Lock()
	headLogs["hpk"] = &headLog{heads: []string{"ccccccc3333"}}
	headLogsMu.Unlock()
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.FireParkedHooks(context.Background(), headTrigger("hpk"), "0:hpk.ping", "ccccccc3333", 6)
	want := "fail kind=parked gave_up=true start=ccccccc3333 reason=" + ParkedReason
	if p := headLogFor("hpk").posts(); len(p) != 1 || p[0] != want {
		t.Fatalf("parked hooks = %v\nwant [%s]", p, want)
	}
}

// `conductor validate` admits the hook contract where hooks see it — every
// phase, start|done|fail|stop, at both levels: {{.run.*}} and {{.hook.*}} in
// workflow-level hooks, {{.hook.*}} in step hooks — and refuses {{.run.*}} in
// a step hook (run facts are workflow-level).
func TestValidateHookScope(t *testing.T) {
	good := loadConfig(t, `
connectors: { hv: { use: fakehead } }
triggers:
  - on: hv.ping
    hooks:
      - { at: start, uses: hv.post, options: { text: "{{.run.start_sha}} {{.hook.phase}}" } }
      - { at: done,  if: "run.pushed", uses: hv.post, options: { text: "{{.run.head_short}}" } }
      - { at: fail,  uses: hv.post, options: { text: "{{.run.reason}} {{.hook.failure.kind}} {{.error}}" } }
      - { at: stop,  uses: hv.post, options: { text: "{{.run.reason}} {{.run.start_sha}} {{.hook.status}}" } }
    steps:
      - id: s
        uses: hv.post
        options: { text: x }
        hooks:
          - { at: fail, uses: hv.post, options: { text: "{{.hook.failure.kind}}" } }
          - { at: stop, uses: hv.post, options: { text: "{{.hook.phase}}" } }
`)
	if err := Validate(good, buildRegistry(t, good)); err != nil {
		t.Fatalf("the hook contract should validate: %v", err)
	}
	bad := loadConfig(t, `
connectors: { hv2: { use: fakehead } }
triggers:
  - on: hv2.ping
    steps:
      - id: s
        uses: hv2.post
        options: { text: x }
        hooks:
          - { at: done, uses: hv2.post, options: { text: "{{.run.pushed}}" } }
`)
	if err := Validate(bad, buildRegistry(t, bad)); err == nil || !strings.Contains(err.Error(), "run.pushed") {
		t.Fatalf("run facts in a STEP hook should be refused at load, got %v", err)
	}
}

// assertInterrupted: the run was recorded interrupted by shutdown — not ok,
// not failed — and its terminal notifications never went out.
func assertInterrupted(t *testing.T, rig *testRig) {
	t.Helper()
	if len(rig.Store.auditsWithEvent("workflow_interrupted")) != 1 {
		t.Fatalf("the run was not recorded interrupted (audit: %v)", rig.Store.auditsWithEvent("workflow_failed"))
	}
	if failed, _ := rig.workflowFailed(); failed {
		t.Fatal("an interrupted run was recorded failed")
	}
	for _, e := range rig.Notifier.snapshot() {
		if e.Event == "complete" || e.Event == "failed" || e.Event == "escalate" {
			t.Fatalf("an interrupted run emitted %q", e.Event)
		}
	}
}

// stepErrorText is the last step_error audit's error.
func stepErrorText(rig *testRig) string {
	es := rig.Store.auditsWithEvent("step_error")
	if len(es) == 0 {
		return ""
	}
	s, _ := es[len(es)-1]["error"].(string)
	return s
}

// stopSpec posts every terminal phase, at workflow and step level.
const stopSpec = `
on: %[1]s.ping
hooks:
  - { at: done, uses: %[1]s.post, options: { text: "done" } }
  - { at: fail, uses: %[1]s.post, options: { text: "fail" } }
  - { at: stop, uses: %[1]s.post, options: { text: "stop start={{.run.start_sha}} head={{.run.head_sha}} pushed={{.run.pushed}} reason={{.run.reason}} status={{.hook.status}}" } }
steps:
  - id: fix
    type: agent
    prompt: p
    hooks:
      - { at: fail, uses: %[1]s.post, options: { text: "step-fail" } }
      - { at: stop, uses: %[1]s.post, options: { text: "step-stop {{.hook.phase}}" } }
`

// The target closed under the run (the PR merged or closed; conductor stopped
// the fixer): the `stop` hooks fire — step-level then workflow-level — with
// run facts and a reason naming merged vs closed; no fail or done hooks.
func TestStopHooksOnTargetClosed(t *testing.T) {
	for _, tc := range []struct{ state, reason string }{
		{"merged", "the PR merged"}, {"closed", "the PR closed"}, {"", "the PR closed"},
	} {
		t.Run("state="+tc.state, func(t *testing.T) {
			rig, l := headRig(t, "hst", "aaaaaaa1111", "bbbbbbb2222")
			l.state = tc.state
			rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
				return dispatch.RunRef{}, dispatch.ErrTargetClosed
			}
			runTrigger(rig, headTrigger("hst"), mustSpec(t, fmt.Sprintf(stopSpec, "hst")))
			want := "step-stop stop | stop start=aaaaaaa1111 head=bbbbbbb2222 pushed=true reason=" + tc.reason + " status=stopped"
			if got := strings.Join(l.posts(), " | "); got != want {
				t.Fatalf("hooks:\n got  %s\n want %s", got, want)
			}
			if len(rig.Store.auditsWithEvent("workflow_stopped")) != 1 {
				t.Fatal("the run wasn't recorded stopped")
			}
		})
	}
}

// Stop hooks fire on NOTHING else: not on done, not on fail.
func TestStopHooksNotOnDoneOrFail(t *testing.T) {
	rig, l := headRig(t, "hsd", "aaaaaaa1111")
	runTrigger(rig, headTrigger("hsd"), mustSpec(t, fmt.Sprintf(stopSpec, "hsd")))
	if got := strings.Join(l.posts(), " | "); got != "done" {
		t.Fatalf("a successful run fired %q, want only done", got)
	}
	rig, l = headRig(t, "hsf", "aaaaaaa1111")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, dispatch.Unrecoverable(errors.New("gitwt: exit status 128"))
	}
	runTrigger(rig, headTrigger("hsf"), mustSpec(t, fmt.Sprintf(stopSpec, "hsf")))
	if got := strings.Join(l.posts(), " | "); got != "step-fail | fail" {
		t.Fatalf("a failed run fired %q, want only the fail hooks", got)
	}
}

// A daemon shutdown under the run is neither a stop nor a failure: no
// terminal hook fires at either level, nothing is notified, and the run
// record is kept (not finished) so it resumes on restart.
func TestShutdownIsNotAStop(t *testing.T) {
	rig, l := headRig(t, "hsh", "aaaaaaa1111")
	ctx, cancel := context.WithCancel(context.Background())
	rig.Agents.dispatchFunc = func(c context.Context, _ dispatch.Request) (dispatch.RunRef, error) {
		cancel() // the daemon shuts down while the agent works
		<-c.Done()
		return dispatch.RunRef{}, c.Err()
	}
	run := emptyRun()
	run.ID = "run-1"
	spec := mustSpec(t, fmt.Sprintf(stopSpec, "hsh"))
	rig.Runner.Run(ctx, run, headTrigger("hsh"), spec, rig.Runner.IndexOf(spec), nil, false)
	if p := l.posts(); len(p) != 0 {
		t.Fatalf("a shutdown fired terminal hooks: %v", p)
	}
	assertInterrupted(t, rig)
	for _, id := range rig.Store.delLog {
		if id == "run-1" {
			t.Fatal("an interrupted run's record was deleted — it would never resume")
		}
	}
}

// Shutdown vs a run's OWN deadline: a step timeout under a live daemon is a
// failure (fail hooks), never mistaken for an interruption.
func TestOwnTimeoutIsAFailureNotAShutdown(t *testing.T) {
	rig, l := headRig(t, "hto", "aaaaaaa1111")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, fmt.Errorf("wait: %w", context.DeadlineExceeded)
	}
	runTrigger(rig, headTrigger("hto"), mustSpec(t, fmt.Sprintf(stopSpec, "hto")))
	if got := strings.Join(l.posts(), " | "); got != "step-fail | fail" {
		t.Fatalf("a timed-out run fired %q, want the fail hooks", got)
	}
}

// A run whose OWN context runs out of time (a caller's deadline — a timed
// one-shot run) timed out: that is a failure with fail hooks, not a shutdown.
// Only a cancellation of the run's context is the daemon going away.
func TestRunDeadlineIsAFailureNotAShutdown(t *testing.T) {
	rig, l := headRig(t, "hdl", "aaaaaaa1111")
	rig.Agents.dispatchFunc = func(c context.Context, _ dispatch.Request) (dispatch.RunRef, error) {
		<-c.Done()
		return dispatch.RunRef{}, c.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	runTriggerCtx(ctx, rig, headTrigger("hdl"), mustSpec(t, fmt.Sprintf(stopSpec, "hdl")))
	if got := strings.Join(l.posts(), " | "); got != "step-fail | fail" {
		t.Fatalf("a run past its deadline fired %q, want the fail hooks", got)
	}
	if len(rig.Store.auditsWithEvent("workflow_interrupted")) != 0 {
		t.Fatal("a run's own deadline was taken for a shutdown")
	}
}
