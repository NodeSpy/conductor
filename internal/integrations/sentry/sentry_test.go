package sentry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func newTest(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("test", func(v any) error { *(v.(*Config)) = cfg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func collect() (core.EmitFunc, *[]core.Trigger) {
	var got []core.Trigger
	return func(_ context.Context, tr core.Trigger) { got = append(got, tr) }, &got
}

const issueBody = `{"action":"created","data":{"issue":{
	"id":"1","shortId":"ROSTER-7","title":"nil deref in streamer","culprit":"streamer.Run",
	"permalink":"https://sentry.io/x/ROSTER-7/","level":"error","project":{"slug":"rosterstream"}}}}`

func TestParseIssueAndRoute(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{
			Match:   Match{Projects: []string{"rosterstream"}, Levels: []string{"error", "fatal"}},
			Repo:    "EdnitionCode/RosterStream",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer", Checkout: "branch-off"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "issue", "", []byte(issueBody))
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if tr.Kind != "sentry_alert" || tr.Dedup != "ROSTER-7" {
		t.Fatalf("unexpected kind/dedup: %q / %q", tr.Kind, tr.Dedup)
	}
	if tr.Target.Repo != "EdnitionCode/RosterStream" {
		t.Fatalf("project→repo routing failed: %+v", tr.Target)
	}
	s := tr.Context["sentry"].(map[string]any)
	if s["title"] != "nil deref in streamer" || s["url"] != "https://sentry.io/x/ROSTER-7/" {
		t.Fatalf("sentry facts wrong: %+v", s)
	}
}

func TestNoMatchingRuleDrops(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{
			Match:   Match{Projects: []string{"other-project"}},
			Repo:    "EdnitionCode/Other",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "issue", "", []byte(issueBody))
	if len(*got) != 0 {
		t.Fatalf("alert for a non-matching project should drop, got %d", len(*got))
	}
}

func TestSyntheticRepoForcesNoCheckout(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{ // no repo → synthetic
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "issue", "", []byte(issueBody))
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if !strings.HasPrefix(tr.Target.Repo, "sentry:") {
		t.Fatalf("expected synthetic repo, got %q", tr.Target.Repo)
	}
	if tr.Action.(config.Action).Checkout != "none" {
		t.Fatal("synthetic repo should force checkout none")
	}
}

func TestErrorResourceParse(t *testing.T) {
	body := `{"action":"triggered","data":{"event":{
		"event_id":"abc123","title":"TimeoutError","level":"warning","environment":"prod",
		"web_url":"https://sentry.io/e/abc123/","project":"rosterstream"}}}`
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{
			Match:   Match{Environments: []string{"prod"}},
			Repo:    "EdnitionCode/RosterStream",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer", Checkout: "branch-off"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "event_alert", "", []byte(body))
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger from event resource, got %d", len(*got))
	}
	if (*got)[0].Dedup != "abc123" {
		t.Fatalf("event dedup should be issue_id/event_id, got %q", (*got)[0].Dedup)
	}
}

func TestHandlerSignature(t *testing.T) {
	secret := "csecret"
	g := newTest(t, Config{
		Listen:       ":0",
		ClientSecret: secret,
		Rules:        []Rule{{Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}}},
	})
	emit, got := collect()
	h := g.handler(context.Background(), emit)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(issueBody))
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/sentry", strings.NewReader(issueBody))
	req.Header.Set("Sentry-Hook-Resource", "issue")
	req.Header.Set("Sentry-Hook-Signature", sig)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 202 || len(*got) != 1 {
		t.Fatalf("valid sig should accept+emit: code=%d emits=%d", rec.Code, len(*got))
	}

	req2 := httptest.NewRequest("POST", "/sentry", strings.NewReader(issueBody))
	req2.Header.Set("Sentry-Hook-Resource", "issue")
	req2.Header.Set("Sentry-Hook-Signature", "bad")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != 401 || len(*got) != 1 {
		t.Fatalf("bad sig should reject: code=%d emits=%d", rec2.Code, len(*got))
	}
}
