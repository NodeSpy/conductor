package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// know is a knowledge document with a title, scope, summary and body.
func know(title, applies, summary, body string) string {
	s := "---\ntitle: " + title + "\n"
	if applies != "" {
		s += "applies_to: [" + applies + "]\n"
	}
	s += "evidence: seen in tests\nconfidence: high\n"
	if summary != "" {
		s += "summary: " + summary + "\n"
	}
	return s + "---\n" + body
}

func writeDoc(t *testing.T, f *svcFixture, kind, slug, text string) string {
	t.Helper()
	p, _, err := f.svc.WriteMemory(context.Background(), Actor{Bot: "sre", Role: config.BotRoleHome}, MemoryWrite{Kind: kind, Slug: slug, Text: text})
	if err != nil {
		t.Fatalf("write %s: %v", slug, err)
	}
	return p
}

func paths(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

// RANKING: a word in the title outranks the same word in scope, which
// outranks it in the summary, which outranks it in the body — however
// often the body repeats it; all the words outrank some of them.
func TestRecallRanksFrontmatterAboveBody(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	filler := strings.Repeat("Unrelated prose about the weather and lunch. ", 20)
	writeDoc(t, f, "knowledge", "body", know("Cache notes", "", "", strings.Repeat("replication replication ", 2)+filler))
	writeDoc(t, f, "knowledge", "summary", know("Storage notes", "", "How replication works here.", filler))
	writeDoc(t, f, "knowledge", "scope", know("Queue notes", "replication", "", filler))
	writeDoc(t, f, "knowledge", "title", know("Replication lag", "", "", filler))
	writeDoc(t, f, "knowledge", "noise", know("Lunch", "", "", filler))
	hits, err := repo.Recall(ctx, "replication", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"knowledge/title.md", "knowledge/scope.md", "knowledge/summary.md", "knowledge/body.md"}
	if strings.Join(paths(hits), ",") != strings.Join(want, ",") {
		t.Fatalf("ranking: %v, want %v", paths(hits), want)
	}
	if hits[0].Title != "Replication lag" || hits[0].Kind != "knowledge" || !strings.Contains(strings.ToLower(hits[0].Snippet), "replication") {
		t.Fatalf("hit: %+v", hits[0])
	}
	// Every word beats some of them, even a title match on one word.
	writeDoc(t, f, "knowledge", "both", know("Primary database", "", "", "the failover of db-main"))
	writeDoc(t, f, "knowledge", "one", know("Failover", "", "", filler))
	hits, _ = repo.Recall(ctx, "database failover", 10, "")
	if len(hits) < 2 || hits[0].Path != "knowledge/both.md" || hits[1].Path != "knowledge/one.md" {
		t.Fatalf("all-words first: %v", paths(hits))
	}
	// Stemming: "replicas" finds "replication"? No — but "replicated" does.
	if hits, _ := repo.Recall(ctx, "replicated", 10, ""); len(hits) == 0 {
		t.Fatal("no stemming")
	}
	// The limit and the kind filter.
	if hits, _ := repo.Recall(ctx, "replication", 2, ""); len(hits) != 2 {
		t.Fatalf("limit: %d", len(hits))
	}
	writeDoc(t, f, "runbook", "replication-repair", "---\ntitle: Repair replication\nwhen_to_use: lag\nsteps: [a]\nverification: v\nrollback: r\nowner: o\n---\n")
	if hits, _ := repo.Recall(ctx, "replication", 10, "runbook"); len(hits) != 1 || hits[0].Path != "runbooks/replication-repair.md" {
		t.Fatalf("kind filter: %v", paths(hits))
	}
}

// A query is words, never FTS syntax: operators, quotes and parentheses
// are only text.
func TestRecallQueryIsOnlyWords(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	writeDoc(t, f, "knowledge", "near", know("NEAR the edge", "", "", "OR and AND are words here"))
	for _, q := range []string{`"unbalanced`, `NEAR(`, `title:near`, `* OR`, `a AND NOT b`, `x"); DROP TABLE docs; --`} {
		if _, err := f.svc.Repo("sre").Recall(ctx, q, 5, ""); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	if hits, _ := f.svc.Repo("sre").Recall(ctx, `title:near`, 5, ""); len(hits) != 1 {
		t.Fatalf("words of a column filter: %v", paths(hits))
	}
	if _, err := f.svc.Repo("sre").Recall(ctx, "  ¿?  ", 5, ""); err == nil {
		t.Fatal("an empty query")
	}
}

// THE INDEX FOLLOWS THE REPO: every commit conductor makes is indexed
// incrementally (no rebuild); an operator's commit, one made by hand, and a
// delete are caught up on the next query; a corrupt index file or a change
// of kinds rebuilds it.
func TestIndexFollowsCommits(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	repo.IndexPath = filepath.Join(t.TempDir(), "idx", "recall.db")
	writeDoc(t, f, "knowledge", "alpha", know("Alpha service", "", "", "runs on port 8080"))
	if hits, _ := repo.Recall(ctx, "alpha", 5, ""); len(hits) != 1 {
		t.Fatalf("first write: %v", paths(hits))
	}
	base := repo.rebuilds
	// A modification replaces what was indexed.
	writeDoc(t, f, "knowledge", "alpha", know("Alpha service", "", "", "moved to port 9090"))
	if hits, _ := repo.Recall(ctx, "8080", 5, ""); len(hits) != 0 {
		t.Fatalf("stale content still indexed: %v", paths(hits))
	}
	if hits, _ := repo.Recall(ctx, "9090", 5, ""); len(hits) != 1 {
		t.Fatal("new content not indexed")
	}
	// An operator's rm, against the head.
	head := repo.Head(ctx)
	if _, _, err := repo.Commit(ctx, OperatorIdent("op"), Edit{Op: EditRm, Path: "knowledge/alpha.md", Base: head}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := repo.Recall(ctx, "alpha", 5, ""); len(hits) != 0 {
		t.Fatalf("a deleted document still recalled: %v", paths(hits))
	}
	// A commit made by hand, outside conductor, is caught up on the next query.
	_ = os.WriteFile(filepath.Join(repo.Dir, "knowledge", "beta.md"), []byte(know("Beta", "", "", "hand written")), 0o600)
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	_, _ = repo.git(ctx, repo.Dir, "commit", "-q", "-m", "by hand")
	if hits, _ := repo.Recall(ctx, "beta", 5, ""); len(hits) != 1 {
		t.Fatalf("a hand commit: %v", paths(hits))
	}
	if repo.rebuilds != base {
		t.Fatalf("%d rebuilds for commits that only needed catching up", repo.rebuilds-base)
	}
	// History rewritten under the index (a reset): rebuilt, and right.
	_, _ = repo.git(ctx, repo.Dir, "reset", "-q", "--hard", "HEAD~1")
	if hits, _ := repo.Recall(ctx, "beta", 5, ""); len(hits) != 0 {
		t.Fatalf("a reset-away document: %v", paths(hits))
	}
	if repo.rebuilds != base+1 {
		t.Fatalf("a rewritten history: %d rebuilds", repo.rebuilds-base)
	}
	// A corrupt index file is rebuilt from the repo.
	repo.idx.Close()
	repo.idx = nil
	_ = os.WriteFile(repo.IndexPath, []byte("not a database"), 0o600)
	writeDoc(t, f, "knowledge", "gamma", know("Gamma", "", "", "after the corruption"))
	if hits, err := repo.Recall(ctx, "gamma", 5, ""); err != nil || len(hits) != 1 {
		t.Fatalf("after corruption: %v %v", paths(hits), err)
	}
	// A new collection (the kinds changed) rebuilds it.
	before := repo.rebuilds
	m := f.cfg.Bots["sre"]
	m.Collections = map[string]config.Collection{"notes": {Dir: "notes", Writers: []string{"bots"}, Fields: map[string]config.CollectionField{"title": {Type: "string", Required: true}}}}
	f.cfg.Bots["sre"] = m
	if _, err := repo.Recall(ctx, "gamma", 5, ""); err != nil || repo.rebuilds != before+1 {
		t.Fatalf("kinds changed: %d rebuilds, %v", repo.rebuilds-before, err)
	}
	// The index lives where it was put — never in the repo.
	if _, err := os.Stat(repo.IndexPath); err != nil {
		t.Fatal("no index file")
	}
	if out, _ := repo.git(ctx, repo.Dir, "status", "--porcelain", "--ignored"); strings.TrimSpace(out) != "" {
		t.Fatalf("the index touched the repo: %q", out)
	}
}

// The default index lives in conductor's state dir, per bot.
func TestIndexPathInStateDir(t *testing.T) {
	cfg := &config.Config{}
	if p := cfg.BotIndexPath("sre"); !strings.HasPrefix(p, config.StateDir()) || !strings.HasSuffix(p, filepath.Join("bots", "sre", "recall.db")) {
		t.Fatalf("bot index path %s", p)
	}
	if p := cfg.TeamIndexPath("platform"); !strings.HasSuffix(p, filepath.Join("teams", "platform", "recall.db")) {
		t.Fatalf("team index path %s", p)
	}
	f := newSvc(t)
	if f.svc.Repo("sre").IndexPath != f.cfg.BotIndexPath("sre") {
		t.Fatal("a bot's repo does not use its state-dir index")
	}
}

// docFor: the title falls back to what (a watch), then the first heading;
// the summary to the body's first sentence; scope gathers where it applies.
func TestDocForFields(t *testing.T) {
	ks := NewKinds(false, nil)
	k, _ := ks.Get("watch")
	d := docFor(k, "watch/disk.md", "---\nwhat: disk usage\nsignal: df\nthreshold: 90%\nwhy: outages\n---\n# Notes\n\nThe **root** volume fills first. Then the rest.\n")
	if d.Title != "disk usage" || d.Summary != "The root volume fills first." || !strings.Contains(d.Fields, "90%") {
		t.Fatalf("watch: %+v", d)
	}
	r, _ := ks.Get("runbook")
	d = docFor(r, "runbooks/x.md", "---\ntitle: X\nwhen_to_use: the primary is down\nsteps: [a, b]\nverification: v\nrollback: r\nowner: o\nsummary: Fail over.\n---\nbody\n")
	if d.Scope != "the primary is down" || d.Summary != "Fail over." || !strings.Contains(d.Fields, "a, b") {
		t.Fatalf("runbook: %+v", d)
	}
	l, _ := ks.Get("log")
	d = docFor(l, "log/x.md", "no frontmatter at all\n## heading\n")
	if d.Title != "heading" || d.Body == "" {
		t.Fatalf("invalid document: %+v", d)
	}
	if s := firstSentence("## Heading\n\n- first item. second\n"); s != "first item." {
		t.Fatalf("first sentence: %q", s)
	}
}

// RENDERING: within budget whatever the repo holds; relevant entries
// first; the rest newest first; what is left out is counted, by kind, with
// where to find it.
func TestRenderIndexBudget(t *testing.T) {
	var entries []IndexEntry
	for i := 0; i < 300; i++ {
		kind := []string{"knowledge", "runbook", "watch"}[i%3]
		entries = append(entries, IndexEntry{Path: fmt.Sprintf("%s/doc-%03d.md", kind, i), Kind: kind, Title: fmt.Sprintf("Document %d", i),
			Summary: strings.Repeat("words ", 10), Updated: int64(1000 - i)})
	}
	st := IndexStats{Logs: 40, Archived: 900}
	for _, budget := range []int{50, 400, 2000, 6000} {
		out := renderIndex(entries, st, nil, IndexView{Budget: budget, Recall: "conductor bot recall", IndexCmd: "conductor bot index"})
		if len(out) > budget {
			t.Fatalf("budget %d: rendered %d bytes", budget, len(out))
		}
		if budget >= 2000 {
			shown := strings.Count(out, "/doc-")
			if !strings.Contains(out, fmt.Sprintf("… %d more not shown", 300-shown)) || !strings.Contains(out, "conductor bot recall <words>") {
				t.Fatalf("budget %d: footer missing or wrong (%d shown):\n%s", budget, shown, out)
			}
			if !strings.Contains(out, "40 raw case logs") || !strings.Contains(out, "900 archived") {
				t.Fatalf("log counts missing:\n%s", out)
			}
			if !strings.Contains(out, "doc-000.md") || strings.Index(out, "doc-000.md") > strings.Index(out, "doc-001.md") {
				t.Fatal("not newest first")
			}
		}
	}
	// A ranked entry goes first, however old.
	out := renderIndex(entries, st, []string{"watch/doc-299.md"}, IndexView{Budget: 1000})
	if !strings.Contains(out, "doc-299.md") || strings.Index(out, "doc-299.md") > strings.Index(out, "doc-000.md") {
		t.Fatalf("ranked entry not first:\n%s", out)
	}
	// Everything fits: no footer.
	small := renderIndex(entries[:3], IndexStats{}, nil, IndexView{Budget: 6000})
	if strings.Contains(small, "more not shown") || strings.Count(small, "\n- ") != 3 {
		t.Fatalf("small index:\n%s", small)
	}
	// The omitted counts are by kind and add up.
	out = renderIndex(entries, IndexStats{}, nil, IndexView{Budget: 1500})
	shown := strings.Count(out, "- knowledge/") + strings.Count(out, "- runbook/") + strings.Count(out, "- watch/")
	rest := entries[shown:]
	c := map[string]int{}
	for _, e := range rest {
		c[e.Kind]++
	}
	if !strings.Contains(out, fmt.Sprintf("(%d %s, %d %s, %d %s)", c[rest[0].Kind], rest[0].Kind, c[rest[1].Kind], rest[1].Kind, c[rest[2].Kind], rest[2].Kind)) {
		t.Fatalf("by-kind counts (%v):\n%s", c, out)
	}
	if renderIndex(nil, IndexStats{}, nil, IndexView{Budget: 500}) == "" || !strings.Contains(renderIndex(nil, IndexStats{}, nil, IndexView{Budget: 500}), "nothing remembered yet") {
		t.Fatal("empty index")
	}
}

// READ IN PARTS: one section, an offset, a cut that says so, the
// sections named; an unknown section is an error that lists them.
func TestReadPart(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	body := "Intro line.\n\n## Symptoms\n\n502s from the edge.\n\n### Detail\n\nmore\n\n## Fix\n\n```sh\n# not a heading\nrestart\n```\n\n## Notes\n\n" + strings.Repeat("filler line of notes\n", 800)
	m := f.cfg.Bots["sre"]
	m.Memory.MaxBytes = map[string]config.ByteSize{"knowledge": 64 << 10}
	f.cfg.Bots["sre"] = m
	p := writeDoc(t, f, "knowledge", "edge", know("Edge 502s", "", "", body))
	res, err := repo.ReadPart(ctx, p, "symptoms", 0, 0)
	if err != nil || res.Section != "Symptoms" || !strings.Contains(res.Text, "502s") || !strings.Contains(res.Text, "### Detail") || strings.Contains(res.Text, "## Fix") || res.Truncated {
		t.Fatalf("section: %+v %v", res, err)
	}
	if res, _ := repo.ReadPart(ctx, p, "## Fix", 0, 0); !strings.Contains(res.Text, "# not a heading") || strings.Contains(res.Text, "Notes") {
		t.Fatalf("a fenced comment ended the section: %q", res.Text)
	}
	if _, err := repo.ReadPart(ctx, p, "Causes", 0, 0); err == nil || !strings.Contains(err.Error(), "Symptoms | Detail | Fix | Notes") {
		t.Fatalf("unknown section: %v", err)
	}
	// The whole document, cut at the cap: it says so, where to go on, and
	// the sections.
	res, err = repo.ReadPart(ctx, p, "", 0, 1000)
	if err != nil || !res.Truncated || len(res.Text) > 1000 || res.Next != len(res.Text) || !strings.Contains(res.Note, "TRUNCATED") ||
		!strings.Contains(res.Note, fmt.Sprintf("--offset %d", res.Next)) || len(res.Sections) != 4 || res.Bytes <= 1000 {
		t.Fatalf("truncated read: %+v %v", res, err)
	}
	if !strings.HasSuffix(res.Text, "\n") {
		t.Fatal("a cut that could end on a line did not")
	}
	// Reading on from the offset continues exactly, and ends untruncated.
	all, next := res.Text, res.Next
	for i := 0; i < 20 && next > 0; i++ {
		r2, err := repo.ReadPart(ctx, p, "", next, 1000)
		if err != nil {
			t.Fatal(err)
		}
		all += r2.Text
		next = r2.Next
	}
	whole, _ := repo.Read(p)
	if all != whole {
		t.Fatal("reading on from the offsets does not reassemble the document")
	}
	// The default cap is the repo's read_max.
	if res, _ := repo.ReadPart(ctx, p, "", 0, 0); len(res.Text) > config.DefaultReadMax || !res.Truncated {
		t.Fatalf("default cap: %d bytes", len(res.Text))
	}
	m.Memory.ReadMax = 100000
	f.cfg.Bots["sre"] = m
	if res, _ := repo.ReadPart(ctx, p, "", 0, 0); res.Truncated {
		t.Fatal("read_max does not loosen the cap")
	}
	if _, err := repo.ReadPart(ctx, p, "", len(whole)+1, 0); err == nil {
		t.Fatal("an offset past the end")
	}
	if _, err := repo.ReadPart(ctx, p, "", -1, 0); err == nil {
		t.Fatal("a negative offset")
	}
	// Multi-byte text is never cut inside a rune.
	p2 := writeDoc(t, f, "knowledge", "utf", know("Ünïcode", "", "", strings.Repeat("ü", 3000)))
	if res, _ := repo.ReadPart(ctx, p2, "", 0, 1001); !strings.HasSuffix(res.Text, "ü") {
		t.Fatal("cut inside a rune")
	}
}

// The agent op: recall, read and index reach a session as their results,
// and the team's knowledge the same way.
func TestMemoryAgentOps(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	hc := homeCaller(f)
	writeDoc(t, f, "knowledge", "db", know("DB primary", "db-main", "The primary is db-1.", "## Where\n\nus-east\n\n## Why\n\nlatency\n"))
	out, err := f.svc.AgentOp(ctx, hc, "recall", map[string]any{"query": "primary", "limit": "3"})
	if err != nil || out["count"] != 1 {
		t.Fatalf("recall: %v %v", out, err)
	}
	if _, err := f.svc.AgentOp(ctx, hc, "recall", map[string]any{"query": "primary", "limit": "99"}); err == nil {
		t.Fatal("a limit past the maximum")
	}
	if _, err := f.svc.AgentOp(ctx, hc, "recall", map[string]any{"query": "primary", "kind": "nope"}); err == nil {
		t.Fatal("an unknown kind")
	}
	out, err = f.svc.AgentOp(ctx, hc, "read", map[string]any{"path": "knowledge/db.md", "section": "Why"})
	if err != nil || out["text"] != "## Why\n\nlatency\n" {
		t.Fatalf("read section: %v %v", out, err)
	}
	out, err = f.svc.AgentOp(ctx, hc, "index", map[string]any{})
	if err != nil || !strings.Contains(out["text"].(string), "knowledge/db.md · knowledge · DB primary · for: db-main · The primary is db-1.") {
		t.Fatalf("index: %v %v", out, err)
	}
	out, err = f.svc.AgentOp(ctx, hc, "index", map[string]any{"kind": "knowledge"})
	if err != nil || out["entries"] != 1 {
		t.Fatalf("index --kind: %v %v", out, err)
	}
}
