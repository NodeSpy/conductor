package config

import (
	"strings"
	"testing"
)

// The memory knobs are validated: nothing negative or past its ceiling,
// only the kinds a repo has; set ones are what the effective values say,
// unset ones the defaults.
func TestMemoryLimitsValidation(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"bots:\n  x:\n    memory: { index_budget: 128k }", "bots.x: memory.index_budget: want 1 to 65536 bytes"},
		{"bots:\n  x:\n    memory: { read_max: -1 }", "memory.read_max: want 1 to 262144 bytes"},
		{"bots:\n  x:\n    memory: { recall_limit: 26 }", "memory.recall_limit: want 1 to 25"},
		{"bots:\n  x:\n    memory: { max_bytes: { knowledge: 300k } }", "memory.max_bytes.knowledge: want 1 to 262144 bytes"},
		{"bots:\n  x:\n    memory: { max_bytes: { notes: 4k } }", "memory.max_bytes.notes: not a kind here"},
		{"bots:\n  x:\n    memory: { max_bytes: { knowledge: 0 } }", "memory.max_bytes.knowledge: want 1 to"},
		{"teams:\n  t:\n    routing: { fallback: x }\n    knowledge: { max_bytes: { digest: 4k } }\nbots:\n  x: { team: t }", "knowledge.max_bytes.digest: not a kind here"},
		{"teams:\n  t:\n    routing: { fallback: x }\n    knowledge: { archive: false }\nbots:\n  x: { team: t }", "knowledge.archive: a team's knowledge repo has no log"},
		{"bots:\n  x:\n    collections:\n      notes: { dir: notes, writers: [bots], fields: { title: { type: string } }, max_bytes: 1m }", "collections.notes: max_bytes: want 1 to 262144 bytes"},
		{"bots:\n  x:\n    collections:\n      notes: { dir: digests, writers: [bots], fields: { title: { type: string } } }", `dir "digests" is a built-in kind's home`},
	} {
		_, err := loadBots(t, c.yaml+"\n")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n  %v\n  want %q", c.yaml, err, c.want)
		}
	}
	cfg, err := loadBots(t, "bots:\n  x:\n    memory: { index_budget: 8k, read_max: 20k, recall_limit: 12, max_bytes: { knowledge: 16k, handoff: 6k }, archive: false }\n  y: { team: t }\n  z: { team: u }\n"+
		"teams:\n  t:\n    routing: { fallback: y }\n    knowledge: { index_budget: 3k, max_bytes: { runbook: 20k } }\n  u: { routing: { fallback: z } }\n")
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Bots["x"].Memory
	if l.IndexBudgetOr(1) != 8192 || l.ReadMaxOr() != 20480 || l.RecallLimitOr() != 12 || l.MaxBytesOf("knowledge") != 16384 ||
		l.MaxBytesOf("handoff") != 6144 || l.MaxBytesOf("runbook") != DefaultMaxBytes["runbook"] || l.ArchiveOn() {
		t.Fatalf("bot limits: %+v", l)
	}
	tl := cfg.TeamLimits("t")
	if tl.IndexBudgetOr(DefaultTeamIndexBudget) != 3072 || tl.MaxBytesOf("runbook") != 20480 || tl.MaxBytesOf("knowledge") != DefaultMaxBytes["knowledge"] {
		t.Fatalf("team limits: %+v", tl)
	}
	if cfg.TeamLimits("u").IndexBudgetOr(DefaultTeamIndexBudget) != DefaultTeamIndexBudget {
		t.Fatal("a team without knowledge: has the defaults")
	}
	var d MemoryLimits
	if !d.ArchiveOn() || d.IndexBudgetOr(DefaultIndexBudget) != DefaultIndexBudget || d.ReadMaxOr() != DefaultReadMax || d.RecallLimitOr() != DefaultRecallLimit || d.MaxBytesOf("episode") != DefaultMaxBytes["episode"] || d.MaxBytesOf("nope") != 0 {
		t.Fatalf("defaults: %+v", d)
	}
}
