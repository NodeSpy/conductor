package memory

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeEntry puts one entry into a directory in the file backend's format —
// how a pack's shipped entries look on disk.
func writeEntry(t *testing.T, dir string, e Entry) {
	t.Helper()
	b, err := NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Put(e); err != nil {
		t.Fatal(err)
	}
}

func at(min int) time.Time { return time.Date(2026, 9, 5, 12, min, 0, 0, time.UTC) }

func readOnlyLayer(t *testing.T, entries ...Entry) Backend {
	t.Helper()
	dir := t.TempDir()
	for _, e := range entries {
		writeEntry(t, dir, e)
	}
	b, err := NewReadOnlyDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ids(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// Recall merges the instance's own entries with every pack layer, sorts the
// union newest-first, and applies the limit AFTER the merge — so the newest
// entries win wherever they live.
func TestRecallMergesLayersBeforeTheLimit(t *testing.T) {
	m := testManager(t, NewMemBackend())
	for _, e := range []Entry{
		{ID: "own-old", Text: "own old", Scope: "acme/api", Created: at(1)},
		{ID: "own-new", Text: "own new", Scope: "acme/api", Created: at(5)},
	} {
		if err := m.backend.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	m.AddLayer("training", readOnlyLayer(t,
		Entry{ID: "pack-mid", Text: "pack mid", Scope: "acme/api", Created: at(3)},
		Entry{ID: "pack-newest", Text: "pack newest", Scope: "acme/api", Created: at(9)},
		Entry{ID: "pack-other", Text: "other scope", Scope: "acme/web", Created: at(8)},
	))
	got, err := m.Recall(Query{Scopes: []string{"acme/api"}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pack-newest", "own-new", "pack-mid"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("recall = %v, want %v (merged, newest first, limited after the merge)", ids(got), want)
	}
	all, _ := m.List()
	if len(all) != 5 {
		t.Fatalf("List should include layer entries, got %v", ids(all))
	}
}

// An exported pack carries the IDs of the entries it came from, so a box that
// has both must recall the note once — its own copy.
func TestOwnEntryShadowsALayerEntryWithTheSameID(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if err := m.backend.Put(Entry{ID: "dup", Text: "own copy", Scope: "s", Created: at(1)}); err != nil {
		t.Fatal(err)
	}
	m.AddLayer("a", readOnlyLayer(t,
		Entry{ID: "dup", Text: "pack copy", Scope: "s", Created: at(9)},
		Entry{ID: "x", Text: "first layer", Scope: "s", Created: at(2)},
	))
	m.AddLayer("b", readOnlyLayer(t, Entry{ID: "x", Text: "second layer", Scope: "s", Created: at(3)}))
	got, err := m.Recall(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want one entry per ID, got %v", ids(got))
	}
	for _, e := range got {
		switch e.ID {
		case "dup":
			if e.Text != "own copy" {
				t.Errorf("the instance's own entry must win, got %q", e.Text)
			}
		case "x":
			if e.Text != "first layer" {
				t.Errorf("an earlier layer must win, got %q", e.Text)
			}
		}
	}
}

// Layers are read-only through every write path the manager has.
func TestLayersAreNeverWritten(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, Entry{ID: "pack1", Text: "shipped", Scope: "acme/api", Tags: []string{"outcome"}, Created: at(1)})
	layer, err := NewReadOnlyDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t, NewMemBackend())
	m.AddLayer("training", layer)
	before, _ := os.ReadFile(filepath.Join(dir, "pack1.md"))

	// Remember goes to the instance backend.
	e, err := m.Remember("mine", nil, "acme/api", Source{})
	if err != nil {
		t.Fatal(err)
	}
	if own, _ := m.backend.List(); len(own) != 1 || own[0].ID != e.ID {
		t.Fatalf("remember must land in the instance backend, got %v", ids(own))
	}
	// Forget of a pack entry is refused, clearly, and changes nothing.
	found, err := m.Forget("pack1")
	if !errors.Is(err, ErrReadOnly) || found {
		t.Fatalf("forget of a pack entry: found=%v err=%v, want ErrReadOnly", found, err)
	}
	if !strings.Contains(err.Error(), "training") {
		t.Errorf("the refusal should name the pack: %v", err)
	}
	// Rescope never touches a layer.
	moves, err := m.Rescope("outcome", "outcome:{scope}", true)
	if err != nil || len(moves) != 0 {
		t.Fatalf("rescope must not see layer entries: moves=%v err=%v", moves, err)
	}
	// The backend itself refuses writes, for a caller that reached it.
	if err := layer.Put(Entry{ID: "pack1", Text: "x", Created: at(2)}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Put on a read-only layer: %v", err)
	}
	if _, err := layer.Delete("pack1"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete on a read-only layer: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "pack1.md"))
	if string(before) != string(after) {
		t.Fatal("the pack entry file changed")
	}
	// Forget of an unknown id is still a plain not-found.
	if found, err := m.Forget("nope"); found || err != nil {
		t.Fatalf("unknown id: found=%v err=%v", found, err)
	}
	// The scope of a pack entry is visible to the ownership check.
	if s, ok, err := m.ScopeOf("pack1"); err != nil || !ok || s != "acme/api" {
		t.Fatalf("ScopeOf(pack entry) = %q %v %v", s, ok, err)
	}
}

// A layer that can't be read (the vendor dir vanished) is skipped rather
// than failing every recall.
func TestABrokenLayerDoesNotBreakRecall(t *testing.T) {
	dir := t.TempDir()
	layer, err := NewReadOnlyDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t, NewMemBackend())
	m.AddLayer("gone", layer)
	if _, err := m.Remember("mine", nil, "s", Source{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	got, err := m.Recall(Query{})
	if err != nil || len(got) != 1 {
		t.Fatalf("recall with a broken layer: %v %v", ids(got), err)
	}
	if _, err := NewReadOnlyDirBackend(dir); err == nil {
		t.Fatal("a missing entries dir must be an error, not created")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the read-only backend must not create its directory")
	}
}

func TestRescope(t *testing.T) {
	for name, b := range backends(t) {
		t.Run(name, func(t *testing.T) {
			m := testManager(t, b)
			src := Source{Trigger: "outcome", Repo: "acme/api", Step: "fixer", Run: "r1"}
			o1, _ := m.Remember("outcome: acme/api#1 merged", []string{"outcome", "merged"}, "acme/api", src)
			o2, _ := m.Remember("outcome: acme/web#2 closed", []string{"outcome", "closed"}, "acme/web", src)
			learned, _ := m.Remember("tests need the fake clock", []string{"testing"}, "acme/api", src)
			done, _ := m.Remember("outcome: acme/api#3 merged", []string{"outcome"}, "outcome:acme/api", src)

			// Dry run: reports, writes nothing.
			moves, err := m.Rescope("outcome", "outcome:{scope}", false)
			if err != nil {
				t.Fatal(err)
			}
			want := []Move{
				{ID: o1.ID, Text: o1.Text, From: "acme/api", To: "outcome:acme/api"},
				{ID: o2.ID, Text: o2.Text, From: "acme/web", To: "outcome:acme/web"},
			}
			if !reflect.DeepEqual(moves, want) {
				t.Fatalf("dry run moves = %+v\nwant %+v", moves, want)
			}
			if got, _ := m.Recall(Query{Scopes: []string{"acme/api"}}); len(got) != 2 {
				t.Fatalf("a dry run must not move anything, acme/api has %v", ids(got))
			}

			// Apply: moved, with ID/text/tags/provenance/created intact.
			if _, err := m.Rescope("outcome", "outcome:{scope}", true); err != nil {
				t.Fatal(err)
			}
			api, _ := m.Recall(Query{Scopes: []string{"acme/api"}})
			if len(api) != 1 || api[0].ID != learned.ID {
				t.Fatalf("acme/api should keep only the learned entry, got %v", ids(api))
			}
			moved, _ := m.Recall(Query{Scopes: []string{"outcome:acme/api"}})
			if len(moved) != 2 {
				t.Fatalf("outcome:acme/api = %v", ids(moved))
			}
			for _, e := range moved {
				if e.ID != o1.ID {
					continue
				}
				if !e.Created.Equal(o1.Created) || !reflect.DeepEqual(e.Tags, o1.Tags) || e.Source != o1.Source || e.Text != o1.Text {
					t.Fatalf("a moved entry must be the same memory: %+v vs %+v", e, o1)
				}
			}
			if got, _ := m.Recall(Query{Scopes: []string{"outcome:outcome:acme/api"}}); len(got) != 0 {
				t.Fatal("an entry already in the target shape was moved again")
			}
			_ = done

			// Idempotent: a second run moves nothing.
			if again, err := m.Rescope("outcome", "outcome:{scope}", true); err != nil || len(again) != 0 {
				t.Fatalf("second run moved %v (%v)", again, err)
			}
		})
	}
}

func TestRescopeTemplates(t *testing.T) {
	m := testManager(t, NewMemBackend())
	e, _ := m.Remember("x", []string{"t"}, "", Source{})
	if _, err := m.Rescope("", "a:{scope}", false); err == nil {
		t.Error("a tag is required")
	}
	if _, err := m.Rescope("t", " ", false); err == nil {
		t.Error("a template is required")
	}
	if _, err := m.Rescope("t", "{scope}:{scope}", false); err == nil {
		t.Error("{scope} twice is refused")
	}
	// A global entry has the global key as its {scope}.
	moves, err := m.Rescope("t", "archive:{scope}", false)
	if err != nil || len(moves) != 1 || moves[0].From != GlobalScope || moves[0].To != "archive:global" {
		t.Fatalf("global entry: %+v %v", moves, err)
	}
	// A fixed target (no placeholder) moves everything to one key, and
	// leaves what is already there.
	moves, err = m.Rescope("t", "archive", true)
	if err != nil || len(moves) != 1 || moves[0].ID != e.ID || moves[0].To != "archive" {
		t.Fatalf("fixed target: %+v %v", moves, err)
	}
	if again, _ := m.Rescope("t", "archive", true); len(again) != 0 {
		t.Fatalf("fixed target is idempotent, moved %+v", again)
	}
	// A suffix template's shape check uses both halves.
	m2 := testManager(t, NewMemBackend())
	_, _ = m2.Remember("y", []string{"t"}, "acme/api", Source{})
	_, _ = m2.Remember("z", []string{"t"}, "acme/api#old", Source{})
	moves, _ = m2.Rescope("t", "{scope}#old", false)
	if len(moves) != 1 || moves[0].From != "acme/api" || moves[0].To != "acme/api#old" {
		t.Fatalf("suffix template: %+v", moves)
	}
}

func TestOwnListsOnlyTheInstanceEntries(t *testing.T) {
	m := testManager(t, NewMemBackend())
	a, _ := m.Remember("a", nil, "acme/api", Source{})
	b, _ := m.Remember("b", nil, "standard:acme/api", Source{})
	_, _ = m.Remember("c", nil, "acme/web", Source{})
	m.AddLayer("p", readOnlyLayer(t, Entry{ID: "packed", Text: "p", Scope: "acme/api", Created: at(30)}))
	got, err := m.Own([]string{"acme/api", "standard:acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{b.ID, a.ID}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Own = %v, want %v (newest first, no layer entries)", ids(got), want)
	}
	if all, _ := m.Own(nil); len(all) != 3 {
		t.Fatalf("Own(nil) = %v", ids(all))
	}
}

func TestExpandScopeRefsInsideKeys(t *testing.T) {
	for _, tc := range []struct {
		name                string
		in                  []string
		repo, workflow, stp string
		want                []string
	}{
		{"embedded", []string{"standard:${repo}", "emphasis:${repo}"}, "acme/api", "", "", []string{"standard:acme/api", "emphasis:acme/api"}},
		{"several in one key", []string{"${workflow}/${step}@${repo}"}, "acme/api", "nightly", "lint", []string{"nightly/lint@acme/api"}},
		{"a key with an empty referent drops", []string{"standard:${repo}", "global-ish"}, "", "w", "s", []string{"global-ish"}},
		{"one empty referent among several drops the key", []string{"${workflow}:${repo}"}, "", "w", "s", nil},
		{"whole-key placeholders still work", []string{"${repo}", "${step}"}, "acme/api", "", "lint", []string{"acme/api", "lint"}},
		{"no placeholder is verbatim", []string{" spaced ", "${unknown}"}, "r", "w", "s", []string{" spaced ", "${unknown}"}},
		{"a referent is not re-scanned", []string{"x:${repo}"}, "${step}", "w", "lint", []string{"x:${step}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandScopeRefs(tc.in, tc.repo, tc.workflow, tc.stp)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// extra_scopes are appended to the defaults, not a replacement for them.
func TestPromptSectionExtraScopesAppend(t *testing.T) {
	m := testManager(t, NewMemBackend())
	for _, n := range []struct{ text, scope string }{
		{"shared note", ""},
		{"repo note", "acme/api"},
		{"standard note", "standard:acme/api"},
		{"other repo", "acme/web"},
	} {
		if _, err := m.Remember(n.text, nil, n.scope, Source{}); err != nil {
			t.Fatal(err)
		}
	}
	keys := ContextKeys("acme/api", "", "lens")
	s := m.PromptSection(Filter{Extra: []string{"standard:acme/api", ""}}, keys)
	for _, want := range []string{"shared note", "repo note", "standard note"} {
		if !strings.Contains(s, want) {
			t.Errorf("defaults + extra should include %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "other repo") {
		t.Errorf("an unlisted scope leaked in:\n%s", s)
	}
	// With explicit scopes, extra appends to THOSE.
	s = m.PromptSection(Filter{Scopes: []string{"acme/web"}, Extra: []string{"standard:acme/api"}}, keys)
	if !strings.Contains(s, "other repo") || !strings.Contains(s, "standard note") || strings.Contains(s, "repo note") || strings.Contains(s, "shared note") {
		t.Errorf("explicit + extra:\n%s", s)
	}
	// No base keys at all: only the extras.
	s = m.PromptSection(Filter{Extra: []string{"standard:acme/api"}}, nil)
	if !strings.Contains(s, "standard note") || strings.Contains(s, "shared note") {
		t.Errorf("extras alone:\n%s", s)
	}
}

// A step grant is the WHOLE allowlist for the op: listed scopes pass, and
// everything else is refused — the dispatch's own scope, an unscoped call,
// what the daemon-wide guard would admit, and the reserved bucket.
func TestCheckOpStepGrant(t *testing.T) {
	m := testManager(t, NewMemBackend())
	m.SetScopeGuard(func(Caller, string, string) error { return nil }) // trust: full
	grant := func(s string) bool { return s == "standard:acme/api" || s == "acme/api" }
	c := NewAgentCaller("acme/api", true).WithGrant(grant)
	if !c.Restricted() {
		t.Fatal("WithGrant should restrict")
	}
	for _, scope := range []string{"standard:acme/api", "acme/api"} {
		if err := m.CheckOp(c, "remember", scope); err != nil {
			t.Errorf("listed %q refused: %v", scope, err)
		}
	}
	for _, scope := range []string{"repo:acme/api", "acme/web", "emphasis:acme/web", "", "  "} {
		if err := m.CheckOp(c, "remember", scope); err == nil {
			t.Errorf("unlisted %q allowed — a grant restriction must deny by default", scope)
		}
	}
	if err := m.CheckOp(NewAgentCaller("", false).WithGrant(func(string) bool { return true }), "remember", GlobalScope); !errors.Is(err, ErrReservedScope) {
		t.Errorf("the reserved bucket must stay refused under any grant: %v", err)
	}
	// The grant replaces the guard rather than intersecting with it.
	m.SetScopeGuard(func(Caller, string, string) error { return errors.New("guard says no") })
	if err := m.CheckOp(c, "remember", "standard:acme/api"); err != nil {
		t.Errorf("a granted scope must not also need the daemon-wide policy: %v", err)
	}
	// No grant: the guard decides, as before.
	if err := m.CheckOp(NewAgentCaller("acme/api", true), "remember", "standard:acme/api"); err == nil {
		t.Error("without a grant the guard must still apply")
	}
	// A nil grant leaves the caller unrestricted.
	if NewAgentCaller("a", true).WithGrant(nil).Restricted() {
		t.Error("WithGrant(nil) must not restrict")
	}
	// A config-authored (non-agent) call is never gated.
	if err := m.CheckOp(Caller{grant: grant}, "remember", "anything"); err != nil {
		t.Errorf("a config-authored call is not gated: %v", err)
	}
}

func TestHarvestHonoursTheStepGrant(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Step: "calibrate", Repo: "acme/api", TargetTrusted: true}
	grant := func(s string) bool { return s == "standard:acme/api" }
	out := "done\n```remember\n- text: prefer table tests\n  scope: standard:acme/api\n```\n"
	got, err := m.HarvestOutputGranted(out, src, grant)
	if err != nil || len(got) != 1 || got[0].Scope != "standard:acme/api" {
		t.Fatalf("granted harvest: %v %v", got, err)
	}
	for _, bad := range []string{
		"```remember\n- text: leak\n  scope: standard:acme/web\n```\n",
		"```remember\n- text: unscoped lands in the shared set\n```\n",
	} {
		before, _ := m.List()
		if _, err := m.HarvestOutputGranted(bad, src, grant); err == nil {
			t.Errorf("harvest outside the grant was accepted: %q", bad)
		}
		if after, _ := m.List(); len(after) != len(before) {
			t.Errorf("a refused harvest persisted something")
		}
	}
	// Without a grant an unscoped note still works (the output contract).
	if _, err := m.HarvestOutput("```remember\n- text: plain\n```\n", src); err != nil {
		t.Fatalf("ungranted unscoped harvest: %v", err)
	}
}

func TestIPCHonoursTheStepGrant(t *testing.T) {
	m := testManager(t, NewMemBackend())
	dispatch := Source{Step: "calibrate", Repo: "acme/api", TargetTrusted: true}
	var asked []string
	SetLiveOps(LiveOps{
		Identify: func(token string, _ Peer) (Source, int, bool) { return dispatch, 0, token == "tok" },
		Grant: func(token, op string, _ Peer) GrantMatcher {
			asked = append(asked, op)
			if token != "tok" || op != "remember" {
				return nil
			}
			return func(s string) bool { return s == "emphasis:acme/api" }
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })
	aud, logf := func(map[string]any) {}, func(string, ...any) {}
	if resp := handleIPC(m, IPCRequest{Op: "remember", Text: "ok", Scope: "emphasis:acme/api", Token: "tok"}, Peer{}, aud, logf); resp.Error != "" {
		t.Fatalf("granted scope refused: %v", resp.Error)
	}
	resp := handleIPC(m, IPCRequest{Op: "remember", Text: "no", Scope: "repo:acme/api", Token: "tok"}, Peer{}, aud, logf)
	if resp.Error == "" || !strings.Contains(resp.Error, "skill.verbs scope list") {
		t.Fatalf("the tool face must apply the step grant: %+v", resp)
	}
	if len(asked) == 0 || asked[0] != "remember" {
		t.Fatalf("the grant should be resolved per op, asked %v", asked)
	}
}
