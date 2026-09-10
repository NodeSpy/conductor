package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
)

func setupEngineMemory(t *testing.T) *memory.Manager {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	m := memory.NewManager(memory.NewMemBackend())
	memory.Configure(m)
	return m
}

// TestMemoryPromptOptIn: an opted-in profile's prompt carries the scoped
// memory section (through the same append path as agent_guidance); a
// non-opted profile gets nothing.
func TestMemoryPromptOptIn(t *testing.T) {
	m := setupEngineMemory(t)
	src := memory.Source{Step: "fixer", Repo: "a/w"}
	if _, err := m.Remember("the deploy needs a warm cache", nil, "a/w", src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remember("global convention: squash merges", nil, "", src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remember("other repo's fact", nil, "x/y", src); err != nil {
		t.Fatal(err)
	}

	cfg := baseCfg()
	houseTone := "HOUSE TONE: terse."
	cfg.AgentGuidance = &houseTone // explicit guidance — conductor injects none by default
	// A legacy Action's `agent:` is a step REFERENCE now; the memory opt-in
	// lives on the workflow step it names.
	cfg.Workflows["w"] = config.WorkflowDef{Steps: []config.Step{
		{ID: "opted", Memory: &config.MemorySelector{Enabled: true}},
		{ID: "plain"},
		{ID: "scoped", Memory: &config.MemorySelector{Enabled: true, Scopes: []string{memory.GlobalScope}, Limit: 1}},
	}}
	prompt := func(agent, sig string) string {
		d := &fakeDispatcher{}
		e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
		e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", sig,
			config.Action{Type: "agent", Agent: agent, Prompt: "do it"}))
		if len(d.reqs) != 1 {
			t.Fatalf("agent %s: want 1 dispatch, got %d", agent, len(d.reqs))
		}
		return d.reqs[0].Action.Prompt
	}

	p := prompt("w/opted", "s1")
	for _, want := range []string{"Shared memory", "warm cache", "squash merges"} {
		if !strings.Contains(p, want) {
			t.Errorf("opted prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "other repo's fact") {
		t.Errorf("opted prompt leaked another repo's memory:\n%s", p)
	}
	// The memory section rides after guidance, like the guidance block itself.
	if gi, mi := strings.Index(p, "HOUSE TONE"), strings.Index(p, "Shared memory"); gi < 0 || mi < gi {
		t.Errorf("memory section must append after guidance (guidance@%d memory@%d)", gi, mi)
	}

	if p := prompt("w/plain", "s2"); strings.Contains(p, "Shared memory") {
		t.Errorf("non-opted profile must get no memory section:\n%s", p)
	}

	p = prompt("w/scoped", "s3")
	if !strings.Contains(p, "squash merges") || strings.Contains(p, "warm cache") {
		t.Errorf("scope filter not applied:\n%s", p)
	}

	// Memory unconfigured → opted profile still gets nothing (and no error).
	memory.Reset()
	if p := prompt("w/opted", "s4"); strings.Contains(p, "Shared memory") {
		t.Errorf("unconfigured memory must inject nothing:\n%s", p)
	}
}

// TestLegacyDispatchHarvestsOutput: the legacy single-action agent path also
// applies the output contract to the captured output — for an agent whose
// profile opts into memory.
func TestLegacyDispatchHarvestsOutput(t *testing.T) {
	m := setupEngineMemory(t)
	cfg := baseCfg()
	// The opt-in lives on the workflow step the Action's `agent:` names.
	cfg.Workflows["w"] = config.WorkflowDef{Steps: []config.Step{
		{ID: "fixer", Memory: &config.MemorySelector{Enabled: true}},
		{ID: "plain"},
	}}
	d := &fakeDispatcher{ref: dispatch.RunRef{AgentID: "a1",
		Output: "done\n```remember\n- text: PR titles use conventional commits\n  scope: repo\n```"}}
	e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", "sig",
		config.Action{Type: "agent", Agent: "w/fixer", Prompt: "go"}))
	all, err := m.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("harvest: %v %d", err, len(all))
	}
	if all[0].Scope != "repo" || all[0].Source.Step != "w/fixer" || all[0].Source.Trigger != "new_comment" {
		t.Fatalf("legacy harvest provenance: %+v", all[0])
	}
}

// TestHarvestGatedOnProfileOptIn (#57 M8): harvesting an agent's output into
// shared memory is write access, and write access is opt-in per agent exactly
// like read access. An agent whose profile does not enable memory must not be
// able to poison the shared store via its output contract — even when the same
// output would harvest cleanly for an opted-in agent. Without this gate an
// untrusted event that steers a non-memory agent could write attacker-chosen
// notes that later agents read as trusted context.
func TestHarvestGatedOnProfileOptIn(t *testing.T) {
	m := setupEngineMemory(t)
	cfg := baseCfg()
	// A "plain" agent emits a well-formed remember block.
	out := "done\n```remember\n- text: injected fact from an untrusted run\n  scope: repo\n```"
	d := &fakeDispatcher{ref: dispatch.RunRef{AgentID: "a1", Output: out}}
	e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", "sig",
		config.Action{Type: "agent", Agent: "w/plain", Prompt: "go"}))
	all, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a non-opted agent must write nothing to shared memory, got %d: %+v", len(all), all)
	}
}
