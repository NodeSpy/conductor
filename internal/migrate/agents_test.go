package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// The `agents:` → steps/runtime migration (docs/design/agents-removal.md §7).
//
// The property that matters most here is TRACK-RECORD CONTINUITY: memory
// scoping, session affinity, and outcome tracking used to key off the agent
// NAME and now key off the step IDENTITY, so the migration must emit the old
// name as the template's `name:` — which is the identity ladder's top rung.
// Without that, every upgraded box silently resets its accumulated history.

// migrateDoc transforms a document and re-parses it THROUGH THE FULL LOAD
// PIPELINE (extends: resolution and defaults included), which is what the
// daemon does after migrating — so these tests assert the config an upgraded
// box actually runs, not just the YAML text.
func migrateDoc(t *testing.T, doc string) (*config.Config, []string) {
	t.Helper()
	res, err := Transform([]byte(doc))
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if !res.Changed {
		t.Fatal("transform reported no change")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, res.Output, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := config.Load(path)
	if err != nil {
		t.Fatalf("migrated config must load: %v\n%s", err, res.Output)
	}
	return out, res.Summary
}

const agentsBase = `
connectors:
  gh: { use: github, token: "x" }
runtimes:
  paseo: { use: paseo, default: true }
`

func TestAgentsMigrationPreservesTrackRecordKeys(t *testing.T) {
	out, notes := migrateDoc(t, agentsBase+`
agents:
  fixer:
    provider: claude
    model: claude-opus-5
    workspace: worktree
    archive_when_done: true
    memory: true
    outcome_feedback: true
triggers:
  - on: gh.pull_request
    name: review
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "fix it" }
`)
	// The behavior is INLINED on the step that referenced it — there is no
	// registry to park it in.
	tmpl := out.Triggers[0].Steps[0]
	// THE continuity property: the old agent name is pinned as the step's
	// name:, so its identity is byte-identical to the key the box already
	// has on disk for memory / sessions / outcomes.
	if tmpl.Name != "fixer" {
		t.Fatalf("the old agent name must be pinned as name:, got %q", tmpl.Name)
	}
	if got := tmpl.Identity(config.ScopeForTrigger(out.Triggers[0], 0), 0); got != "fixer" {
		t.Fatalf("migrated identity = %q, want fixer", got)
	}
	// Behavior moved verbatim.
	if tmpl.Workspace != "worktree" || !tmpl.ArchiveWhenDone || !tmpl.OutcomeFeedback {
		t.Fatalf("behavior lost: %+v", tmpl)
	}
	if tmpl.Memory == nil || !tmpl.Memory.Enabled {
		t.Fatal("memory opt-in lost")
	}
	// provider+model collapsed to an EXACT pin — a migration must never
	// invent a fleet.
	if tmpl.Model.Ref != "claude-opus-5" {
		t.Fatalf("model = %+v, want an exact claude-opus-5 pin", tmpl.Model)
	}
	if len(tmpl.Model.Any) != 0 {
		t.Fatal("the migration must not invent a fleet")
	}

	if !hasNote(notes, "history carries over") {
		t.Fatalf("the summary should say the history carries over: %v", notes)
	}
}

// Where several triggers shared one agent, they share one template — which
// is what preserves the reuse (and the single shared track record).
func TestAgentsMigrationPreservesSharedReuse(t *testing.T) {
	out, _ := migrateDoc(t, agentsBase+`
agents:
  fixer: { provider: claude, workspace: worktree }
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: fixer, prompt: "one" }] }
  - { on: gh.issues, name: b, steps: [{ id: s, type: agent, agent: fixer, prompt: "two" }] }
`)
	if len(out.Triggers) != 2 {
		t.Fatalf("triggers = %d", len(out.Triggers))
	}
	for i, tr := range out.Triggers {
		s := tr.Steps[0]
		if s.Name != "fixer" || s.Workspace != "worktree" {
			t.Fatalf("trigger %d did not inherit the shared named step: %+v", i, s)
		}
		if got := s.Identity(config.ScopeForTrigger(tr, i), 0); got != "fixer" {
			t.Fatalf("trigger %d identity = %q — both must share one record", i, got)
		}
	}
}

// A budget moves to the RUNTIME the agent ran on (§1).
func TestAgentsMigrationMovesBudgetToRuntime(t *testing.T) {
	out, notes := migrateDoc(t, `
connectors:
  gh: { use: github, token: "x" }
runtimes:
  paseo: { use: paseo, default: true }
  gpu:   { use: paseo }
agents:
  heavy:
    runtime: gpu
    budget: { max_cost_usd: 5, window: 24h }
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: heavy, prompt: "p" }] }
`)
	rt, ok := out.Runtimes["gpu"]
	if !ok || rt.Budget == nil {
		t.Fatalf("the budget should land on runtimes.gpu, got %+v", out.Runtimes)
	}
	if rt.Budget.MaxCostUSD != 5 {
		t.Fatalf("budget = %+v", rt.Budget)
	}
	// A Step has no budget field at all — the anchor is the runtime.
	if !hasNote(notes, "runtimes.gpu.budget") {
		t.Fatalf("the move should be summarized: %v", notes)
	}
}

// With no runtime named, the budget lands on the default runtime.
func TestAgentsMigrationBudgetFallsBackToDefaultRuntime(t *testing.T) {
	out, _ := migrateDoc(t, agentsBase+`
agents:
  a: { budget: { max_tokens: 100 } }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`)
	if rt := out.Runtimes["paseo"]; rt.Budget == nil || rt.Budget.MaxTokens != 100 {
		t.Fatalf("budget should land on the default runtime, got %+v", rt.Budget)
	}
}

// `provider:` alone named a backend, not a model — that is a bare launch,
// and the summary must say so rather than inventing a model.
func TestAgentsMigrationProviderOnlyBecomesBareLaunch(t *testing.T) {
	out, notes := migrateDoc(t, agentsBase+`
agents:
  a: { provider: claude }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`)
	if m := out.Triggers[0].Steps[0].Model; m.Set() {
		t.Fatalf("provider-only must not become a model pin, got %+v", m)
	}
	if !hasNote(notes, "BARE LAUNCH") {
		t.Fatalf("the bare-launch outcome should be summarized: %v", notes)
	}
}

// A session: on the profile rides onto the step (its own pool).
func TestAgentsMigrationCarriesSession(t *testing.T) {
	out, _ := migrateDoc(t, agentsBase+`
agents:
  a:
    session: { key: "{{.repo}}#{{.pr}}", idle_ttl: 12h, end_on: [gh._closed] }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`)
	sess := out.Triggers[0].Steps[0].Session
	if sess == nil || sess.Key != "{{.repo}}#{{.pr}}" || len(sess.EndOn) != 1 {
		t.Fatalf("session lost: %+v", sess)
	}
}

// agent_guidance is layer 0 for everything and must survive.
func TestAgentsMigrationPreservesAgentGuidance(t *testing.T) {
	out, _ := migrateDoc(t, agentsBase+`
agent_guidance: "Terse and human."
agents:
  a: { provider: claude }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`)
	// applyDefaults folds it into the global policy scope.
	if out.Policy == nil || out.Policy.Guidance == nil ||
		len(out.Policy.Guidance.Parts) == 0 || out.Policy.Guidance.Parts[0] != "Terse and human." {
		t.Fatalf("agent_guidance was dropped: %+v", out.Policy)
	}
}

// Nothing is dropped in silence.
func TestAgentsMigrationNotesUnhandledFields(t *testing.T) {
	_, notes := migrateDoc(t, agentsBase+`
agents:
  a: { provider: claude, retired_knob: 3 }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`)
	if !hasNote(notes, "retired_knob") {
		t.Fatalf("an unhandled field must be named in the summary: %v", notes)
	}
}

// The pass is idempotent: a second run over its own output changes nothing.
func TestAgentsMigrationIsIdempotent(t *testing.T) {
	doc := agentsBase + `
agents:
  a: { provider: claude, workspace: local }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: a, prompt: "p" }] }
`
	first, err := Transform([]byte(doc))
	if err != nil || !first.Changed {
		t.Fatalf("first: %v %v", err, first)
	}
	second, err := Transform(first.Output)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Changed {
		t.Fatalf("a second run must be a no-op, got:\n%s", second.Output)
	}
}

// An `agents:` block a config no longer references still migrates rather
// than tripping the strict decoder.
// L3: a SINGLE-file pass must not claim a profile is unreferenced — the
// referencing step is routinely in another file of the same tree. The
// claim belongs to AutoMigrate, which sees all of them (see
// TestAutoMigrateReportsGenuinelyUnreferencedProfiles).
func TestSingleFilePassDoesNotClaimUnreferenced(t *testing.T) {
	_, notes := migrateDoc(t, agentsBase+`
agents:
  orphan: { provider: claude, workspace: local }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, uses: gh.comment, options: { body: hi } }] }
`)
	if hasNote(notes, "no step in this config referenced it") {
		t.Fatalf("one file cannot know the tree: %v", notes)
	}
}

// A profile referenced from SEVERAL steps in one file becomes an anchor
// under x-migrated: rather than being copied at each site.
func TestAgentsMigrationSharesAnAnchorAcrossSites(t *testing.T) {
	res, err := Transform([]byte(agentsBase + `
agents:
  fixer: { provider: claude, workspace: worktree }
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: fixer, prompt: "one" }] }
  - { on: gh.issues, name: b, steps: [{ id: s, type: agent, agent: fixer, prompt: "two" }] }
`))
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	out := string(res.Output)
	if !strings.Contains(out, "x-migrated:") || !strings.Contains(out, "&fixer") {
		t.Fatalf("two sites should share one anchor:\n%s", out)
	}
	if strings.Count(out, "<<: *fixer") != 2 {
		t.Fatalf("both sites should merge the anchor:\n%s", out)
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// L4: an `agents:` entry that is not a block was skipped in silence, so
// the operator saw it vanish with no note.
func TestNonBlockAgentEntryIsNoted(t *testing.T) {
	_, notes := migrateDoc(t, agentsBase+`
agents:
  weird: "not a block"
  fixer: { workspace: worktree }
triggers:
  - { on: gh.pull_request, name: t, steps: [{ id: s, type: agent, agent: fixer, prompt: p }] }
`)
	if !hasNote(notes, "agents.weird skipped") {
		t.Fatalf("a non-block agents: entry must be reported: %v", notes)
	}
}
