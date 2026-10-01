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
)

// reviewStub serves what the fold needs from REST: the installation token, a
// review's state, and its inline comments. stateCalls counts state reads.
type reviewStub struct {
	state      string // what GET /pulls/7/reviews/99 reports ("" → 404)
	comments   int    // how many inline comments review 99 carries
	stateCalls atomic.Int32
}

func (s *reviewStub) attach(t *testing.T, g *Integration) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"t","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7/reviews/99", func(w http.ResponseWriter, _ *http.Request) {
		s.stateCalls.Add(1)
		if s.state == "" {
			http.NotFound(w, nil)
			return
		}
		fmt.Fprintf(w, `{"id":99,"state":%q}`, s.state)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/7/reviews/99/comments", func(w http.ResponseWriter, _ *http.Request) {
		var items []string
		for i := 0; i < s.comments; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"path":"f%d.go","line":%d,"body":"fix %d {{.gh_token}}","html_url":"u%d","user":{"login":"reviewer"}}`, 500+i, i, 10+i, i, i))
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
		"comment":{"id":%d,"pull_request_review_id":%d,"user":{"login":"reviewer","type":"User"},"body":"fix this"}
	}`, id, reviewID))
}

// deliver runs a review and its n inline comments through the webhook path in
// the given order and returns every trigger they produced.
func deliver(g *Integration, state string, n int, reviewFirst bool) []core.Trigger {
	ctx := context.Background()
	var out []core.Trigger
	if reviewFirst {
		out = append(out, g.triggersFor(ctx, "pull_request_review", reviewEvent(state))...)
	}
	for i := 0; i < n; i++ {
		out = append(out, g.triggersFor(ctx, "pull_request_review_comment", reviewCommentEvent(int64(1000+i), 99))...)
	}
	if !reviewFirst {
		out = append(out, g.triggersFor(ctx, "pull_request_review", reviewEvent(state))...)
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

// THE incident: a changes-requested review with N inline comments must become
// exactly ONE run — the changes_requested flow, which re-requests the reviewer
// after — not that plus one new_comment fixer per comment. Both delivery
// orders: GitHub does not order a review's events.
func TestChangesRequestedReviewFoldsItsInlineComments(t *testing.T) {
	const n = 4
	for _, reviewFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("reviewFirst=%v", reviewFirst), func(t *testing.T) {
			g := newTestIntegration(t, baseConfig())
			stub := &reviewStub{state: "CHANGES_REQUESTED", comments: n}
			stub.attach(t, g)

			trs := deliver(g, "changes_requested", n, reviewFirst)
			if k := kindsOf(trs); len(trs) != 1 || k["changes_requested"] != 1 {
				t.Fatalf("a changes-requested review with %d inline comments produced %v, want exactly one changes_requested", n, k)
			}
			cr := trs[0]
			list, _ := cr.Context["review_comments"].([]any)
			if len(list) != n {
				t.Fatalf("changes_requested carries %d review_comments, want all %d", len(list), n)
			}
			first, _ := list[0].(map[string]any)
			if first["path"] != "f0.go" || first["line"] != 10 || first["author"] != "reviewer" || !strings.HasPrefix(first["body"].(string), "fix 0") {
				t.Fatalf("review comment not carried faithfully: %v", first)
			}
			if cr.Context["review_id"] != int64(99) || cr.Context["review_body"] != "see inline" {
				t.Fatalf("review identity not carried: id=%v body=%v", cr.Context["review_id"], cr.Context["review_body"])
			}
			// The N comments cost at most one state read between them (none
			// when the review event arrived first and seeded the cache).
			want := int32(1)
			if reviewFirst {
				want = 0
			}
			if got := stub.stateCalls.Load(); got != want {
				t.Fatalf("review state read %d times for %d comments, want %d", got, n, want)
			}
		})
	}
}

// Inline comments of a review that did NOT request changes have no run that
// addresses them as a whole: each is still a new_comment.
func TestCommentedReviewInlineCommentsStillFireNewComment(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	(&reviewStub{state: "COMMENTED"}).attach(t, g)
	trs := deliver(g, "commented", 3, false)
	if k := kindsOf(trs); k["new_comment"] != 3 || k["changes_requested"] != 0 {
		t.Fatalf("commented review: got %v, want 3 new_comment", k)
	}
}

// Folding must never DROP feedback: if no changes_requested trigger would take
// the review, or its state can't be read, the comments stay new_comment events.
func TestReviewCommentsAreNotFoldedWhenNothingAddressesTheReview(t *testing.T) {
	t.Run("no changes_requested trigger", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Rules[0].Actions = as1(map[string]config.Action{"new_comment": {Type: "agent", Agent: "fixer"}})
		g := newTestIntegration(t, cfg)
		(&reviewStub{state: "CHANGES_REQUESTED"}).attach(t, g)
		if k := kindsOf(deliver(g, "changes_requested", 3, true)); k["new_comment"] != 3 {
			t.Fatalf("got %v, want the 3 comments as new_comment", k)
		}
	})
	t.Run("changes_requested filter rejects the reviewer", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Rules[0].Actions["changes_requested"] = config.ActionSet{{Type: "agent", Agent: "fixer",
			Filter: config.FilterExpr("reviewer != 'reviewer'")}}
		g := newTestIntegration(t, cfg)
		(&reviewStub{state: "CHANGES_REQUESTED"}).attach(t, g)
		if k := kindsOf(deliver(g, "changes_requested", 3, true)); k["new_comment"] != 3 || k["changes_requested"] != 0 {
			t.Fatalf("got %v, want the 3 comments as new_comment and no changes_requested", k)
		}
	})
	t.Run("review state unreadable", func(t *testing.T) {
		g := newTestIntegration(t, baseConfig())
		(&reviewStub{state: ""}).attach(t, g) // 404
		var trs []core.Trigger
		for i := 0; i < 3; i++ {
			trs = append(trs, g.triggersFor(context.Background(), "pull_request_review_comment", reviewCommentEvent(int64(1000+i), 99))...)
		}
		if k := kindsOf(trs); k["new_comment"] != 3 {
			t.Fatalf("got %v, want the 3 comments as new_comment", k)
		}
	})
}

// The sweep's missed-comment recovery must not re-fan a changes-requested
// review out into one new_comment per inline comment either: those comments'
// marks never advance (no new_comment ever ran for them), so without the fold
// every sweep in the recovery window would re-emit them. Conversation comments
// and comments on other reviews are still recovered.
func TestSweepDoesNotRefanAChangesRequestedReview(t *testing.T) {
	var stateCalls atomic.Int32
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
	// Three inline comments of changes-requested review 41, one of commented
	// review 42.
	mux.HandleFunc("/repos/acme/widget/pulls/9/comments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[
			{"id":3918412103,"pull_request_review_id":42,"user":{"login":"carol"},"body":"fyi","created_at":%[1]q},
			{"id":3918412102,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"c","created_at":%[1]q},
			{"id":3918412101,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"b","created_at":%[1]q},
			{"id":3918412100,"pull_request_review_id":41,"user":{"login":"reviewer"},"body":"a","created_at":%[1]q}]`, fresh)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/reviews/41", func(w http.ResponseWriter, _ *http.Request) {
		stateCalls.Add(1)
		fmt.Fprint(w, `{"id":41,"state":"CHANGES_REQUESTED"}`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/9/reviews/42", func(w http.ResponseWriter, _ *http.Request) {
		stateCalls.Add(1)
		fmt.Fprint(w, `{"id":42,"state":"COMMENTED"}`)
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

	cfg := Config{
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
	g := newTestIntegration(t, cfg)
	g.app = &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}
	g.rest = newRESTClient(g.app)

	sweepOnce := func() []core.Trigger {
		var got []core.Trigger
		if err := g.sweep(context.Background(), func(_ context.Context, tr core.Trigger) { got = append(got, tr) }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := sweepOnce()
	newComments := map[int64]bool{}
	var cr []core.Trigger
	for _, tr := range got {
		switch tr.Kind {
		case "new_comment":
			id, _ := tr.Context["comment_id"].(int64)
			newComments[id] = true
		case "changes_requested":
			cr = append(cr, tr)
		}
	}
	if len(newComments) != 2 || !newComments[5515854542] || !newComments[3918412103] {
		t.Fatalf("sweep recovered new_comment for %v, want only the conversation comment and the commented review's comment", newComments)
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
	// One state read per distinct review, cached across comments and sweeps.
	_ = sweepOnce()
	if n := stateCalls.Load(); n != 2 {
		t.Fatalf("review state read %d times over two sweeps, want 2 (once per review)", n)
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
}
