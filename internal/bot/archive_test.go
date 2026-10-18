package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

// consolidateNow starts a consolidation and returns its worker (the ledger's
// copy, with what it was given).
func consolidateNow(t *testing.T, f *svcFixture) store.Worker {
	t.Helper()
	w, err := f.svc.Consolidate(context.Background(), "sre")
	if err != nil || w.ID == "" {
		t.Fatalf("consolidate: %+v %v", w, err)
	}
	return *f.st.Bot("sre").Workers[w.ID]
}

// writeDigest is the consolidation worker committing its digest.
func writeDigest(t *testing.T, f *svcFixture, w store.Worker) {
	t.Helper()
	wc := Actor{Bot: "sre", Role: config.BotRoleWorker, CaseID: w.CaseID, Dispatch: w.Dispatch}
	if _, _, err := f.svc.WriteMemory(context.Background(), wc, MemoryWrite{Kind: "digest", Slug: "episodes-" + w.ID,
		Text: "---\ntitle: Episodes " + w.ID + "\nsummary: the db failed over twice\ncases: [c1]\n---\nThe primary failed over; replication caught up.\n"}); err != nil {
		t.Fatal(err)
	}
}

// THE ARCHIVE'S INVARIANT: a log entry leaves the working copy only when a
// consolidation that FINISHED DONE, having written a digest, covered
// exactly its current content, and its case is closed. Everything else
// stays: what a failed or digest-less consolidation covered, what was
// appended since, an open case's log. What is archived is still recalled
// and read.
func TestArchiveOnlyConsolidatedEntries(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	hc := homeCaller(f)
	repo := f.svc.Repo("sre")
	var cases []store.Case
	for i, title := range []string{"db failover", "cache eviction storm", "disk full on web-3"} {
		cs, err := f.svc.CaseOpen(hc, title, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Episode(ctx, hc, cs.ID, fmt.Sprintf("investigated %s; zanzibar-%d", title, i)); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, cs)
	}
	logOf := func(cs store.Case) string { return repo.EpisodePath(cs.ID, cs.Title, cs.Created) }
	for _, cs := range cases[:2] {
		if err := f.svc.CaseClose(ctx, hc, cs.ID, "fixed"); err != nil {
			t.Fatal(err)
		}
	}
	// 1. A consolidation is given the unconsolidated entries, oldest first.
	w1 := consolidateNow(t, f)
	if len(w1.Consolidates) != 3 || w1.Consolidates[logOf(cases[0])] == "" {
		t.Fatalf("given: %v", w1.Consolidates)
	}
	if !strings.Contains(w1.Brief, "--kind digest") {
		t.Fatalf("brief:\n%s", w1.Brief)
	}
	// Oldest first: the brief lists them in the order of their last commits.
	logs, _ := repo.LogDocs(ctx)
	last := -1
	for _, e := range logs {
		i := strings.Index(w1.Brief, e.Path)
		if i < last {
			t.Fatalf("brief not oldest first (%s):\n%s", e.Path, w1.Brief)
		}
		last = i
	}
	// A second consolidation while it runs takes nothing it was given.
	if w, err := f.svc.Consolidate(ctx, "sre"); err != nil || w.ID != "" {
		t.Fatalf("an overlapping consolidation: %+v %v", w, err)
	}
	// 2. It finishes done WITHOUT a digest: nothing is consolidated, nothing
	// archived.
	f.svc.WorkerDone(ctx, w1.Dispatch, "", map[string]any{"summary": "did nothing"})
	if n := len(f.st.Bot("sre").ConsolidatedDocs); n != 0 {
		t.Fatalf("a digest-less consolidation consolidated %d entries", n)
	}
	for _, cs := range cases {
		if !exists(repo.Dir, logOf(cs)) {
			t.Fatalf("%s archived without a digest", logOf(cs))
		}
	}
	// 3. A consolidation that fails: nothing either.
	w2 := consolidateNow(t, f)
	writeDigest(t, f, w2)
	f.svc.OnRunEvent(ctx, "run_done", w2.HistoryID, "failed", "crashed")
	if n := len(f.st.Bot("sre").ConsolidatedDocs); n != 0 {
		t.Fatalf("a failed consolidation consolidated %d entries", n)
	}
	// 4. One that finishes done with a digest — but case 0's log changed
	// after it was given: that one stays, with case 2's (open). Case 1's
	// leaves the working copy, in one commit.
	w3 := consolidateNow(t, f)
	if _, err := f.svc.Episode(ctx, hc, cases[0].ID, "a late note after the consolidation started"); err != nil {
		t.Fatal(err)
	}
	writeDigest(t, f, w3)
	f.svc.WorkerDone(ctx, w3.Dispatch, "", map[string]any{"summary": "digest written"})
	if exists(repo.Dir, logOf(cases[1])) {
		t.Fatal("the consolidated, closed, unchanged entry was not archived")
	}
	if !exists(repo.Dir, logOf(cases[0])) {
		t.Fatal("an entry appended after it was consolidated was archived")
	}
	if !exists(repo.Dir, logOf(cases[2])) {
		t.Fatal("an open case's log was archived")
	}
	if home := f.st.Bot("sre").Home; exists(home.Cwd, logOf(cases[1])) || !exists(home.Cwd, logOf(cases[0])) {
		t.Fatal("the session's working copy does not follow the archive")
	}
	if subj := repo.Log(ctx, 1); len(subj) != 1 || !strings.Contains(subj[0], "memory: archive 1 consolidated log entries") {
		t.Fatalf("archive commit: %v", subj)
	}
	rec := f.st.Bot("sre")
	if _, ok := rec.ConsolidatedDocs[logOf(cases[1])]; ok {
		t.Fatal("an archived entry stayed in the consolidated map")
	}
	if rec.ConsolidatedDocs[logOf(cases[2])] == "" {
		t.Fatal("the open case's consolidated entry was forgotten")
	}
	// 5. Archived, it is still recalled — marked — and read back.
	hits, err := repo.Recall(ctx, "zanzibar-1", 5, "")
	if err != nil || len(hits) == 0 || hits[0].Path != logOf(cases[1]) || hits[0].Archived == "" {
		t.Fatalf("archived recall: %+v %v", hits, err)
	}
	res, err := repo.ReadPart(ctx, logOf(cases[1]), "", 0, 0)
	if err != nil || !strings.Contains(res.Text, "zanzibar-1") || res.Archived == "" || !strings.Contains(res.Note, "Archived log entry") {
		t.Fatalf("archived read: %+v %v", res, err)
	}
	// ... after a rebuild from nothing, too.
	rebuilds := repo.rebuilds
	dropIndex(t, repo)
	if hits, err := repo.Recall(ctx, "zanzibar-1", 5, ""); err != nil || len(hits) == 0 || hits[0].Path != logOf(cases[1]) || hits[0].Archived == "" {
		t.Fatalf("archived recall after a rebuild: %+v %v", hits, err)
	}
	if repo.rebuilds != rebuilds+1 {
		t.Fatal("the index was not rebuilt")
	}
	// The digest is what the index shows; the raw logs are counted.
	out, _ := repo.RenderIndex(ctx, IndexView{Budget: 6000})
	if !strings.Contains(out, "digests/episodes-"+w3.ID+".md · digest") || !strings.Contains(out, "1 archived after consolidation") {
		t.Fatalf("index:\n%s", out)
	}
	// 6. The next consolidation takes what changed (case 0) and the
	// consolidations' own logs — not what is consolidated and unchanged
	// (case 2), not what is archived (case 1).
	w4 := consolidateNow(t, f)
	if w4.Consolidates[logOf(cases[0])] == "" || w4.Consolidates[logOf(cases[2])] != "" || w4.Consolidates[logOf(cases[1])] != "" {
		t.Fatalf("next round given: %v", w4.Consolidates)
	}
	c3 := f.st.Bot("sre").Cases[w3.CaseID]
	if own := repo.EpisodePath(c3.ID, c3.Title, c3.Created); !exists(repo.Dir, own) || w4.Consolidates[own] != "" {
		t.Fatalf("the successful consolidation's own log came back as new: %v", keys(w4.Consolidates))
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A consolidation takes at most a batch, oldest first, and says how many
// wait; with nothing new it starts nothing.
func TestConsolidationBatch(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	if w, err := f.svc.Consolidate(ctx, "sre"); err != nil || w.ID != "" {
		t.Fatalf("nothing to consolidate: %+v %v", w, err)
	}
	var all []string
	for i := 0; i < consolidateBatch+2; i++ {
		p := fmt.Sprintf("log/2026/10/2026-10-%02d-c%d-entry.md", 1+i%28, i)
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo.Dir, p)), 0o700)
		_ = os.WriteFile(filepath.Join(repo.Dir, p), []byte(fmt.Sprintf("---\ntitle: entry %d\ncase: c%d\n---\nbody\n", i, i)), 0o600)
		all = append(all, p)
	}
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	_, _ = repo.git(ctx, repo.Dir, "commit", "-q", "-m", "old logs")
	sort.Strings(all)
	w := consolidateNow(t, f)
	if len(w.Consolidates) != consolidateBatch || !strings.Contains(w.Brief, "(2 more wait for the next consolidation)") {
		t.Fatalf("batch: %d given; brief %q", len(w.Consolidates), w.Brief[:200])
	}
	for _, p := range all[:consolidateBatch] {
		if w.Consolidates[p] == "" {
			t.Fatalf("not oldest first: %s missing", p)
		}
	}
}

// archivable: only consolidated entries, at their blob, whose case is not
// open; never today's sweep log; nothing missing.
func TestArchivableSelection(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	files := map[string]string{
		"log/2026/10/2026-10-01-c1-closed.md":    "---\ntitle: a\ncase: c1\n---\nx\n",
		"log/2026/10/2026-10-01-c2-open.md":      "---\ntitle: b\ncase: c2\n---\nx\n",
		"log/2026/10/2026-10-01-c9-unknown.md":   "---\ntitle: c\ncase: c9\n---\nx\n",
		"log/proactive/2026/10/2026-10-17.md":    "---\ntitle: today\nsweep: s1\n---\nx\n",
		"log/proactive/2026/10/2026-10-16.md":    "---\ntitle: yesterday\nsweep: s0\n---\nx\n",
		"episodes/2026/09/2026-09-01-legacy.md":  "an old episode\n",
		"log/2026/10/2026-10-02-c3-not-consd.md": "---\ntitle: d\ncase: c3\n---\nx\n",
	}
	for p, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo.Dir, p)), 0o700)
		_ = os.WriteFile(filepath.Join(repo.Dir, p), []byte(body), 0o600)
	}
	_, _ = repo.git(ctx, repo.Dir, "add", "-A")
	_, _ = repo.git(ctx, repo.Dir, "commit", "-q", "-m", "logs")
	blobs, _ := repo.logBlobs(ctx)
	rec := store.BotRecord{Cases: map[string]*store.Case{"c1": {ID: "c1", Status: "closed"}, "c2": {ID: "c2", Status: "open"}}, ConsolidatedDocs: map[string]string{}}
	for p := range files {
		if !strings.Contains(p, "not-consd") {
			rec.ConsolidatedDocs[p] = blobs[p]
		}
	}
	rec.ConsolidatedDocs["log/2026/10/gone.md"] = "deadbeef"
	got := keys(archivable(rec, repo, now))
	want := []string{"episodes/2026/09/2026-09-01-legacy.md", "log/2026/10/2026-10-01-c1-closed.md", "log/2026/10/2026-10-01-c9-unknown.md", "log/proactive/2026/10/2026-10-16.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("archivable:\n got %v\nwant %v", got, want)
	}
	// ArchiveIf re-checks each blob under the lock: a stale one stays.
	sel := archivable(rec, repo, now)
	sel["log/2026/10/2026-10-01-c1-closed.md"] = "0000000000000000000000000000000000000000"
	removed, err := repo.ArchiveIf(ctx, sel, nil)
	if err != nil || len(removed) != 3 || !exists(repo.Dir, "log/2026/10/2026-10-01-c1-closed.md") {
		t.Fatalf("ArchiveIf: %v %v", removed, err)
	}
	if removed, err := repo.ArchiveIf(ctx, map[string]string{}, nil); err != nil || removed != nil {
		t.Fatal("an empty archive made a commit")
	}
	if repo.wroteDigest(ctx, "") {
		t.Fatal("no dispatch, no digest")
	}
	// archive: false keeps everything in place.
	m := f.cfg.Bots["sre"]
	off := false
	m.Memory.Archive = &off
	f.cfg.Bots["sre"] = m
	if err := f.svc.Ledger.UpdateBot("sre", func(r *store.BotRecord) error {
		r.ConsolidatedDocs = map[string]string{"log/2026/10/2026-10-01-c1-closed.md": blobs["log/2026/10/2026-10-01-c1-closed.md"]}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if p, err := f.svc.Archive(ctx, "sre"); err != nil || len(p) != 0 {
		t.Fatalf("archive: false archived %v %v", p, err)
	}
}
