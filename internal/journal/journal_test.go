package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T, dir string, o Options) *Journal {
	t.Helper()
	o.Dir = dir
	j, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func appendN(t *testing.T, j *Journal, topic string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := j.Append(topic, "x", "", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
}

func seqs(evs []Event) []int64 {
	var out []int64
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

func TestReadAfterCursorAndFromNow(t *testing.T) {
	j := open(t, t.TempDir(), Options{})
	start := j.Cursor()
	appendN(t, j, "a", 3)

	evs, next, reset, err := j.Read(start, nil, 0)
	if err != nil || reset {
		t.Fatalf("read: %v reset=%v", err, reset)
	}
	if got := seqs(evs); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("seqs = %v", got)
	}
	if evs[2].ID != next {
		t.Fatalf("next %q != last id %q", next, evs[2].ID)
	}
	// Caught up: nothing more, same cursor.
	evs, again, reset, _ := j.Read(next, nil, 0)
	if len(evs) != 0 || reset || again != next {
		t.Fatalf("caught-up read = %v %q %v", evs, again, reset)
	}
	// Empty cursor: from now.
	evs, now, reset, _ := j.Read("", nil, 0)
	if len(evs) != 0 || reset || now != next {
		t.Fatalf("from-now read = %v %q %v", evs, now, reset)
	}
}

func TestTopicFilterAdvancesPastOtherTopics(t *testing.T) {
	j := open(t, t.TempDir(), Options{})
	start := j.Cursor()
	appendN(t, j, "a", 2)
	appendN(t, j, "b", 2)
	evs, next, _, _ := j.Read(start, []string{"a"}, 0)
	if len(evs) != 2 {
		t.Fatalf("got %d a-events", len(evs))
	}
	if !strings.HasSuffix(next, "-4") {
		t.Fatalf("next = %q, want past the b events", next)
	}
}

func TestLimit(t *testing.T) {
	j := open(t, t.TempDir(), Options{})
	start := j.Cursor()
	appendN(t, j, "a", 10)
	evs, next, _, _ := j.Read(start, nil, 4)
	if len(evs) != 4 || !strings.HasSuffix(next, "-4") {
		t.Fatalf("limit read: %d %q", len(evs), next)
	}
	evs, _, _, _ = j.Read(next, nil, 100)
	if len(evs) != 6 || evs[0].Seq != 5 {
		t.Fatalf("rest: %v", seqs(evs))
	}
}

func TestResetOnForeignEpochFutureAndGarbage(t *testing.T) {
	j := open(t, t.TempDir(), Options{})
	appendN(t, j, "a", 2)
	for _, c := range []string{"deadbeef-1", j.Epoch() + "-99", "5000", "x-y"} {
		evs, next, reset, err := j.Read(c, nil, 0)
		if err != nil || !reset || len(evs) != 0 || next != j.Cursor() {
			t.Errorf("cursor %q: evs=%d next=%q reset=%v err=%v", c, len(evs), next, reset, err)
		}
	}
}

func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, Options{})
	appendN(t, j, "a", 3)
	mid := j.Cursor()
	appendN(t, j, "a", 2)
	epoch := j.Epoch()
	j.Close()

	j2 := open(t, dir, Options{})
	if j2.Epoch() != epoch {
		t.Fatal("epoch changed across restart")
	}
	evs, _, reset, _ := j2.Read(mid, nil, 0)
	if reset || len(evs) != 2 || evs[0].Seq != 4 {
		t.Fatalf("after restart: %v reset=%v", seqs(evs), reset)
	}
	ev, _ := j2.Append("a", "x", "", nil)
	if ev.Seq != 6 {
		t.Fatalf("seq after restart = %d, want 6", ev.Seq)
	}
}

func TestTornTailIsCut(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, Options{})
	appendN(t, j, "a", 2)
	j.Close()
	segs, _ := listSegments(dir)
	f, _ := os.OpenFile(segs[len(segs)-1].path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":3,"topic":"a","ty`)
	f.Close()

	j2 := open(t, dir, Options{})
	ev, err := j2.Append("a", "x", "", nil)
	if err != nil || ev.Seq != 3 {
		t.Fatalf("append after torn tail: seq=%d err=%v", ev.Seq, err)
	}
	evs, _, _, _ := j2.Read(j2.Epoch()+"-0", nil, 0)
	if got := seqs(evs); len(got) != 3 || got[2] != 3 {
		t.Fatalf("seqs = %v", got)
	}
}

func TestReadsOlderEventsFromDisk(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, Options{SegmentBytes: 200, MemEvents: 3})
	start := j.Cursor()
	appendN(t, j, "a", 20)
	if segs, _ := listSegments(dir); len(segs) < 3 {
		t.Fatalf("want several segments, got %d", len(segs))
	}
	evs, _, reset, err := j.Read(start, nil, 0)
	if err != nil || reset || len(evs) != 20 {
		t.Fatalf("disk read: %d reset=%v err=%v", len(evs), reset, err)
	}
	for i, e := range evs {
		if e.Seq != int64(i+1) || e.ID == "" {
			t.Fatalf("event %d = seq %d id %q", i, e.Seq, e.ID)
		}
	}
	// Also from the middle of a segment.
	evs, _, _, _ = j.Read(j.Epoch()+"-7", nil, 0)
	if len(evs) != 13 || evs[0].Seq != 8 {
		t.Fatalf("mid read: %v", seqs(evs))
	}
}

func TestRetentionResetsAgedOutCursor(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, Options{SegmentBytes: 200, MaxBytes: 400, MemEvents: 2})
	start := j.Cursor()
	appendN(t, j, "a", 40)
	evs, next, reset, _ := j.Read(start, nil, 0)
	if !reset || len(evs) != 0 || next != j.Cursor() {
		t.Fatalf("aged-out cursor: reset=%v evs=%d", reset, len(evs))
	}
	segs, _ := listSegments(dir)
	var total int64
	for _, s := range segs[:len(segs)-1] {
		fi, _ := os.Stat(s.path)
		total += fi.Size()
	}
	if total > 400 {
		t.Fatalf("closed segments total %d > MaxBytes", total)
	}
}

func TestRetentionByAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	j := open(t, dir, Options{SegmentBytes: 200, MaxAge: time.Hour, Now: func() time.Time { return now }})
	appendN(t, j, "a", 5)
	segs, _ := listSegments(dir)
	old := now.Add(-2 * time.Hour)
	for _, s := range segs {
		os.Chtimes(s.path, old, old)
	}
	appendN(t, j, "a", 10)
	after, _ := listSegments(dir)
	if after[0].first == 1 {
		t.Fatal("aged segment survived a rotation")
	}
}

func TestChangedWakesReader(t *testing.T) {
	j := open(t, t.TempDir(), Options{})
	ch := j.Changed()
	select {
	case <-ch:
		t.Fatal("changed before any append")
	default:
	}
	appendN(t, j, "a", 1)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("append did not wake the reader")
	}
}

func TestSegmentsWithoutEpochAreDiscarded(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, Options{})
	appendN(t, j, "a", 3)
	old := j.Epoch()
	j.Close()
	os.Remove(filepath.Join(dir, epochFile))

	j2 := open(t, dir, Options{})
	if j2.Epoch() == old {
		t.Fatal("epoch reused")
	}
	if ev, _ := j2.Append("a", "x", "", nil); ev.Seq != 1 {
		t.Fatalf("new journal starts at seq %d", ev.Seq)
	}
}

func TestMemoryOnly(t *testing.T) {
	j, err := Open(Options{MemEvents: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	start := j.Cursor()
	appendN(t, j, "a", 2)
	if evs, _, reset, _ := j.Read(start, nil, 0); reset || len(evs) != 2 {
		t.Fatalf("memory read: %d reset=%v", len(evs), reset)
	}
	appendN(t, j, "a", 5)
	if _, _, reset, _ := j.Read(start, nil, 0); !reset {
		t.Fatal("a cursor older than memory must reset")
	}
}
