package gitwt

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeSlugMatchesClaudeCode(t *testing.T) {
	got := claudeSlug("/home/adam/.local/state/conductor/worktrees/rdllc4fdx4u8z-step1")
	want := "-home-adam--local-state-conductor-worktrees-rdllc4fdx4u8z-step1"
	if got != want {
		t.Fatalf("claudeSlug = %q, want %q", got, want)
	}
}

// The reaper clears a stale transcript whose worktree is gone, and nothing
// else: not one whose worktree is still on disk, not a recent one, and never a
// transcript recorded anywhere outside this provisioner's worktrees dir.
func TestReapTranscriptsOnlyRemovesStaleOnesForGoneWorktrees(t *testing.T) {
	p := New(t.TempDir())
	p.TranscriptsDir = t.TempDir()
	p.TranscriptRetention = time.Hour
	old := time.Now().Add(-2 * time.Hour)

	transcript := func(cwd string, at time.Time) string {
		dir := filepath.Join(p.TranscriptsDir, claudeSlug(cwd))
		writeFile(t, filepath.Join(dir, "s.jsonl"), "{}\n")
		touch(t, filepath.Join(dir, "s.jsonl"), at)
		touch(t, dir, at)
		return dir
	}

	gone := transcript(filepath.Join(p.WorktreesDir(), "gone"), old)
	live := filepath.Join(p.WorktreesDir(), "live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	liveT := transcript(live, old)
	fresh := transcript(filepath.Join(p.WorktreesDir(), "fresh"), time.Now())
	users := transcript("/home/u/Projects/app", old)

	// A transcript still being appended to: the dir is old, the file is not.
	appending := transcript(filepath.Join(p.WorktreesDir(), "appending"), old)
	touch(t, filepath.Join(appending, "s.jsonl"), time.Now())

	p.reapTranscripts()

	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("stale transcript for a gone worktree survived: %v", err)
	}
	for _, keep := range []string{liveT, fresh, users, appending} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("reaper removed %s: %v", keep, err)
		}
	}
}

func TestReapTranscriptsIsOffWithoutADir(t *testing.T) {
	p := New(t.TempDir())
	p.reapTranscripts() // must not panic or touch anything
}
