package configedit

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// prepRecorder is a Prepare that records what happened to what it readied.
type prepRecorder struct {
	saw          string
	commitErr    error
	committed    int
	discarded    int
	liveAtCommit string
	path         string
}

func (r *prepRecorder) prepare(path string) (Prepared, error) {
	b, _ := os.ReadFile(path)
	r.saw = string(b)
	return Prepared{
		Commit: func() error {
			r.committed++
			live, _ := os.ReadFile(r.path)
			r.liveAtCommit = string(live)
			return r.commitErr
		},
		Discard: func() { r.discarded++ },
	}, nil
}

func newPreparedPipe(t *testing.T, r *prepRecorder) (*Pipeline, string) {
	t.Helper()
	path := writeCfg(t, cfgSample)
	r.path = path
	p := New(Options{
		Path:     path,
		Validate: func(string) error { t.Fatal("Validate ran though Prepare is set"); return nil },
		Prepare:  r.prepare,
		Now:      func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
	})
	return p, path
}

// An apply readies the candidate, writes the file, and only then makes what
// it readied live.
func TestApplyCommitsWhatPrepareReadiedAfterTheWrite(t *testing.T) {
	r := &prepRecorder{}
	p, _ := newPreparedPipe(t, r)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(rev.Token); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(r.saw, "concurrency: 8") {
		t.Fatalf("prepare did not see the candidate: %q", r.saw)
	}
	if r.committed != 1 || !strings.Contains(r.liveAtCommit, "concurrency: 8") {
		t.Fatalf("committed %d times, live file at commit:\n%s", r.committed, r.liveAtCommit)
	}
}

// A preflight validate readies and discards: nothing goes live.
func TestValidateDiscardsWhatPrepareReadied(t *testing.T) {
	r := &prepRecorder{}
	p, _ := newPreparedPipe(t, r)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.committed != 0 || r.discarded != 1 {
		t.Fatalf("committed %d, discarded %d; want 0, 1", r.committed, r.discarded)
	}
}

// If what was readied can't go live, the previous file is put back, so the
// file never names what isn't there.
func TestApplyPutsTheFileBackWhenCommitFails(t *testing.T) {
	r := &prepRecorder{commitErr: errors.New("swap failed")}
	p, path := newPreparedPipe(t, r)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	rev, _ := p.Review()
	_, err := p.Apply(rev.Token)
	if err == nil || !strings.Contains(err.Error(), "swap failed") {
		t.Fatalf("err = %v, want the commit failure", err)
	}
	if got, _ := os.ReadFile(path); string(got) != cfgSample {
		t.Fatalf("live file not restored:\n%s", got)
	}
}

// A candidate that doesn't prepare is refused with nothing written.
func TestApplyRefusesWhenPrepareFails(t *testing.T) {
	path := writeCfg(t, cfgSample)
	p := New(Options{Path: path, Prepare: func(string) (Prepared, error) { return Prepared{}, errors.New("couldn't fetch its packs") }})
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	rev, _ := p.Review()
	if _, err := p.Apply(rev.Token); err == nil || !strings.Contains(err.Error(), "couldn't fetch its packs") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != cfgSample {
		t.Fatal("live file changed")
	}
}
