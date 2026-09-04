package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// fakePaseoDir writes a paseo stub whose behavior is driven by files in its
// own directory and which appends every invocation to calls.log.
func fakePaseoDir(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
dir="$(cd "$(dirname "$0")" && pwd)"
echo "$@" >> "$dir/calls.log"
case "$1" in
  ls)
    labeled=0
    for a in "$@"; do [ "$a" = "--label" ] && labeled=1; done
    if [ "$labeled" = 1 ] && [ -f "$dir/ls-label.json" ]; then cat "$dir/ls-label.json"
    else cat "$dir/ls.json" 2>/dev/null || echo '[]'; fi ;;
  workspace)
    case "$2" in
      ls)      cat "$dir/workspaces.json" 2>/dev/null || echo '[]' ;;
      archive) exit 0 ;;
      create)  cat "$dir/wscreate.json" 2>/dev/null || echo '{"workspaceId":"wks_new","cwd":"'"$dir"'/wt"}' ;;
    esac ;;
  inspect)
    id=""
    for a in "$@"; do case "$a" in inspect|--json) ;; *) id="$a" ;; esac; done
    cat "$dir/inspect-$id.json" 2>/dev/null || echo '{}' ;;
  wait)
    [ -f "$dir/wait-slow" ] && sleep 5
    exit 0 ;;
  send)
    [ -f "$dir/send-fail" ] && { echo "send boom" >&2; exit 1; }
    exit 0 ;;
  archive) exit 0 ;;
  clone)   [ -f "$dir/clone-fail" ] && { echo "no forge" >&2; exit 1; }; echo '{}' ;;
  run)     cat "$dir/run.json" 2>/dev/null || echo '{"agentId":"a-new"}' ;;
  *) echo '{}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func callsLog(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return string(b)
}

func put(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitRepoAt initializes a real git repo with a branch and an origin URL.
func gitRepoAt(t *testing.T, branch, origin string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
	return dir
}

func TestNewDefaults(t *testing.T) {
	d := New("", config.Retry{}, true)
	if d.PaseoBin != "paseo" || !d.DryRun {
		t.Fatalf("defaults: %+v", d)
	}
	d = New("/x/paseo", config.Retry{Max: 3}, false)
	if d.PaseoBin != "/x/paseo" || d.RetryMax != 3 {
		t.Fatalf("explicit: %+v", d)
	}
}

func TestWaitForAgentTimeoutAndNoop(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	d.WaitForAgent(context.Background(), "", time.Second) // no-op
	if strings.Contains(callsLog(t, dir), "wait") {
		t.Fatal("empty id must not invoke paseo")
	}
	put(t, dir, "wait-slow", "1")
	start := time.Now()
	d.WaitForAgent(context.Background(), "a-1", 100*time.Millisecond)
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not bound the wait")
	}
	if !strings.Contains(callsLog(t, dir), "wait a-1") {
		t.Fatal("wait not invoked")
	}
}

func TestSendSurfacesStderr(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	if err := d.Send(context.Background(), "a-1", "do it"); err != nil {
		t.Fatalf("send ok path: %v", err)
	}
	put(t, dir, "send-fail", "1")
	err := d.Send(context.Background(), "a-1", "do it")
	if err == nil || !strings.Contains(err.Error(), "send boom") {
		t.Fatalf("send failure should carry stderr detail, got %v", err)
	}
}

func TestHasLiveAgentAndArchive(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	if d.HasLiveAgent(context.Background(), "a/w#1", "review_requested") {
		t.Fatal("empty ls should report no live agent")
	}
	put(t, dir, "ls.json", `[{"id":"a-1"}]`)
	if !d.HasLiveAgent(context.Background(), "a/w#1", "review_requested") {
		t.Fatal("non-empty ls should report a live agent")
	}
	if err := d.Archive(context.Background(), ""); err != nil {
		t.Fatal("blank id archive is a no-op")
	}
	if strings.Contains(callsLog(t, dir), "archive") {
		t.Fatal("blank archive must not invoke paseo")
	}
	if err := d.Archive(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callsLog(t, dir), "archive a-1") {
		t.Fatal("archive not invoked")
	}
}

func TestAdoptAgentForBranch(t *testing.T) {
	repo := gitRepoAt(t, "feature/auth", "git@github.com:Acme/Widget.git")
	other := gitRepoAt(t, "main", "git@github.com:acme/widget.git")
	bin, dir := fakePaseoDir(t)
	put(t, dir, "ls.json", fmt.Sprintf(
		`[{"id":"a-match","cwd":%q},{"id":"a-wrongbranch","cwd":%q},{"id":"a-nodir","cwd":"/nonexistent"},{"id":""}]`,
		repo, other))
	put(t, dir, "inspect-a-match.json", `{"LastUsage":"2026-01-02T00:00:00Z"}`)
	d := &Dispatcher{PaseoBin: bin}

	req := Request{Trigger: core.Trigger{
		Target:  core.Target{Repo: "acme/widget"},
		Context: map[string]any{"head_ref": "feature/auth"},
	}}
	if got := d.adoptAgentForBranch(context.Background(), req); got != "a-match" {
		t.Fatalf("adopt = %q, want a-match", got)
	}
	// No head_ref → no adoption.
	req.Trigger.Context = map[string]any{}
	if got := d.adoptAgentForBranch(context.Background(), req); got != "" {
		t.Fatalf("no head_ref should not adopt, got %q", got)
	}
}
func TestAgentLastActiveFallbacks(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	put(t, dir, "inspect-a-1.json", `{"LastUsage":"L","UpdatedAt":"U","CreatedAt":"C"}`)
	if got := d.agentLastActive(context.Background(), "a-1"); got != "L" {
		t.Fatalf("LastUsage first: %q", got)
	}
	put(t, dir, "inspect-a-2.json", `{"UpdatedAt":"U","CreatedAt":"C"}`)
	if got := d.agentLastActive(context.Background(), "a-2"); got != "U" {
		t.Fatalf("UpdatedAt next: %q", got)
	}
	put(t, dir, "inspect-a-3.json", `{"CreatedAt":"C"}`)
	if got := d.agentLastActive(context.Background(), "a-3"); got != "C" {
		t.Fatalf("CreatedAt last: %q", got)
	}
	put(t, dir, "inspect-a-4.json", `not json`)
	if got := d.agentLastActive(context.Background(), "a-4"); got != "" {
		t.Fatalf("bad json: %q", got)
	}
}

func TestGitRepoMatches(t *testing.T) {
	repo := gitRepoAt(t, "main", "https://github.com/Acme/Widget.git")
	if !gitRepoMatches(context.Background(), repo, "acme/widget") {
		t.Fatal("case-insensitive origin match")
	}
	if gitRepoMatches(context.Background(), repo, "other/repo") {
		t.Fatal("mismatched repo")
	}
	if gitRepoMatches(context.Background(), repo, "") {
		t.Fatal("empty repo never matches")
	}
	if gitRepoMatches(context.Background(), t.TempDir(), "acme/widget") {
		t.Fatal("non-repo dir never matches")
	}
}

func TestClearStaleGitLock(t *testing.T) {
	repo := gitRepoAt(t, "main", "")
	lock := filepath.Join(repo, ".git", "config.lock")
	// Old lock → removed.
	if err := os.WriteFile(lock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	os.Chtimes(lock, old, old)
	clearStaleGitLock(context.Background(), "paseo", repo)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("stale lock should be removed")
	}
	// Fresh lock → kept.
	if err := os.WriteFile(lock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	clearStaleGitLock(context.Background(), "paseo", repo)
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("fresh lock must be kept")
	}
	// Non-repo and empty cwd: no panic, no effect.
	clearStaleGitLock(context.Background(), "paseo", t.TempDir())
	clearStaleGitLock(context.Background(), "paseo", "")
}

func TestStderrTailAndTruncate(t *testing.T) {
	var b bytes.Buffer
	if stderrTail(&b) != "" {
		t.Fatal("empty stderr")
	}
	b.WriteString("short err")
	if got := stderrTail(&b); got != ": short err" {
		t.Fatalf("short tail: %q", got)
	}
	b.Reset()
	b.WriteString(strings.Repeat("x", 400))
	if got := stderrTail(&b); !strings.HasPrefix(got, ": …") || len(got) > 310 {
		t.Fatalf("long tail: %d %q", len(got), got[:10])
	}
	if truncate("abc", 2) != "ab…" || truncate("abc", 5) != "abc" {
		t.Fatal("truncate")
	}
}

func TestCreateWorktreeStrategies(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	prReq := Request{Trigger: core.Trigger{Target: core.Target{Repo: "a/w", PR: 5, Number: 5, BaseRef: "main"}},
		Action: config.Action{Checkout: "checkout-pr"}}
	id, cwd, err := d.createWorktree(context.Background(), prReq, "/base")
	if err != nil || id != "wks_new" || cwd == "" {
		t.Fatalf("checkout-pr: %v %q %q", err, id, cwd)
	}
	if !strings.Contains(callsLog(t, dir), "--mode checkout-pr --json --pr-number 5 --forge github") {
		t.Fatalf("pr argv: %s", callsLog(t, dir))
	}

	brReq := Request{Trigger: core.Trigger{Kind: "issue_matched", Target: core.Target{Repo: "a/w", Number: 9, BaseRef: "main"}},
		Action: config.Action{Checkout: "branch-off"}}
	if _, _, err := d.createWorktree(context.Background(), brReq, "/base"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callsLog(t, dir), "--new-branch conductor/issue_matched-9 --base main") {
		t.Fatalf("branch argv: %s", callsLog(t, dir))
	}

	// checkout "none" is an unexpected strategy here.
	noneReq := Request{Action: config.Action{Checkout: "none"}}
	if _, _, err := d.createWorktree(context.Background(), noneReq, "/base"); err == nil {
		t.Fatal("none strategy must error")
	}

	// A create that lands in $HOME is the guarded-against fallback.
	home := t.TempDir()
	t.Setenv("HOME", home)
	put(t, dir, "wscreate.json", `{"workspaceId":"wks_h","cwd":"`+home+`"}`)
	if _, _, err := d.createWorktree(context.Background(), prReq, "/base"); err == nil || !strings.Contains(err.Error(), "produced no worktree") {
		t.Fatalf("home cwd should fail: %v", err)
	}
	// Unparseable output.
	put(t, dir, "wscreate.json", `nope`)
	if _, _, err := d.createWorktree(context.Background(), prReq, "/base"); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("bad json: %v", err)
	}
}

func TestResolveCheckoutDirReuseCloneAndMemo(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := &Dispatcher{PaseoBin: bin, repoDirs: map[string]string{}}

	// Empty repo errors.
	if _, err := d.resolveCheckoutDir(context.Background(), ""); err == nil {
		t.Fatal("empty repo must error")
	}

	// A registered paseo workspace for the repo wins.
	repoDir := gitRepoAt(t, "main", "git@github.com:acme/widget.git")
	put(t, dir, "workspaces.json", fmt.Sprintf(`[{"project":"ACME/widget","cwd":%q,"isolation":"local"}]`, repoDir))
	got, err := d.resolveCheckoutDir(context.Background(), "acme/widget")
	if err != nil || got == "" {
		t.Fatalf("workspace reuse: %q %v", got, err)
	}
	// Memoized: a second call revalidates the git repo and returns the memo.
	got2, err := d.resolveCheckoutDir(context.Background(), "acme/widget")
	if err != nil || got2 != got {
		t.Fatalf("memo: %q %v", got2, err)
	}

	// No workspace + no prior clone → paseo clone runs; failure surfaces.
	put(t, dir, "workspaces.json", `[]`)
	put(t, dir, "clone-fail", "1")
	if _, err := d.resolveCheckoutDir(context.Background(), "acme/other"); err == nil || !strings.Contains(err.Error(), "no forge") {
		t.Fatalf("clone failure: %v", err)
	}
	os.Remove(filepath.Join(dir, "clone-fail"))

	// A prior clone at the target dir is reused without calling clone.
	pre := filepath.Join(home, ".conductor", "checkouts", "prior")
	os.MkdirAll(filepath.Dir(pre), 0o755)
	os.Rename(gitRepoAt(t, "main", ""), pre)
	got, err = d.resolveCheckoutDir(context.Background(), "acme/prior")
	if err != nil || got != pre {
		t.Fatalf("prior clone reuse: %q %v", got, err)
	}

	// A clone that "succeeds" but leaves no git checkout is caught.
	if _, err := d.resolveCheckoutDir(context.Background(), "acme/ghost"); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("ghost clone: %v", err)
	}
}

func TestScratchWorkspaceResolution(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := &Dispatcher{PaseoBin: bin, repoDirs: map[string]string{}}

	// Found by title.
	put(t, dir, "workspaces.json", `[{"workspaceId":"wks_s","name":"conductor-scratch","isolation":"local"}]`)
	id, err := d.resolveScratchWorkspace(context.Background())
	if err != nil || id != "wks_s" {
		t.Fatalf("find by title: %q %v", id, err)
	}
	// Not found → created.
	put(t, dir, "workspaces.json", `[]`)
	put(t, dir, "wscreate.json", `{"workspaceId":"wks_created"}`)
	id, err = d.resolveScratchWorkspace(context.Background())
	if err != nil || id != "wks_created" {
		t.Fatalf("create: %q %v", id, err)
	}
	if !strings.Contains(callsLog(t, dir), "--title conductor-scratch") {
		t.Fatal("scratch create argv missing title")
	}
	// Fallback id key.
	put(t, dir, "wscreate.json", `{"id":"wks_alt"}`)
	if id, _ := d.createScratchWorkspace(context.Background()); id != "wks_alt" {
		t.Fatalf("id fallback: %q", id)
	}
}

func TestLabelArgsAndSlug(t *testing.T) {
	req := Request{
		Trigger: core.Trigger{Source: "github", Instance: "gh", Kind: "new_comment", Variant: "v",
			Target: core.Target{Repo: "a/w", PR: 3, Number: 3, HeadSHA: "h"},
			Labels: map[string]string{"t": "1"}},
		Profile: config.AgentProfile{ArchiveWhenDone: true, Labels: map[string]string{"p": "2"}},
	}
	labels := strings.Join(labelArgs(req), " ")
	for _, want := range []string{"conductor=1", "variant=v", "archive=1", "p=2", "t=1", "kind=new_comment"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("labels missing %q: %s", want, labels)
		}
	}
	if got := branchSlug(core.Trigger{Kind: "merge conflict", Target: core.Target{Number: 7}}); got != "conductor/merge-conflict-7" {
		t.Fatalf("slug: %q", got)
	}
}

func TestTemplateDataPrecedence(t *testing.T) {
	req := Request{
		Trigger: core.Trigger{Kind: "k", Title: "T",
			Target:  core.Target{Repo: "a/w", PR: 1},
			Context: map[string]any{"repo": "shadowed", "extra": "ctx"}},
		Tokens: Tokens{App: "at", User: "ut"},
		Data:   map[string]any{"extra": "step-wins", "out": 7},
	}
	d := templateData(req)
	if d["repo"] != "a/w" {
		t.Fatal("target fields shadow context")
	}
	if d["extra"] != "step-wins" || d["out"] != 7 {
		t.Fatal("step data wins over context")
	}
	if d["app_token"] != "at" || d["gh_token"] != "ut" {
		t.Fatal("tokens plumbed")
	}
}
