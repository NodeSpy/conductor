package configedit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cfgSample = `settings:
  concurrency: 4   # inline comment
  greeting: hello

# a section this binary does not know
x-notes:
  by: platform-sre

sandbox:
  agents: on

update:
  apply: true
`

func writeCfg(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newPipe(t *testing.T, content string, validate func(string) error) (*Pipeline, string, *bool) {
	t.Helper()
	path := writeCfg(t, content)
	reloaded := false
	fixedNow := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	p := New(Options{
		Path:     path,
		Validate: validate,
		Reload:   func() error { reloaded = true; return nil },
		Now:      func() time.Time { return fixedNow },
	})
	return p, path, &reloaded
}

func TestStageReplaceLastWins(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 12})
	if len(p.Staged()) != 1 {
		t.Fatalf("expected 1 staged change, got %d", len(p.Staged()))
	}
	_, after, err := p.Candidate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "concurrency: 12") {
		t.Fatalf("last write did not win:\n%s", after)
	}
}

func TestReviewNoChangeEmpty(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 4}) // same value
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if rev.Diff.Changed {
		t.Fatalf("a no-op stage produced a diff: %+v", rev.Diff)
	}
}

func TestApplyFullFlow(t *testing.T) {
	validated := ""
	p, path, reloaded := newPipe(t, cfgSample, func(copyPath string) error {
		b, _ := os.ReadFile(copyPath)
		validated = string(b)
		return nil
	})
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8, Summary: "more parallelism"})
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if rev.Diff.Added != 1 || rev.Diff.Removed != 1 {
		t.Fatalf("expected a one-line diff, got +%d -%d", rev.Diff.Added, rev.Diff.Removed)
	}
	applied, err := p.Apply(rev.Token)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !*reloaded {
		t.Fatal("reload was not called")
	}
	if applied.BackupID == "" {
		t.Fatal("no backup recorded")
	}
	// The live file now carries the change, with the comment preserved.
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "concurrency: 8   # inline comment") {
		t.Fatalf("live file wrong after apply:\n%s", got)
	}
	// validate-on-copy saw the candidate, not the live file.
	if !strings.Contains(validated, "concurrency: 8") {
		t.Fatalf("validate did not see the candidate: %q", validated)
	}
	// staged set cleared.
	if len(p.Staged()) != 0 {
		t.Fatal("staged set not cleared after apply")
	}
	// A backup exists and restores the original.
	hist, err := p.History()
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v len=%d", err, len(hist))
	}
}

func TestApplyRejectsBadCandidate(t *testing.T) {
	p, path, _ := newPipe(t, cfgSample, func(copyPath string) error {
		return errors.New("settings.concurrency: must be >= 1")
	})
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 0})
	rev, _ := p.Review()
	if _, err := p.Apply(rev.Token); err == nil {
		t.Fatal("expected apply to reject an invalid candidate")
	}
	// The live file is untouched.
	got, _ := os.ReadFile(path)
	if string(got) != cfgSample {
		t.Fatalf("live file changed despite failed validation:\n%s", got)
	}
}

func TestApplyStaleToken(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	p.Stage(Change{Path: []string{"settings", "concurrency"}, Value: 8})
	rev, _ := p.Review()
	// The staged set changes after review → the old token no longer binds.
	p.Stage(Change{Path: []string{"settings", "greeting"}, Value: "hi"})
	if _, err := p.Apply(rev.Token); !errors.Is(err, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", err)
	}
}

func TestWideningDetected(t *testing.T) {
	p, _, _ := newPipe(t, cfgSample, nil)
	p.Stage(Change{Path: []string{"sandbox", "agents"}, Value: "off"})
	rev, err := p.Review()
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Widening) != 1 {
		t.Fatalf("expected 1 widening, got %d: %+v", len(rev.Widening), rev.Effects)
	}
	if !strings.Contains(rev.Widening[0].Text, "sandbox.agents") {
		t.Fatalf("widening text: %q", rev.Widening[0].Text)
	}
}

func TestRestoreFlow(t *testing.T) {
	p, path, _ := newPipe(t, cfgSample, func(string) error { return nil })
	// First apply a change so a backup of the original exists.
	p.Stage(Change{Path: []string{"settings", "greeting"}, Value: "changed"})
	rev, _ := p.Review()
	if _, err := p.Apply(rev.Token); err != nil {
		t.Fatal(err)
	}
	hist, _ := p.History()
	if len(hist) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(hist))
	}
	// Restore the backup (the original greeting).
	rrev, _, err := p.RestoreReview(hist[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Restore(hist[0].ID, rrev.Token); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "greeting: hello") {
		t.Fatalf("restore did not revert greeting:\n%s", got)
	}
}
