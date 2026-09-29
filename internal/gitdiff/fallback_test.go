package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forceNoGit makes hasGit() report false for the duration of the test,
// exercising the go-git fallback path exactly as gitwt does when the `git`
// binary is missing from PATH.
func forceNoGit(t *testing.T) {
	t.Helper()
	prev := LookPath
	LookPath = func(string) (string, error) { return "", errors.New("git not found (forced for test)") }
	t.Cleanup(func() { LookPath = prev })
}

func TestProposedFallbackUncommitted(t *testing.T) {
	dir := initRepo(t)
	forceNoGit(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := Proposed(context.Background(), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# uncommitted (vs HEAD)") || !strings.Contains(diff, "+two") {
		t.Fatalf("fallback uncommitted diff:\n%s", diff)
	}
}

func TestProposedFallbackAddedAndDeletedFiles(t *testing.T) {
	dir := initRepo(t)
	forceNoGit(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	diff, err := Proposed(context.Background(), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+brand new") {
		t.Fatalf("fallback diff missing the added file:\n%s", diff)
	}
	if !strings.Contains(diff, "-one") {
		t.Fatalf("fallback diff missing the deleted file's content:\n%s", diff)
	}
}

func TestProposedFallbackUnpushedCommits(t *testing.T) {
	origin := initRepo(t)
	if _, err := git(context.Background(), origin, "config", "receive.denyCurrentBranch", "ignore"); err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(clone, "b.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "local work"}} {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	forceNoGit(t)
	diff, err := Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# committed, not pushed") || !strings.Contains(diff, "+local") {
		t.Fatalf("fallback unpushed diff:\n%s", diff)
	}
}

func TestProposedFallbackCleanAndErrors(t *testing.T) {
	dir := initRepo(t)
	forceNoGit(t)
	diff, err := Proposed(context.Background(), dir, 0)
	if err != nil || diff != "" {
		t.Fatalf("clean tree (fallback): %q %v", diff, err)
	}
	if _, err := Proposed(context.Background(), t.TempDir(), 0); err == nil {
		t.Fatal("non-repo must error in fallback mode too")
	}
}

func TestProposedFallbackNoUpstreamPushedBranch(t *testing.T) {
	origin := initRepo(t)
	if _, err := git(context.Background(), origin, "config", "receive.denyCurrentBranch", "ignore"); err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", clone, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(clone, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-q", "-m", file)
	}
	run("switch", "-q", "-c", "pr-7")
	commit("pr.txt", "pr work\n")
	run("push", "-q", "origin", "HEAD:refs/heads/feat")

	forceNoGit(t)
	diff, err := Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("pushed branch without upstream must report nothing unpushed (fallback), got:\n%s", diff)
	}

	commit("more.txt", "later\n")
	diff, err = Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# committed, not pushed (vs origin/feat)") || !strings.Contains(diff, "+later") || strings.Contains(diff, "+pr work") {
		t.Fatalf("fallback: want only the unpushed commit vs origin/feat, got:\n%s", diff)
	}
}
