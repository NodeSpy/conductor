package pagerduty

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

const incidentBody = `{"event":{"event_type":"incident.triggered","resource_type":"incident","data":{
	"id":"PABC123","number":42,"status":"triggered","title":"High error rate on api",
	"html_url":"https://acme.pagerduty.com/incidents/PABC123","urgency":"high",
	"priority":{"summary":"P1"},"service":{"id":"PSVC1","summary":"RosterStream API"}}}}`

func TestParseAndRoute(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{
			Match: Match{EventTypes: []string{"incident.triggered"}, Urgencies: []string{"high"},
				Priorities: []string{"P1", "P2"}, Services: []string{"RosterStream API"}},
			Repo:    "EdnitionCode/RosterStream",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer", Checkout: "branch-off"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "", []byte(incidentBody))
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if tr.Kind != "pagerduty_incident" || tr.Dedup != "PABC123:incident.triggered" {
		t.Fatalf("unexpected kind/dedup: %q / %q", tr.Kind, tr.Dedup)
	}
	if tr.Target.Repo != "EdnitionCode/RosterStream" {
		t.Fatalf("repo routing failed: %+v", tr.Target)
	}
	p := tr.Context["pagerduty"].(map[string]any)
	if p["title"] != "High error rate on api" || p["priority"] != "P1" || p["url"] != "https://acme.pagerduty.com/incidents/PABC123" {
		t.Fatalf("pagerduty facts wrong: %+v", p)
	}
}

func TestServiceOrPriorityNoMatchDrops(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules: []Rule{{
			Match:   Match{Services: []string{"Some Other Service"}},
			Repo:    "EdnitionCode/RosterStream",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "", []byte(incidentBody))
	if len(*got) != 0 {
		t.Fatalf("incident on a non-matching service should drop, got %d", len(*got))
	}
}

func TestSyntheticRepoForcesNoCheckout(t *testing.T) {
	g := newTest(t, Config{
		SmeeURL: "https://smee.io/x",
		Rules:   []Rule{{Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}}}, // no repo, no filters
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, "", []byte(incidentBody))
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if !strings.HasPrefix(tr.Target.Repo, "pagerduty:") {
		t.Fatalf("expected synthetic repo, got %q", tr.Target.Repo)
	}
	if tr.Action.(config.Action).Checkout != "none" {
		t.Fatal("synthetic repo should force checkout none")
	}
}

func TestSignatureVerify(t *testing.T) {
	secret := "sig-secret"
	g := newTest(t, Config{
		Listen:        ":0",
		SigningSecret: secret,
		Rules:         []Rule{{Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}}},
	})
	emit, got := collect()
	h := g.handler(context.Background(), emit)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(incidentBody))
	good := hex.EncodeToString(mac.Sum(nil))

	// A valid v1 signature (with a bogus rotated one alongside it) accepts.
	req := httptest.NewRequest("POST", "/pagerduty", strings.NewReader(incidentBody))
	req.Header.Set("X-PagerDuty-Signature", "v1=deadbeef,v1="+good)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 202 || len(*got) != 1 {
		t.Fatalf("valid sig should accept+emit: code=%d emits=%d", rec.Code, len(*got))
	}

	// Only-bad signatures reject.
	req2 := httptest.NewRequest("POST", "/pagerduty", strings.NewReader(incidentBody))
	req2.Header.Set("X-PagerDuty-Signature", "v1=deadbeef")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != 401 || len(*got) != 1 {
		t.Fatalf("bad sig should reject: code=%d emits=%d", rec2.Code, len(*got))
	}
}
