package memory

import (
	"strings"
	"testing"
)

func TestHarvestFencedBlock(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Agent: "fixer", Run: "r9", Trigger: "failing_checks", Repo: "acme/api"}
	out := "Fixed the flaky test by pinning the clock.\n\n" +
		"```remember\n" +
		"- text: TestPoll needs a fake clock — real time flakes on CI\n" +
		"  tags: [flaky, ci]\n" +
		"  scope: repo\n" +
		"- plain global note\n" +
		"```\n\nDone."
	entries, err := m.HarvestOutput(out, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Scope != "repo:acme/api" || entries[0].Tags[0] != "flaky" {
		t.Errorf("first entry: %+v", entries[0])
	}
	if entries[1].Scope != "global" || entries[1].Text != "plain global note" {
		t.Errorf("second entry: %+v", entries[1])
	}
	if entries[0].Source != src {
		t.Errorf("provenance: %+v", entries[0].Source)
	}
	// Persisted, not just parsed.
	all, _ := m.List()
	if len(all) != 2 {
		t.Fatalf("persisted: %d", len(all))
	}
}

func TestHarvestJSONOutput(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Agent: "assess", Repo: "acme/api"}

	// String form.
	entries, err := m.HarvestOutput(`{"verdict":"ok","remember":"the deploy takes 10 minutes"}`, src)
	if err != nil || len(entries) != 1 || entries[0].Text != "the deploy takes 10 minutes" {
		t.Fatalf("string form: %v %+v", err, entries)
	}
	// Object list under the standard output wrapper.
	entries, err = m.HarvestOutput(`{"output":{"remember":[{"text":"pin the linter","tags":["ci"],"scope":"repo"},"and a plain one"]}}`, src)
	if err != nil || len(entries) != 2 {
		t.Fatalf("wrapped list form: %v %+v", err, entries)
	}
	if entries[0].Scope != "repo:acme/api" || entries[1].Scope != "global" {
		t.Errorf("scopes: %q %q", entries[0].Scope, entries[1].Scope)
	}
	// JSON without a remember key harvests nothing.
	entries, err = m.HarvestOutput(`{"verdict":"ok"}`, src)
	if err != nil || len(entries) != 0 {
		t.Fatalf("no-op JSON: %v %+v", err, entries)
	}
}

func TestHarvestEdgeCases(t *testing.T) {
	m := testManager(t, NewMemBackend())
	// Plain text without a block: nothing.
	if entries, err := m.HarvestOutput("all done, nothing to save", Source{}); err != nil || len(entries) != 0 {
		t.Fatalf("plain text: %v %+v", err, entries)
	}
	// Empty output.
	if entries, err := m.HarvestOutput("", Source{}); err != nil || len(entries) != 0 {
		t.Fatalf("empty: %v %+v", err, entries)
	}
	// "```remembering" prose is not a fence.
	if entries, err := m.HarvestOutput("I keep ```remembering things``` badly", Source{}); err != nil || len(entries) != 0 {
		t.Fatalf("prose: %v %+v", err, entries)
	}
	// Unterminated block is a visible error.
	if _, err := m.HarvestOutput("```remember\n- oops", Source{}); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("unterminated: %v", err)
	}
	// An item without text errors.
	if _, err := m.HarvestOutput("```remember\n- tags: [x]\n```", Source{}); err == nil {
		t.Fatal("missing text should error")
	}
	// Two blocks both harvest.
	out := "```remember\nfirst\n```\nmiddle\n```remember\nsecond\n```"
	entries, err := m.HarvestOutput(out, Source{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("two blocks: %v %+v", err, entries)
	}
	// Comma-separated tags string form.
	entries, err = m.HarvestOutput("```remember\n- text: t\n  tags: a, b\n```", Source{})
	if err != nil || len(entries) != 1 || len(entries[0].Tags) != 2 {
		t.Fatalf("tags string: %v %+v", err, entries)
	}
}

func TestPromptSection(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Agent: "reviewer", Repo: "acme/api"}
	_, _ = m.Remember("global fact", []string{"a"}, "global", src)
	_, _ = m.Remember("repo fact", nil, "repo", src)
	_, _ = m.Remember("my fact", nil, "agent", src)
	_, _ = m.Remember("other repo fact", nil, "repo:other/repo", src)
	_, _ = m.Remember("other agent fact", nil, "agent:other", src)

	s := m.PromptSection(Filter{}, "acme/api", "reviewer")
	for _, want := range []string{"global fact", "repo fact", "my fact", "Shared memory", "```remember"} {
		if !strings.Contains(s, want) {
			t.Errorf("section missing %q:\n%s", want, s)
		}
	}
	for _, absent := range []string{"other repo fact", "other agent fact"} {
		if strings.Contains(s, absent) {
			t.Errorf("section leaked %q:\n%s", absent, s)
		}
	}
	// Newest first.
	if strings.Index(s, "my fact") > strings.Index(s, "global fact") {
		t.Errorf("ordering wrong:\n%s", s)
	}
	// Scope filter + limit narrow the section.
	s = m.PromptSection(Filter{Scopes: []string{"global"}, Limit: 1}, "acme/api", "reviewer")
	if !strings.Contains(s, "global fact") || strings.Contains(s, "repo fact") {
		t.Errorf("scoped section:\n%s", s)
	}
	// Nothing matching → empty (no token cost).
	if s = m.PromptSection(Filter{Tags: []string{"nope"}}, "acme/api", "reviewer"); s != "" {
		t.Errorf("empty section should be \"\": %q", s)
	}
	// No repo/agent in the dispatch: relative scopes drop, globals still come.
	if s = m.PromptSection(Filter{}, "", ""); !strings.Contains(s, "global fact") {
		t.Errorf("global-only section:\n%s", s)
	}
}
