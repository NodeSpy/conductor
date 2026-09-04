package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	ghint "github.com/NodeSpy/paseo-conductor/internal/integrations/github"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
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
