package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// shellRig builds a config with the real `command` connector (type: command,
// LOCAL — no host:) wired to a fake trigger, so execVerb's real option
// rendering runs (unlike internal/connector's own command tests, which call
// Invoke directly with already-literal options and so never exercise
// templating at all).
func shellRig(t *testing.T, step string) (*testRig, *config.TriggerSpec) {
	t.Helper()
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
  sh: { use: command }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	spec := mustSpec(t, `
on: svc.ping
steps:
`+step)
	return rig, &spec
}

// rt2 #3: a templated value spliced into the command connector's STRING form
// must not become shell syntax. Before the fix, an event field landing in
// `command: "printf '%s' {{.payload}}"` was substituted VERBATIM into the
// script text handed to `sh -c` — a payload of `$(...)` ran as a command
// substitution. After the fix (connector.Field.Shell +
// flow.renderShellQuoted), the substituted value is shell-quoted before
// splicing, so it can only ever appear as inert data to the command it
// lands in.
func TestCommandStringFormShellQuotesTemplatedFields(t *testing.T) {
	rig, spec := shellRig(t, `
  - id: r1
    uses: sh.run
    options: { command: "printf '%s' {{.payload}}" }
`)
	payload := "$(echo INJECTED)"
	runTrigger(rig, newTrigger("ping", map[string]any{"payload": payload}), *spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	out := lastOutputs(rig, "r1")
	if out["stdout"] != payload {
		t.Fatalf("templated field was not shell-quoted: got stdout %q, want the literal payload %q unexpanded", out["stdout"], payload)
	}
}

// The operator's OWN literal shell syntax around a templated field keeps
// working — only the SUBSTITUTED value is quoted, not the template author's
// text.
func TestCommandStringFormKeepsOperatorShellSyntax(t *testing.T) {
	rig, spec := shellRig(t, `
  - id: r1
    uses: sh.run
    options: { command: "echo {{.a}} && echo {{.b}}" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"a": "one", "b": "two"}), *spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	out := lastOutputs(rig, "r1")
	if got := out["stdout"]; got != "one\ntwo\n" {
		t.Fatalf("operator shell syntax (&&) broke: stdout %q", got)
	}
}

// A field value containing a single quote (the hardest case for the
// close/escape/reopen quoting trick) still renders as inert data.
func TestCommandStringFormQuotesEmbeddedSingleQuotes(t *testing.T) {
	rig, spec := shellRig(t, `
  - id: r1
    uses: sh.run
    options: { command: "printf '%s' {{.payload}}" }
`)
	payload := `it's a '$(trap)' test`
	runTrigger(rig, newTrigger("ping", map[string]any{"payload": payload}), *spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	out := lastOutputs(rig, "r1")
	if out["stdout"] != payload {
		t.Fatalf("embedded single quotes broke quoting: got %q, want %q", out["stdout"], payload)
	}
}

// A templated argv-list command never needed this fix (hosts.ShellJoin
// already quotes each element independently); confirm it still works
// unchanged, including with injection-shaped content.
func TestCommandArgvFormUnaffected(t *testing.T) {
	rig, spec := shellRig(t, `
  - id: r1
    uses: sh.run
    options: { command: ["printf", "%s", "{{.payload}}"] }
`)
	payload := "$(echo INJECTED)"
	runTrigger(rig, newTrigger("ping", map[string]any{"payload": payload}), *spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	out := lastOutputs(rig, "r1")
	if out["stdout"] != payload {
		t.Fatalf("argv form: got %q, want literal %q", out["stdout"], payload)
	}
}

// Control-flow template actions ({{if}}/{{range}}/{{with}}) inside a
// shell-marked field are refused with a clear error pointing at the
// argv-list form, rather than silently rendering unquoted (which would
// reopen the injection) or panicking.
func TestCommandStringFormRefusesControlFlowTemplates(t *testing.T) {
	rig, spec := shellRig(t, `
  - id: r1
    uses: sh.run
    options: { command: "echo {{if .ok}}yes{{else}}no{{end}}" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"ok": true}), *spec)
	failed, errStr := rig.workflowFailed()
	if !failed {
		t.Fatal("want the workflow to fail on an unsupported control-flow template")
	}
	if !strings.Contains(errStr, "argv-list form") {
		t.Fatalf("error should point at the argv-list form: %s", errStr)
	}
}

// lastOutputs returns one step's outputs from its "step" audit entry — a
// same-turn verb step never checkpoints to the store (checkpoint() only
// persists when the run carries an ID, which emptyRun()/runTrigger don't
// set), but every completed step is always audited with its outputs.
func lastOutputs(rig *testRig, stepID string) map[string]any {
	for _, e := range rig.Store.auditsWithEvent("step") {
		if e["step"] == stepID {
			if out, ok := e["outputs"].(map[string]any); ok {
				return out
			}
		}
	}
	return nil
}
