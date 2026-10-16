package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func teamCols() map[string]config.Collection {
	return map[string]config.Collection{
		"postmortems": {Dir: "postmortems", Writers: []string{"operator", "proposal"},
			Fields: map[string]config.CollectionField{"title": {Type: "string", Required: true}, "severity": {Type: "enum", Required: true, Values: []string{"sev1", "sev2"}}}},
		"notes": {Dir: "notes", FreePaths: true, Writers: []string{"operator"},
			Fields: map[string]config.CollectionField{"title": {Type: "string", Required: true}}},
	}
}

// PATH DERIVATION: kind + slug → home/<slug>.md; a title slugs; nothing
// else derives a path.
func TestKindPathDerivation(t *testing.T) {
	ks := NewKinds(false, nil)
	for kind, want := range map[string]string{KindKnowledge: "knowledge/db-primary.md", KindRunbook: "runbooks/db-primary.md", KindWatch: "watch/db-primary.md"} {
		k, _ := ks.Get(kind)
		if p, err := k.Derive("db-primary"); err != nil || p != want {
			t.Errorf("%s: %q %v", kind, p, err)
		}
	}
	k, _ := ks.Get(KindKnowledge)
	for _, bad := range []string{"", "Db", "a/b", "../x", "-x", "x.md", strings.Repeat("a", 81)} {
		if p, err := k.Derive(bad); err == nil {
			t.Errorf("slug %q derived %q", bad, p)
		}
	}
	if p, err := resolveDocPath(ks, KindRunbook, "", "", "Failover: the DB!", ""); err != nil || p != "runbooks/failover-the-db.md" {
		t.Fatalf("from a title: %q %v", p, err)
	}
	if p, err := resolveDocPath(ks, KindKnowledge, "", "", "", kd("x")); err != nil || p != "knowledge/a-fact.md" {
		t.Fatalf("from the frontmatter's title: %q %v", p, err)
	}
	if p, err := resolveDocPath(ks, KindWatch, "", "", "", "---\nwhat: Disk on db-1\n---\n"); err != nil || p != "watch/disk-on-db-1.md" {
		t.Fatalf("a watch from its what: %q %v", p, err)
	}
	if _, err := resolveDocPath(ks, "", "", "", "x", ""); err == nil {
		t.Fatal("no kind, no path")
	}
	if _, err := resolveDocPath(ks, KindKnowledge, "runbooks/x.md", "", "", ""); err == nil {
		t.Fatal("a knowledge path under runbooks/")
	}
}

// OUT-OF-HOME: a document is written only in its kind's home, and a write
// naming another kind than its path's is refused.
func TestKindOutOfHomeRejection(t *testing.T) {
	ks := NewKinds(false, nil)
	if _, err := ks.CheckWrite("knowledge/r.md", rd("x"), WriterBot, KindRunbook); err == nil ||
		!strings.Contains(err.Error(), "knowledge/r.md is a knowledge path (knowledge/); a runbook document lives under runbooks/") {
		t.Fatalf("a runbook into knowledge/: %v", err)
	}
	if _, err := ks.CheckWrite("knowledge/r.md", rd("x"), WriterBot, ""); err == nil || !strings.Contains(err.Error(), "knowledge schema") {
		t.Fatalf("a runbook's content as knowledge: %v", err)
	}
	for _, p := range []string{"notes/x.md", "x.md", "episodes/2026/x.md", "knowledge/sub/x.md", "knowledge/X.md", "knowledge/x.txt"} {
		if _, err := ks.CheckWrite(p, kd("x"), WriterBot, ""); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if _, err := ks.CheckWrite("episodes/2026/x.md", "---\ntitle: x\n---\n", WriterSystem, ""); err == nil || !strings.Contains(err.Error(), "legacy home") {
		t.Fatalf("a write into the legacy home: %v", err)
	}
	if _, err := ks.CheckWrite("knowledge/x.md", kd("x"), WriterBot, ""); err != nil {
		t.Fatalf("a valid document: %v", err)
	}
	if _, err := ks.CheckWrite("knowledge/x.md", kd("x"), WriterBot, "nope"); err == nil {
		t.Fatal("an unknown kind")
	}
}

// SCHEMA: every problem, precisely.
func TestKindSchemaValidation(t *testing.T) {
	ks := NewKinds(false, nil)
	rb, _ := ks.Get(KindRunbook)
	err := rb.Validate("---\ntitle: Failover\nsteps: do it\nowner: [a, b]\nextra: 1\n---\nbody\n")
	if err == nil {
		t.Fatal("a malformed runbook passed")
	}
	for _, want := range []string{"missing required field when_to_use", "missing required field verification", "missing required field rollback",
		"field steps: want a list", "field owner: want a string", "unknown field extra", "(fields: title string, required;"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in: %v", want, err)
		}
	}
	if err := rb.Validate(rd("ok")); err != nil {
		t.Fatalf("a valid runbook: %v", err)
	}
	for name, doc := range map[string]string{
		"no frontmatter":    "just text",
		"unterminated":      "---\ntitle: x\n",
		"not yaml":          "---\ntitle: [x\n---\n",
		"empty steps":       strings.Replace(rd(""), "steps:\n  - look\n  - act\n", "steps: []\n", 1),
		"blank step":        strings.Replace(rd(""), "  - act\n", "  - \"\"\n", 1),
		"multi-line string": strings.Replace(rd(""), "owner: sre", "owner: \"a\\nb\"", 1),
	} {
		if err := rb.Validate(doc); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	k, _ := ks.Get(KindKnowledge)
	if err := k.Validate(strings.Replace(kd(""), "confidence: high", "confidence: certain", 1)); err == nil || !strings.Contains(err.Error(), "certain is not one of low, medium, high") {
		t.Fatalf("an enum: %v", err)
	}
	if err := k.Validate(strings.Replace(kd(""), "title: a fact\n", "title: a fact\napplies_to: db\n", 1)); err == nil || !strings.Contains(err.Error(), "applies_to: want a list") {
		t.Fatalf("a list: %v", err)
	}
	if err := k.Validate(strings.Replace(kd(""), "title: a fact\n", "title: a fact\napplies_to: [db, cache]\n", 1)); err != nil {
		t.Fatalf("a valid list: %v", err)
	}
	// Collections' number and bool types.
	nk := NewKinds(true, map[string]config.Collection{"m": {Dir: "m", Writers: []string{"operator"},
		Fields: map[string]config.CollectionField{"n": {Type: "number", Required: true}, "b": {Type: "bool"}}}})
	m, _ := nk.Get("m")
	if err := m.Validate("---\nn: 3\nb: true\n---\n"); err != nil {
		t.Fatalf("number/bool: %v", err)
	}
	if err := m.Validate("---\nn: three\nb: yes please\n---\n"); err == nil || !strings.Contains(err.Error(), "want a number") || !strings.Contains(err.Error(), "want true or false") {
		t.Fatalf("number/bool types: %v", err)
	}
}

// FREE PATHS: only a collection with free_paths: true takes sub-paths.
func TestKindFreePathsGate(t *testing.T) {
	ks := NewKinds(true, teamCols())
	doc := "---\ntitle: x\n---\n"
	if _, err := ks.CheckWrite("notes/2026/q4/retro.md", doc, WriterOperator, ""); err != nil {
		t.Fatalf("a free-path collection: %v", err)
	}
	pm := "---\ntitle: x\nseverity: sev1\n---\n"
	if _, err := ks.CheckWrite("postmortems/2026/db.md", pm, WriterOperator, ""); err == nil || !strings.Contains(err.Error(), "free paths are only for a collection with free_paths: true") {
		t.Fatalf("a sub-path in a slug collection: %v", err)
	}
	if _, err := ks.CheckWrite("postmortems/db-outage.md", pm, WriterOperator, ""); err != nil {
		t.Fatalf("a slug path: %v", err)
	}
	if _, err := ks.CheckWrite("knowledge/a/b.md", kd("x"), WriterOperator, ""); err == nil {
		t.Fatal("a built-in took a free path")
	}
}

// WRITERS: log and handoff are conductor's; an operator annotates or flags
// a log entry, never rewrites it; a collection's writers hold.
func TestKindWriterRules(t *testing.T) {
	ks := NewKinds(false, nil)
	logDoc := "---\ntitle: case c1\ncase: c1\n---\nentry\n"
	for _, w := range []Writer{WriterBot, WriterOperator, WriterProposal} {
		if _, err := ks.CheckWrite("log/2026/10/c1.md", logDoc, w, ""); err == nil {
			t.Errorf("%s wrote the log", w)
		}
		if _, err := ks.CheckWrite("handoff/x.md", "---\nsession: s1\n---\n", w, ""); err == nil {
			t.Errorf("%s wrote a handoff", w)
		}
		if err := ks.CheckRemove("log/2026/10/c1.md", w); err == nil {
			t.Errorf("%s removed a log entry", w)
		}
	}
	if _, err := ks.CheckWrite("log/2026/10/c1.md", logDoc, WriterSystem, ""); err != nil {
		t.Fatalf("conductor's log write: %v", err)
	}
	if _, err := ks.CheckWrite("knowledge/x.md", kd("x"), WriterSystem, ""); err == nil {
		t.Fatal("conductor wrote a bot's knowledge as itself")
	}
	// The team repo: knowledge by operator or proposal, never a bot directly.
	tk := NewKinds(true, teamCols())
	if _, err := tk.CheckWrite("knowledge/x.md", kd("x"), WriterBot, ""); err == nil {
		t.Fatal("a bot wrote team knowledge directly")
	}
	if _, err := tk.CheckWrite("notes/x.md", "---\ntitle: x\n---\n", WriterProposal, ""); err == nil {
		t.Fatal("a promotion into an operator-only collection")
	}
	if _, err := tk.CheckWrite("postmortems/x.md", "---\ntitle: x\nseverity: sev2\n---\n", WriterProposal, ""); err != nil {
		t.Fatalf("a promotion into a proposal collection: %v", err)
	}
	if _, err := tk.Get(KindLog); err == nil {
		t.Fatal("a team repo has a log")
	}

	// Through the service: the bot, the operator, conductor.
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	for p, hint := range map[string]string{"log/2026/10/x.md": "conductor bot episode", "episodes/2026/x.md": "conductor bot episode", "handoff/x.md": "conductor bot handoff"} {
		if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Path: p, Text: logDoc}); err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("a bot remembered into %s, or was not told how: %v", p, err)
		}
	}
	cid := f.deliver(t, Event{Bot: "b", Source: "pd.incident", Title: "db down"})
	if _, err := f.svc.Episode(ctx, f.homeOf("b"), cid, "investigated"); err != nil {
		t.Fatal(err)
	}
	lp := f.svc.Repo("b").EpisodePath(cid, "db down", f.caseOf(t, "b", cid).Created)
	if text, err := f.svc.Repo("b").Read(lp); err != nil || !strings.HasPrefix(text, "---\ntitle: db down\ncase: "+cid+"\n---\n") {
		t.Fatalf("conductor's log entry: %q %v", text, err)
	}
	base := f.head(t, "", "b")
	for _, e := range []Edit{
		{Op: EditPut, Path: lp, Text: logDoc, Base: base},
		{Op: EditRm, Path: lp, Base: base},
		{Op: EditMv, Path: lp, To: "log/2026/10/y.md", Base: base},
	} {
		if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", e); err == nil || !strings.Contains(err.Error(), "conductor only") {
			t.Errorf("an operator %s on the log: %v", e.Op, err)
		}
	}
	if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", Edit{Op: EditAnnotate, Path: lp, Text: "this was the cache, not the db", Base: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", Edit{Op: EditFlag, Path: lp, Text: "wrong root cause", Base: f.head(t, "", "b")}); err != nil {
		t.Fatal(err)
	}
	text, _ := f.svc.Repo("b").Read(lp)
	if !strings.Contains(text, "investigated") || !strings.Contains(text, "## Operator note — operator op") || !strings.Contains(text, "this was the cache") ||
		!strings.Contains(text, "flagged_wrong: wrong root cause") || !strings.Contains(text, "## Flagged WRONG by the operator") {
		t.Fatalf("annotated / flagged log entry:\n%s", text)
	}
	if k, _ := f.svc.Repo("b").Kinds().Get(KindLog); k.Validate(text) != nil {
		t.Fatalf("the annotated entry is no longer valid: %v", k.Validate(text))
	}
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("b"), MemoryWrite{Kind: KindKnowledge, Title: "x", Text: kd("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", Edit{Op: EditAnnotate, Path: "knowledge/x.md", Text: "x", Base: f.head(t, "", "b")}); err == nil || !strings.Contains(err.Error(), "annotate and flag are for log documents") {
		t.Fatalf("annotate on a document the operator can just edit: %v", err)
	}
	// A revert that would rewrite the log is refused too — even one that
	// would only delete a fresh entry (the commit that created it).
	hist, _ := f.svc.Repo("b").History(ctx, lp, 1)
	if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", Edit{Op: EditRevert, Commit: hist[0].Commit, Base: f.head(t, "", "b")}); err == nil {
		t.Fatal("an operator reverted a log entry")
	}
	c2 := f.deliver(t, Event{Bot: "b", Source: "pd.incident", Title: "cache down"})
	if _, err := f.svc.Episode(ctx, f.homeOf("b"), c2, "looked"); err != nil {
		t.Fatal(err)
	}
	lp2 := f.svc.Repo("b").EpisodePath(c2, "cache down", f.caseOf(t, "b", c2).Created)
	created, _ := f.svc.Repo("b").History(ctx, lp2, 1)
	if _, err := f.svc.OperatorEdit(ctx, "", "b", "op", "", Edit{Op: EditRevert, Commit: created[0].Commit, Base: f.head(t, "", "b")}); err == nil || !strings.Contains(err.Error(), "conductor only") {
		t.Fatalf("an operator deleted a log entry by reverting its creation: %v", err)
	}
	if _, err := f.svc.Repo("b").Read(lp2); err != nil {
		t.Fatalf("the log entry is gone: %v", err)
	}
}

// PROMOTION is checked against the destination's kind when proposed: a
// malformed runbook is refused with its schema errors, nothing proposed.
func TestPromotionChecksTheDestinationKind(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Kind: KindKnowledge, Title: "failover", Text: kd("fail over by hand")}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/failover.md", "runbooks/failover.md", "")
	if err == nil || !strings.Contains(err.Error(), "runbook schema") || !strings.Contains(err.Error(), "missing required field steps") {
		t.Fatalf("a malformed runbook promoted: %v", err)
	}
	if n := len(f.st.Bot("a").Proposals); n != 0 {
		t.Fatalf("a refused promotion left %d proposal(s)", n)
	}
	if _, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/failover.md", "notes/x.md", ""); err == nil {
		t.Fatal("promoted into no kind's home")
	}
	if _, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/failover.md", "", ""); err != nil {
		t.Fatalf("a valid promotion: %v", err)
	}
	// A bot's own write is typed too.
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Kind: KindRunbook, Title: "x", Text: "---\ntitle: x\n---\nsteps"}); err == nil || !strings.Contains(err.Error(), "missing required field steps") {
		t.Fatalf("a bot's malformed runbook: %v", err)
	}
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Kind: KindRunbook, Path: "knowledge/x.md", Text: rd("x")}); err == nil {
		t.Fatal("a runbook into knowledge/")
	}
}

// COLLECTIONS in a team's repo, through the operator's `new`.
func TestOperatorNewAndCollections(t *testing.T) {
	f := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Collections = teamCols()
		c.Teams["platform"] = tc
	})
	ctx := context.Background()
	out, err := f.svc.OperatorNew(ctx, "platform", "", "op", "", NewDoc{Kind: KindRunbook, Title: "DB failover",
		Fields: map[string]string{"when_to_use": "the primary is down", "steps": "[promote the replica, repoint the app]",
			"verification": "writes succeed", "rollback": "repoint back", "owner": "platform-data"}, Base: f.head(t, "platform", "")})
	if err != nil {
		t.Fatal(err)
	}
	if out["path"] != "runbooks/db-failover.md" {
		t.Fatalf("path: %v", out["path"])
	}
	text, _ := f.svc.TeamRead(ctx, "b", "runbooks/db-failover.md")
	if !strings.Contains(text, "steps:\n    - promote the replica\n    - repoint the app\n") && !strings.Contains(text, "steps:\n  - promote the replica\n  - repoint the app\n") {
		t.Fatalf("steps not a list:\n%s", text)
	}
	// Create only.
	if _, err := f.svc.OperatorNew(ctx, "platform", "", "op", "", NewDoc{Kind: KindRunbook, Title: "DB failover",
		Fields: map[string]string{"when_to_use": "x", "steps": "[x]", "verification": "x", "rollback": "x", "owner": "x"}, Base: f.head(t, "platform", "")}); err == nil {
		t.Fatal("new over an existing document")
	}
	// Missing fields: precise.
	if _, err := f.svc.OperatorNew(ctx, "platform", "", "op", "", NewDoc{Kind: KindRunbook, Title: "Half", Base: f.head(t, "platform", "")}); err == nil || !strings.Contains(err.Error(), "missing required field when_to_use") {
		t.Fatalf("a half runbook: %v", err)
	}
	// A free-path collection takes a nested path through put.
	if _, err := f.svc.OperatorEdit(ctx, "platform", "", "op", "", Edit{Op: EditPut, Path: "notes/2026/q4/retro.md", Text: "---\ntitle: Q4 retro\n---\nwhat went well", Base: f.head(t, "platform", "")}); err != nil {
		t.Fatalf("a free path: %v", err)
	}
	// A move into another kind's home is checked against that kind.
	if _, err := f.svc.OperatorEdit(ctx, "platform", "", "op", "", Edit{Op: EditMv, Path: "runbooks/db-failover.md", To: "knowledge/db-failover.md", Base: f.head(t, "platform", "")}); err == nil || !strings.Contains(err.Error(), "knowledge schema") {
		t.Fatalf("a runbook moved into knowledge/: %v", err)
	}
	if _, err := f.svc.OperatorEdit(ctx, "platform", "", "op", "", Edit{Op: EditMv, Path: "runbooks/db-failover.md", To: "runbooks/sub/db-failover.md", Base: f.head(t, "platform", "")}); err == nil {
		t.Fatal("a runbook moved to a free path")
	}
	// ls --kind.
	ls, err := f.svc.RepoFile(ctx, "platform", "", "", KindRunbook)
	if err != nil || len(ls["files"].([]any)) != 1 {
		t.Fatalf("ls --kind runbook: %v %v", ls, err)
	}
	// The kinds op describes them, collections included.
	ki, _ := kindsInfo(f.svc.TeamRepo("platform").Kinds(), "")
	var names []string
	for _, k := range ki["kinds"].([]any) {
		names = append(names, k.(map[string]any)["kind"].(string))
	}
	if strings.Join(names, ",") != "knowledge,runbook,watch,notes,postmortems" {
		t.Fatalf("kinds: %v", names)
	}
}

// MIGRATION: nothing is deleted; existing files are classified, legacy
// episodes are still read.
func TestInventoryAndLegacy(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	repo := f.svc.Repo("a")
	for p, body := range map[string]string{
		"knowledge/old-style.md":   "no frontmatter here",
		"episodes/2026/09/c1.md":   "an old episode about db-main",
		"scratch/notes.md":         "outside every home",
		"knowledge/good.md":        kd("fine"),
		"runbooks/Nested/thing.md": rd("x"),
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo.Dir, p)), 0o700)
		_ = os.WriteFile(filepath.Join(repo.Dir, p), []byte(body), 0o600)
	}
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	_, _ = repo.git(ctx, repo.Dir, "commit", "-q", "-m", "an old layout")
	got := map[string]string{}
	for _, d := range repo.Inventory() {
		got[d.Path] = d.Status
	}
	want := map[string]string{"knowledge/old-style.md": DocInvalid, "episodes/2026/09/c1.md": DocLegacy, "scratch/notes.md": DocUnmapped,
		"knowledge/good.md": DocOK, "runbooks/Nested/thing.md": DocInvalid}
	for p, st := range want {
		if got[p] != st {
			t.Errorf("%s: %q, want %q", p, got[p], st)
		}
	}
	for p := range want {
		if _, err := os.Stat(filepath.Join(repo.Dir, p)); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	if text, err := repo.Read("episodes/2026/09/c1.md"); err != nil || !strings.Contains(text, "db-main") {
		t.Fatalf("a legacy episode no longer reads: %q %v", text, err)
	}
	if hits := repo.Search("db-main", 10); len(hits) == 0 || hits[0]["path"] != "episodes/2026/09/c1.md" {
		t.Fatalf("legacy episodes are not searched: %v", hits)
	}
	// Rewriting an invalid old file must make it valid.
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Path: "knowledge/old-style.md", Text: "still no frontmatter"}); err == nil {
		t.Fatal("an invalid rewrite")
	}
	if _, _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), MemoryWrite{Path: "knowledge/old-style.md", Text: kd("now typed")}); err != nil {
		t.Fatal(err)
	}
	// The start-up report names them.
	f.audits = nil
	f.svc.reportInventory("bot a", repo)
	var rep map[string]any
	for _, a := range f.audits {
		if a["event"] == "bot_memory_inventory" {
			rep = a
		}
	}
	if rep == nil || !strings.Contains(strings.Join(rep["unmapped_or_invalid"].([]string), "\n"), "scratch/notes.md (unmapped") {
		t.Fatalf("inventory report: %v", rep)
	}
}
