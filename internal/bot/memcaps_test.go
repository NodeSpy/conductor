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

// CAPS: a document over its kind's cap is refused at the one write path —
// by any writer but conductor itself — with the exact size, the cap and the
// knob that loosens it; the knob loosens it.
func TestDocumentCapsOnWrite(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	hc := Actor{Bot: "sre", Role: config.BotRoleHome}
	at := know("Big", "", "", "") // the frontmatter alone
	fits := at + strings.Repeat("x", config.DefaultMaxBytes["knowledge"]-len(at)-1) + "\n"
	if len(fits) != config.DefaultMaxBytes["knowledge"] {
		t.Fatalf("fixture: %d", len(fits))
	}
	if _, _, err := f.svc.WriteMemory(ctx, hc, MemoryWrite{Kind: "knowledge", Slug: "fits", Text: fits}); err != nil {
		t.Fatalf("a document exactly at the cap: %v", err)
	}
	over := fits + "y"
	_, _, err := f.svc.WriteMemory(ctx, hc, MemoryWrite{Kind: "knowledge", Slug: "over", Text: over})
	want := fmt.Sprintf("knowledge/over.md: knowledge document is %d bytes, over its cap of %d bytes (memory.max_bytes.knowledge) — keep one topic per document", len(over)+1, config.DefaultMaxBytes["knowledge"])
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("over the cap: %v\nwant: %s", err, want)
	}
	if _, err := os.Stat(filepath.Join(f.svc.Repo("sre").Dir, "knowledge", "over.md")); err == nil {
		t.Fatal("a refused write left a file")
	}
	// Appending past the cap is refused the same way: the cap is on what the
	// file would hold.
	if _, _, err := f.svc.WriteMemory(ctx, hc, MemoryWrite{Path: "knowledge/fits.md", Text: "more", Append: true}); err == nil || !strings.Contains(err.Error(), "over its cap") {
		t.Fatalf("append past the cap: %v", err)
	}
	// Each built-in kind has its own default.
	for kind, doc := range map[string]string{
		"runbook": "---\ntitle: R\nwhen_to_use: w\nsteps: [a]\nverification: v\nrollback: r\nowner: o\n---\n",
		"watch":   "---\nwhat: W\nsignal: s\nthreshold: t\nwhy: y\n---\n",
		"digest":  "---\ntitle: D\nsummary: s\n---\n",
	} {
		big := doc + strings.Repeat("z", config.DefaultMaxBytes[kind])
		if _, _, err := f.svc.WriteMemory(ctx, hc, MemoryWrite{Kind: kind, Slug: "big-" + kind, Text: big}); err == nil ||
			!strings.Contains(err.Error(), fmt.Sprintf("cap of %d bytes (memory.max_bytes.%s)", config.DefaultMaxBytes[kind], kind)) {
			t.Errorf("%s over its cap: %v", kind, err)
		}
	}
	// The knob loosens it.
	m := f.cfg.Bots["sre"]
	m.Memory.MaxBytes = map[string]config.ByteSize{"knowledge": 16 << 10}
	f.cfg.Bots["sre"] = m
	if _, _, err := f.svc.WriteMemory(ctx, hc, MemoryWrite{Kind: "knowledge", Slug: "over", Text: over}); err != nil {
		t.Fatalf("loosened: %v", err)
	}
	if k, _ := f.svc.Repo("sre").Kinds().Get("knowledge"); k.Describe()["max_bytes"] != 16<<10 {
		t.Fatalf("kinds do not show the cap: %v", k.Describe()["max_bytes"])
	}
	// An operator's edit is held to it too.
	repo := f.svc.Repo("sre")
	huge := at + strings.Repeat("q", 17<<10)
	if _, _, err := repo.Commit(ctx, OperatorIdent("op"), Edit{Op: EditPut, Path: "knowledge/huge.md", Text: huge, Base: repo.Head(ctx)}); err == nil || !strings.Contains(err.Error(), "over its cap of 16384 bytes") {
		t.Fatalf("operator over the cap: %v", err)
	}
	// Conductor's own log entries are bounded where they are composed, not
	// refused here.
	if _, err := repo.AppendLog(ctx, "log/2026/10/x.md", map[string]any{"title": "x"}, strings.Repeat("l", 20<<10), "log", nil); err != nil {
		t.Fatalf("a system log write: %v", err)
	}
}

// A collection's cap: its default, its own max_bytes, named in the refusal.
func TestCollectionCap(t *testing.T) {
	ks := NewKinds(false, map[string]config.Collection{
		"notes":       {Dir: "notes", Writers: []string{"bots"}, Fields: map[string]config.CollectionField{"title": {Type: "string", Required: true}}},
		"postmortems": {Dir: "postmortems", Writers: []string{"bots"}, MaxBytes: 32 << 10, Fields: map[string]config.CollectionField{"title": {Type: "string", Required: true}}},
	})
	doc := "---\ntitle: t\n---\n" + strings.Repeat("n", config.DefaultCollectionMaxBytes)
	if _, err := ks.CheckWrite("notes/a.md", doc, WriterBot, ""); err == nil || !strings.Contains(err.Error(), "(collections.notes.max_bytes)") {
		t.Fatalf("a collection's default cap: %v", err)
	}
	if _, err := ks.CheckWrite("postmortems/a.md", doc, WriterBot, ""); err != nil {
		t.Fatalf("a collection's own cap: %v", err)
	}
	if _, err := ks.CheckWrite("postmortems/a.md", doc+strings.Repeat("n", 32<<10), WriterBot, ""); err == nil || !strings.Contains(err.Error(), "cap of 32768 bytes (collections.postmortems.max_bytes)") {
		t.Fatalf("over a collection's own cap: %v", err)
	}
}

// A team's knowledge repo: a promotion over its cap is refused when it is
// proposed, naming the team's knob.
func TestPromotionOverTeamCap(t *testing.T) {
	f := newTeamFixture(t, func(c *config.Config) {
		tm := c.Teams["platform"]
		tm.Knowledge.MaxBytes = map[string]config.ByteSize{"knowledge": 2048}
		c.Teams["platform"] = tm
	})
	ctx := context.Background()
	hb := f.homeOf("b")
	text := kd(strings.Repeat("a long fact ", 300))
	if _, _, err := f.svc.WriteMemory(ctx, hb, MemoryWrite{Path: "knowledge/long.md", Text: text}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Promote(ctx, hb, "knowledge/long.md", "", "")
	if err == nil || !strings.Contains(err.Error(), "over its cap of 2048 bytes (knowledge.max_bytes.knowledge)") {
		t.Fatalf("promotion over the team cap: %v", err)
	}
}

// THE HANDOFF CAP: a session's note over it is refused with the exact
// limit, so it rewrites it shorter; conductor's own note is cut to it; and
// a prompt never carries more than the cap of one an operator edited.
func TestHandoffCap(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	hc := homeCaller(f)
	max := config.DefaultMaxBytes["handoff"]
	long := strings.Repeat("h", max+1)
	_, err := f.svc.Handoff(ctx, hc, long)
	want := fmt.Sprintf("handoff is %d bytes, over its cap of %d bytes (memory.max_bytes.handoff) — rewrite it shorter", max+1, max)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("an oversized handoff: %v", err)
	}
	if _, err := f.svc.Handoff(ctx, hc, strings.Repeat("h", max)); err != nil {
		t.Fatalf("a handoff at the cap: %v", err)
	}
	m := f.cfg.Bots["sre"]
	m.Memory.MaxBytes = map[string]config.ByteSize{"handoff": config.ByteSize(max * 2)}
	f.cfg.Bots["sre"] = m
	if _, err := f.svc.Handoff(ctx, hc, long); err != nil {
		t.Fatalf("a loosened handoff cap: %v", err)
	}
	// In a prompt: the body, at most the cap plus its heading's room.
	note := "---\nsession: s1\n---\n# Handoff\n\n" + strings.Repeat("ü", 4000)
	got := capHandoff(note, "handoff/x.md", 1000)
	if strings.Contains(got, "session: s1") || len(got) > 1000+256+120 || !strings.Contains(got, "truncated at") || !strings.Contains(got, "conductor bot read handoff/x.md") {
		t.Fatalf("capped note (%d bytes): %q", len(got), got[len(got)-150:])
	}
	if capHandoff("short", "p", 1000) != "short" {
		t.Fatal("a short note was changed")
	}
}

// THE EPISODE CAP: one `conductor bot episode` entry.
func TestEpisodeCap(t *testing.T) {
	f := newSvc(t)
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	hc := homeCaller(f)
	cs, err := f.svc.CaseOpen(hc, "a case", "")
	if err != nil {
		t.Fatal(err)
	}
	max := config.DefaultMaxBytes["episode"]
	if _, err := f.svc.Episode(ctx, hc, cs.ID, strings.Repeat("e", max+1)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("episode entry is %d bytes, over its cap of %d bytes (memory.max_bytes.episode)", max+1, max)) {
		t.Fatalf("an oversized episode: %v", err)
	}
	if _, err := f.svc.Episode(ctx, hc, cs.ID, strings.Repeat("e", max)); err != nil {
		t.Fatalf("an episode at the cap: %v", err)
	}
}

// A seed document over its kind's cap is skipped and reported.
func TestSeedOverCapSkipped(t *testing.T) {
	seed := t.TempDir()
	_ = os.MkdirAll(filepath.Join(seed, "knowledge"), 0o700)
	_ = os.WriteFile(filepath.Join(seed, "knowledge", "big.md"), []byte(kd(strings.Repeat("b", 9000))), 0o600)
	_ = os.WriteFile(filepath.Join(seed, "knowledge", "ok.md"), []byte(kd("fine")), 0o600)
	r := &Repo{Dir: filepath.Join(t.TempDir(), "mem"), Bot: "x", Seed: seed}
	if err := r.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.SeedSkipped) != 1 || !strings.Contains(r.SeedSkipped[0], "knowledge/big.md: knowledge document is") {
		t.Fatalf("skipped: %v", r.SeedSkipped)
	}
	if _, err := r.Read("knowledge/ok.md"); err != nil {
		t.Fatal("the valid seed document was not copied")
	}
}
