package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
)

const scopeKeyStep = `
x-t:
  lens: &lens
    type: agent
    name: lens
    model: x
    memory: { extra_scopes: ["team:{{.team}}"] }
    skill:
      verbs:
        memory.remember: { scope: ["team:{{.team}}", "org:{{.owner | default \"none\"}}"] }
`

// An agent step's facts — the data its prompt sees, minus secrets — reach
// the recall, the dispatch (where its session's grant is rendered) and the
// output contract alike.
func TestAgentStepScopeFacts(t *testing.T) {
	mem := tempMemory(t)
	cfg := loadConfig(t, memBase+scopeKeyStep)
	reg := buildRegistry(t, cfg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: look
    <<: *lens
    prompt: "review"
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.SecretVals = map[string]string{"gh": "tok-secret"}
	var recallFacts map[string]any
	rig.Runner.Agents.Memory = func(_ string, _ config.Step, _ core.Trigger, _ string, facts map[string]any) string {
		recallFacts = facts
		return ""
	}
	out := "```remember\n- text: kept\n  scope: team:payments\n- text: org\n  scope: org:none\n```\n"
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: out}, nil
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"team": "payments"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if recallFacts["team"] != "payments" || recallFacts["repo"] != "o/r" || recallFacts["step"] != "lens" {
		t.Fatalf("recall facts: %v", recallFacts)
	}
	if _, ok := recallFacts["secrets"]; ok {
		t.Fatal("named secrets must not reach the scope facts")
	}
	reqs := rig.Agents.requests()
	if len(reqs) != 1 || reqs[0].ScopeFacts["team"] != "payments" {
		t.Fatalf("the dispatch must carry the step's facts: %+v", reqs)
	}
	id, _ := dispatch.SkillIdentityFor(reqs[0])
	g := MemoryGrantFrom(id.Policy.VerbScopes, id.MemoryScopes, "memory.remember")
	if g == nil || !g("team:payments") || g("team:billing") || !g("org:none") {
		t.Fatalf("the session's grant must be rendered from the step's facts: %#v", id.MemoryScopes)
	}
	// The output contract on the flow path honours the step's grant too:
	// granted notes persist, and a block naming another team's key is
	// refused whole (the harvest is all or nothing).
	if all, _ := mem.List(); len(all) != 2 {
		t.Fatalf("granted notes must persist, got %+v", all)
	}
	mem2 := tempMemory(t)
	out = "```remember\n- text: kept\n  scope: team:payments\n- text: other team\n  scope: team:billing\n```\n"
	rig2 := newTestRunner(t, cfg, reg)
	rig2.Agents.dispatchFunc = rig.Agents.dispatchFunc
	runTrigger(rig2, newTrigger("ping", map[string]any{"team": "payments"}), spec)
	if all, _ := mem2.List(); len(all) != 0 {
		t.Fatalf("a note outside the step's grant must refuse the harvest, got %+v", all)
	}
	if a := rig2.Store.auditsWithEvent("memory_remember"); len(a) != 1 || a[0]["outcome"] != "failed" ||
		!strings.Contains(a[0]["error"].(string), "skill.verbs scope list") {
		t.Fatalf("refused harvest audit: %+v", a)
	}
}

// A memory verb step's (and hook's) scope: is a scope key: rendered over the
// step's facts, never re-rendered, and refused — not written under a partial
// key — when it reads a fact the run has no value for.
func TestMemoryVerbScopeKey(t *testing.T) {
	mem := tempMemory(t)
	cfg := loadConfig(t, memBase)
	reg := buildRegistry(t, cfg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: keep
    uses: memory.remember
    options: { text: "t", scope: "team:{{.team}}" }
  - id: lit
    uses: memory.remember
    options: { text: "t2", scope: "title:{{.label}}" }
hooks:
  - at: done
    uses: memory.remember
    options: { text: "h", scope: "hook:{{.team}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"team": "payments", "label": "{{.repo}}"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	got := map[string]bool{}
	all, _ := mem.List()
	for _, e := range all {
		got[e.Scope] = true
	}
	if !got["team:payments"] || !got["title:{{.repo}}"] || !got["hook:payments"] || len(all) != 3 {
		t.Fatalf("scopes written: %v", got)
	}

	mem2 := tempMemory(t)
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", map[string]any{}), spec)
	failed, errStr := rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "no value") {
		t.Fatalf("a scope key reading a missing fact must fail the step: %v %s", failed, errStr)
	}
	if all, _ := mem2.List(); len(all) != 0 {
		t.Fatalf("nothing may be written under a partial key: %+v", all)
	}
	var _ = memory.ScopeOption
}

// A code step's ctx.memory.key renders over facts the step hands it, through
// the runner's tracked-secret redactor (code.Spec.Redact) — and with no
// memory: section configured, since key reads no memory.
func TestCodeStepMemoryKeyRedacts(t *testing.T) {
	const secretVal = "key-fact-s3cr3t"
	memory.Reset()
	helper := ctxHelper(t)
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
`)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: k
    use: cli
    command: [sh]
    env: { HELPER: "`+helper+`", CONDUCTOR_FLOW_CTX_TEST_CLIENT: "1" }
    code: |
      k=$("$HELPER" ctx memory key 'team:{{.team}}' '{"team":"key-fact-s3cr3t"}') || exit 1
      t=$("$HELPER" ctx memory key 'team:{{.team}}' '{"team":"payments"}') || exit 1
      leaked=false; case "$k" in *s3cr3t*) leaked=true;; esac
      plain=false; case "$t" in *'"team:payments"'*) plain=true;; esac
      printf '{"leaked": %s, "plain": %s}' "$leaked" "$plain"
  - id: post
    uses: svc.post
    options: { text: "leaked={{.k.leaked}} plain={{.k.plain}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	shared := testSecrets(nil)
	shared.Track(secretVal)
	rig.Runner.Secrets = shared
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "leaked=false plain=true" {
		t.Fatalf("code-step memory key: %+v", calls)
	}
}
