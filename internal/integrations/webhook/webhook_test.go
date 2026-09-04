package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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

func TestDeliverMapsBodyToTrigger(t *testing.T) {
	g := newTest(t, Config{
		Listen: ":0",
		Sources: []Source{{
			Name:  "cloudwatch",
			Path:  "/hooks/cw",
			Title: "{{.body.detail.alarmName}} → {{.body.detail.state}}",
			Dedup: "{{.body.detail.alarmName}}-{{.body.time}}",
			Repo:  "EdnitionCode/infra",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer",
				Prompt: "alarm {{.body.detail.alarmName}}"}},
		}},
	})
	emit, got := collect()
	body := []byte(`{"detail":{"alarmName":"cpu-high","state":"ALARM"},"time":"t1"}`)
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", body, false)

	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if tr.Kind != "cloudwatch" || tr.Title != "cpu-high → ALARM" {
		t.Fatalf("unexpected kind/title: %q / %q", tr.Kind, tr.Title)
	}
	if tr.Dedup != "cpu-high-t1" {
		t.Fatalf("dedup mapping wrong: %q", tr.Dedup)
	}
	if tr.Target.Repo != "EdnitionCode/infra" || tr.Target.Owner != "EdnitionCode" {
		t.Fatalf("repo target wrong: %+v", tr.Target)
	}
	// The action's prompt can reach the body via Context.
	if tr.Context["body"] == nil {
		t.Fatal("parsed body not attached to Context")
	}
}

func TestDeliverSyntheticRepoForcesNoCheckout(t *testing.T) {
	g := newTest(t, Config{
		Listen: ":0",
		Sources: []Source{{
			Name:    "statuspage",
			Path:    "/hooks/sp",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}, // no checkout, no repo
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", []byte(`{"x":1}`), false)
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if !strings.HasPrefix(tr.Target.Repo, "webhook:") {
		t.Fatalf("expected synthetic repo, got %q", tr.Target.Repo)
	}
	if act := tr.Action.(config.Action); act.Checkout != "none" {
		t.Fatalf("synthetic repo should force checkout none, got %q", act.Checkout)
	}
}

func TestDeliverMatchPredicate(t *testing.T) {
	g := newTest(t, Config{
		Listen: ":0",
		Sources: []Source{{
			Name:    "only-alarms",
			Path:    "/h",
			Match:   `{{if eq .body.type "alarm"}}true{{end}}`,
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", []byte(`{"type":"info"}`), false)
	if len(*got) != 0 {
		t.Fatalf("non-matching delivery should be dropped, got %d", len(*got))
	}
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", []byte(`{"type":"alarm"}`), false)
	if len(*got) != 1 {
		t.Fatalf("matching delivery should fire, got %d", len(*got))
	}
}

func TestDeliverDedupsRedelivery(t *testing.T) {
	g := newTest(t, Config{
		Listen: ":0",
		Sources: []Source{{
			Name: "x", Path: "/h", Dedup: "{{.body.id}}",
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	body := []byte(`{"id":"evt-1"}`)
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", body, false)
	g.deliver(context.Background(), emit, g.cfg.Sources[0], "", body, false)
	if len(*got) != 1 {
		t.Fatalf("identical delivery should emit once, got %d", len(*got))
	}
}

func TestHandlerVerifiesSignature(t *testing.T) {
	secret := "shh"
	g := newTest(t, Config{
		Listen: ":0",
		Sources: []Source{{
			Name: "signed", Path: "/h",
			Sign:    Sign{Header: "X-Sig", Secret: secret},
			Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}},
		}},
	})
	emit, got := collect()
	h := g.handler(context.Background(), emit, g.cfg.Sources[0])
	body := `{"ok":true}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))

	// Good signature → 202 + emit.
	req := httptest.NewRequest("POST", "/h", strings.NewReader(body))
	req.Header.Set("X-Sig", sig)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 202 || len(*got) != 1 {
		t.Fatalf("valid sig should accept+emit: code=%d emits=%d", rec.Code, len(*got))
	}
	// Bad signature → 401 + no new emit.
	req2 := httptest.NewRequest("POST", "/h", strings.NewReader(body))
	req2.Header.Set("X-Sig", "deadbeef")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != 401 || len(*got) != 1 {
		t.Fatalf("bad sig should reject: code=%d emits=%d", rec2.Code, len(*got))
	}
}

func TestValidate(t *testing.T) {
	// No transport → error.
	if err := newTest(t, Config{Sources: []Source{{Name: "a", Actions: config.ActionSet{{Type: "agent"}}}}}).Validate(); err == nil {
		t.Fatal("missing listen/smee_url should fail validate")
	}
	// Listener source without a path → error.
	bad := Config{Listen: ":9", Sources: []Source{{Name: "a", Actions: config.ActionSet{{Type: "agent"}}}}}
	if err := newTest(t, bad).Validate(); err == nil {
		t.Fatal("listener source without path should fail validate")
	}
	// Valid.
	ok := Config{Listen: ":9", Sources: []Source{{Name: "a", Path: "/a", Actions: config.ActionSet{{Type: "agent"}}}}}
	if err := newTest(t, ok).Validate(); err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
}
