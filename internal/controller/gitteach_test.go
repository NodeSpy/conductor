package controller

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/keycache"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// teachRig is a checkout at a default-branch head, a teaching entry for it,
// and the learned sets TaughtGit hands over.
func teachRig(t *testing.T) (agentsandbox.Entry, func() [][]string) {
	t.Helper()
	ws := t.TempDir()
	gitIn(t, ws, "init", "-q", "-b", "main")
	gitIn(t, ws, "commit", "-q", "--allow-empty", "-m", "one")
	head := gitIn(t, ws, "rev-parse", "HEAD")
	oldA, oldL, oldD := TeachGitAuth, TeachGitLearned, TeachStateDir
	t.Cleanup(func() {
		TeachGitAuth, TeachGitLearned, TeachStateDir = oldA, oldL, oldD
		teachMu.Lock()
		teachBy = map[string]*teaching{}
		teachMu.Unlock()
	})
	TeachGitAuth = func(context.Context, string) []sandbox.GitAuth {
		return []sandbox.GitAuth{{Host: "forge.test", Learn: true, Username: "u", Secret: "s"}}
	}
	var learned [][]string
	TeachGitLearned = func(repo, key string, fetched []string) {
		learned = append(learned, slices.Sorted(slices.Values(fetched)))
	}
	SetTeachStateDir(t.TempDir())
	e := agentsandbox.Entry{Workspace: ws, Dispatch: "d-" + t.Name(), Repo: "o/app", StartHead: head,
		Cache: &agentsandbox.CacheBinding{Key: keycache.Key{Template: "{{.repo}}", Value: "o/app"}, Teach: true}}
	return e, func() [][]string { return learned }
}

func fetch(t *testing.T, e agentsandbox.Entry, repos ...string) {
	t.Helper()
	auth, ok := teachingGitAuth(e)
	if !ok || len(auth) != 1 {
		t.Fatalf("no teaching credential: %v %v", auth, ok)
	}
	for _, r := range repos {
		auth[0].OnFetch(r)
	}
}

// A teaching dispatch whose checkout is still at the head it started at
// teaches what it fetched; one whose checkout moved teaches nothing.
func TestTaughtGitRechecksTheHead(t *testing.T) {
	t.Run("unmoved", func(t *testing.T) {
		e, learned := teachRig(t)
		fetch(t, e, "/org/dep")
		TaughtGit(e.Dispatch, true)
		if got := learned(); len(got) != 1 || !slices.Equal(got[0], []string{"forge.test/org/dep"}) {
			t.Fatalf("learned %v", got)
		}
	})
	t.Run("moved", func(t *testing.T) {
		e, learned := teachRig(t)
		fetch(t, e, "/org/dep")
		gitIn(t, e.Workspace, "commit", "-q", "--allow-empty", "-m", "the agent's")
		TaughtGit(e.Dispatch, true)
		if got := learned(); len(got) != 0 {
			t.Fatalf("a checkout that left its default-branch head taught %v", got)
		}
	})
	t.Run("checked out elsewhere and back", func(t *testing.T) {
		e, learned := teachRig(t)
		gitIn(t, e.Workspace, "checkout", "-q", "-b", "pr")
		gitIn(t, e.Workspace, "commit", "-q", "--allow-empty", "-m", "pr")
		fetch(t, e, "/org/dep")
		TaughtGit(e.Dispatch, true)
		if got := learned(); len(got) != 0 {
			t.Fatalf("taught %v from another head", got)
		}
	})
	t.Run("no head recorded", func(t *testing.T) {
		e, _ := teachRig(t)
		e.StartHead = ""
		if _, ok := teachingGitAuth(e); ok {
			t.Fatal("a launch with no start head to check got a teaching credential")
		}
	})
}

// A restart in the middle of a teaching dispatch resumes its record: what
// its launches fetched before the restart is kept, not replaced by what
// the launches after fetched alone.
func TestTeachingSurvivesRestart(t *testing.T) {
	e, learned := teachRig(t)
	fetch(t, e, "/org/before")
	teachMu.Lock()
	teachBy = map[string]*teaching{} // a new conductor process
	teachMu.Unlock()
	fetch(t, e, "/org/after")
	TaughtGit(e.Dispatch, true)
	if got := learned(); len(got) != 1 || !slices.Equal(got[0], []string{"forge.test/org/after", "forge.test/org/before"}) {
		t.Fatalf("learned %v", got)
	}
	ents, _ := os.ReadDir(TeachStateDir)
	if len(ents) != 0 {
		t.Fatalf("the ended dispatch's record stayed: %v", ents)
	}
	// A failed step drops the record too.
	fetch(t, e, "/org/x")
	TaughtGit(e.Dispatch, false)
	if ents, _ := os.ReadDir(TeachStateDir); len(ents) != 0 || len(learned()) != 1 {
		t.Fatalf("a failed dispatch taught or kept its record (%v)", ents)
	}
}

// A cached launch's credential values — what its publish is scanned for —
// are the dispatch's credentials, its git fetch secrets, its skill token,
// and its credential-named environment; nothing else of the environment.
func TestLaunchSecretsCollectsTheLaunchsCredentials(t *testing.T) {
	req := &dispatch.Request{Credentials: dispatch.Credentials{
		Env:       map[string]string{"GH_TOKEN": "ghs_envvalue"},
		Templates: map[string]string{"api": "tmpl-secret-value"},
	}}
	pol := agentsandbox.Policy{Token: "skill-token-value", GitAuth: []sandbox.GitAuth{{Secret: "git-fetch-secret"}}}
	got := launchSecrets(req, pol, []string{"ANTHROPIC_API_KEY=sk-ant-value", "PATH=/usr/bin:/bin", "HOME=/home/x", "NPM_AUTH=npm-auth-value"})
	for _, want := range []string{"ghs_envvalue", "tmpl-secret-value", "skill-token-value", "git-fetch-secret", "sk-ant-value", "npm-auth-value"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s not collected: %v", want, got)
		}
	}
	for _, not := range []string{"/usr/bin:/bin", "/home/x"} {
		if slices.Contains(got, not) {
			t.Errorf("%s collected: %v", not, got)
		}
	}
}
