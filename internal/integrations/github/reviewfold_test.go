package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// reviewStub serves what review folding needs from REST: the installation
// token, review 99 itself, and its inline comments.
type reviewStub struct {
	state      string // what GET /pulls/7/reviews/99 reports ("" → 404)
	comments   int    // how many inline comments review 99 carries
	listFails  bool   // GET .../reviews/99/comments → 500
	reviewGets atomic.Int32
}

func (s *reviewStub) attach(t *testing.T, g *Integration) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"t","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7/reviews/99", func(w http.ResponseWriter, _ *http.Request) {
		s.reviewGets.Add(1)
		if s.state == "" {
			http.NotFound(w, nil)
			return
		}
		fmt.Fprintf(w, `{"id":99,"state":%q,"body":"see inline","user":{"login":"reviewer","type":"User"}}`, s.state)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7/reviews/99/comments", func(w http.ResponseWriter, _ *http.Request) {
		if s.listFails {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var items []string
		for i := 0; i < s.comments; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"path":"f%d.go","line":%d,"body":"fix %d {{.gh_token}}","html_url":"u%d","user":{"login":"reviewer"}}`, 1000+i, i, 10+i, i, i))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	g.app = &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}
	g.rest = newRESTClient(g.app)
}

func reviewEvent(state string) []byte {
	return []byte(fmt.Sprintf(`{
		"action":"submitted","installation":{"id":77},
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"pull_request":{"number":7,"head":{"sha":"abc123","ref":"feat"},"base":{"ref":"main"},"user":{"login":"me"}},
		"review":{"state":%q,"id":99,"body":"see inline","user":{"login":"reviewer","type":"User"}}
	}`, state))
}

func reviewCommentEvent(id int64, reviewID int64) []byte {
	return []byte(fmt.Sprintf(`{
		"action":"created","installation":{"id":77},
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"pull_request":{"number":7,"html_url":"u","head":{"sha":"abc123","ref":"feat"},"base":{"ref":"main"},"user":{"login":"me"}},
		"comment":{"id":%d,"pull_request_review_id":%d,"user":{"login":"reviewer","type":"User"},"body":"fix %d"}
	}`, id, reviewID, id))
}

func issueCommentEvent(id int64) []byte {
	return []byte(fmt.Sprintf(`{
		"action":"created","installation":{"id":77},
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"issue":{"number":7,"html_url":"u","pull_request":{},"user":{"login":"me"}},
		"comment":{"id":%d,"user":{"login":"teammate","type":"User"},"body":"question %d?"}
	}`, id, id))
}

// The orders a review's deliveries can land in. GitHub doesn't order them, and
// a webhook can be lost — the review is one event whichever way it arrives.
var deliveryOrders = []string{"review-first", "comments-first", "review-only", "comments-only"}

// deliver runs review 99 and its n inline comments through the webhook path
// in the given order and returns every trigger they produced.
func deliver(g *Integration, state string, n int, order string) []core.Trigger {
	ctx := context.Background()
	var out []core.Trigger
	review := func() { out = append(out, g.triggersFor(ctx, "pull_request_review", reviewEvent(state))...) }
	comments := func() {
		for i := 0; i < n; i++ {
			out = append(out, g.triggersFor(ctx, "pull_request_review_comment", reviewCommentEvent(int64(1000+i), 99))...)
		}
	}
	switch order {
	case "review-first":
		review()
		comments()
	case "comments-first":
		comments()
		review()
	case "review-only":
		review()
	case "comments-only":
		comments()
	}
	return out
}

func kindsOf(trs []core.Trigger) map[string]int {
	out := map[string]int{}
	for _, tr := range trs {
		out[tr.Kind]++
	}
	return out
}

// One review submission is ONE event, whatever its state, however its
// deliveries arrive: a changes-request a changes_requested trigger takes is
// that run (the incident: not that plus one fixer per comment), and any other
// review with inline comments is a single new_comment. Either way the event
// carries the review body and every inline comment.
func TestReviewIsExactlyOneEvent(t *testing.T) {
	const n = 4
	for _, tc := range []struct{ restState, hookState, wantKind string }{
		{"CHANGES_REQUESTED", "changes_requested", "changes_requested"},
		{"COMMENTED", "commented", "new_comment"},
		{"APPROVED", "approved", "new_comment"},
	} {
		for _, order := range deliveryOrders {
			t.Run(tc.hookState+"/"+order, func(t *testing.T) {
				g := newTestIntegration(t, baseConfig())
				stub := &reviewStub{state: tc.restState, comments: n}
				stub.attach(t, g)

				trs := deliver(g, tc.hookState, n, order)
				if k := kindsOf(trs); len(trs) != 1 || k[tc.wantKind] != 1 {
					t.Fatalf("a %s review with %d inline comments produced %v, want exactly one %s", tc.hookState, n, k, tc.wantKind)
				}
				ev := trs[0]
				list, _ := ev.Context["review_comments"].([]any)
				if len(list) != n {
					t.Fatalf("%s carries %d review_comments, want all %d", ev.Kind, len(list), n)
				}
				first, _ := list[0].(map[string]any)
				if first["path"] != "f0.go" || first["line"] != 10 || first["author"] != "reviewer" || !strings.HasPrefix(first["body"].(string), "fix 0") {
					t.Fatalf("review comment not carried faithfully: %v", first)
				}
				if ev.Context["review_id"] != int64(99) || ev.Context["review_body"] != "see inline" || ev.Context["author"] != "reviewer" {
					t.Fatalf("review identity not carried: %v", ev.Context)
				}
				// The review's facts cost at most one REST read across all of
				// its deliveries (none when the review event came first).
				if got := stub.reviewGets.Load(); got > 1 {
					t.Fatalf("review read %d times for one review, want at most 1", got)
				}
				if tc.wantKind != "new_comment" {
					return
				}
				// A single-comment trigger's fields still read sensibly: the
				// reviewer is the commenter, comment_body is the whole review,
				// comment_id is the review's highest (the engine's high-water
				// mark then drops any later recovery of it).
				body, _ := ev.Context["comment_body"].(string)
				if !strings.HasPrefix(body, "see inline") || !strings.Contains(body, "f3.go:13: fix 3") {
					t.Fatalf("comment_body should be the review body plus every inline comment, got %q", body)
				}
				if ev.Context["comment_id"] != int64(1000+n-1) || ev.Context["comment_kind"] != store.CommentKindReview {
					t.Fatalf("comment_id/kind = %v/%v, want the review's highest id %d / review", ev.Context["comment_id"], ev.Context["comment_kind"], 1000+n-1)
				}
				if ev.Dedup != "review:99" || ev.Context["review_state"] != tc.hookState {
					t.Fatalf("dedup %q / review_state %v", ev.Dedup, ev.Context["review_state"])
				}
			})
		}
	}
}

// A changes-request no changes_requested trigger takes is still one review —
// so ONE new_comment, never one per inline comment.
func TestUntakenChangesRequestIsOneNewComment(t *testing.T) {
	t.Run("no changes_requested trigger", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Rules[0].Actions = as1(map[string]config.Action{"new_comment": {Type: "agent", Agent: "fixer"}})
		g := newTestIntegration(t, cfg)
		(&reviewStub{state: "CHANGES_REQUESTED", comments: 3}).attach(t, g)
		if k := kindsOf(deliver(g, "changes_requested", 3, "comments-first")); k["new_comment"] != 1 || len(k) != 1 {
			t.Fatalf("got %v, want one new_comment", k)
		}
	})
	t.Run("changes_requested filter rejects the reviewer", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Rules[0].Actions["changes_requested"] = config.ActionSet{{Type: "agent", Agent: "fixer",
			Filter: config.FilterExpr("reviewer != 'reviewer'")}}
		g := newTestIntegration(t, cfg)
		(&reviewStub{state: "CHANGES_REQUESTED", comments: 3}).attach(t, g)
		if k := kindsOf(deliver(g, "changes_requested", 3, "review-first")); k["new_comment"] != 1 || len(k) != 1 {
			t.Fatalf("got %v, want one new_comment and no changes_requested", k)
		}
	})
}

// Folding must never DROP feedback: when a review can't be read, its comments
// stand alone — one new_comment each, as before.
func TestUnreadableReviewCommentsStandAlone(t *testing.T) {
	for name, stub := range map[string]*reviewStub{
		"review unreadable":   {state: ""},
		"comments unreadable": {state: "COMMENTED", listFails: true},
	} {
		t.Run(name, func(t *testing.T) {
			g := newTestIntegration(t, baseConfig())
			stub.attach(t, g)
			if k := kindsOf(deliver(g, "commented", 3, "comments-only")); k["new_comment"] != 3 {
				t.Fatalf("got %v, want the 3 comments as a new_comment each", k)
			}
		})
	}
}

// A standalone comment — a conversation comment, or a review comment that
// names no review — is its own event: N of them are N events, never merged.
func TestStandaloneCommentsAreOneEventEach(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	(&reviewStub{state: "COMMENTED", comments: 3}).attach(t, g)
	ctx := context.Background()
	var trs []core.Trigger
	for i := 0; i < 3; i++ {
		trs = append(trs, g.triggersFor(ctx, "issue_comment", issueCommentEvent(int64(2000+i)))...)
		trs = append(trs, g.triggersFor(ctx, "pull_request_review_comment", reviewCommentEvent(int64(3000+i), 0))...)
	}
	if k := kindsOf(trs); len(trs) != 6 || k["new_comment"] != 6 {
		t.Fatalf("6 standalone comments produced %v, want 6 new_comment", k)
	}
	seen := map[string]bool{}
	for _, tr := range trs {
		if !strings.HasPrefix(tr.Dedup, "comment:") || seen[tr.Dedup] {
			t.Fatalf("standalone comment dedup %q: want a distinct comment:<id> each", tr.Dedup)
		}
		seen[tr.Dedup] = true
	}
}

// sweepStubFor serves an `acme/widget` sweep over PR 9 whose recent comments
// are: one conversation comment, three inline comments of changes-requested
// review 41, and two of commented review 42.
func sweepStubFor(t *testing.T, reviewGets *atomic.Int32) *appAuth {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"t","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/acme/widget/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":77}`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"number":9,"user":{"login":"me"},"head":{"sha":"h9","ref":"feat"},"base":{"ref":"main"},"html_url":"u"}]`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"mergeable_state":"clean","head":{"sha":"h9","ref":"feat"},"base":{"ref":"main"},"html_url":"u"}`)
	})
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	mux.HandleFunc("/repos/acme/widget/issues/9/comments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"id":5515854542,"user":{"login":"teammate"},"body":"question?","created_at":%q}]`, fresh)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/comments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[
			{"id":3918412105,"pull_request_review_id":42,"path":"y.go","line":2,"user":{"login":"carol"},"body":"nit 2","created_at":%[1]q},
			{"id":3918412104,"pull_request_review_id":42,"path":"x.go","line":1,"user":{"login":"carol"},"body":"nit 1","created_at":%[1]q},
			{"id":3918412102,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"c","created_at":%[1]q},
			{"id":3918412101,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"b","created_at":%[1]q},
			{"id":3918412100,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"a","created_at":%[1]q}]`, fresh)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/reviews/41", func(w http.ResponseWriter, _ *http.Request) {
		reviewGets.Add(1)
		fmt.Fprint(w, `{"id":41,"state":"CHANGES_REQUESTED","user":{"login":"reviewer","type":"User"}}`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/reviews/42", func(w http.ResponseWriter, _ *http.Request) {
		reviewGets.Add(1)
		fmt.Fprint(w, `{"id":42,"state":"COMMENTED","body":"two nits","user":{"login":"carol","type":"User"}}`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/reviews/42/comments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[
			{"id":3918412104,"path":"x.go","line":1,"body":"nit 1","user":{"login":"carol"}},
			{"id":3918412105,"path":"y.go","line":2,"body":"nit 2","user":{"login":"carol"}}]`)
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"id":"t1","isResolved":false,"comments":{"nodes":[{"author":{"login":"reviewer","__typename":"User"},"path":"a.go","line":3,"body":"a","url":"ua"}]}},
			{"id":"t2","isResolved":false,"comments":{"nodes":[{"author":{"login":"reviewer","__typename":"User"},"path":"b.go","originalLine":8,"body":"b","url":"ub"}]}},
			{"id":"t3","isResolved":false,"comments":{"nodes":[{"author":{"login":"reviewer","__typename":"User"},"path":"c.go","line":5,"body":"c","url":"uc"}]}}]}}}}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	return &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}
}

func sweepConfig() Config {
	return Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x", Secret: "s"},
		Sweep:   SweepConfig{Enabled: boolp(true), Repos: []string{"acme/widget"}},
		Rules: []Rule{{
			Match: Match{Repos: []string{"acme/widget"}},
			Me:    config.Actors{Logins: []string{"me"}},
			Actions: as1(map[string]config.Action{
				"changes_requested": {Type: "agent", Agent: "fixer"},
				"new_comment":       {Type: "agent", Agent: "fixer"},
			}),
		}},
	}
}

// The sweep's missed-comment recovery follows the same rule: a review is
// recovered as its one event (the commented review → one new_comment; the
// changes-requested review → the unresolved-threads changes_requested, no
// new_comment), a standalone comment as its own, and a review already emitted
// is not recovered again.
func TestSweepRecoversAReviewAsOneEvent(t *testing.T) {
	var reviewGets atomic.Int32
	g := newTestIntegration(t, sweepConfig())
	g.app = sweepStubFor(t, &reviewGets)
	g.rest = newRESTClient(g.app)

	sweepOnce := func() []core.Trigger {
		var got []core.Trigger
		if err := g.sweep(context.Background(), func(_ context.Context, tr core.Trigger) { got = append(got, tr) }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	var standalone, reviews, cr []core.Trigger
	for _, tr := range sweepOnce() {
		switch {
		case tr.Kind == "changes_requested":
			cr = append(cr, tr)
		case tr.Kind == "new_comment" && strings.HasPrefix(tr.Dedup, "review:"):
			reviews = append(reviews, tr)
		case tr.Kind == "new_comment":
			standalone = append(standalone, tr)
		}
	}
	if len(standalone) != 1 || standalone[0].Context["comment_id"] != int64(5515854542) {
		t.Fatalf("standalone recovered: %v, want just the conversation comment", standalone)
	}
	if len(reviews) != 1 || reviews[0].Dedup != "review:42" {
		t.Fatalf("reviews recovered as new_comment: %v, want ONE for commented review 42 (and none for changes-requested 41)", reviews)
	}
	rv := reviews[0]
	if list, _ := rv.Context["review_comments"].([]any); len(list) != 2 || rv.Context["review_body"] != "two nits" ||
		rv.Context["comment_id"] != int64(3918412105) || rv.Context["author"] != "carol" {
		t.Fatalf("recovered review lost its content: %v", rv.Context)
	}
	if len(cr) != 1 {
		t.Fatalf("sweep emitted %d changes_requested, want 1 for the unresolved threads", len(cr))
	}
	list, _ := cr[0].Context["review_comments"].([]any)
	if len(list) != 3 {
		t.Fatalf("sweep changes_requested carries %d review_comments, want the 3 unresolved threads", len(list))
	}
	if second, _ := list[1].(map[string]any); second["path"] != "b.go" || second["line"] != 8 || second["body"] != "b" {
		t.Fatalf("outdated thread comment not carried (want b.go:8 from originalLine): %v", second)
	}

	// A second sweep must not recover review 42 again, and costs no further
	// review reads (one per review, cached).
	for _, tr := range sweepOnce() {
		if tr.Dedup == "review:42" {
			t.Fatal("second sweep re-emitted review 42")
		}
	}
	if n := reviewGets.Load(); n != 2 {
		t.Fatalf("review read %d times over two sweeps, want 2 (once per review)", n)
	}
}

// A review the webhook already turned into its event is not recovered by the
// sweep as another one.
func TestSweepSkipsAReviewTheWebhookEmitted(t *testing.T) {
	var reviewGets atomic.Int32
	g := newTestIntegration(t, sweepConfig())
	g.app = sweepStubFor(t, &reviewGets)
	g.rest = newRESTClient(g.app)
	hook := []byte(`{
		"action":"submitted","installation":{"id":77},
		"repository":{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
		"pull_request":{"number":9,"head":{"sha":"h9","ref":"feat"},"base":{"ref":"main"},"user":{"login":"me"}},
		"review":{"state":"commented","id":42,"body":"two nits","user":{"login":"carol","type":"User"}}
	}`)
	if k := kindsOf(g.triggersFor(context.Background(), "pull_request_review", hook)); k["new_comment"] != 1 {
		t.Fatalf("webhook review: %v, want one new_comment", k)
	}
	var got []core.Trigger
	if err := g.sweep(context.Background(), func(_ context.Context, tr core.Trigger) { got = append(got, tr) }); err != nil {
		t.Fatal(err)
	}
	for _, tr := range got {
		if tr.Dedup == "review:42" {
			t.Fatal("sweep re-emitted a review the webhook already emitted")
		}
	}
}

// review_comments stays inside a prompt a runtime will accept: each body is
// capped (on a rune boundary) and the list stops at a byte budget, counting
// what it left out so the agent knows to read the rest on the PR.
func TestReviewCommentsAreCapped(t *testing.T) {
	long := strings.Repeat("é", maxReviewCommentBody) // 2 bytes per rune
	ctx := map[string]any{}
	addReviewComments(ctx, []reviewComment{{Author: "a", Path: "p", Body: long}})
	body := ctx["review_comments"].([]any)[0].(map[string]any)["body"].(string)
	if len(body) > maxReviewCommentBody+len("…") || !strings.HasSuffix(body, "…") {
		t.Fatalf("body not capped: %d bytes", len(body))
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(body, "…")) {
		t.Fatal("cap split a rune")
	}
	if _, ok := ctx["review_comments_omitted"]; ok {
		t.Fatal("nothing was omitted, but review_comments_omitted is set")
	}

	many := make([]reviewComment, 200)
	for i := range many {
		many[i] = reviewComment{Author: "a", Path: "p", Body: strings.Repeat("x", maxReviewCommentBody)}
	}
	ctx = map[string]any{}
	addReviewComments(ctx, many)
	kept := len(ctx["review_comments"].([]any))
	total := 0
	for _, c := range ctx["review_comments"].([]any) {
		total += len(c.(map[string]any)["body"].(string))
	}
	if total > maxReviewCommentsBytes || kept == 0 {
		t.Fatalf("kept %d comments / %d body bytes, want within the %d-byte budget", kept, total, maxReviewCommentsBytes)
	}
	if got := ctx["review_comments_omitted"]; got != len(many)-kept {
		t.Fatalf("review_comments_omitted = %v, want %d", got, len(many)-kept)
	}
	if s := reviewSummary("", many); len(s) > maxReviewSummaryBytes+len("…") {
		t.Fatalf("folded comment_body not capped: %d bytes", len(s))
	}
}
