package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	ghint "github.com/NodeSpy/conductor/internal/integrations/github"
)

// fakeGH serves the PR head (movable, to simulate a push), GET /user, a
// review's node id + GraphQL addReaction, both comment reaction endpoints, and
// commit statuses — recording every write.
type fakeGH struct {
	mu       sync.Mutex
	head     string
	userHits int
	pullHits int
	calls    []string
}

var (
	reIssueReact  = regexp.MustCompile(`^/repos/org/repo/issues/comments/(\d+)/reactions$`)
	reReviewCReac = regexp.MustCompile(`^/repos/org/repo/pulls/comments/(\d+)/reactions$`)
	reReview      = regexp.MustCompile(`^/repos/org/repo/pulls/(\d+)/reviews/(\d+)$`)
	reStatus      = regexp.MustCompile(`^/repos/org/repo/statuses/(\w+)$`)
)

func newFakeGH(t *testing.T) *fakeGH {
	f := &fakeGH{head: "aaaaaaa1111"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/user":
			f.userHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo-me"})
		case r.Method == http.MethodGet && p == "/repos/org/repo/pulls/7":
			f.pullHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "open", "head": map[string]any{"sha": f.head}})
		case r.Method == http.MethodGet && p == "/repos/org/repo/pulls/7/reviews":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && reReview.MatchString(p):
			_ = json.NewEncoder(w).Encode(map[string]any{"node_id": "PRR_" + reReview.FindStringSubmatch(p)[2]})
		case r.Method == http.MethodPost && reIssueReact.MatchString(p):
			f.calls = append(f.calls, fmt.Sprintf("react issue_comment %s %v", reIssueReact.FindStringSubmatch(p)[1], body["content"]))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && reReviewCReac.MatchString(p):
			f.calls = append(f.calls, fmt.Sprintf("react review_comment %s %v", reReviewCReac.FindStringSubmatch(p)[1], body["content"]))
			w.WriteHeader(200) // already there: GitHub returns the existing reaction
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && p == "/graphql":
			vars, _ := body["variables"].(map[string]any)
			f.calls = append(f.calls, fmt.Sprintf("react review %s %v", strings.TrimPrefix(fmt.Sprint(vars["id"]), "PRR_"), vars["c"]))
			_, _ = w.Write([]byte(`{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`))
		case r.Method == http.MethodPost && reStatus.MatchString(p):
			f.calls = append(f.calls, fmt.Sprintf("status %s %v %v %v", reStatus.FindStringSubmatch(p)[1], body["state"], body["context"], body["description"]))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	return f
}

func (f *fakeGH) push(sha string) {
	f.mu.Lock()
	f.head = sha
	f.mu.Unlock()
}

func (f *fakeGH) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func subj(kind string, id int64) []any { return []any{map[string]any{"kind": kind, "id": id}} }

// react: every subject kind on its own endpoint (a review over GraphQL — REST
// has no review reactions), the single-subject shorthand, idempotent 200s
// fine, bad content/subjects refused.
func TestReactVerb(t *testing.T) {
	f := newFakeGH(t)
	g := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()
	ss := append(append(subj("review", 99), subj("issue_comment", 5)...), subj("review_comment", 42)...)
	out, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "pr": 7, "subjects": ss, "content": "eyes"})
	if err != nil || out["reacted"] != 3 {
		t.Fatalf("react: %v %v", out, err)
	}
	wantCalls(t, f.take(), "react review 99 EYES", "react issue_comment 5 eyes", "react review_comment 42 eyes")
	if _, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "kind": "issue_comment", "id": 9, "content": "+1"}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f.take(), "react issue_comment 9 +1")
	for _, bad := range []map[string]any{
		{"repo": "org/repo", "kind": "issue_comment", "id": 9, "content": "THUMBS_UP"},
		{"repo": "org/repo", "content": "eyes"},
		{"repo": "org/repo", "subjects": subj("review", 3), "content": "eyes"}, // a review needs pr
	} {
		if _, err := g.Invoke(ctx, "react", bad); err == nil {
			t.Fatalf("react accepted %v", bad)
		}
	}
}

// set_status pr: resolves the PR's head AT CALL TIME, fresh — a status after
// a push lands on the new commit, not a cached old one. sha wins over pr.
// The context is the caller's (templated or not); unset, it is the login.
func TestSetStatusPRResolvesCurrentHead(t *testing.T) {
	f := newFakeGH(t)
	g := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()
	call := func(opts map[string]any) map[string]any {
		t.Helper()
		opts["repo"] = "org/repo"
		out, err := g.Invoke(ctx, "set_status", opts)
		if err != nil {
			t.Fatalf("set_status %v: %v", opts, err)
		}
		return out
	}
	out := call(map[string]any{"pr": 7, "state": "pending", "context": "octo-me / review", "description": "working"})
	if out["sha"] != "aaaaaaa1111" {
		t.Fatalf("out.sha = %v", out["sha"])
	}
	f.push("bbbbbbb2222")
	call(map[string]any{"pr": 7, "state": "failure", "context": "octo-me / review", "description": "gave up"})
	call(map[string]any{"sha": "ccccccc3333", "pr": 7, "state": "success", "description": "explicit sha wins"})
	wantCalls(t, f.take(),
		"status aaaaaaa1111 pending octo-me / review working",
		"status bbbbbbb2222 failure octo-me / review gave up",
		"status ccccccc3333 success octo-me explicit sha wins")
	if f.pullHits != 2 {
		t.Fatalf("PR head reads = %d, want one fresh read per pr: call (none when sha is given)", f.pullHits)
	}
	// Fresh, not cached: a read verb cached the PR, then the head moved with
	// no write in between (a push from elsewhere) — pr: must still see it.
	if _, err := g.Invoke(ctx, "pr_get", map[string]any{"repo": "org/repo", "pr": 7}); err != nil {
		t.Fatal(err)
	}
	f.push("ddddddd4444")
	call(map[string]any{"pr": 7, "state": "pending", "context": "c"})
	if c := f.take(); len(c) != 1 || !strings.HasPrefix(c[0], "status ddddddd4444 ") {
		t.Fatalf("pr: after an outside push = %q — served a cached head", c)
	}
	if _, err := g.Invoke(ctx, "set_status", map[string]any{"repo": "org/repo", "state": "pending"}); err == nil {
		t.Fatal("set_status with neither sha nor pr succeeded")
	}
	if _, err := g.Invoke(ctx, "set_status", map[string]any{"repo": "org/repo", "sha": "abc", "state": "done"}); err == nil {
		t.Fatal("set_status accepted an unknown state")
	}
	long := strings.Repeat("x", 200)
	call(map[string]any{"sha": "abc", "state": "pending", "description": long})
	c := f.take()
	if len(c) != 1 || len([]rune(strings.TrimPrefix(c[0], "status abc pending octo-me "))) != 140 {
		t.Fatalf("description not clipped to 140: %q", c)
	}
	if f.userHits != 1 {
		t.Fatalf("GET /user hits = %d, want 1 (default context resolved once, cached)", f.userHits)
	}
}

// Every context set_status posts under — custom ones included — joins the
// source's own-status guard at call time, so a status a hook wrote is never
// read back as CI.
func TestSetStatusContextJoinsOwnStatusGuard(t *testing.T) {
	newFakeGH(t)
	g := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	spec := mkTriggerSpec(t, "gh.failing_checks", "fix", "")
	on := true
	spec.Enabled = &on
	src, err := g.Source([]CompiledTrigger{{Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	gi := src.(*ghint.Integration)
	for _, c := range []string{"octo-me / ci-fix", "Release Gate"} {
		if gi.OwnStatus(c) {
			t.Fatalf("%q known as own before anything posted it", c)
		}
		if _, err := g.Invoke(context.Background(), "set_status", map[string]any{"repo": "org/repo", "sha": "abc", "state": "failure", "context": c}); err != nil {
			t.Fatal(err)
		}
		if !gi.OwnStatus(c) || !gi.OwnStatus(strings.ToUpper(c)) {
			t.Fatalf("posted context %q didn't reach the source's own-status guard", c)
		}
	}
	// The default context (the login) joins too.
	if _, err := g.Invoke(context.Background(), "set_status", map[string]any{"repo": "org/repo", "sha": "abc", "state": "pending"}); err != nil {
		t.Fatal(err)
	}
	if !gi.OwnStatus("octo-me") {
		t.Fatal("the default (login) context didn't reach the guard")
	}
}

// TargetHead: a trusted github PR target's head, read fresh (never cached —
// it is compared across a push); nothing for a target the sender chose.
func TestTargetHead(t *testing.T) {
	f := newFakeGH(t)
	g := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	tr := core.Trigger{Source: "github", Instance: "gh", Kind: "new_comment", TargetTrusted: true,
		Target: core.Target{Repo: "org/repo", Number: 7}}
	if h, err := g.TargetHead(context.Background(), tr); err != nil || h != "aaaaaaa1111" {
		t.Fatalf("head = %q, %v", h, err)
	}
	f.push("bbbbbbb2222")
	if h, _ := g.TargetHead(context.Background(), tr); h != "bbbbbbb2222" {
		t.Fatalf("head after a push = %q — served a cached read", h)
	}
	forged := tr
	forged.TargetTrusted = false
	before := f.pullHits
	if h, _ := g.TargetHead(context.Background(), forged); h != "" || f.pullHits != before {
		t.Fatalf("an untrusted target was read (head %q)", h)
	}
}
