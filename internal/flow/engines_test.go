package flow

import (
	"os/exec"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// lastPostText returns the `text:` option of the last svc.post the fake
// connector saw — how these tests read a step's outputs back out.
func lastPostText(t *testing.T, st *fakeState) string {
	t.Helper()
	calls := st.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Verb == "post" {
			s, _ := calls[i].Opts["text"].(string)
			return s
		}
	}
	t.Fatal("no svc.post call recorded")
	return ""
}

// Step dispatch by engine: a step whose resolved engine is `cli` runs its
// argv; `js` still runs in-process; a bare host-interpreter name still
// shells out — one `execCode` path, three destinations.

func TestExecCodeRoutesByEngine(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: argv,  use: cli, command: [sh, -c, "echo '{\"who\": \"cli\"}'"] }
  - { id: piped, use: cli, command: [sh, -c, cat] }
  - { id: inproc, use: js, code: "return { who: 'js' }" }
  - { id: host, run: sh, code: "echo '{\"who\": \"host\"}'" }
  - id: after
    uses: svc.post
    options:
      text: "{{.argv.who}}/{{.piped.msg}}/{{.inproc.who}}/{{.host.who}}"
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "ctx-reached-stdin"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	got := lastPostText(t, st)
	if got != "cli/ctx-reached-stdin/js/host" {
		t.Fatalf("engine routing: %q", got)
	}
}

// The cli engine's argv is templated like any other execution input.
func TestCLICommandIsTemplated(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: echo, use: cli, command: [sh, -c, "printf '%s' \"$1\"", sh, "{{.msg}}"] }
  - { id: after, uses: svc.post, options: { text: "{{.echo.text}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "rendered"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("run failed: %s", errStr)
	}
	if got := lastPostText(t, st); got != "rendered" {
		t.Fatalf("command: was not templated: %q", got)
	}
}

// A dry run stubs a cli step exactly as it stubs any other code step —
// nothing is executed.
func TestCLIStepIsStubbedUnderDryRun(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.DryRun = true
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: c, use: cli, command: [/bin/false] }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	// A cli step that actually ran would have exited non-zero and failed the
	// workflow, so a clean run IS the evidence that nothing was executed.
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("a stubbed cli step must not run its argv: %s", errStr)
	}
}

// A `use: cli` step classes as "command" for the agent-authored plan
// allowlist: it runs an argv, which is the capability an operator grants
// with `allow: [command]` (or its `cli` alias) — not the sandboxed-code one.
func TestCLIEngineStepClassesAsCommand(t *testing.T) {
	for _, tc := range []struct {
		step config.Step
		want string
	}{
		{config.Step{Use: "cli", Command: config.Argv{"make"}}, "command"},
		{config.Step{Run: "cli", Command: config.Argv{"make"}}, "command"},
		{config.Step{Type: "command", Command: config.Argv{"make"}}, "command"},
		{config.Step{Use: "js", Code: "x"}, "code"},
		{config.Step{Run: "bash", Code: "x"}, "code"},
	} {
		s := tc.step
		if got := stepClass(nil, &s); got != tc.want {
			t.Errorf("%+v: class = %q, want %q", tc.step, got, tc.want)
		}
	}
	// …and both `command` and the `cli` alias admit it.
	for _, pat := range []string{"command", "cli", "*"} {
		if !matchAny([]string{pat}, "command") {
			t.Errorf("pattern %q must admit a command-class step", pat)
		}
	}
}
