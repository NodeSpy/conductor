package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
	"gopkg.in/yaml.v3"
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
	// --scope alone archives one scope's entries.
	out, err = captureStdout(t, func() error {
		return cmdMemoryRescope([]string{"--config", path, "--scope", "acme/api", "--to", "archive:{scope}", "--apply"})
	})
	if err != nil || !strings.Contains(out, `moved 1 entry in scope "acme/api"`) {
		t.Fatalf("--scope apply: %v\n%s", err, out)
	}
	if got := scopesIn(t, dir); got["archive:acme/api"] != 1 || got["outcome:acme/api"] != 3 {
		t.Fatalf("after --scope: %v", got)
	}
	if err := cmdMemoryRescope([]string{"--config", path, "--to", "x:{scope}"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("a filter is required: %v", err)
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
	// The canonical, unspaced form: ">= 1.2.3" was refused by daemons whose
	// constraint parser split on the space.
	if got := exportConductorConstraint("v1.2.3"); got != ">=1.2.3" {
		t.Fatalf("constraint: %q", got)
	}
	if got := exportConductorConstraint("dev"); got != ">=0.0.0" {
		t.Fatalf("dev constraint: %q", got)
	}
}

// A pack is meant to be shared: what wrote an entry on this box — repo, step,
// run, trigger, dispatch — must not ride along. Only id/tags/scope/created
// and the text leave.
func TestMemoryExportStripsProvenance(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "")
	src := memory.Source{Repo: "internal-org/secret-infra", Step: "deploy-prod-step", Run: "run-abc123",
		Trigger: "pull_request", Dispatch: "dispatch-xyz789", TargetTrusted: true}
	path, _ := memoryCLIConfig(t, func(m *memory.Manager) {
		_, _ = m.Remember("api: prefer table tests", []string{"standard"}, "standard:acme/api", src)
	})
	out := filepath.Join(t.TempDir(), "p")
	if _, err := captureStdout(t, func() error {
		return cmdMemoryExport([]string{"--config", path, "--scope", "standard:acme/api", "--out", out, "--name", "p"})
	}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(out, exportEntriesDir, "*.md"))
	if len(files) != 1 {
		t.Fatalf("exported files: %v", files)
	}
	var all strings.Builder
	for _, name := range append(files, filepath.Join(out, config.PackManifestFile)) {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
	}
	for _, leak := range []string{"source", "internal-org", "secret-infra", "deploy-prod-step", "run-abc123",
		"pull_request", "dispatch-xyz789", "target_trusted", "agent:", "repo:"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("export leaked %q:\n%s", leak, all.String())
		}
	}
	raw, _ := os.ReadFile(files[0])
	parts := strings.SplitN(string(raw), "---\n", 3)
	if len(parts) != 3 {
		t.Fatalf("no frontmatter:\n%s", raw)
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "tags": true, "scope": true, "created": true}
	for k := range fm {
		if !allowed[k] {
			t.Errorf("frontmatter key %q is not allowed in an export", k)
		}
	}
	for k := range allowed {
		if _, ok := fm[k]; !ok {
			t.Errorf("frontmatter lost %q", k)
		}
	}
	if !strings.Contains(parts[2], "api: prefer table tests") {
		t.Errorf("the text must survive:\n%s", raw)
	}
	// The exported entry still reads back with nothing to say about where it came from.
	b, _ := memory.NewReadOnlyDirBackend(filepath.Join(out, exportEntriesDir))
	got, _ := b.List()
	if len(got) != 1 || got[0].Source != (memory.Source{}) || got[0].Scope != "standard:acme/api" {
		t.Fatalf("read back: %+v", got)
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
	// The identity a real dispatch mints: the step's grant rendered over the
	// facts its dispatch carried — here a team input, not a repo.
	trig := core.Trigger{Target: core.Target{Repo: "acme/api", Owner: "acme", Name: "api"}, TargetTrusted: true}
	id, ok := dispatch.SkillIdentityFor(dispatch.Request{
		Trigger: trig, Action: config.Action{Agent: "calibrate"},
		Step: config.Step{Skill: &config.SkillPolicy{Verbs: []string{"memory.remember"},
			VerbScopes: map[string]map[string][]string{"memory.remember": {"scope": {"standard:{{.repo}}", "team:{{.inputs.team}}", "x:{{.inputs.none}}"}}}}},
		ScopeFacts: memory.ScopeFacts(map[string]any{"inputs": map[string]any{"team": "payments"}}, trig, "review", "lens", nil),
	})
	if !ok {
		t.Fatal("no identity")
	}
	tok, err := b.MintSessionUID(id, uint32(os.Getuid()), skill.SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	open, err := b.MintSessionUID(skill.Identity{Agent: "x", Repo: "acme/api", TargetTrusted: true,
		Policy: config.SkillPolicy{Verbs: []string{"memory.remember"}}}, uint32(os.Getuid()), skill.SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	grant := memoryGrantOp(b)
	peer := memory.Peer{UID: uint32(os.Getuid()), Valid: true}
	g := grant(tok, "remember", peer)
	if g == nil || !g("standard:acme/api") || !g("team:payments") || g("acme/api") || g("x:") || g("team:") {
		t.Fatal("the token's memory.remember scope list, rendered at dispatch, must bind the tool")
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

// `conductor memory key` is core's renderer: it agrees with every shared
// vector (internal/memory/testdata), so a script mapping an event to its key
// cannot drift from the engine.
func TestMemoryKeyCLIMatchesTheVectors(t *testing.T) {
	raw, err := os.ReadFile("../../internal/memory/testdata/scope_key_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Facts json.RawMessage `json:"facts"`
		Cases []struct {
			Template string `json:"template"`
			Want     string `json:"want"`
			Drop     bool   `json:"drop"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, c := range v.Cases {
		if c.Template == "" {
			continue // the CLI needs a template to render
		}
		var out bytes.Buffer
		err := cmdMemoryKey(bytes.NewReader(v.Facts), &out, []string{"--template", c.Template, "--facts", "-"})
		switch {
		case c.Drop && err == nil:
			t.Errorf("%q must not render, got %q", c.Template, out.String())
		case !c.Drop && err != nil:
			t.Errorf("%q: %v", c.Template, err)
		case !c.Drop && strings.TrimSuffix(out.String(), "\n") != c.Want:
			t.Errorf("%q = %q, want %q", c.Template, out.String(), c.Want)
		}
		ran++
	}
	if ran < 30 {
		t.Fatalf("only %d vectors ran", ran)
	}
	// --fact pairs work alone and win over --facts.
	var out bytes.Buffer
	if err := cmdMemoryKey(strings.NewReader(`{"team":"a"}`), &out, []string{"--template", "team:{{.team}}", "--facts", "-", "--fact", "team=b"}); err != nil || out.String() != "team:b\n" {
		t.Fatalf("--fact: %q %v", out.String(), err)
	}
	if err := cmdMemoryKey(nil, &out, []string{"--template", "team:{{.team}}"}); err == nil || !strings.Contains(err.Error(), "no value") {
		t.Fatalf("no facts: %v", err)
	}
	if err := cmdMemoryKey(nil, &out, nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("--template is required: %v", err)
	}
}
