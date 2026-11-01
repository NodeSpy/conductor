package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// memoryCLIConfig writes a config whose memory is a dir under a temp root,
// seeded through the real file backend, and returns the config path and
// memory dir.
func memoryCLIConfig(t *testing.T, seed func(m *memory.Manager)) (string, string) {
	t.Helper()
	root := t.TempDir()
	memDir := filepath.Join(root, "memory")
	b, err := memory.NewDirBackend(memDir)
	if err != nil {
		t.Fatal(err)
	}
	m := memory.NewManager(b)
	n := 0
	m.SetClock(func() time.Time {
		n++
		return time.Date(2026, 9, 1, 0, n, 0, 0, time.UTC)
	}, nil)
	seed(m)
	path := filepath.Join(root, "config.yaml")
	body := "connectors: { box: { use: command } }\nmemory: { dir: " + memDir + " }\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, memDir
}

func scopesIn(t *testing.T, dir string) map[string]int {
	t.Helper()
	b, err := memory.NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range all {
		out[e.Scope]++
	}
	return out
}

func TestMemoryRescopeCLI(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "")
	path, dir := memoryCLIConfig(t, func(m *memory.Manager) {
		for i := 0; i < 3; i++ {
			_, _ = m.Remember("outcome: acme/api#1 merged", []string{"outcome", "merged"}, "acme/api", memory.Source{})
		}
		_, _ = m.Remember("learned fact", []string{"testing"}, "acme/api", memory.Source{})
	})
	out, err := captureStdout(t, func() error {
		return cmdMemoryRescope([]string{"--config", path, "--tag", "outcome", "--to", "outcome:{scope}"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would move 3 entries") || !strings.Contains(out, "dry run") ||
		!strings.Contains(out, "3  acme/api -> outcome:acme/api") {
		t.Fatalf("dry-run output:\n%s", out)
	}
	if got := scopesIn(t, dir); got["acme/api"] != 4 {
		t.Fatalf("a dry run wrote: %v", got)
	}
	out, err = captureStdout(t, func() error {
		return cmdMemoryRescope([]string{"--config", path, "--tag", "outcome", "--to", "outcome:{scope}", "--apply"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "moved 3 entries") {
		t.Fatalf("apply output:\n%s", out)
	}
	if got := scopesIn(t, dir); got["acme/api"] != 1 || got["outcome:acme/api"] != 3 {
		t.Fatalf("after apply: %v", got)
	}
	if err := cmdMemoryRescope([]string{"--config", path, "--tag", "outcome"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("--to is required: %v", err)
	}
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "tok")
	if err := cmdMemoryRescope([]string{"--config", path, "--tag", "outcome", "--to", "x"}); err == nil || !strings.Contains(err.Error(), "agent session") {
		t.Fatalf("an agent shell must be refused: %v", err)
	}
}

// export → install the result as a pack → its entries are recalled
// read-only alongside the consumer's own memory, and the source is untouched.
func TestMemoryExportRoundTripsAsAPack(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "")
	path, srcDir := memoryCLIConfig(t, func(m *memory.Manager) {
		_, _ = m.Remember("api: prefer table tests", []string{"standard"}, "standard:acme/api", memory.Source{Repo: "acme/api"})
		_, _ = m.Remember("api: look hard at migrations", nil, "emphasis:acme/api", memory.Source{})
		_, _ = m.Remember("web only", nil, "standard:acme/web", memory.Source{})
	})
	before := snapshotDir(t, srcDir)
	out := filepath.Join(t.TempDir(), "training")
	if _, err := captureStdout(t, func() error {
		return cmdMemoryExport([]string{"--config", path, "--scope", "standard:acme/api", "--scope", "emphasis:acme/api",
			"--out", out, "--name", "acme-training"})
	}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, srcDir); after != before {
		t.Fatal("export modified the source memory")
	}
	problems, err := config.LintPackDir(out)
	if err != nil || len(problems) != 0 {
		t.Fatalf("the exported pack must lint clean: %v %v", problems, err)
	}
	if got := scopesIn(t, filepath.Join(out, "memory")); len(got) != 2 || got["standard:acme/web"] != 0 {
		t.Fatalf("exported scopes: %v", got)
	}
	// A second export refuses to clobber without --force, and --force
	// replaces rather than merges.
	if err := cmdMemoryExport([]string{"--config", path, "--scope", "standard:acme/api", "--out", out, "--name", "acme-training"}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("re-export without --force: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return cmdMemoryExport([]string{"--config", path, "--scope", "standard:acme/api", "--out", out, "--name", "acme-training", "--force"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := scopesIn(t, filepath.Join(out, "memory")); len(got) != 1 || got["standard:acme/api"] != 1 {
		t.Fatalf("--force must replace the entries: %v", got)
	}

	// Install it.
	consumer := t.TempDir()
	ownDir := filepath.Join(consumer, "own")
	cpath := filepath.Join(consumer, "config.yaml")
	body := "connectors: { box: { use: command } }\nmemory: { dir: " + ownDir + " }\npacks:\n  training:\n    use: " + out + "\n"
	if err := os.WriteFile(cpath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(cpath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}
	memory.Reset()
	t.Cleanup(memory.Reset)
	if err := configureMemory(cfg, nil); err != nil {
		t.Fatal(err)
	}
	m := memory.Active()
	if _, err := m.Remember("consumer's own", nil, "standard:acme/api", memory.Source{}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Recall(memory.Query{Scopes: []string{"standard:acme/api"}})
	if err != nil || len(got) != 2 {
		t.Fatalf("recall should merge the pack's entry with the consumer's own: %v %v", got, err)
	}
	var packID string
	for _, e := range got {
		if e.Text == "api: prefer table tests" {
			packID = e.ID
		}
	}
	if packID == "" {
		t.Fatalf("the pack's entry is not recalled: %v", got)
	}
	if _, err := m.Forget(packID); !errors.Is(err, memory.ErrReadOnly) {
		t.Fatalf("forgetting a pack entry must be refused: %v", err)
	}
	if own := scopesIn(t, ownDir); len(own) != 1 {
		t.Fatalf("the consumer's writes go to its own dir only: %v", own)
	}
}

func TestMemoryExportGuards(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "")
	path, _ := memoryCLIConfig(t, func(m *memory.Manager) {
		_, _ = m.Remember("token is hunter2-secret-value", nil, "acme/api", memory.Source{})
	})
	out := filepath.Join(t.TempDir(), "p")
	if err := cmdMemoryExport([]string{"--config", path, "--scope", "nothing-here", "--out", out}); err == nil || !strings.Contains(err.Error(), "nothing to export") {
		t.Fatalf("empty export: %v", err)
	}
	if err := cmdMemoryExport([]string{"--config", path, "--scope", "acme/api", "--out", out, "--name", "Bad Name"}); err == nil {
		t.Fatal("a bad pack name must be refused")
	}
	if err := cmdMemoryExport([]string{"--config", path, "--out", out}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("--scope is required: %v", err)
	}
	mgr, done, err := operatorMemory(mustLoad(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	_, err = exportMemoryPack(mgr, exportOpts{Scopes: []string{"acme/api"}, Out: out, Name: "p", Conductor: ">= 0.0.0",
		Secret: func(s string) bool { return strings.Contains(s, "hunter2-secret-value") }})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("an entry carrying a tracked secret must not be exported: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("a refused export must write nothing")
	}
	if got := exportConductorConstraint("v1.2.3"); got != ">= 1.2.3" {
		t.Fatalf("constraint: %q", got)
	}
	if got := exportConductorConstraint("dev"); got != ">= 0.0.0" {
		t.Fatalf("dev constraint: %q", got)
	}
}

func mustLoad(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		raw, _ := os.ReadFile(filepath.Join(dir, de.Name()))
		fi, _ := de.Info()
		b.WriteString(de.Name() + fi.ModTime().String() + string(raw))
	}
	return b.String()
}

// The live memory tool resolves the step's scope list from the session
// token, so a step restricted through skill.verbs is held to it there too.
func TestMemoryGrantOpReadsTheTokensStepGrant(t *testing.T) {
	b := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
	tok, err := b.MintSession(skill.Identity{
		Agent: "calibrate", Repo: "acme/api", TargetTrusted: true,
		Policy: config.SkillPolicy{Verbs: []string{"memory.remember"},
			VerbScopes: map[string]map[string][]string{"memory.remember": {"scope": {"standard:{{.repo}}"}}}},
	}, uint32(os.Getuid()), skill.SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	open, err := b.MintSession(skill.Identity{Agent: "x", Repo: "acme/api", TargetTrusted: true,
		Policy: config.SkillPolicy{Verbs: []string{"memory.remember"}}}, uint32(os.Getuid()), skill.SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	grant := memoryGrantOp(b, flow.New(flow.Runner{}))
	peer := memory.Peer{UID: uint32(os.Getuid()), Valid: true}
	g := grant(tok, "remember", peer)
	if g == nil || !g("standard:acme/api") || g("acme/api") {
		t.Fatal("the token's memory.remember scope list must bind the tool")
	}
	if grant(tok, "recall", peer) != nil {
		t.Fatal("a remember list must not restrict recall")
	}
	if grant(open, "remember", peer) != nil {
		t.Fatal("a step without a scope list is not restricted")
	}
	if grant("not-a-token", "remember", peer) != nil {
		t.Fatal("an unknown token resolves no grant")
	}
}
