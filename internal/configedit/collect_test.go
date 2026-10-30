package configedit

import (
	"errors"
	"strings"
	"testing"
)

// COLLECT runs only after an apply or a restore, under the lock, and sees
// every text that is or may become the config: the live file, every backup,
// and every owner's staged values. Staging never runs it; StageWith builds
// its change under the same lock.
func TestCollectSeesEveryReference(t *testing.T) {
	path := writeCfg(t, cfgSample)
	var calls [][][]byte
	p := New(Options{Path: path, Collect: func(refs [][]byte) { calls = append(calls, refs) }})
	joined := func(refs [][]byte) string {
		var b strings.Builder
		for _, r := range refs {
			b.Write(r)
			b.WriteByte('\n')
		}
		return b.String()
	}
	if _, err := p.As("a").Stage(Change{Path: []string{"settings", "greeting"}, Value: "from-a"}); err != nil {
		t.Fatal(err)
	}
	inLock := false
	if _, err := p.As("b").StageWith(func() (Change, error) {
		if p.mu.TryLock() {
			p.mu.Unlock()
		} else {
			inLock = true
		}
		return Change{Path: []string{"settings", "greeting"}, Value: "from-b"}, nil
	}); err != nil || !inLock {
		t.Fatalf("StageWith: %v, built under the lock: %v", err, inLock)
	}
	if _, err := p.As("c").StageWith(func() (Change, error) { return Change{}, errors.New("no plan") }); err == nil || len(p.As("c").Staged()) != 0 {
		t.Fatal("a failed build staged something")
	}
	if len(calls) != 0 {
		t.Fatal("staging collected")
	}
	rev, err := p.As("a").Review()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.As("a").ApplyWith(rev.Token, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("an apply collected %d times", len(calls))
	}
	all := joined(calls[0])
	for _, want := range []string{"greeting: from-a", "greeting: hello", `"from-b"`} {
		if !strings.Contains(all, want) {
			t.Fatalf("the collection's references lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "from-a\"") {
		t.Fatal("the applied set's staged values outlived its apply")
	}
	hist, err := p.History()
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v %v", hist, err)
	}
	rr, _, err := p.RestoreReview(hist[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Restore(hist[0].ID, rr.Token); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(joined(calls[1]), "greeting: from-a") {
		t.Fatalf("a restore collects too, keeping what the new backup names: %d", len(calls))
	}
}
