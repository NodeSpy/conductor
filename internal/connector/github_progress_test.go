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
	"github.com/NodeSpy/conductor/internal/secrets"
)

// fakeGH is just enough of the GitHub API for run progress: the PR's head
// (movable, to simulate a push), GET /user, reactions on both comment kinds,
// a review's node id + GraphQL addReaction, and commit statuses.
type fakeGH struct {
	mu       sync.Mutex
	head     string
	state    string
	login    string
	userHits int
	// failAll makes every write 500 (reads still answer).
	failAll bool
	// calls is every write, in order: "react <kind> <id> <content>" /
	// "status <sha> <state> <context> <description>".
	calls []string
}

var (
	reIssueReact  = regexp.MustCompile(`^/repos/org/repo/issues/comments/(\d+)/reactions$`)
	reReviewCReac = regexp.MustCompile(`^/repos/org/repo/pulls/comments/(\d+)/reactions$`)
	reReview      = regexp.MustCompile(`^/repos/org/repo/pulls/(\d+)/reviews/(\d+)$`)
	reStatus      = regexp.MustCompile(`^/repos/org/repo/statuses/(\w+)$`)
)

func newFakeGH(t *testing.T) (*fakeGH, *httptest.Server) {
	f := &fakeGH{head: "aaaaaaa1111", state: "open", login: "octo-me"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		if r.Method == http.MethodPost && f.failAll {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && p == "/user":
			f.userHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"login": f.login})
		case r.Method == http.MethodGet && p == "/repos/org/repo/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": f.state, "head": map[string]any{"sha": f.head}})
		case r.Method == http.MethodGet && reReview.MatchString(p):
			m := reReview.FindStringSubmatch(p)
			_ = json.NewEncoder(w).Encode(map[string]any{"node_id": "PRR_" + m[2]})
		case r.Method == http.MethodPost && reIssueReact.MatchString(p):
			f.calls = append(f.calls, fmt.Sprintf("react issue_comment %s %v", reIssueReact.FindStringSubmatch(p)[1], body["content"]))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && reReviewCReac.MatchString(p):
			f.calls = append(f.calls, fmt.Sprintf("react review_comment %s %v", reReviewCReac.FindStringSubmatch(p)[1], body["content"]))
			w.WriteHeader(200) // already there: GitHub returns the existing one
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && p == "/graphql":
			vars, _ := body["variables"].(map[string]any)
			if !strings.Contains(fmt.Sprint(body["query"]), "addReaction") {
				t.Errorf("unexpected graphql: %v", body["query"])
			}
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
	return f, srv
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

// progImplWith builds a github connector with a literal write token and the
// given extra connection YAML, and an audit sink.
func progImplWith(t *testing.T, extra string) (*githubImpl, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	audits := &[]map[string]any{}
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    use: github
    identity:
      write_token: literal-tok
`+extra)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Audit: func(m map[string]any) {
		mu.Lock()
		*audits = append(*audits, m)
		mu.Unlock()
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built: %v %q", ok, in.DisabledReason)
	}
	return in.Impl.(*githubImpl), audits
}

func ghTrig(kind string, ctx map[string]any) core.Trigger {
	return core.Trigger{Source: "github", Instance: "gh", Kind: kind, TargetTrusted: true,
		Target: core.Target{Repo: "org/repo", Number: 7, HeadSHA: "stale"}, Context: ctx}
}

func subj(kind string, id int64) []any { return []any{map[string]any{"kind": kind, "id": id}} }

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

var outcomeOK = RunOutcome{Result: OutcomeOK}

// A review run that pushes: 👀 on the review at start (GraphQL — a review has
// no REST reactions endpoint) and a pending status as your login on the head
// as it is NOW (not the event's stale one); then 🚀, success on the NEW head,
// and the old head's pending resolved.
func TestProgressReviewPushed(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("changes_requested",
		map[string]any{"author": "alice", "review_id": int64(99), "reaction_subjects": subj("review", 99)})})
	if p == nil {
		t.Fatal("no progress for a changes_requested run")
	}
	wantCalls(t, f.take(),
		"react review 99 EYES",
		"status aaaaaaa1111 pending octo-me addressing alice's review")
	f.push("bbbbbbb2222")
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(),
		"react review 99 ROCKET",
		"status bbbbbbb2222 success octo-me pushed bbbbbbb",
		"status aaaaaaa1111 success octo-me pushed bbbbbbb")
}

// A standalone comment answered without a push: 👀 then 👍 on that comment,
// one success on the unchanged head.
func TestProgressCommentNoPush(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment",
		map[string]any{"author": "bob", "reaction_subjects": subj("issue_comment", 5)})})
	wantCalls(t, f.take(),
		"react issue_comment 5 eyes",
		"status aaaaaaa1111 pending octo-me replying to bob's comment")
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(),
		"react issue_comment 5 +1",
		"status aaaaaaa1111 success octo-me done — no changes pushed")
}

// An inline review comment reacts on the review-comments endpoint; a failed
// run gets 😕 and a failure status naming the (public-safe) reason.
func TestProgressReviewCommentFailed(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment",
		map[string]any{"author": "bob", "reaction_subjects": subj("review_comment", 42)})})
	f.take()
	p.Finish(context.Background(), RunOutcome{Result: OutcomeFailed, Reason: "the agent couldn't be started"})
	wantCalls(t, f.take(),
		"react review_comment 42 confused",
		"status aaaaaaa1111 failure octo-me gave up: the agent couldn't be started")
}

// A sweep-recovered threads run reacts on every thread opener it carries.
func TestProgressSweepThreads(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	ss := append(subj("review_comment", 1), subj("review_comment", 2)...)
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("changes_requested",
		map[string]any{"author": "alice", "reaction_subjects": ss})})
	wantCalls(t, f.take(),
		"react review_comment 1 eyes", "react review_comment 2 eyes",
		"status aaaaaaa1111 pending octo-me addressing unresolved review threads")
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(),
		"react review_comment 1 +1", "react review_comment 2 +1",
		"status aaaaaaa1111 success octo-me done — no changes pushed")
}

// An event with no comment or review (failing checks) gets no reaction — but
// still a status, since every PR-targeted run is visible on the PR.
func TestProgressSubjectlessStatusOnly(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("failing_checks",
		map[string]any{"failing_check": "build"})})
	wantCalls(t, f.take(), "status aaaaaaa1111 pending octo-me fixing failing check build")
	f.push("ccccccc3333")
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(),
		"status ccccccc3333 success octo-me pushed ccccccc",
		"status aaaaaaa1111 success octo-me pushed ccccccc")
	for _, kind := range []string{"merge_conflict"} {
		p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig(kind, nil)})
		wantCalls(t, f.take(), "status ccccccc3333 pending octo-me resolving merge conflict")
		p.Finish(context.Background(), RunOutcome{Result: OutcomeFailed, Reason: "timed out"})
		wantCalls(t, f.take(), "status ccccccc3333 failure octo-me gave up: timed out")
	}
}

// The status context is your login, looked up once (GET /user), not per run.
func TestProgressContextIsLoginCached(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	for i := 0; i < 3; i++ {
		g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)}).Finish(context.Background(), outcomeOK)
	}
	if f.userHits != 1 {
		t.Fatalf("GET /user hits = %d, want 1 (cached)", f.userHits)
	}
	// A configured context wins and never asks.
	f2, _ := newFakeGH(t)
	g2, _ := progImplWith(t, "    progress: { status_context: my-autopilot }\n")
	g2.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)})
	wantCalls(t, f2.take(), "status aaaaaaa1111 pending my-autopilot resolving merge conflict")
	if f2.userHits != 0 {
		t.Fatalf("GET /user hits = %d with a configured context, want 0", f2.userHits)
	}
}

// The knobs: connector-wide off, per-trigger off (each face separately), the
// non-autopilot kinds off by default, and a trigger opting one in.
func TestProgressKnobs(t *testing.T) {
	f, _ := newFakeGH(t)
	comment := map[string]any{"author": "bob", "reaction_subjects": subj("issue_comment", 5)}

	g, _ := progImplWith(t, "    progress: { reactions: false, status: false }\n")
	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment", comment)}); p != nil {
		t.Fatal("connector-wide off still started progress")
	}
	wantCalls(t, f.take())

	g, _ = progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment", comment),
		Options: map[string]any{"status": false}})
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(), "react issue_comment 5 eyes", "react issue_comment 5 +1")

	p = g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment", comment),
		Options: map[string]any{"reactions": false}})
	p.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(),
		"status aaaaaaa1111 pending octo-me replying to bob's comment",
		"status aaaaaaa1111 success octo-me done — no changes pushed")

	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment", comment),
		Options: map[string]any{"reactions": false, "status": false}}); p != nil {
		t.Fatal("trigger-level off still started progress")
	}
	// review_requested is someone else's PR: off unless a trigger asks.
	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("review_requested", nil)}); p != nil {
		t.Fatal("review_requested got progress by default")
	}
	g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("review_requested", nil),
		Options: map[string]any{"status": true}})
	wantCalls(t, f.take(), "status aaaaaaa1111 pending octo-me working on this PR")
	// A target the event's sender chose: progress writes to the repo, so none.
	forged := ghTrig("new_comment", comment)
	forged.TargetTrusted = false
	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: forged}); p != nil {
		t.Fatal("an untrusted target got progress")
	}
	wantCalls(t, f.take())
	// Not a github event, no PR: nothing.
	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: core.Trigger{Source: "slack", Kind: "new_comment"}}); p != nil {
		t.Fatal("non-github trigger got progress")
	}
}

// A closed PR shows nothing; a PR that closes under the run resolves its
// pending instead of leaving it forever.
func TestProgressClosedPR(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)})
	f.take()
	p.Finish(context.Background(), RunOutcome{Result: OutcomeStopped})
	wantCalls(t, f.take(), "status aaaaaaa1111 success octo-me stopped — the PR closed")
	f.mu.Lock()
	f.state = "closed"
	f.mu.Unlock()
	if p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)}); p != nil {
		t.Fatal("progress started on a closed PR")
	}
}

// Fail-soft: every write failing is logged + audited, never a panic, and the
// run's lifecycle carries on.
func TestProgressAPIFailureIsSoft(t *testing.T) {
	f, _ := newFakeGH(t)
	f.failAll = true
	g, audits := progImplWith(t, "")
	p := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment",
		map[string]any{"reaction_subjects": subj("issue_comment", 5)})})
	if p == nil {
		t.Fatal("a failing reaction API must not stop the run's progress handle")
	}
	p.Finish(context.Background(), outcomeOK)
	var whats []string
	for _, a := range *audits {
		if a["event"] == "progress" && a["outcome"] == "failed" {
			whats = append(whats, fmt.Sprint(a["what"]))
		}
	}
	if got := strings.Join(whats, ","); got != "react:eyes,status:pending,react:+1,status:success" {
		t.Fatalf("audited failures = %q", got)
	}
}

// Concurrency: the most recently started run owns the PR's status row. An
// older run finishing afterwards never overwrites the newer one's pending on
// the current head — it only resolves its own, different, starting commit.
func TestProgressNewerRunOwnsTheRow(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	start := func(kind string) Progress {
		return g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig(kind, nil)})
	}

	// Same head: the older run finishing writes nothing.
	a := start("merge_conflict")
	b := start("failing_checks")
	f.take()
	a.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take())
	b.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(), "status aaaaaaa1111 success octo-me done — no changes pushed")

	// Older run A starts on head1, pushes head2; B starts on head2. A ends
	// after B started: head2 is B's, so A only resolves head1.
	a = start("merge_conflict")
	f.push("bbbbbbb2222")
	b = start("failing_checks")
	f.take()
	a.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(), "status aaaaaaa1111 success octo-me pushed bbbbbbb")
	b.Finish(context.Background(), RunOutcome{Result: OutcomeFailed, Reason: "timed out"})
	wantCalls(t, f.take(), "status bbbbbbb2222 failure octo-me gave up: timed out")

	// And when the newer run finished FIRST, the older one still yields.
	a = start("merge_conflict")
	b = start("failing_checks")
	f.take()
	b.Finish(context.Background(), outcomeOK)
	f.take()
	a.Finish(context.Background(), RunOutcome{Result: OutcomeFailed, Reason: "x"})
	wantCalls(t, f.take())
}

// A run with its status switched off doesn't take the row: an older
// status-posting run on the same PR still writes its verdict on the head.
func TestProgressStatuslessRunDoesNotTakeTheRow(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	a := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)})
	b := g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("new_comment",
		map[string]any{"reaction_subjects": subj("issue_comment", 5)}), Options: map[string]any{"status": false}})
	f.take()
	b.Finish(context.Background(), outcomeOK)
	a.Finish(context.Background(), outcomeOK)
	wantCalls(t, f.take(), "react issue_comment 5 +1", "status aaaaaaa1111 success octo-me done — no changes pushed")
}

// The resolved status context joins the source's own-status guard, so a
// status conductor posted never comes back as CI.
func TestProgressContextFeedsOwnStatusGuard(t *testing.T) {
	newFakeGH(t)
	g, _ := progImplWith(t, "")
	spec := mkTriggerSpec(t, "gh.failing_checks", "fix", "")
	spec.Enabled = boolp(true)
	src, err := g.Source([]CompiledTrigger{{Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	gi := src.(*ghint.Integration)
	if gi.OwnStatus("octo-me") {
		t.Fatal("login known as own before progress resolved it")
	}
	g.StartProgress(context.Background(), ProgressRun{Trigger: ghTrig("merge_conflict", nil)})
	if !gi.OwnStatus("octo-me") || !gi.OwnStatus("OCTO-ME") {
		t.Fatal("the resolved status context didn't reach the source's own-status guard")
	}
}

// A misspelled progress key fails loading instead of silently leaving the
// feature on.
func TestProgressKeysValidated(t *testing.T) {
	cfg := mustDecodeConfig(t, "connectors:\n  gh:\n    use: github\n    progress: { reaction: false }\n")
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err == nil {
		if in, _ := reg.Get("gh"); in == nil || in.DisabledReason == "" {
			t.Fatal("a misspelled connector progress key was accepted")
		}
	}
	g, _ := progImplWith(t, "")
	spec := mkTriggerSpec(t, "gh.new_comment", "c", "")
	spec.Options = map[string]any{"progress": map[string]any{"status": "nope"}}
	if _, err := g.Source([]CompiledTrigger{{Spec: spec}}); err == nil || !strings.Contains(err.Error(), "options.progress") {
		t.Fatalf("a bad trigger progress value: err = %v", err)
	}
	spec.Options = map[string]any{"progress": map[string]any{"status_context": "x"}}
	if _, err := g.Source([]CompiledTrigger{{Spec: spec}}); err == nil {
		t.Fatal("status_context per trigger was accepted")
	}
}

func boolp(b bool) *bool { return &b }

// The verbs themselves, as a flow would call them.
func TestReactAndSetStatusVerbs(t *testing.T) {
	f, _ := newFakeGH(t)
	g, _ := progImplWith(t, "")
	ctx := context.Background()
	// Single-subject shorthand.
	if out, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "kind": "issue_comment", "id": 9, "content": "heart"}); err != nil || out["reacted"] != 1 {
		t.Fatalf("react shorthand: %v %v", out, err)
	}
	wantCalls(t, f.take(), "react issue_comment 9 heart")
	if _, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "kind": "issue_comment", "id": 9, "content": "THUMBS_UP"}); err == nil {
		t.Fatal("react accepted a GraphQL-spelled content")
	}
	if _, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "content": "eyes"}); err == nil {
		t.Fatal("react with no subject succeeded")
	}
	if _, err := g.Invoke(ctx, "react", map[string]any{"repo": "org/repo", "subjects": subj("review", 3), "content": "eyes"}); err == nil {
		t.Fatal("a review subject without pr succeeded")
	}
	// set_status: default context is the login; description clipped to 140.
	long := strings.Repeat("x", 200)
	out, err := g.Invoke(ctx, "set_status", map[string]any{"repo": "org/repo", "sha": "abc", "state": "pending", "description": long})
	if err != nil || out["context"] != "octo-me" {
		t.Fatalf("set_status: %v %v", out, err)
	}
	c := f.take()
	if len(c) != 1 || !strings.HasPrefix(c[0], "status abc pending octo-me ") || len([]rune(strings.TrimPrefix(c[0], "status abc pending octo-me "))) != 140 {
		t.Fatalf("set_status call = %q", c)
	}
	if _, err := g.Invoke(ctx, "set_status", map[string]any{"repo": "org/repo", "sha": "abc", "state": "done"}); err == nil {
		t.Fatal("set_status accepted an unknown state")
	}
}
