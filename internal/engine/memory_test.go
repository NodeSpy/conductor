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
	src := memory.Source{Agent: "fixer", Repo: "a/w"}
	if _, err := m.Remember("the deploy needs a warm cache", nil, "repo", src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remember("global convention: squash merges", nil, "", src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remember("other repo's fact", nil, "repo:x/y", src); err != nil {
		t.Fatal(err)
	}

	cfg := baseCfg()
	cfg.Agents = map[string]config.AgentProfile{
		"opted":  {Provider: "claude", Memory: &config.MemorySelector{Enabled: true}},
		"plain":  {Provider: "claude"},
		"scoped": {Provider: "claude", Memory: &config.MemorySelector{Enabled: true, Scopes: []string{"global"}, Limit: 1}},
	}
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

	p := prompt("opted", "s1")
	for _, want := range []string{"Shared memory", "warm cache", "squash merges"} {
		if !strings.Contains(p, want) {
			t.Errorf("opted prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "other repo's fact") {
		t.Errorf("opted prompt leaked another repo's memory:\n%s", p)
	}
	// The memory section rides after guidance, like the guidance block itself.
	if gi, mi := strings.Index(p, "be concise and human"), strings.Index(p, "Shared memory"); gi < 0 || mi < gi {
		t.Errorf("memory section must append after guidance (guidance@%d memory@%d)", gi, mi)
	}

	if p := prompt("plain", "s2"); strings.Contains(p, "Shared memory") {
		t.Errorf("non-opted profile must get no memory section:\n%s", p)
	}

	p = prompt("scoped", "s3")
	if !strings.Contains(p, "squash merges") || strings.Contains(p, "warm cache") {
		t.Errorf("scope filter not applied:\n%s", p)
	}

	// Memory unconfigured → opted profile still gets nothing (and no error).
	memory.Reset()
	if p := prompt("opted", "s4"); strings.Contains(p, "Shared memory") {
		t.Errorf("unconfigured memory must inject nothing:\n%s", p)
	}
}

// TestLegacyDispatchHarvestsOutput: the legacy single-action agent path also
// applies the output contract to the captured output.
func TestLegacyDispatchHarvestsOutput(t *testing.T) {
	m := setupEngineMemory(t)
	d := &fakeDispatcher{ref: dispatch.RunRef{AgentID: "a1",
		Output: "done\n```remember\n- text: PR titles use conventional commits\n  scope: repo\n```"}}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", "sig",
		config.Action{Type: "agent", Agent: "fixer", Prompt: "go"}))
	all, err := m.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("harvest: %v %d", err, len(all))
	}
	if all[0].Scope != "repo:a/w" || all[0].Source.Agent != "fixer" || all[0].Source.Trigger != "new_comment" {
		t.Fatalf("legacy harvest provenance: %+v", all[0])
	}
}
