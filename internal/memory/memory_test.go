package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/kv"
)

// testManager builds a manager with a deterministic clock and ID sequence.
func testManager(t *testing.T, b Backend) *Manager {
	t.Helper()
	m := NewManager(b)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	n := 0
	m.SetClock(func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Minute)
	}, func() string {
		return fmt.Sprintf("m%06d", n+1)
	})
	return m
}

// backends builds one instance of each backend family against temp storage.
func backends(t *testing.T) map[string]Backend {
	t.Helper()
	dir := t.TempDir()
	kv.ResetStores()
	t.Cleanup(kv.ResetStores)
	st, err := kv.OpenBoltStore("memtest", filepath.Join(dir, "memtest.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Register("memtest", st); err != nil {
		t.Fatal(err)
	}
	sb, err := NewStoreBackend("memtest")
	if err != nil {
		t.Fatal(err)
	}
	fb, err := NewDirBackend(filepath.Join(dir, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Backend{"store": sb, "file": fb, "memory": NewMemBackend()}
}

func TestBackendConformance(t *testing.T) {
	for name, b := range backends(t) {
		t.Run(name, func(t *testing.T) {
			m := testManager(t, b)
			src := Source{Step: "reviewer", Run: "flow:pr:1", Trigger: "pr_opened", Repo: "acme/api"}

			// remember → recall round trip with provenance.
			// Scope keys are OPAQUE: the repo string is just a key the
			// caller chose, with no privileged `repo:` type behind it.
			e1, err := m.Remember("prefer table-driven tests", []string{"style", "go"}, "acme/api", src)
			if err != nil {
				t.Fatalf("remember: %v", err)
			}
			if e1.Scope != "acme/api" {
				t.Fatalf("scope key: got %q", e1.Scope)
			}
			e2, err := m.Remember("CI needs the fake clock", []string{"testing"}, "", src)
			if err != nil {
				t.Fatal(err)
			}
			if e2.Scope != GlobalScope {
				t.Fatalf("empty scope is the shared set: got %q", e2.Scope)
			}
			e3, err := m.Remember("my own note", nil, "reviewer", src)
			if err != nil {
				t.Fatal(err)
			}
			if e3.Scope != "reviewer" {
				t.Fatalf("step scope key: got %q", e3.Scope)
			}

			all, err := m.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 3 {
				t.Fatalf("list: want 3 entries, got %d", len(all))
			}
			// Recency ordering: newest first.
			if all[0].ID != e3.ID || all[2].ID != e1.ID {
				t.Fatalf("recency order wrong: %v", []string{all[0].ID, all[1].ID, all[2].ID})
			}
			// Provenance survives the round trip.
			if all[2].Source != src {
				t.Fatalf("provenance lost: %+v", all[2].Source)
			}
			if len(all[2].Tags) != 2 || all[2].Tags[0] != "style" {
				t.Fatalf("tags lost: %v", all[2].Tags)
			}
			if !all[2].Created.Equal(e1.Created) {
				t.Fatalf("created lost: %v vs %v", all[2].Created, e1.Created)
			}

			// Scope filtering.
			got, err := m.Recall(Query{Scopes: []string{"acme/api"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].ID != e1.ID {
				t.Fatalf("scope filter: %+v", got)
			}
			// Tag filtering (ALL requested tags).
			got, _ = m.Recall(Query{Tags: []string{"style", "go"}})
			if len(got) != 1 || got[0].ID != e1.ID {
				t.Fatalf("tag filter: %+v", got)
			}
			got, _ = m.Recall(Query{Tags: []string{"style", "missing"}})
			if len(got) != 0 {
				t.Fatalf("tag filter should require all tags: %+v", got)
			}
			// Substring, case-insensitive.
			got, _ = m.Recall(Query{Substring: "FAKE CLOCK"})
			if len(got) != 1 || got[0].ID != e2.ID {
				t.Fatalf("substring filter: %+v", got)
			}
			// Limit keeps the newest.
			got, _ = m.Recall(Query{Limit: 2})
			if len(got) != 2 || got[0].ID != e3.ID || got[1].ID != e2.ID {
				t.Fatalf("limit: %+v", got)
			}

			// Forget.
			found, err := m.Forget(e2.ID)
			if err != nil || !found {
				t.Fatalf("forget: found=%v err=%v", found, err)
			}
			found, err = m.Forget(e2.ID)
			if err != nil || found {
				t.Fatalf("double forget should report not found, got found=%v err=%v", found, err)
			}
			if all, _ = m.List(); len(all) != 2 {
				t.Fatalf("after forget want 2, got %d", len(all))
			}
		})
	}
}

// Scope keys are opaque strings the memory core never interprets: there are
// no `global`/`repo`/`agent` TYPES any more (design §2), so the only
// normalization left is "empty means the shared set".
func TestNormalizeScopeIsOpaque(t *testing.T) {
	cases := map[string]string{
		"":                GlobalScope,
		"   ":             GlobalScope,
		GlobalScope:       GlobalScope,
		"acme/api":        "acme/api",
		"reviewer":        "reviewer",
		"repo":            "repo",  // no longer a type — just a key
		"agent":           "agent", // ditto
		"github.pr/audit": "github.pr/audit",
		"anything at all": "anything at all",
	}
	for in, want := range cases {
		if got := NormalizeScope(in); got != want {
			t.Errorf("NormalizeScope(%q) = %q, want %q", in, got, want)
		}
	}
}

// A scope key is never rejected: the core does not interpret keys, so there
// is nothing that could be invalid.
func TestRememberAcceptsAnyScopeKey(t *testing.T) {
	m := testManager(t, NewMemBackend())
	for _, k := range []string{"", "repo:", "weird key with spaces", "🙂", "a/b/c"} {
		if _, err := m.Remember("note", nil, k, Source{}); err != nil {
			t.Errorf("scope %q rejected: %v", k, err)
		}
	}
}

// The empty key and the persisted global token are the same set.
func TestGlobalScopeRecallsByEitherSpelling(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if _, err := m.Remember("shared", nil, "", Source{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", GlobalScope} {
		got, err := m.Recall(Query{Scopes: []string{q}})
		if err != nil {
			t.Fatal(err)
		}
		if q == GlobalScope && len(got) != 1 {
			t.Errorf("recall by %q: got %d", q, len(got))
		}
	}
}

func TestRememberValidation(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if _, err := m.Remember("  ", nil, "", Source{}); err == nil {
		t.Error("empty text should error")
	}
	// There is no such thing as a bad scope any more — keys are opaque.
	if _, err := m.Remember("x", nil, "weird", Source{}); err != nil {
		t.Errorf("an opaque scope key must be accepted: %v", err)
	}
	if _, err := m.Forget(""); err == nil {
		t.Error("empty forget id should error")
	}
	e, err := m.Remember("x", []string{" a ", "", "a", "b"}, "", Source{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Tags) != 2 || e.Tags[0] != "a" || e.Tags[1] != "b" {
		t.Errorf("tag normalization: %v", e.Tags)
	}
}

func TestFileBackendFrontmatterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t, b)
	src := Source{Step: "fixer", Run: "r1", Trigger: "failing_checks", Repo: "acme/api"}
	e, err := m.Remember("flaky: TestFoo needs -count=1", []string{"flaky", "ci"}, "acme/api", src)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, e.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	// The provenance key stays `agent:` on disk so entries written before
	// the agents: removal keep their attribution.
	for _, want := range []string{"---\n", "id: " + e.ID, "scope: acme/api", "agent: fixer", "trigger: failing_checks", "flaky: TestFoo needs -count=1"} {
		if !strings.Contains(s, want) {
			t.Errorf("file missing %q:\n%s", want, s)
		}
	}
	// Round trip.
	all, err := m.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	got := all[0]
	if got.ID != e.ID || got.Text != e.Text || got.Scope != e.Scope || got.Source != src ||
		!got.Created.Equal(e.Created) || len(got.Tags) != 2 {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, e)
	}
}

func TestFileBackendToleratesHandEditedFiles(t *testing.T) {
	dir := t.TempDir()
	b, err := NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A bare hand-written note: no frontmatter at all.
	if err := os.WriteFile(filepath.Join(dir, "deploy-notes.md"), []byte("Always run migrations before deploy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Partial frontmatter (tags only) — hand-added metadata.
	partial := "---\ntags: [infra]\n---\n\nStaging redis lives on box-7.\n"
	if err := os.WriteFile(filepath.Join(dir, "staging.md"), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	// Broken frontmatter must not lose the content.
	broken := "---\n: not yaml [\n---\n\nkeep me anyway\n"
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-memory files are skipped.
	_ = os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a memory"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("nope"), 0o644)

	m := NewManager(b)
	all, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 memories, got %d: %+v", len(all), all)
	}
	byID := map[string]Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	if e := byID["deploy-notes"]; e.Text != "Always run migrations before deploy." || e.Scope != "global" || e.Created.IsZero() {
		t.Errorf("bare file: %+v", e)
	}
	if e := byID["staging"]; e.Text != "Staging redis lives on box-7." || len(e.Tags) != 1 || e.Tags[0] != "infra" {
		t.Errorf("partial frontmatter: %+v", e)
	}
	if e := byID["broken"]; !strings.Contains(e.Text, "keep me anyway") {
		t.Errorf("broken frontmatter should keep content: %+v", e)
	}
	// A hand-edited file is editable in place and survives a Put/Delete cycle.
	if found, err := m.Forget("deploy-notes"); err != nil || !found {
		t.Fatalf("forget hand file: %v %v", found, err)
	}
}

func TestStoreBackendIgnoresForeignValues(t *testing.T) {
	kv.ResetStores()
	t.Cleanup(kv.ResetStores)
	st, err := kv.OpenBoltStore("m2", filepath.Join(t.TempDir(), "m2.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = kv.Register("m2", st)
	if err := st.Set("memory", "junk", "just a string", 0); err != nil {
		t.Fatal(err)
	}
	b, err := NewStoreBackend("m2")
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t, b)
	if _, err := m.Remember("real one", nil, "", Source{}); err != nil {
		t.Fatal(err)
	}
	all, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Text != "real one" {
		t.Fatalf("foreign value should be skipped: %+v", all)
	}
}

func TestBuildOptions(t *testing.T) {
	if _, err := Build(Options{}); err == nil {
		t.Error("no backend should error")
	}
	if _, err := Build(Options{Dir: t.TempDir(), Type: "memory"}); err == nil {
		t.Error("two backends should error")
	}
	if _, err := Build(Options{Store: "ghost"}); err == nil {
		t.Error("unknown store should error")
	}
	if _, err := Build(Options{Type: "memory"}); err != nil {
		t.Errorf("ephemeral: %v", err)
	}
	if _, err := Build(Options{Dir: filepath.Join(t.TempDir(), "notes")}); err != nil {
		t.Errorf("dir: %v", err)
	}
}

func TestActiveRegistry(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if Active() != nil {
		t.Fatal("fresh registry should be empty")
	}
	m := NewManager(NewMemBackend())
	Configure(m)
	if Active() != m {
		t.Fatal("Configure should install the manager")
	}
	SetToolCommand([]string{"/bin/conductor", "mcp", "memory", "--socket", "/tmp/x"})
	if got := ToolCommand(); len(got) != 5 || got[1] != "mcp" {
		t.Fatalf("tool command: %v", got)
	}
	Reset()
	if Active() != nil || ToolCommand() != nil {
		t.Fatal("Reset should clear everything")
	}
}
