package connector

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	ghint "github.com/NodeSpy/conductor/internal/integrations/github"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// --- github lowering ---

func TestGithubSourceLowersTriggerFilters(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    type: github
    repos: ["org/*"]
    identity:
      write_token: literal-tok
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built cleanly: ok=%v reason=%q", ok, in.DisabledReason)
	}
	trig := CompiledTrigger{
		Index: 0,
		Spec: mkTriggerSpec("gh.review_requested", "myvariant", map[string]any{
			"repos":      []any{"org/repo1"},
			"reviewer":   map[string]any{"logins": []any{"alice"}},
			"gates":      map[string]any{"not_draft": false},
			"exclude":    map[string]any{"branches": []any{"release/*"}, "labels": []any{"wip"}, "title": []any{"WIP"}},
			"from_users": []any{"bob"},
		}),
	}
	trig.Spec.Options = map[string]any{
		"flaky_rerun": map[string]any{"enabled": true, "max": 3},
		"stuck_after": "45m",
	}
	result, err := in.Impl.Source([]CompiledTrigger{trig})
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	giInt, ok := result.(*ghint.Integration)
	if !ok {
		t.Fatalf("Source returned %T, want *github.Integration", result)
	}
	refs := giInt.Actions()
	if len(refs) != 1 {
		t.Fatalf("Actions() = %d entries, want 1: %+v", len(refs), refs)
	}
	act := refs[0].Action
	if act.Name != "myvariant" {
		t.Fatalf("act.Name = %q, want myvariant", act.Name)
	}
	if act.FlowRef != trig.Ref() {
		t.Fatalf("act.FlowRef = %q, want %q", act.FlowRef, trig.Ref())
	}
	if len(act.Repos) != 1 || act.Repos[0] != "org/repo1" {
		t.Fatalf("act.Repos = %v, want [org/repo1]", act.Repos)
	}
	if len(act.Reviewer.Logins) != 1 || act.Reviewer.Logins[0] != "alice" {
		t.Fatalf("act.Reviewer = %+v", act.Reviewer)
	}
	if len(act.FromUsers) != 1 || act.FromUsers[0] != "bob" {
		t.Fatalf("act.FromUsers = %v", act.FromUsers)
	}
	if act.Gates["not_draft"] != false {
		t.Fatalf("act.Gates = %v", act.Gates)
	}
	if len(act.Exclude.Branches) != 1 || act.Exclude.Branches[0] != "release/*" {
		t.Fatalf("act.Exclude.Branches = %v", act.Exclude.Branches)
	}
	if !act.FlakyRerun.Enabled || act.FlakyRerun.Max != 3 {
		t.Fatalf("act.FlakyRerun = %+v", act.FlakyRerun)
	}
	if act.StuckAfter.D().String() != "45m0s" {
		t.Fatalf("act.StuckAfter = %v", act.StuckAfter.D())
	}
}

// TestGithubSourceSweepSurvivesLowering: the connector's `sweep:` block rides
// into the lowered integration, with the connector-level repos: filling in
// when the sweep declares none.
func TestGithubSourceSweepSurvivesLowering(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    type: github
    repos: ["org/*"]
    sweep: { enabled: true, interval: 10m }
    identity:
      write_token: literal-tok
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, _ := reg.Get("gh")
	result, err := in.Impl.Source([]CompiledTrigger{{
		Spec: mkTriggerSpec("gh.release", "rel", nil),
	}})
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	sw := result.(*ghint.Integration).SweepSettings()
	if !sw.Enabled {
		t.Fatal("sweep.enabled lost in lowering")
	}
	if sw.Interval.D() != 10*time.Minute {
		t.Fatalf("sweep.interval = %v", sw.Interval.D())
	}
	if len(sw.Repos) != 1 || sw.Repos[0] != "org/*" {
		t.Fatalf("sweep.repos must fall back to the connector repos, got %v", sw.Repos)
	}
}

func TestGithubSourceRepoFallsBackToConnectorRepos(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    type: github
    repos: ["default/repo"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, _ := reg.Get("gh")
	trig := CompiledTrigger{Index: 0, Spec: mkTriggerSpec("gh.self_review", "", nil)}
	result, err := in.Impl.Source([]CompiledTrigger{trig})
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	refs := result.(*ghint.Integration).Actions()
	if len(refs) != 1 || len(refs[0].Action.Repos) != 1 || refs[0].Action.Repos[0] != "default/repo" {
		t.Fatalf("Actions() = %+v", refs)
	}
}

func TestGithubSourceNoTriggersReturnsNil(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    type: github
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, _ := reg.Get("gh")
	result, err := in.Impl.Source(nil)
	if err != nil || result != nil {
		t.Fatalf("Source(nil) = %v, %v; want nil, nil", result, err)
	}
}

// --- github verb HTTP tests ---

// mkTriggerSpec builds a minimal config.TriggerSpec for lowering tests.
func mkTriggerSpec(on, name string, filters map[string]any) config.TriggerSpec {
	return config.TriggerSpec{On: on, Name: name, Filters: filters}
}

func newGithubTestImpl(t *testing.T, extraYAML string) *githubImpl {
	t.Helper()
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    type: github
`+extraYAML)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built cleanly: ok=%v reason=%q", ok, in.DisabledReason)
	}
	impl, ok := in.Impl.(*githubImpl)
	if !ok {
		t.Fatalf("Impl is %T, want *githubImpl", in.Impl)
	}
	return impl
}

func TestGithubVerbCommentHTTP(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "html_url": "https://example/comment/42"})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)

	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	out, err := impl.Invoke(context.Background(), "comment", map[string]any{
		"repo": "org/repo", "number": 7, "body": "hello",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/repos/org/repo/issues/7/comments" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer literal-tok" {
		t.Fatalf("auth = %q, want Bearer literal-tok", gotAuth)
	}
	if gotBody["body"] != "hello" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["id"] != int64(42) {
		t.Fatalf("out.id = %v (%T)", out["id"], out["id"])
	}
	if out["url"] != "https://example/comment/42" {
		t.Fatalf("out.url = %v", out["url"])
	}
}

func TestGithubVerbReplyHTTP(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "html_url": "u"})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	_, err := impl.Invoke(context.Background(), "reply", map[string]any{
		"repo": "org/repo", "pr": 7, "in_reply_to": 3, "body": "hi",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/repos/org/repo/pulls/7/comments/3/replies" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestGithubVerbRerequestReviewHTTP(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	out, err := impl.Invoke(context.Background(), "rerequest_review", map[string]any{
		"repo": "org/repo", "pr": 7, "reviewers": []any{"alice"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/repos/org/repo/pulls/7/requested_reviewers" {
		t.Fatalf("path = %q", gotPath)
	}
	if out["ok"] != true {
		t.Fatalf("out = %v", out)
	}
	reviewers, _ := gotBody["reviewers"].([]any)
	if len(reviewers) != 1 || reviewers[0] != "alice" {
		t.Fatalf("body.reviewers = %v", gotBody)
	}
}

func TestGithubVerbSubmitReviewHTTP(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"id": 5})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	out, err := impl.Invoke(context.Background(), "submit_review", map[string]any{
		"repo": "org/repo", "pr": 7, "event": "APPROVE", "body": "lgtm",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/repos/org/repo/pulls/7/reviews" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["event"] != "APPROVE" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["id"] != int64(5) {
		t.Fatalf("out.id = %v", out["id"])
	}
}

func TestGithubVerbSubmitReviewInlineComments(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"id": 9})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")

	out, err := impl.Invoke(context.Background(), "submit_review", map[string]any{
		"repo": "org/repo", "pr": 7, "event": "REQUEST_CHANGES", "body": "see inline",
		"comments": []any{
			map[string]any{"path": "a.go", "line": 42, "body": "nil deref here"},
			map[string]any{"path": "b.go", "line": 10, "side": "RIGHT", "start_line": 8, "body": "tighten this range"},
		},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out["comments"] != 2 {
		t.Fatalf("out.comments = %v, want 2", out["comments"])
	}
	cs, ok := gotBody["comments"].([]any)
	if !ok || len(cs) != 2 {
		t.Fatalf("posted comments = %v", gotBody["comments"])
	}
	c0 := cs[0].(map[string]any)
	if c0["path"] != "a.go" || c0["body"] != "nil deref here" || c0["line"].(float64) != 42 {
		t.Fatalf("comment[0] = %v", c0)
	}
	c1 := cs[1].(map[string]any)
	if c1["side"] != "RIGHT" || c1["start_line"].(float64) != 8 {
		t.Fatalf("comment[1] multi-line fields = %v", c1)
	}
}

func TestReviewComments(t *testing.T) {
	// nil / empty → no comments, no error (a summary-only review).
	if got, err := reviewComments(nil); err != nil || got != nil {
		t.Fatalf("nil: got %v, %v", got, err)
	}
	// Coercion: line passes through, side/start_line optional, defaults omitted.
	got, err := reviewComments([]any{
		map[string]any{"path": "x.go", "line": 3, "body": "b"},
		map[string]any{"path": "y.go", "body": "file-level"}, // no line: a file comment
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["line"] != 3 || got[0]["path"] != "x.go" {
		t.Fatalf("coerced[0] = %v", got[0])
	}
	if _, hasLine := got[1]["line"]; hasLine {
		t.Fatalf("a comment with no line must not carry a zero line: %v", got[1])
	}
	// Missing path or body is an error — a comment must anchor somewhere and say something.
	if _, err := reviewComments([]any{map[string]any{"line": 1, "body": "b"}}); err == nil {
		t.Fatal("missing path must error")
	}
	if _, err := reviewComments([]any{map[string]any{"path": "x.go", "line": 1}}); err == nil {
		t.Fatal("missing body must error")
	}
	// Not a list → error.
	if _, err := reviewComments("nope"); err == nil {
		t.Fatal("non-list must error")
	}
}

func TestGithubVerbAddLabelsHTTP(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	out, err := impl.Invoke(context.Background(), "add_labels", map[string]any{
		"repo": "org/repo", "number": 7, "labels": []any{"bug", "p1"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/repos/org/repo/issues/7/labels" {
		t.Fatalf("path = %q", gotPath)
	}
	if out["ok"] != true {
		t.Fatalf("out = %v", out)
	}
	labels, _ := gotBody["labels"].([]any)
	if len(labels) != 2 {
		t.Fatalf("body.labels = %v", gotBody)
	}
}

// TestGithubVerbAsBotMintsInstallationToken is the `as: bot` green path: the
// verb call resolves the App installation for the repo (JWT-authenticated),
// mints an installation access token, and posts WITH THAT TOKEN — not the
// user's write token.
func TestGithubVerbAsBotMintsInstallationToken(t *testing.T) {
	var installAuth, mintAuth, commentAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/org/repo/installation":
			installAuth = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{"id": 555})
		case "POST /app/installations/555/access_tokens":
			mintAuth = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_minted", "expires_at": time.Now().Add(time.Hour).UTC(),
			})
		case "POST /repos/org/repo/issues/7/comments":
			commentAuth = r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "html_url": "https://example/c/9"})
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)

	impl := newGithubTestImpl(t, fmt.Sprintf(`
    app:
      app_id: 99
      private_key_path: %s
    identity:
      write_token: user-tok
`, writeTestRSAKey(t)))
	out, err := impl.Invoke(context.Background(), "comment", map[string]any{
		"repo": "org/repo", "number": 7, "body": "from the app", "as": "bot",
	})
	if err != nil {
		t.Fatalf("as: bot comment: %v", err)
	}
	// The App endpoints authenticate with the App JWT (an RS256 compact JWT).
	for name, auth := range map[string]string{"installation lookup": installAuth, "token mint": mintAuth} {
		if !strings.HasPrefix(auth, "Bearer eyJ") {
			t.Errorf("%s auth = %q, want an App JWT", name, auth)
		}
	}
	if commentAuth != "Bearer ghs_minted" {
		t.Fatalf("comment auth = %q, want the minted installation token (not the user token)", commentAuth)
	}
	if gotBody["body"] != "from the app" || out["id"] != int64(9) {
		t.Fatalf("body/out: %v / %v", gotBody, out)
	}
}

func writeTestRSAKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	p := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGithubVerbAsBotWithoutAppErrors(t *testing.T) {
	impl := newGithubTestImpl(t, "")
	_, err := impl.Invoke(context.Background(), "comment", map[string]any{
		"repo": "org/repo", "number": 1, "body": "x", "as": "bot",
	})
	if err == nil || !strings.Contains(err.Error(), "needs GitHub App credentials") {
		t.Fatalf("got %v, want App-credentials-required error", err)
	}
}

func TestGithubVerbErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"message": "not allowed"})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	_, err := impl.Invoke(context.Background(), "comment", map[string]any{
		"repo": "org/repo", "number": 1, "body": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("got %v, want error containing the API message", err)
	}
}

func TestGithubVerbRepoRequired(t *testing.T) {
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	_, err := impl.Invoke(context.Background(), "comment", map[string]any{"number": 1, "body": "x"})
	if err == nil || !strings.Contains(err.Error(), "options.repo is required") {
		t.Fatalf("got %v", err)
	}
}

// --- read verbs + cache + rate-limit ---

func TestGithubReadVerbs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org/repo/pulls/7" && r.Header.Get("Accept") == "application/vnd.github.diff":
			w.Write([]byte("diff --git a/x b/x\n+line"))
		case r.URL.Path == "/repos/org/repo/pulls/7":
			json.NewEncoder(w).Encode(map[string]any{
				"title": "T", "body": "B", "state": "open", "draft": false,
				"additions": 3, "deletions": 1, "changed_files": 2, "html_url": "u",
				"user":   map[string]any{"login": "alice"},
				"base":   map[string]any{"ref": "main"},
				"head":   map[string]any{"ref": "feat", "sha": "abc"},
				"labels": []any{map[string]any{"name": "bug"}},
			})
		case r.URL.Path == "/repos/org/repo/pulls/7/files":
			json.NewEncoder(w).Encode([]any{
				map[string]any{"filename": "x.go", "status": "modified", "additions": 2, "deletions": 1, "changes": 3},
			})
		case r.URL.Path == "/repos/org/repo/pulls/7/comments":
			json.NewEncoder(w).Encode([]any{
				map[string]any{"id": 11, "path": "x.go", "line": 0, "original_line": 9, "body": "old", "user": map[string]any{"login": "bob"}},
			})
		case r.URL.Path == "/repos/org/repo/contents/CLAUDE.md":
			w.Write([]byte("# standards"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()

	diff, err := impl.Invoke(ctx, "pr_diff", map[string]any{"repo": "org/repo", "pr": 7})
	if err != nil || !strings.Contains(diff["diff"].(string), "diff --git") {
		t.Fatalf("pr_diff = %v, %v", diff, err)
	}
	meta, err := impl.Invoke(ctx, "pr_get", map[string]any{"repo": "org/repo", "pr": 7})
	if err != nil || meta["title"] != "T" || meta["author"] != "alice" || meta["head_sha"] != "abc" {
		t.Fatalf("pr_get = %v, %v", meta, err)
	}
	if labels, _ := meta["labels"].([]string); len(labels) != 1 || labels[0] != "bug" {
		t.Fatalf("pr_get labels = %v", meta["labels"])
	}
	files, err := impl.Invoke(ctx, "pr_files", map[string]any{"repo": "org/repo", "pr": 7})
	fl, _ := files["files"].([]any)
	if err != nil || len(fl) != 1 || fl[0].(map[string]any)["path"] != "x.go" {
		t.Fatalf("pr_files = %v, %v", files, err)
	}
	rc, err := impl.Invoke(ctx, "review_comments", map[string]any{"repo": "org/repo", "pr": 7})
	cl, _ := rc["comments"].([]any)
	if err != nil || len(cl) != 1 || cl[0].(map[string]any)["line"] != 9 { // falls back to original_line
		t.Fatalf("review_comments = %v, %v", rc, err)
	}
	f, err := impl.Invoke(ctx, "file", map[string]any{"repo": "org/repo", "path": "CLAUDE.md"})
	if err != nil || f["text"] != "# standards" {
		t.Fatalf("file = %v, %v", f, err)
	}
}

func TestGithubReadCacheHit(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("the diff"))
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	for i := 0; i < 3; i++ {
		if _, err := impl.Invoke(context.Background(), "pr_diff", map[string]any{"repo": "org/repo", "pr": 7}); err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("3 identical reads hit the API %d times, want 1 (cached)", n)
	}
}

func TestGithubReadRevalidatesWithETag(t *testing.T) {
	var full, conditional int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&conditional, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("the diff"))
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	impl.cacheTTL = 0 // always revalidate → exercise the 304 path
	for i := 0; i < 3; i++ {
		out, err := impl.Invoke(context.Background(), "pr_diff", map[string]any{"repo": "org/repo", "pr": 7})
		if err != nil || out["diff"] != "the diff" {
			t.Fatalf("call %d: %v %v", i, out, err) // 304 must still serve the cached body
		}
	}
	if full != 1 || conditional != 2 {
		t.Fatalf("full=%d conditional=%d, want 1 full + 2 conditional (304)", full, conditional)
	}
}

func TestGithubRateLimit(t *testing.T) {
	// Pure helpers: classification + retry-after parsing.
	rl := &http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}}
	if !isRateLimited(rl) {
		t.Fatal("403 + remaining:0 should be rate-limited")
	}
	if isRateLimited(&http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": {"5"}}}) {
		t.Fatal("403 with remaining>0 is not a rate limit")
	}
	ra := &http.Response{Header: http.Header{"Retry-After": {"7"}}}
	if retryAfter(ra) != 7*time.Second {
		t.Fatalf("retryAfter(Retry-After: 7) = %v", retryAfter(ra))
	}

	// Integration: a rate-limited read with no cache errors clearly; with a
	// primed cache it serves stale rather than failing the caller.
	var mode int32 // 0 = serve, 1 = rate-limit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&mode) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1") // in the past → no wait
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("cached diff"))
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)

	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	impl.cacheTTL = 0 // force a revalidating request each call
	if _, err := impl.Invoke(context.Background(), "pr_diff", map[string]any{"repo": "org/repo", "pr": 7}); err != nil {
		t.Fatal(err) // prime the cache
	}
	atomic.StoreInt32(&mode, 1)
	out, err := impl.Invoke(context.Background(), "pr_diff", map[string]any{"repo": "org/repo", "pr": 7})
	if err != nil || out["diff"] != "cached diff" {
		t.Fatalf("rate-limited read should serve stale cache: %v %v", out, err)
	}

	// A fresh repo with no cached copy surfaces a clear rate-limit error.
	fresh := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	if _, err := fresh.Invoke(context.Background(), "pr_diff", map[string]any{"repo": "org/repo", "pr": 8}); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("uncached rate-limited read should error with 'rate limit', got %v", err)
	}
}

// --- PR/issue/repo/actions write + action verbs ---

func TestGithubWriteAndActionVerbs(t *testing.T) {
	type rec struct {
		method, path string
		body         map[string]any
	}
	var reqs []rec
	// A superset response body that decodes for every verb's output struct.
	resp := map[string]any{
		"number": 123, "html_url": "u", "merged": true, "sha": "deadbeef", "state": "closed",
		"assignees": []any{map[string]any{"login": "alice"}},
		"labels":    []any{map[string]any{"name": "bug"}},
		"title":     "T", "body": "B",
		"user":          map[string]any{"login": "octo"},
		"content":       map[string]any{"sha": "blob1"},
		"commit":        map[string]any{"sha": "c1"},
		"object":        map[string]any{"sha": "ref1"},
		"workflow_runs": []any{map[string]any{"id": 9, "name": "CI", "status": "completed", "conclusion": "success", "head_branch": "main", "head_sha": "h", "html_url": "ru"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		reqs = append(reqs, rec{r.Method, r.URL.Path, b})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()

	last := func() rec { return reqs[len(reqs)-1] }

	// create_pr
	out, err := impl.Invoke(ctx, "create_pr", map[string]any{"repo": "o/r", "title": "T", "head": "feat", "base": "main", "draft": true})
	if err != nil || out["number"] != int64(123) {
		t.Fatalf("create_pr: %v %v", out, err)
	}
	if r := last(); r.method != "POST" || r.path != "/repos/o/r/pulls" || r.body["head"] != "feat" || r.body["draft"] != true {
		t.Fatalf("create_pr req: %+v", r)
	}
	// merge_pr
	if out, err := impl.Invoke(ctx, "merge_pr", map[string]any{"repo": "o/r", "pr": 7, "method": "squash"}); err != nil || out["merged"] != true {
		t.Fatalf("merge_pr: %v %v", out, err)
	}
	if r := last(); r.method != "PUT" || r.path != "/repos/o/r/pulls/7/merge" || r.body["merge_method"] != "squash" {
		t.Fatalf("merge_pr req: %+v", r)
	}
	// update_pr (close)
	if _, err := impl.Invoke(ctx, "update_pr", map[string]any{"repo": "o/r", "pr": 7, "state": "closed"}); err != nil {
		t.Fatalf("update_pr: %v", err)
	}
	if r := last(); r.method != "PATCH" || r.path != "/repos/o/r/pulls/7" || r.body["state"] != "closed" {
		t.Fatalf("update_pr req: %+v", r)
	}
	// create_issue
	if out, err := impl.Invoke(ctx, "create_issue", map[string]any{"repo": "o/r", "title": "bug", "labels": []any{"a", "b"}}); err != nil || out["number"] != int64(123) {
		t.Fatalf("create_issue: %v %v", out, err)
	}
	if r := last(); r.path != "/repos/o/r/issues" || len(r.body["labels"].([]any)) != 2 {
		t.Fatalf("create_issue req: %+v", r)
	}
	// update_issue (close as not_planned)
	if _, err := impl.Invoke(ctx, "update_issue", map[string]any{"repo": "o/r", "number": 5, "state": "closed", "state_reason": "not_planned"}); err != nil {
		t.Fatalf("update_issue: %v", err)
	}
	if r := last(); r.method != "PATCH" || r.path != "/repos/o/r/issues/5" || r.body["state_reason"] != "not_planned" {
		t.Fatalf("update_issue req: %+v", r)
	}
	// assign (add + remove → POST then DELETE)
	if out, err := impl.Invoke(ctx, "assign", map[string]any{"repo": "o/r", "number": 5, "add": []any{"alice"}, "remove": []any{"bob"}}); err != nil {
		t.Fatalf("assign: %v %v", out, err)
	} else if as, _ := out["assignees"].([]string); len(as) != 1 || as[0] != "alice" {
		t.Fatalf("assign out: %v", out)
	}
	if r := last(); r.method != "DELETE" || r.path != "/repos/o/r/issues/5/assignees" {
		t.Fatalf("assign remove req: %+v", r)
	}
	// remove_label (label path-escaped)
	if _, err := impl.Invoke(ctx, "remove_label", map[string]any{"repo": "o/r", "number": 5, "label": "needs review"}); err != nil {
		t.Fatalf("remove_label: %v", err)
	}
	if r := last(); r.method != "DELETE" || r.path != "/repos/o/r/issues/5/labels/needs review" { // server decodes %20
		t.Fatalf("remove_label req: %+v", r)
	}
	// get_issue
	if out, err := impl.Invoke(ctx, "get_issue", map[string]any{"repo": "o/r", "number": 5}); err != nil || out["author"] != "octo" {
		t.Fatalf("get_issue: %v %v", out, err)
	}
	// put_file (base64-encodes content)
	if out, err := impl.Invoke(ctx, "put_file", map[string]any{"repo": "o/r", "path": "x.md", "content": "hi", "message": "add"}); err != nil || out["sha"] != "blob1" || out["commit"] != "c1" {
		t.Fatalf("put_file: %v %v", out, err)
	}
	if r := last(); r.method != "PUT" || r.path != "/repos/o/r/contents/x.md" || r.body["content"] != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Fatalf("put_file req: %+v", r)
	}
	// delete_file
	if out, err := impl.Invoke(ctx, "delete_file", map[string]any{"repo": "o/r", "path": "x.md", "message": "rm", "sha": "blob1"}); err != nil || out["commit"] != "c1" {
		t.Fatalf("delete_file: %v %v", out, err)
	}
	if r := last(); r.method != "DELETE" || r.path != "/repos/o/r/contents/x.md" {
		t.Fatalf("delete_file req: %+v", r)
	}
	// get_ref
	if out, err := impl.Invoke(ctx, "get_ref", map[string]any{"repo": "o/r", "ref": "main"}); err != nil || out["sha"] != "deadbeef" {
		t.Fatalf("get_ref: %v %v", out, err)
	}
	// create_branch (GET commits/HEAD then POST git/refs)
	if out, err := impl.Invoke(ctx, "create_branch", map[string]any{"repo": "o/r", "branch": "feat-x"}); err != nil || out["sha"] != "ref1" {
		t.Fatalf("create_branch: %v %v", out, err)
	}
	if r := last(); r.method != "POST" || r.path != "/repos/o/r/git/refs" || r.body["ref"] != "refs/heads/feat-x" {
		t.Fatalf("create_branch req: %+v", r)
	}
	// dispatch_workflow
	if _, err := impl.Invoke(ctx, "dispatch_workflow", map[string]any{"repo": "o/r", "workflow": "ci.yml", "ref": "main", "inputs": map[string]any{"env": "prod"}}); err != nil {
		t.Fatalf("dispatch_workflow: %v", err)
	}
	if r := last(); r.path != "/repos/o/r/actions/workflows/ci.yml/dispatches" || r.body["ref"] != "main" {
		t.Fatalf("dispatch_workflow req: %+v", r)
	}
	// rerun_run (failed_only → rerun-failed-jobs)
	if _, err := impl.Invoke(ctx, "rerun_run", map[string]any{"repo": "o/r", "run_id": 99, "failed_only": true}); err != nil {
		t.Fatalf("rerun_run: %v", err)
	}
	if r := last(); r.path != "/repos/o/r/actions/runs/99/rerun-failed-jobs" {
		t.Fatalf("rerun_run req: %+v", r)
	}
	// cancel_run
	if _, err := impl.Invoke(ctx, "cancel_run", map[string]any{"repo": "o/r", "run_id": 99}); err != nil {
		t.Fatalf("cancel_run: %v", err)
	}
	if r := last(); r.path != "/repos/o/r/actions/runs/99/cancel" {
		t.Fatalf("cancel_run req: %+v", r)
	}
	// list_runs
	if out, err := impl.Invoke(ctx, "list_runs", map[string]any{"repo": "o/r", "branch": "main"}); err != nil {
		t.Fatalf("list_runs: %v", err)
	} else if runs, _ := out["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["conclusion"] != "success" {
		t.Fatalf("list_runs out: %v", out)
	}
}

// A write invalidates the read cache, so a mutate-then-read never serves stale.
func TestGithubWriteInvalidatesCache(t *testing.T) {
	var diffHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			atomic.AddInt32(&diffHits, 1)
			w.Write([]byte("d"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()

	impl.Invoke(ctx, "pr_diff", map[string]any{"repo": "o/r", "pr": 7}) // caches
	impl.Invoke(ctx, "pr_diff", map[string]any{"repo": "o/r", "pr": 7}) // cache hit
	if n := atomic.LoadInt32(&diffHits); n != 1 {
		t.Fatalf("before write: %d GETs, want 1", n)
	}
	impl.Invoke(ctx, "update_pr", map[string]any{"repo": "o/r", "pr": 7, "state": "closed"}) // write → invalidate
	impl.Invoke(ctx, "pr_diff", map[string]any{"repo": "o/r", "pr": 7})                      // must refetch
	if n := atomic.LoadInt32(&diffHits); n != 2 {
		t.Fatalf("after write the cache must be cold: %d GETs, want 2", n)
	}
}

func TestGithubReviewRequestVerbs(t *testing.T) {
	var last struct {
		method, path string
		body         map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.method, last.path = r.Method, r.URL.Path
		last.body = nil
		json.NewDecoder(r.Body).Decode(&last.body)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()

	// request_review → POST requested_reviewers with the reviewers.
	if _, err := impl.Invoke(ctx, "request_review", map[string]any{"repo": "o/r", "pr": 7, "reviewers": []any{"alice"}, "team_reviewers": []any{"platform"}}); err != nil {
		t.Fatal(err)
	}
	if last.method != "POST" || last.path != "/repos/o/r/pulls/7/requested_reviewers" {
		t.Fatalf("request_review req: %+v", last)
	}
	if rs, _ := last.body["reviewers"].([]any); len(rs) != 1 || rs[0] != "alice" {
		t.Fatalf("request_review body: %+v", last.body)
	}
	// rerequest_review is the same endpoint (back-compat alias).
	if _, err := impl.Invoke(ctx, "rerequest_review", map[string]any{"repo": "o/r", "pr": 7, "reviewers": []any{"bob"}}); err != nil {
		t.Fatal(err)
	}
	if last.method != "POST" || last.path != "/repos/o/r/pulls/7/requested_reviewers" {
		t.Fatalf("rerequest_review req: %+v", last)
	}
	// remove_reviewer → DELETE the same endpoint.
	if _, err := impl.Invoke(ctx, "remove_reviewer", map[string]any{"repo": "o/r", "pr": 7, "reviewers": []any{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if last.method != "DELETE" || last.path != "/repos/o/r/pulls/7/requested_reviewers" {
		t.Fatalf("remove_reviewer req: %+v", last)
	}
	// Nothing to request is an error.
	if _, err := impl.Invoke(ctx, "request_review", map[string]any{"repo": "o/r", "pr": 7}); err == nil {
		t.Fatal("request_review with no reviewers must error")
	}
}

func TestGithubReleaseSearchChecksDraftVerbs(t *testing.T) {
	var last struct {
		method, path, ct string
		body             map[string]any
		raw              string
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.method, last.path, last.ct = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		last.body, last.raw = nil, ""
		if last.ct == "application/octet-stream" {
			b, _ := io.ReadAll(r.Body)
			last.raw = string(b)
		} else {
			json.NewDecoder(r.Body).Decode(&last.body)
		}
		// list_issues wants a top-level array; everything else an object.
		if r.Method == "GET" && r.URL.Path == "/repos/o/r/issues" {
			json.NewEncoder(w).Encode([]any{
				map[string]any{"number": 1, "title": "real", "state": "open", "html_url": "iu", "labels": []any{map[string]any{"name": "bug"}}, "user": map[string]any{"login": "octo"}},
				map[string]any{"number": 2, "title": "a pr", "state": "open", "pull_request": map[string]any{}}, // filtered out
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 55, "html_url": "u", "upload_url": srv.URL + "/uploads/repos/o/r/releases/55/assets{?name,label}",
			"browser_download_url": "dl", "node_id": "PR_node",
			"total_count": 1,
			"items":       []any{map[string]any{"number": 3, "title": "t", "state": "open", "html_url": "iu", "pull_request": map[string]any{}}},
			"check_runs":  []any{map[string]any{"name": "build", "status": "completed", "conclusion": "success", "html_url": "cu"}},
			"data":        map[string]any{"ok": true},
		})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	impl := newGithubTestImpl(t, "\n    identity:\n      write_token: literal-tok\n")
	ctx := context.Background()

	// create_release
	if out, err := impl.Invoke(ctx, "create_release", map[string]any{"repo": "o/r", "tag": "v1", "name": "One", "prerelease": true}); err != nil || out["id"] != int64(55) {
		t.Fatalf("create_release: %v %v", out, err)
	}
	if last.method != "POST" || last.path != "/repos/o/r/releases" || last.body["tag_name"] != "v1" || last.body["prerelease"] != true {
		t.Fatalf("create_release req: %+v", last)
	}
	// upload_asset (GET release for upload_url, then raw POST to it)
	if out, err := impl.Invoke(ctx, "upload_asset", map[string]any{"repo": "o/r", "release_id": 55, "name": "a.txt", "content": "hi"}); err != nil || out["url"] != "dl" {
		t.Fatalf("upload_asset: %v %v", out, err)
	}
	if last.path != "/uploads/repos/o/r/releases/55/assets" || last.ct != "application/octet-stream" || last.raw != "hi" {
		t.Fatalf("upload_asset req: %+v", last)
	}
	// list_issues
	if out, err := impl.Invoke(ctx, "list_issues", map[string]any{"repo": "o/r", "state": "open"}); err != nil {
		t.Fatalf("list_issues: %v", err)
	} else if is := out["issues"].([]any); len(is) != 1 || is[0].(map[string]any)["title"] != "real" { // the PR is filtered out
		t.Fatalf("list_issues must drop PRs: %v", out)
	}
	// search_issues (scoped to repo; item flagged is_pr)
	if out, err := impl.Invoke(ctx, "search_issues", map[string]any{"repo": "o/r", "q": "is:open label:bug"}); err != nil || out["total"] != 1 {
		t.Fatalf("search_issues: %v %v", out, err)
	} else if it := out["items"].([]any)[0].(map[string]any); it["is_pr"] != true {
		t.Fatalf("search item: %v", it)
	}
	if !strings.Contains(last.path, "/search/issues") {
		t.Fatalf("search path: %q", last.path)
	}
	// checks
	if out, err := impl.Invoke(ctx, "checks", map[string]any{"repo": "o/r", "ref": "main"}); err != nil {
		t.Fatalf("checks: %v", err)
	} else if cs := out["checks"].([]any); len(cs) != 1 || cs[0].(map[string]any)["conclusion"] != "success" {
		t.Fatalf("checks out: %v", out)
	}
	// ready_for_review (GET pull for node_id, then GraphQL)
	if _, err := impl.Invoke(ctx, "ready_for_review", map[string]any{"repo": "o/r", "pr": 7}); err != nil {
		t.Fatalf("ready_for_review: %v", err)
	}
	if last.method != "POST" || last.path != "/graphql" || !strings.Contains(last.body["query"].(string), "markPullRequestReadyForReview") {
		t.Fatalf("ready_for_review req: %+v", last)
	}
	// convert_to_draft
	if _, err := impl.Invoke(ctx, "convert_to_draft", map[string]any{"repo": "o/r", "pr": 7}); err != nil {
		t.Fatalf("convert_to_draft: %v", err)
	}
	if !strings.Contains(last.body["query"].(string), "convertPullRequestToDraft") {
		t.Fatalf("convert_to_draft query: %v", last.body)
	}
}
