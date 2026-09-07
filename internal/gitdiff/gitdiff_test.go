package gitdiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo builds a real repo with one committed file.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestProposedUncommitted(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := Proposed(context.Background(), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# uncommitted (vs HEAD)") || !strings.Contains(diff, "+two") {
		t.Fatalf("uncommitted diff:\n%s", diff)
	}
}

func TestProposedUnpushedCommits(t *testing.T) {
	origin := initRepo(t)
	if _, err := git(context.Background(), origin, "config", "receive.denyCurrentBranch", "ignore"); err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	// Commit locally, don't push.
	if err := os.WriteFile(filepath.Join(clone, "b.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "local work"}} {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	diff, err := Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# committed, not pushed") || !strings.Contains(diff, "+local") {
		t.Fatalf("unpushed diff:\n%s", diff)
	}
}

func TestProposedCleanAndErrors(t *testing.T) {
	dir := initRepo(t)
	diff, err := Proposed(context.Background(), dir, 0)
	if err != nil || diff != "" {
		t.Fatalf("clean tree: %q %v", diff, err)
	}
	if _, err := Proposed(context.Background(), t.TempDir(), 0); err == nil {
		t.Fatal("non-repo must error")
	}
}

func TestProposedClip(t *testing.T) {
	dir := initRepo(t)
	big := strings.Repeat("line of change\n", 2000)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := Proposed(context.Background(), dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) > 1100 || !strings.HasSuffix(diff, "(diff clipped)") {
		t.Fatalf("clip: len=%d tail=%q", len(diff), diff[max(0, len(diff)-30):])
	}
}
