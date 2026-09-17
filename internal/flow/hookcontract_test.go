package flow

import (
	"strings"
	"testing"
)

// The uniform `hook` lifecycle contract (flow.go hookData): every hook phase gets
// hook.{phase,status,run_id,step}; the fail phase adds hook.failure, and the legacy
// flat {{.error}}/{{.failed_step}} stay populated.

const hookCfg = `
connectors:
  svc: { use: fake }
`

// TestHookContractStartDone: start/done hooks see phase/status/step.
func TestHookContractStartDone(t *testing.T) {
	cfg := loadConfig(t, hookCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: work
    sleep: 1ms
    hooks:
      - { at: start, uses: svc.post, options: { text: "S:{{.hook.phase}}/{{.hook.status}}/{{.hook.step}}" } }
      - { at: done,  uses: svc.post, options: { text: "D:{{.hook.phase}}/{{.hook.status}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", e)
	}
	texts := postTexts(st)
	if !hasText(texts, "S:start/running/work") {
		t.Fatalf("start hook contract wrong: %v", texts)
	}
	if !hasText(texts, "D:done/ok") {
		t.Fatalf("done hook contract wrong: %v", texts)
	}
}

// TestHookContractFail: a fail hook sees phase/status and the failure sub-object
// (kind/step), and the legacy flat {{.error}} still renders.
func TestHookContractFail(t *testing.T) {
	cfg := loadConfig(t, hookCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: work
    uses: svc.fail
    hooks:
      - { at: fail, uses: svc.post, options: { text: "F:{{.hook.phase}}/{{.hook.status}}/{{.hook.failure.kind}}/{{.hook.failure.step}}/gaveup={{.hook.failure.gave_up}}/err={{.error}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	texts := postTexts(st)
	var got string
	for _, x := range texts {
		if strings.HasPrefix(x, "F:") {
			got = x
		}
	}
	if got == "" {
		t.Fatalf("at: fail hook did not fire; posts=%v", texts)
	}
	if !strings.HasPrefix(got, "F:fail/failed/ordinary/work/gaveup=false/err=") {
		t.Fatalf("fail hook contract wrong: %q", got)
	}
	if strings.HasSuffix(got, "err=") {
		t.Fatalf("legacy {{.error}} not populated: %q", got)
	}
}

func postTexts(st *fakeState) []string {
	var out []string
	for _, c := range st.snapshot() {
		if c.Verb == "post" {
			if s, ok := c.Opts["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func hasText(texts []string, want string) bool {
	for _, x := range texts {
		if x == want {
			return true
		}
	}
	return false
}
