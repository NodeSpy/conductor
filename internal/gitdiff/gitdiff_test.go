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

// A PR worktree checks the PR out as a local branch with no upstream and
// pushes with an explicit refspec. Once pushed, there's nothing "committed,
// not pushed" — it must not diff the whole PR against origin/HEAD. A later
// local commit is reported alone, against the pushed tip.
func TestProposedNoUpstreamPushedBranch(t *testing.T) {
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
	run("switch", "-q", "-c", "pr-7") // no upstream
	commit("pr.txt", "pr work\n")
	run("push", "-q", "origin", "HEAD:refs/heads/feat")

	diff, err := Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("pushed branch without upstream must report nothing unpushed, got:\n%s", diff)
	}

	commit("more.txt", "later\n")
	diff, err = Proposed(context.Background(), clone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "# committed, not pushed (vs origin/feat)") || !strings.Contains(diff, "+later") || strings.Contains(diff, "+pr work") {
		t.Fatalf("want only the unpushed commit vs origin/feat, got:\n%s", diff)
	}
}

// Unpushed counts exactly the local commits no remote has: none on a fresh
// clone, two after two local commits (an uncommitted edit is not one), none
// once they are pushed; a non-repo is an error.
func TestUnpushedCounts(t *testing.T) {
	ctx := context.Background()
	origin := initRepo(t)
	if _, err := git(ctx, origin, "config", "receive.denyCurrentBranch", "ignore"); err != nil {
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
	if n, _, err := Unpushed(ctx, clone, ""); err != nil || n != 0 {
		t.Fatalf("fresh clone: n=%d err=%v", n, err)
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(clone, f), []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-q", "-m", f)
	}
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, base, err := Unpushed(ctx, clone, ""); err != nil || n != 2 || base == "" {
		t.Fatalf("two local commits: n=%d base=%q err=%v", n, base, err)
	}
	run("push", "-q", "origin", "HEAD")
	if n, _, err := Unpushed(ctx, clone, ""); err != nil || n != 0 {
		t.Fatalf("after push: n=%d err=%v", n, err)
	}
	if _, _, err := Unpushed(ctx, t.TempDir(), ""); err == nil {
		t.Fatal("non-repo must error")
	}
}

// A fork's PR checked out: its commits are on no remote of the clone, so
// only since — where the workspace started — tells the agent's commits from
// the PR's own.
func TestUnpushedSinceStart(t *testing.T) {
	ctx := context.Background()
	origin := initRepo(t)
	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", clone, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("switch", "-q", "--detach") // the fork's PR head, taken detached
	for _, m := range []string{"fork one", "fork two"} {
		run("commit", "-q", "--allow-empty", "-m", m)
	}
	start := run("rev-parse", "HEAD")
	if n, base, err := Unpushed(ctx, clone, start); err != nil || n != 0 || base != start {
		t.Fatalf("nothing past the start: n=%d base=%q err=%v", n, base, err)
	}
	if n, _, _ := Unpushed(ctx, clone, ""); n != 2 {
		t.Fatalf("without a start the fork's commits read as unpushed (why since exists): n=%d", n)
	}
	run("commit", "-q", "--allow-empty", "-m", "agent fix")
	if n, _, err := Unpushed(ctx, clone, start); err != nil || n != 1 {
		t.Fatalf("the agent's one commit: n=%d err=%v", n, err)
	}
	// Part of the agent's work already reached a remote (a git.push mid-step):
	// only what came after counts.
	originDir := run("remote", "get-url", "origin")
	if out, err := exec.Command("git", "-C", originDir, "config", "receive.denyCurrentBranch", "ignore").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	run("push", "-q", "origin", "HEAD:refs/heads/pr")
	run("fetch", "-q", "origin")
	run("commit", "-q", "--allow-empty", "-m", "agent fix two")
	if n, _, err := Unpushed(ctx, clone, start); err != nil || n != 1 {
		t.Fatalf("only the commit after the mid-step push: n=%d err=%v", n, err)
	}
	if _, _, err := Unpushed(ctx, clone, strings.Repeat("ab", 20)); err == nil {
		t.Fatal("a start commit the clone lacks is an error (flow falls back to the remotes)")
	}
}
