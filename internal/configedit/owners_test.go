package configedit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Staged sets are per owner: another owner's edits never show in, ride into or
// get applied with this owner's review, and a token is the minting owner's.
func TestStagedSetsArePerOwner(t *testing.T) {
	p, path, _ := newPipe(t, cfgSample, nil)
	a, b := p.As("session-a"), p.As("session-b")
	if _, err := a.Stage(Change{Path: []string{"settings", "greeting"}, Value: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Stage(Change{Path: []string{"sandbox", "agents"}, Value: "off"}); err != nil {
		t.Fatal(err)
	}
	if len(a.Staged()) != 1 || a.Staged()[0].Path[0] != "settings" || len(p.Staged()) != 0 {
		t.Fatalf("a sees %v, the unowned set %v", a.Staged(), p.Staged())
	}
	revA, err := a.Review()
	if err != nil {
		t.Fatal(err)
	}
	if len(revA.Widening) != 0 || strings.Contains(revA.Diff.Unified, "agents") {
		t.Fatalf("b's edit rode into a's review: %+v", revA)
	}
	if _, err := b.ApplyWith(revA.Token, nil); !errors.Is(err, ErrStale) {
		t.Fatalf("b applied a's token: %v", err)
	}
	if _, err := a.ApplyWith(revA.Token, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "greeting: hi") || strings.Contains(string(got), `agents: "off"`) {
		t.Fatalf("the apply wrote more than a's set:\n%s", got)
	}
	if len(a.Staged()) != 0 || len(b.Staged()) != 1 {
		t.Fatal("an apply clears the applying owner's set only")
	}
	// b's set is still staged, against the new file.
	revB, err := b.Review()
	if err != nil || !strings.Contains(revB.Diff.Unified, "+  agents: \"off\"") || strings.Contains(revB.Diff.Unified, "greeting") {
		t.Fatalf("b's review after a's apply: %v %s", err, revB.Diff.Unified)
	}
}

// A review token belongs to the owner that reviewed: another owner with the
// very same staged edits (so the same candidate) still can't apply it, nor
// review a restore with it.
func TestReviewTokensAreTheReviewersOwn(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	a, b := p.As("session-a"), p.As("session-b")
	for _, e := range []Edits{a, b} {
		if _, err := e.Stage(Change{Path: []string{"settings", "greeting"}, Value: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	revA, err := a.Review()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyWith(revA.Token, nil); !errors.Is(err, ErrStale) {
		t.Fatalf("b applied a's token over an identical candidate: %v", err)
	}
	if _, err := a.ApplyWith(revA.Token, nil); err != nil {
		t.Fatalf("a's own token: %v", err)
	}
}

// Raising an existing price, at most tenfold and to a sane value, is the only
// inert pricing change; lowering, removing or adding one, a value that isn't
// a finite number, or a raise past those bounds, widens.
func TestPricingOnlyRaisingIsInert(t *testing.T) {
	base := "pricing:\n  default: {input: 3, output: 15}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n"
	for _, c := range []struct {
		name, after string
		widens      bool
	}{
		{"raise", "pricing:\n  default: {input: 4, output: 15.5}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", false},
		{"lower", "pricing:\n  default: {input: 3, output: 15}\n  models:\n    \"*sonnet*\": {input: 0, output: 15}\n", true},
		{"remove", "pricing:\n  default: {input: 3, output: 15}\n", true},
		{"remove the block", "{}\n", true},
		{"add a pattern", base + "    \"*\": {input: 0, output: 0}\n", true},
		{"not a number", "pricing:\n  default: {input: \"3\", output: 15}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", true},
		{"tenfold", "pricing:\n  default: {input: 30, output: 150}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", false},
		{"past tenfold", "pricing:\n  default: {input: 30.01, output: 15}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", true},
		{"infinite", "pricing:\n  default: {input: .inf, output: 15}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", true},
		{"not a number (NaN)", "pricing:\n  default: {input: .nan, output: 15}\n  models:\n    \"*sonnet*\": {input: 3, output: 15}\n", true},
	} {
		w, g, leaves := classify([]byte(base), []byte(c.after))
		if len(g) != 0 {
			t.Errorf("%s: pricing is not a guarded section: %v", c.name, g)
		}
		if (len(w) > 0) != c.widens {
			t.Errorf("%s: widening %v, want %v", c.name, w, c.widens)
		}
		for _, l := range leaves {
			if l.Guarded {
				t.Errorf("%s: %s marked guarded", c.name, l.Path)
			}
		}
	}
	// Within tenfold, but past the sane ceiling.
	if w, _, _ := classify([]byte("pricing:\n  default: {input: 9000, output: 15}\n"), []byte("pricing:\n  default: {input: 10001, output: 15}\n")); len(w) == 0 {
		t.Error("an absurd price was inert")
	}
}

// Retention keeps the newest maxBackups and the oldest backup of each of the
// last keepDays days: a burst of applies today can't prune the configs earlier
// days started from.
func TestHistoryKeepsEachDaysStart(t *testing.T) {
	path := writeCfg(t, cfgSample)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	p := New(Options{Path: path, Now: func() time.Time { return now }})
	dir := filepath.Join(filepath.Dir(path), historyDir)
	var dayStarts []string
	for day := 0; day < 5; day++ {
		now = time.Date(2026, 10, 20+day, 9, 0, 0, 0, time.UTC)
		for i := 0; i < 3; i++ {
			id, err := p.backup([]byte(fmt.Sprintf("day %d apply %d\n", day, i)))
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				dayStarts = append(dayStarts, id)
			}
			now = now.Add(time.Minute)
		}
	}
	// A burst today: far more than maxBackups.
	for i := 0; i < 3*maxBackups; i++ {
		if _, err := p.backup([]byte(fmt.Sprintf("burst %d\n", i))); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, id := range dayStarts {
		if !have[id] {
			t.Fatalf("day start %s was pruned (%d kept)", id, len(names))
		}
	}
	if len(names) > maxBackups+keepDays {
		t.Fatalf("kept %d backups", len(names))
	}
	// Past keepDays, a day's start goes too.
	now = now.AddDate(0, 0, keepDays+10)
	for i := 0; i < maxBackups+1; i++ {
		_, _ = p.backup([]byte(fmt.Sprintf("later %d\n", i)))
		now = now.Add(time.Second)
	}
	ents, _ = os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), dayStarts[0]) {
			t.Fatal("a day start older than keepDays was kept")
		}
	}
}

// REVIEW TOKENS ARE BOUNDED: each owner holds one live token, replaced by
// its next review or restore review, and at most maxOwners owners hold one
// (the oldest goes), so no number of reviews grows the daemon's memory.
func TestReviewTokensAreBounded(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	a := p.As("session-a")
	if _, err := a.Stage(Change{Path: []string{"settings", "greeting"}, Value: "hi"}); err != nil {
		t.Fatal(err)
	}
	first, err := a.Review()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := a.Review(); err != nil {
			t.Fatal(err)
		}
	}
	last, _ := a.Review()
	if n := len(p.tokens); n != 1 {
		t.Fatalf("one owner's reviews hold %d tokens", n)
	}
	if _, err := a.ApplyWith(first.Token, nil); !errors.Is(err, ErrStale) {
		t.Fatalf("a replaced token still applies: %v", err)
	}
	for i := 0; i < 3*maxOwners; i++ {
		if _, err := p.As(fmt.Sprintf("flood-%d", i)).Review(); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.tokens) > maxOwners || len(p.ownerOf) > maxOwners {
		t.Fatalf("%d tokens for %d owners held", len(p.tokens), len(p.ownerOf))
	}
	if _, err := a.ApplyWith(last.Token, nil); !errors.Is(err, ErrStale) {
		t.Fatalf("the oldest owner's token outlived the cap: %v", err)
	}
	fresh, _ := a.Review()
	if _, err := a.ApplyWith(fresh.Token, nil); err != nil {
		t.Fatalf("a fresh review applies: %v", err)
	}
	if len(p.tokens) > maxOwners {
		t.Fatalf("%d tokens", len(p.tokens))
	}
}

// A FULL TABLE REFUSES A NEW OWNER rather than evicting anyone's staged
// edits, but first reclaims the sets of owners whose session has ended.
func TestStagedSetsAreNeverEvictedByAFlood(t *testing.T) {
	path := writeCfg(t, cfgSample)
	ended := map[string]bool{}
	p := New(Options{Path: path, Live: func(owner string) bool { return !ended[owner] }})
	victim := p.As("victim")
	if _, err := victim.Stage(Change{Path: []string{"settings", "greeting"}, Value: "mine"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxOwners-1; i++ {
		if _, err := p.As(fmt.Sprintf("flood-%d", i)).Stage(Change{Path: []string{"settings", "greeting"}, Value: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.As("one-more").Stage(Change{Path: []string{"settings", "greeting"}, Value: "x"}); err == nil || !strings.Contains(err.Error(), "sessions have staged config edits") {
		t.Fatalf("a new owner past the cap: %v", err)
	}
	if len(victim.Staged()) != 1 {
		t.Fatal("a flood evicted another owner's staged set")
	}
	ended["flood-7"] = true
	if _, err := p.As("one-more").Stage(Change{Path: []string{"settings", "greeting"}, Value: "x"}); err != nil {
		t.Fatalf("an ended session's set is reclaimed: %v", err)
	}
	if len(p.As("flood-7").Staged()) != 0 || len(victim.Staged()) != 1 {
		t.Fatal("the wrong set was reclaimed")
	}
}
