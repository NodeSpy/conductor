package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// One-shot mode (once.go) end-to-end through its internal entrypoint: a real
// config, a real fixture event, real subprocess steps. Nothing is stubbed —
// these tests assert the steps ACTUALLY RAN (a marker file on disk), which is
// the whole distinction from `replay`.

// onceFixture is a pull_request review_requested delivery for
// AcmeCorp/Widget#5300 — the same shape TestCmdReplayConnectorsModel uses.
const onceFixture = `{"event": "pull_request", "body": {
  "action": "review_requested",
  "installation": { "id": 0 },
  "repository": { "full_name": "AcmeCorp/Widget", "name": "Widget",
    "default_branch": "main", "owner": { "login": "AcmeCorp" } },
  "pull_request": { "number": 5300, "state": "open", "draft": false,
    "title": "auth: rework session refresh",
    "html_url": "https://github.com/AcmeCorp/Widget/pull/5300",
    "head": { "sha": "cafebabe1234", "ref": "feature/auth-refresh" },
    "base": { "ref": "main" }, "user": { "login": "someone-else" } },
  "requested_reviewer": { "login": "danielcbaldwin" }
}}`

// onceConnectors is the connector block every one-shot test shares: a github
// connector with the sweep and webhook inert (nothing must listen in one-shot
// mode) and its repo list covering the fixture's PR.
const onceConnectors = `
connectors:
  gh:
    use: github
    token: dummy-once-token
    me: { logins: [danielcbaldwin] }
    repos: ["AcmeCorp/Widget"]
    webhook: { listen: "127.0.0.1:0", secret: once-test }
    sweep: { enabled: false }
`

// writeOnceCase writes a config + fixture into a fresh temp dir and returns
// the loaded config and the fixture path, ready for runOnce.
func writeOnceCase(t *testing.T, triggers string) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(onceConnectors+triggers), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "event.json")
	if err := os.WriteFile(fixture, []byte(onceFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg, fixture
}

// onceRun drives runOnce with the job log captured, so a test reads what an
// Actions job would see without racing on os.Stdout.
func onceRun(t *testing.T, cfg *config.Config, o onceOptions) (string, error) {
	t.Helper()
	var log strings.Builder
	o.stdout = func(s string) { log.WriteString(s + "\n") }
	o.stderr = func(s string) { log.WriteString(s + "\n") }
	err := runOnce(context.Background(), cfg, o)
	return log.String(), err
}

// exitCodeOf maps a runOnce error onto the process status main would exit
// with: 0 for nil, the carried code for an exitError, else 1.
func exitCodeOf(err error) int {
	if err == nil {
		return onceExitOK
	}
	if ex, ok := err.(*exitError); ok {
		return ex.code
	}
	return 1
}

// TestOnceRealExecution: the matched trigger's `use: cli` step really runs —
// the marker file it writes exists afterwards — and the job exits 0.
func TestOnceRealExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran.txt")
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - id: mark
        use: cli
        command: ["sh", "-c", "printf ok > `+marker+`"]
`)
	out, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture})
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if code := exitCodeOf(err); code != onceExitOK {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}
	// THE assertion: the step executed for real, not dry.
	b, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("step did not run (no marker at %s): %v\n%s", marker, rerr, out)
	}
	if string(b) != "ok" {
		t.Errorf("marker = %q, want %q", b, "ok")
	}
	for _, want := range []string{`running trigger "review"`, "review_requested AcmeCorp/Widget#5300", "outcome: ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("job log missing %q:\n%s", want, out)
		}
	}
	// A dry-run stub would have said so; a real one must not.
	if strings.Contains(out, "dry-run") || strings.Contains(out, "would run") {
		t.Errorf("one-shot mode streamed dry-run output:\n%s", out)
	}
}

// TestOnceStepFailureExits3: a step that exits non-zero fails the job.
func TestOnceStepFailureExits3(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - id: boom
        use: cli
        command: ["sh", "-c", "exit 7"]
`)
	out, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture,
		failOn: []string{failOnStepError, failOnGateReject}})
	if code := exitCodeOf(err); code != onceExitOutcome {
		t.Fatalf("exit code = %d, want %d (err %v)\n%s", code, onceExitOutcome, err, out)
	}
	for _, want := range []string{"boom", "step failed", "outcome: failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("job log missing %q:\n%s", want, out)
		}
	}
}

// TestOnceFailOnNonePasses: `--fail-on none` reports the same failure and
// still exits 0 — a job that wants the run's output without gating on it.
func TestOnceFailOnNonePasses(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - id: boom
        use: cli
        command: ["sh", "-c", "exit 7"]
`)
	out, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture, failOn: nil})
	if code := exitCodeOf(err); code != onceExitOK {
		t.Fatalf("--fail-on none: exit code = %d, want 0 (err %v)\n%s", code, err, out)
	}
	if !strings.Contains(out, "step failed") {
		t.Errorf("--fail-on none must still REPORT the failure:\n%s", out)
	}
}

// TestOnceFailOnGateRejectOnly: with only gate-reject selected, a plain step
// error no longer fails the job — the mapping is honored per category.
func TestOnceFailOnGateRejectOnly(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - id: boom
        use: cli
        command: ["sh", "-c", "exit 7"]
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture,
		failOn: []string{failOnGateReject}})
	if code := exitCodeOf(err); code != onceExitOK {
		t.Fatalf("--fail-on gate-reject with a step error: exit code = %d, want 0 (err %v)", code, err)
	}
}

// TestOnceNoMatchExitsZero: a job that fired on an event this trigger does not
// listen for is a PASS — the predicate simply did not hold — and the steps
// must not have run.
func TestOnceNoMatchExitsZero(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran.txt")
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.new_comment
    name: comment
    steps:
      - id: mark
        use: cli
        command: ["sh", "-c", "printf ok > `+marker+`"]
`)
	out, err := onceRun(t, cfg, onceOptions{trigger: "comment", fixturePath: fixture})
	if code := exitCodeOf(err); code != onceExitOK {
		t.Fatalf("no-match: exit code = %d, want 0 (err %v)\n%s", code, err, out)
	}
	if !strings.Contains(out, "no match") {
		t.Errorf("job log missing the no-match line:\n%s", out)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("a non-matching event still ran the trigger's steps")
	}
}

// TestOnceRequireMatchFails: --require-match turns the same non-match into a
// failure, for a job whose whole purpose is that the trigger fires.
func TestOnceRequireMatchFails(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.new_comment
    name: comment
    steps:
      - { id: noop, use: cli, command: ["true"] }
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "comment", fixturePath: fixture, requireMatch: true})
	if code := exitCodeOf(err); code != onceExitOutcome {
		t.Fatalf("--require-match: exit code = %d, want %d (err %v)", code, onceExitOutcome, err)
	}
	if err == nil || !strings.Contains(err.Error(), "--require-match") {
		t.Errorf("--require-match error should say so, got %v", err)
	}
}

// TestOncePaseoRejected: a step that resolves to the paseo runtime is a clear
// ERROR at load — never a silent fallback — because the paseo daemon is not in
// the runner. Both the implicit default and an explicit paseo runtime.
func TestOncePaseoRejected(t *testing.T) {
	cases := []struct {
		name     string
		triggers string
	}{
		{"implicit default", `
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - { id: fix, agent: dev, prompt: "fix it" }
`},
		{"explicit paseo runtime", `
runtimes:
  box: { use: paseo }
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - { id: fix, agent: dev, runtime: box, prompt: "fix it" }
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, fixture := writeOnceCase(t, tc.triggers)
			_, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture})
			if err == nil {
				t.Fatal("a paseo-runtime step must be refused in one-shot mode")
			}
			if code := exitCodeOf(err); code != 1 {
				t.Errorf("exit code = %d, want 1 (a config error, not an outcome)", code)
			}
			for _, want := range []string{"paseo daemon", "conductor once"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error missing %q: %v", want, err)
				}
			}
		})
	}
}

// TestOnceBackgroundHandoffRejected: an interactive hand-off step has no human
// in CI, so it is refused up front rather than left to hang.
func TestOnceBackgroundHandoffRejected(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
runtimes:
  local: { use: cli, command: ["true"], default: true }
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - { id: handoff, agent: dev, background: true, prompt: "review with me" }
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture})
	if err == nil || !strings.Contains(err.Error(), "no human in a one-shot run") {
		t.Fatalf("a background hand-off step must be refused, got %v", err)
	}
}

// TestOnceAskVerbRejected: an `ask` verb blocks for a reply that cannot come.
func TestOnceAskVerbRejected(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
  ask:
    use: web
    listen: "127.0.0.1:0"
triggers:
  - on: gh.review_requested
    name: review
    steps:
      - { id: confirm, uses: ask.ask, options: { prompt: "ship it?" } }
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture})
	if err == nil || !strings.Contains(err.Error(), "blocks for their answer") {
		t.Fatalf("an ask verb must be refused in one-shot mode, got %v", err)
	}
}

// TestOnceUnknownTrigger names what IS runnable rather than just failing.
func TestOnceUnknownTrigger(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps: [{ id: noop, use: cli, command: ["true"] }]
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "nope", fixturePath: fixture})
	if err == nil || !strings.Contains(err.Error(), "no trigger named") || !strings.Contains(err.Error(), "review") {
		t.Fatalf("unknown trigger should list the named ones, got %v", err)
	}
}

// TestOnceManualTriggerRefused: `on: manual` has no event to process; point
// the operator at the command that does fire one.
func TestOnceManualTriggerRefused(t *testing.T) {
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: manual
    name: chore
    steps: [{ id: noop, use: cli, command: ["true"] }]
`)
	_, err := onceRun(t, cfg, onceOptions{trigger: "chore", fixturePath: fixture})
	if err == nil || !strings.Contains(err.Error(), "manual trigger") {
		t.Fatalf("a manual trigger should be refused with guidance, got %v", err)
	}
}

// TestOnceEphemeralStateByDefault: the run leaves nothing behind in the
// configured state dir (design O4) — a runner is thrown away, so dedup /
// history / blobs go with it.
func TestOnceEphemeralStateByDefault(t *testing.T) {
	stateDir := t.TempDir()
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps: [{ id: noop, use: cli, command: ["true"] }]
`)
	cfg.Store.StateFile = filepath.Join(stateDir, "state.json")
	cfg.Store.AuditLog = filepath.Join(stateDir, "audit.jsonl")
	if _, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture}); err != nil {
		t.Fatalf("once: %v", err)
	}
	ents, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("one-shot mode wrote durable state by default: %s", strings.Join(names, ", "))
	}
}

// TestOnceDurableStateWhenAsked: --state-dir (or a config that points
// store.state_file somewhere on purpose) keeps the run record.
func TestOnceDurableStateWhenAsked(t *testing.T) {
	stateDir := t.TempDir()
	cfg, fixture := writeOnceCase(t, `
triggers:
  - on: gh.review_requested
    name: review
    steps: [{ id: noop, use: cli, command: ["true"] }]
`)
	cfg.Store.StateFile = filepath.Join(stateDir, "state.json")
	cfg.Store.AuditLog = filepath.Join(stateDir, "audit.jsonl")
	if _, err := onceRun(t, cfg, onceOptions{trigger: "review", fixturePath: fixture, durableState: true}); err != nil {
		t.Fatalf("once: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "history")); err != nil {
		t.Errorf("--state-dir should keep the run history: %v", err)
	}
}

// TestParseOnceFlags covers the flag surface, including the pass-through of
// --config/--state-dir to configPath and the trigger positional.
func TestParseOnceFlags(t *testing.T) {
	o, rest, err := parseOnceFlags([]string{
		"review", "--event", "/e.json", "--event-name", "pull_request",
		"--config", "/c.yaml", "--fail-on", "gate-reject", "--require-match",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.eventPath != "/e.json" || o.eventName != "pull_request" || !o.requireMatch {
		t.Errorf("flags not parsed: %+v", o)
	}
	if len(o.failOn) != 1 || o.failOn[0] != failOnGateReject {
		t.Errorf("--fail-on = %v, want [gate-reject]", o.failOn)
	}
	if strings.Join(rest, " ") != "review --config /c.yaml" {
		t.Errorf("rest = %v, want the trigger plus --config passed through", rest)
	}

	// --state-dir is passed through AND noted, so state stays durable.
	o, rest, err = parseOnceFlags([]string{"review", "--state-dir", "/s"})
	if err != nil || !o.durableState || strings.Join(rest, " ") != "review --state-dir /s" {
		t.Errorf("--state-dir: durable=%v rest=%v err=%v", o.durableState, rest, err)
	}

	// Unknown categories and missing values are refused, not guessed.
	if _, _, err := parseOnceFlags([]string{"review", "--fail-on", "everything"}); err == nil {
		t.Error("unknown --fail-on category should error")
	}
	if _, _, err := parseOnceFlags([]string{"review", "--event"}); err == nil {
		t.Error("--event with no value should error")
	}
	// The default is every category.
	o, _, _ = parseOnceFlags([]string{"review"})
	if len(o.failOn) != len(onceFailOnCategories) {
		t.Errorf("default --fail-on = %v, want %v", o.failOn, onceFailOnCategories)
	}
	// Defaults come from the two variables Actions sets.
	t.Setenv("GITHUB_EVENT_PATH", "/gh/event.json")
	t.Setenv("GITHUB_EVENT_NAME", "issue_comment")
	o, _, _ = parseOnceFlags([]string{"review"})
	if o.eventPath != "/gh/event.json" || o.eventName != "issue_comment" {
		t.Errorf("Actions env defaults not picked up: %+v", o)
	}
}

// TestReadOnceEvent covers both intakes: the Actions pair (a raw payload plus
// an event name, no signature) and the replay fixture.
func TestReadOnceEvent(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "event.json")
	if err := os.WriteFile(body, []byte(`{"action":"opened"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ev, err := readOnceEvent(onceOptions{eventPath: body, eventName: "pull_request"})
	if err != nil || ev.Name != "pull_request" || string(ev.Body) != `{"action":"opened"}` {
		t.Fatalf("actions intake: %+v %v", ev, err)
	}

	fx := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fx, []byte(onceFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	ev, err = readOnceEvent(onceOptions{fixturePath: fx})
	if err != nil || ev.Name != "pull_request" {
		t.Fatalf("fixture intake: %+v %v", ev, err)
	}

	// Each missing half of the Actions pair names itself.
	if _, err := readOnceEvent(onceOptions{}); err == nil || !strings.Contains(err.Error(), "--event") {
		t.Errorf("missing event should name --event, got %v", err)
	}
	if _, err := readOnceEvent(onceOptions{eventPath: body}); err == nil || !strings.Contains(err.Error(), "--event-name") {
		t.Errorf("missing event name should name --event-name, got %v", err)
	}
	// A payload that is not JSON fails here, not three layers down.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("not json"), 0o600)
	if _, err := readOnceEvent(onceOptions{eventPath: bad, eventName: "push"}); err == nil {
		t.Error("a non-JSON payload should be refused")
	}
}

// TestOnceResultFiredCategories pins the outcome→exit-code mapping itself.
func TestOnceResultFiredCategories(t *testing.T) {
	all := onceFailOnCategories
	cases := []struct {
		name string
		res  onceResult
		want string
	}{
		{"clean run", onceResult{status: "ok"}, ""},
		{"step error", onceResult{status: "failed", stepFail: []string{"build: exit 1"}}, failOnStepError},
		{"gate reject", onceResult{status: "failed", gateFail: []string{"fix: tests"}}, failOnGateReject},
		{"both", onceResult{status: "failed", stepFail: []string{"a"}, gateFail: []string{"b"}},
			failOnStepError + "," + failOnGateReject},
		// A run that failed without attributing a step still fails: an
		// unattributed failure must never read as a pass.
		{"unattributed failure", onceResult{status: "failed"}, failOnStepError},
	}
	for _, tc := range cases {
		got := strings.Join(tc.res.firedCategories(all), ",")
		if got != tc.want {
			t.Errorf("%s: fired = %q, want %q", tc.name, got, tc.want)
		}
	}
	// A category that was not requested never fires.
	res := onceResult{status: "failed", stepFail: []string{"a"}, gateFail: []string{"b"}}
	if got := strings.Join(res.firedCategories([]string{failOnGateReject}), ","); got != failOnGateReject {
		t.Errorf("selective fail-on: fired = %q, want %q", got, failOnGateReject)
	}
	if got := res.firedCategories(nil); len(got) != 0 {
		t.Errorf("--fail-on none: fired = %v, want nothing", got)
	}
}

// TestParseFailOn covers the list parser directly.
func TestParseFailOn(t *testing.T) {
	got, err := parseFailOn("step-error, gate-reject")
	if err != nil || len(got) != 2 {
		t.Fatalf("parseFailOn: %v %v", got, err)
	}
	if got, err := parseFailOn("none"); err != nil || got != nil {
		t.Errorf("none should be the empty set: %v %v", got, err)
	}
	if _, err := parseFailOn("step-error,nope"); err == nil {
		t.Error("unknown category should error")
	}
}

// TestWalkOnceSteps: the refusal walk reaches nested steps — parallel
// branches, compensations, and the steps of a called workflow — so an
// unsupported step cannot hide one level down.
func TestWalkOnceSteps(t *testing.T) {
	cfg := &config.Config{
		Workflows: map[string]config.WorkflowDef{
			"sub": {Steps: []config.Step{{ID: "inner", Agent: "dev"}}},
		},
	}
	spec := config.TriggerSpec{Name: "review", On: "gh.review_requested", Steps: []config.Step{
		{ID: "top"},
		{ID: "fan", Parallel: &config.ParallelSpec{Branches: [][]config.Step{{{ID: "b0"}}}}},
		{ID: "undoable", Compensate: &config.Step{ID: "undo"}},
		{ID: "callsub", Workflow: "sub"},
	}}
	seen := map[string]bool{}
	walkOnceSteps(cfg, spec, func(where string, s config.Step) { seen[s.ID] = true })
	for _, want := range []string{"top", "fan", "b0", "undoable", "undo", "callsub", "inner"} {
		if !seen[want] {
			t.Errorf("walk missed step %q (seen: %v)", want, seen)
		}
	}
}

// TestOnceRuntimeOf pins the runtime ladder the paseo refusal reads: explicit
// → fleet default → the built-in paseo, with a paseo-TYPE named runtime
// collapsing onto paseo.
func TestOnceRuntimeOf(t *testing.T) {
	bare := &config.Config{}
	if got := onceRuntimeOf(bare, config.Step{Agent: "dev"}); got != config.BuiltinPaseoRuntime {
		t.Errorf("no runtime, no default → %q, want paseo", got)
	}
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"local": {Use: "cli", Command: []string{"true"}, Default: true},
		"box":   {Use: "paseo"},
	}}
	if got := onceRuntimeOf(cfg, config.Step{Agent: "dev"}); got != "local" {
		t.Errorf("fleet default → %q, want local", got)
	}
	if got := onceRuntimeOf(cfg, config.Step{Agent: "dev", Runtime: "box"}); got != config.BuiltinPaseoRuntime {
		t.Errorf("a paseo-TYPE runtime must collapse onto paseo, got %q", got)
	}
}

// TestOnceStartsNoDaemonSurfaces is the structural guard for the mode's whole
// premise: one-shot must start NONE of the daemon's background machinery.
// cmdRun wires each of these; once.go must not, and a future edit that adds
// one back fails here rather than in a hung Actions job.
func TestOnceStartsNoDaemonSurfaces(t *testing.T) {
	src, err := os.ReadFile("once.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	// Everything cmdRun launches that must never run for one event. The
	// engine's own loop (eng.Run) is the important one: it owns the trigger
	// queue, the GC loop and the affinity sweep, and one-shot calls the flow
	// runner directly instead.
	for _, forbidden := range []string{
		"serveControl(",     // the control socket
		"eng.Run(",          // the engine event loop (GC + affinity sweep)
		"ResumeWorkflows(",  // mid-flight workflow resume
		"autoUpdateLoop(",   // periodic self-update
		"digestLoop(",       // the activity digest
		"dispatch.Reaper{",  // the archive-when-done reaper
		"gitProv.Run(",      // the git-worktree orphan sweep
		"callable.New(",     // the inbound invoke HTTP surface
		"inbound.Register(", // the shared inbound listener (webhook/handoff pages)
		"memory.ListenSocket(",
		"memory.ServeIPC(",
		"handoff.RunDiscordGateway(",
		"ig.Start(",                    // source integrations (webhook/smee/sweep watchers)
		"connector.SetSweepHook(",      // the gh.sweep verb's nudge
		"connector.SetConductorOps(",   // daemon self-ops verbs
		"notifier.SetPublisher(",       // lifecycle → conductor.* (needs a live engine)
		"dispatch.InitSkillTunnels(",   // per-host SSH reverse tunnels
		"wireConnectorSurfaces(",       // web ask pages + discord gateways
		"signal.Notify",                // SIGUSR1 sweep-now
		"os.WriteFile(pidFile",         // the daemon pidfile
		"controlSockPath(", "pidPath(", // even the paths
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("once.go starts a daemon surface it must not: %q", forbidden)
		}
	}
	// And the positive half: it must run the pipeline NON-dry.
	for _, want := range []string{"buildFlowStack(cfg, st, notifier, false)", "dispatch.New(paseoBin, retry, false)", "stack.Runner.Run(ctx, run, t, spec, tidx, nil, false)"} {
		if !strings.Contains(body, want) {
			t.Errorf("once.go should execute for real: missing %q", want)
		}
	}
}
