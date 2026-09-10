package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// H5: the tree used to be validated after EVERY per-file write, so the
// outcome depended on filename order. A file that REFERENCES a profile
// sorts before the file that DEFINES it, the mid-pass reload saw a
// half-migrated tree, and the box aborted and restored — on every boot,
// forever.
func TestAutoMigrateIsIndependentOfFilenameOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(dir, "config.yaml")
	write("config.yaml", `
imports: [conf.d/*.yaml]
connectors:
  gh: { use: github, token: x }
runtimes:
  paseo: { use: paseo, default: true }
`)
	// aaa- sorts FIRST and only references; zzz- sorts last and defines.
	write("conf.d/aaa-triggers.yaml", `
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: fixer, prompt: "p" }] }
`)
	write("conf.d/zzz-agents.yaml", `
agents:
  fixer: { workspace: worktree }
`)

	n, summary, err := AutoMigrate(main, func() error {
		_, lerr := config.Load(main)
		return lerr
	}, nil)
	if err != nil {
		t.Fatalf("the tree must migrate regardless of order: %v\nsummary: %v", err, summary)
	}
	if n == 0 {
		t.Fatal("nothing migrated")
	}
	// The behavior actually reached the referencing file.
	cfg, err := config.Load(main)
	if err != nil {
		t.Fatalf("migrated tree must load: %v", err)
	}
	if got := cfg.Triggers[0].Steps[0].Workspace; got != "worktree" {
		t.Fatalf("the cross-file profile did not inline: workspace=%q", got)
	}
	if got := cfg.Triggers[0].Steps[0].Name; got != "fixer" {
		t.Fatalf("identity continuity lost: name=%q", got)
	}
}

// The fail-safe still holds, and now covers the WHOLE tree: if the
// migrated result does not validate, every file goes back.
func TestAutoMigrateRestoresEveryFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	sub := filepath.Join(dir, "conf.d", "x.yaml")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	mainBody := "imports: [conf.d/*.yaml]\nagents:\n  a: { workspace: local }\n"
	subBody := "agents:\n  b: { workspace: local }\n"
	if err := os.WriteFile(main, []byte(mainBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte(subBody), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := AutoMigrate(main, func() error { return os.ErrInvalid }, nil)
	if err == nil {
		t.Fatal("a failing validation must abort the migration")
	}
	for path, want := range map[string]string{main: mainBody, sub: subBody} {
		got, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		if string(got) != want {
			t.Fatalf("%s was not restored:\n%s", path, got)
		}
	}
	if !strings.Contains(err.Error(), "restored") {
		t.Fatalf("the error should say the originals are back: %v", err)
	}
}

// M3: `runtimes:` in the main file, `agents.x.budget` in an imported one.
// moveBudgetToRuntime only ever looked at the CURRENT file, so the budget
// was dropped with a "declares no runtimes" note that was not true of the
// tree. Imports merge maps, so writing the budget under the runtime's name
// in the file being transformed lands it on the same entry.
func TestAutoMigrateMovesBudgetAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	sub := filepath.Join(dir, "conf.d", "agents.yaml")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte(`
imports: [conf.d/*.yaml]
connectors:
  gh: { use: github, token: x }
runtimes:
  gpu: { use: paseo, default: true }
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: fixer, prompt: p }] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte(`
agents:
  fixer: { runtime: gpu, budget: { max_cost_usd: 5 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, summary, err := AutoMigrate(main, func() error {
		_, lerr := config.Load(main)
		return lerr
	}, nil)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if strings.Contains(strings.Join(summary, "\n"), "declares no runtimes") {
		t.Fatalf("the budget must not be dropped as unplaceable: %v", summary)
	}
	cfg, err := config.Load(main)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if b := cfg.Runtimes["gpu"].Budget; b == nil || b.MaxCostUSD != 5 {
		t.Fatalf("budget should have landed on runtimes.gpu, got %+v", b)
	}
}

// L3: the "nothing referenced it" note belongs to the whole-tree pass. A
// profile referenced from ANOTHER file is not an orphan (that note was
// false and alarming); one nothing references anywhere is, and is still
// reported rather than vanishing.
func TestAutoMigrateReportsGenuinelyUnreferencedProfiles(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	sub := filepath.Join(dir, "conf.d", "t.yaml")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte(`
imports: [conf.d/*.yaml]
connectors:
  gh: { use: github, token: x }
runtimes:
  paseo: { use: paseo, default: true }
agents:
  used:   { workspace: worktree }
  orphan: { workspace: local }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The reference to `used` lives in the OTHER file.
	if err := os.WriteFile(sub, []byte(`
triggers:
  - { on: gh.pull_request, name: a, steps: [{ id: s, type: agent, agent: used, prompt: p }] }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, summary, err := AutoMigrate(main, func() error {
		_, lerr := config.Load(main)
		return lerr
	}, nil)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	joined := strings.Join(summary, "\n")
	if !strings.Contains(joined, "agents.orphan dropped") {
		t.Errorf("a genuinely unreferenced profile should be reported: %v", summary)
	}
	if strings.Contains(joined, "agents.used dropped") {
		t.Errorf("a profile referenced from another file is NOT an orphan: %v", summary)
	}
}
