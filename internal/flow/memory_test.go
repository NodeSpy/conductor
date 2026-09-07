package flow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
)

// tempMemory configures an ephemeral memory manager for one test.
func tempMemory(t *testing.T) *memory.Manager {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	m := memory.NewManager(memory.NewMemBackend())
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	n := 0
	m.SetClock(func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }, nil)
	memory.Configure(m)
	return m
}

const memBase = `
connectors:
  svc: { type: fake }
memory: { type: memory }
`

// TestMemoryStepAndHookWrites: a `uses: memory.remember` step and an at:done
// hook both persist, carrying the run's provenance (run/trigger/repo) without
// any step plumbing; a later memory.recall step reads the entry back and the
// verb calls are audited.
func TestMemoryStepAndHookWrites(t *testing.T) {
	mem := tempMemory(t)
	cfg := loadConfig(t, memBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: keep
    uses: memory.remember
    options: { text: "note about {{.msg}}", tags: [note], scope: repo }
  - id: back
    uses: memory.recall
    options: { tags: [note] }
  - id: post
    uses: svc.post
    options: { text: "kept {{.keep.id}} n={{.back.count}}" }
hooks:
  - at: done
    uses: memory.remember
    options: { text: "workflow finished for {{.repo}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	run := emptyRun()
	run.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "inv-9"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	all, err := mem.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 memories (step + hook), got %d: %+v", len(all), all)
	}
	// Newest first: the hook wrote last.
	if all[0].Text != "workflow finished for o/r" || all[0].Scope != "global" {
		t.Errorf("hook write: %+v", all[0])
	}
	step := all[1]
	if step.Text != "note about inv-9" || step.Scope != "repo:o/r" {
		t.Errorf("step write: %+v", step)
	}
	// Provenance stamped by the runner, not the step.
	if step.Source.Run != "flow:ping:o/r#7" || step.Source.Trigger != "ping" || step.Source.Repo != "o/r" {
		t.Errorf("provenance: %+v", step.Source)
	}

	// The recall step's outputs flowed into the post verb.
	if calls := fake.snapshot(); len(calls) != 1 || !strings.Contains(calls[0].Opts["text"].(string), "n=1") {
		t.Fatalf("post call: %+v", calls)
	}

	// Verb invocations are audited like any other verb.
	var audited int
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if e["connector"] == "memory" && e["outcome"] == "ok" {
			audited++
		}
	}
	if audited != 3 { // remember + recall + hook remember
		t.Fatalf("want 3 audited memory verb calls, got %d", audited)
	}
}

// TestMemoryVerbValidation: memory.* steps/hooks validate at load — they need
// a memory: section, and their options check like any verb.
func TestMemoryVerbValidation(t *testing.T) {
	valid := func(base, y string) error {
		cfg := loadConfig(t, base+y)
		reg := buildRegistry(t, cfg)
		return Validate(cfg, reg)
	}
	ok := `
triggers:
  - on: svc.ping
    steps: [ { uses: memory.remember, options: { text: hi, tags: [a], scope: global } } ]
    hooks: [ { at: done, uses: memory.list } ]
`
	if err := valid(memBase, ok); err != nil {
		t.Fatalf("memory verbs must validate with a memory: section: %v", err)
	}
	noMem := "\nconnectors:\n  svc: { type: fake }\n"
	if err := valid(noMem, ok); err == nil || !strings.Contains(err.Error(), "memory: section") {
		t.Fatalf("memory verbs without memory: must fail load: %v", err)
	}
	cases := []struct{ name, yaml, wantErr string }{
		{"unknown option", `
triggers:
  - on: svc.ping
    steps: [ { uses: memory.remember, options: { text: x, bogus: 1 } } ]`, `"bogus"`},
		{"missing required text", `
triggers:
  - on: svc.ping
    steps: [ { uses: memory.remember, options: { tags: [a] } } ]`, `"text"`},
		{"unknown verb", `
triggers:
  - on: svc.ping
    steps: [ { uses: memory.push, options: {} } ]`, `no verb "push"`},
		{"hook needs memory section too", `
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: t } } ]
    hooks: [ { at: done, uses: memory.remember, options: { text: x } } ]`, "memory: section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := memBase
			if tc.name == "hook needs memory section too" {
				base = noMem
			}
			if err := valid(base, tc.yaml); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestMemoryOutputContractHarvest: an agent step whose final output carries a
// ```remember fence persists the notes with full provenance (including the
// agent profile name) and audits the write; a malformed block audits a
// failure without failing the step.
func TestMemoryOutputContractHarvest(t *testing.T) {
	mem := tempMemory(t)
	cfg := loadConfig(t, memBase+`
agents:
  fixer: { model: x }
`)
	reg := buildRegistry(t, cfg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix it"
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		out := "did the fix\n```remember\n- text: tests need -count=1\n  tags: [flaky]\n  scope: repo\n```\n"
		return dispatch.RunRef{AgentID: "a1", Output: out}, nil
	}
	run := emptyRun()
	run.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	all, _ := mem.List()
	if len(all) != 1 {
		t.Fatalf("want 1 harvested memory, got %d", len(all))
	}
	e := all[0]
	if e.Text != "tests need -count=1" || e.Scope != "repo:o/r" {
		t.Errorf("harvested entry: %+v", e)
	}
	if e.Source.Agent != "fixer" || e.Source.Run != "flow:ping:o/r#7" || e.Source.Trigger != "ping" {
		t.Errorf("harvest provenance: %+v", e.Source)
	}
	audits := rig.Store.auditsWithEvent("memory_remember")
	if len(audits) != 1 || audits[0]["via"] != "output" || audits[0]["outcome"] != "ok" {
		t.Fatalf("harvest audit: %+v", audits)
	}

	// Malformed block: step still succeeds, failure audited.
	mem2 := tempMemory(t)
	rig2 := newTestRunner(t, cfg, reg)
	rig2.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: "```remember\n- tags: [x]\n```"}, nil
	}
	runTrigger(rig2, newTrigger("ping", nil), spec)
	if failed, _ := rig2.workflowFailed(); failed {
		t.Fatal("a malformed remember block must not fail the step")
	}
	if all, _ := mem2.List(); len(all) != 0 {
		t.Fatalf("malformed block must persist nothing: %+v", all)
	}
	audits = rig2.Store.auditsWithEvent("memory_remember")
	if len(audits) != 1 || audits[0]["outcome"] != "failed" {
		t.Fatalf("failed-harvest audit: %+v", audits)
	}
}

// TestMemoryPromptInjectionSeam: the runner appends AgentServices.Memory's
// section for agent steps (after guidance), passing the profile name and
// trigger through; the harness's default fake omits it, so also check nil
// Memory keeps prompts unchanged.
func TestMemoryPromptInjectionSeam(t *testing.T) {
	cfg := loadConfig(t, memBase+`
agents:
  opted: { model: x }
`)
	reg := buildRegistry(t, cfg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    agent: opted
    prompt: "do it"
`)
	rig2 := newTestRunner(t, cfg, reg)
	var gotName string
	rig2.Runner.Agents.Memory = func(name string, p config.AgentProfile, tr core.Trigger) string {
		gotName = name
		return "\n\n---\nShared memory: remembered fact"
	}
	runTrigger(rig2, newTrigger("ping", nil), spec)
	reqs := rig2.Agents.requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(reqs))
	}
	p := reqs[0].Action.Prompt
	if !strings.Contains(p, "remembered fact") {
		t.Fatalf("prompt missing memory section:\n%s", p)
	}
	// Ordering: guidance (|G|) before the memory section.
	if strings.Index(p, "|G|") > strings.Index(p, "remembered fact") {
		t.Fatalf("memory must append after guidance:\n%s", p)
	}
	if gotName != "opted" {
		t.Fatalf("profile name: %q", gotName)
	}

	// No Memory service wired → prompt untouched.
	rig3 := newTestRunner(t, cfg, reg)
	runTrigger(rig3, newTrigger("ping", nil), spec)
	if p := rig3.Agents.requests()[0].Action.Prompt; strings.Contains(p, "Shared memory") {
		t.Fatalf("unwired memory must not inject: %s", p)
	}
}

// TestMemoryTemplateFunc: {{ memory <scope> <limit> }} renders newest-first
// bullet lines, read-only.
func TestMemoryTemplateFunc(t *testing.T) {
	mem := tempMemory(t)
	src := memory.Source{Repo: "o/r"}
	_, _ = mem.Remember("older global", nil, "", src)
	_, _ = mem.Remember("newer global", nil, "global", src)
	_, _ = mem.Remember("repo-scoped", nil, "repo", src)

	cases := []struct{ tmpl, want string }{
		{`{{ memory "global" 0 }}`, "- newer global\n- older global"},
		{`{{ memory "global" 1 }}`, "- newer global"},
		{`{{ memory "repo:o/r" 0 }}`, "- repo-scoped"},
		{`{{ memory "" 0 }}`, "- repo-scoped\n- newer global\n- older global"},
		{`{{ memory "repo:none/none" 0 }}`, ""},
	}
	for _, c := range cases {
		got, err := render(c.tmpl, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.tmpl, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.tmpl, got, c.want)
		}
	}
	if _, err := render(`{{ memory "global" }}`, nil); err == nil {
		t.Fatal("memory with one arg must error (scope, limit)")
	}
	if _, err := render(`{{ memory "repo" 1 }}`, nil); err == nil {
		t.Fatal("relative scope must error in templates")
	}
	memory.Reset()
	if _, err := render(`{{ memory "global" 1 }}`, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured memory: %v", err)
	}
}
