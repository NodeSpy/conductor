package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestForceUnconfiguredKind(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	// A kind with no configured action errors before any network call.
	n, err := g.Force(context.Background(), "nonexistent_kind", "acme/widget", 1,
		func(context.Context, core.Trigger) {})
	if err == nil || n != 0 {
		t.Fatalf("force should error for an unconfigured kind, got n=%d err=%v", n, err)
	}
}

// TestForceBypassesDraftAndExclude (#57 T3): `conductor force` runs an action on
// demand, skipping the applicability filters (draft gate, exclude) AND the
// engine's dedup/liveness gates (Trigger.Force). It fetches the PR to fill the
// target, resolves the App installation, and injects the installation id + a
// freshly minted app token into each trigger's Context — so the forced run has
// the same credentials a webhook-driven one would.
func TestForceBypassesDraftAndExclude(t *testing.T) {
	// review_requested gated by not_draft and excluding the "wip" label.
	cfg := richConfig()
	cfg.Rules[0].Actions = as1(map[string]config.Action{
		"review_requested": {
			Type:    "command",
			Command: []string{"critique"},
			Gates:   map[string]any{"not_draft": true},
			Exclude: config.Exclude{Labels: []string{"wip"}},
		},
	})

	// Stub the App + REST endpoints: installation lookup, the PR (a draft with
	// the excluded label, authored by "me"), and the installation token.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/w/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":42}`)
	})
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"inst-tok","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/acme/w/pulls/6", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"draft":true,"head":{"sha":"h6","ref":"feature/x"},"base":{"ref":"main"},"html_url":"http://x/6","user":{"login":"me"},"labels":[{"name":"wip"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	app := &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}

	g := newTestIntegration(t, cfg)
	g.app = app
	g.rest = newRESTClient(app)

	// Baseline: the normal webhook path filters this PR out — the requested
	// reviewer matches, but the not_draft gate + label exclude suppress it.
	body := `{"action":"review_requested","installation":{"id":42},
		"requested_reviewer":{"login":"me"},
		"repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"draft":true,"head":{"sha":"h6","ref":"feature/x"},"base":{"ref":"main"},"user":{"login":"me"},"labels":[{"name":"wip"}]}}`
	if trs := g.triggersFor(context.Background(), "pull_request", []byte(body)); len(trs) != 0 {
		t.Fatalf("draft + excluded label must filter the normal path, got %+v", trs)
	}

	// Force fires it regardless, marks the trigger Force, and injects the
	// installation id + minted app token.
	var got []core.Trigger
	n, err := g.Force(context.Background(), "review_requested", "acme/w", 6,
		func(_ context.Context, tr core.Trigger) { got = append(got, tr) })
	if err != nil || n != 1 || len(got) != 1 {
		t.Fatalf("force should fire once past the filters, got n=%d err=%v trs=%d", n, err, len(got))
	}
	tr := got[0]
	if tr.Kind != "review_requested" || !tr.Force {
		t.Fatalf("forced trigger must carry the kind and Force flag: %+v", tr)
	}
	if tr.Target.PR != 6 || tr.Target.HeadSHA != "h6" {
		t.Fatalf("forced trigger target should be filled from the fetched PR: %+v", tr.Target)
	}
	if tr.Context["installation_id"] != int64(42) {
		t.Fatalf("installation id must be injected into context, got %v", tr.Context["installation_id"])
	}
	if tr.Context["app_token"] != "inst-tok" {
		t.Fatalf("app token must be injected into context, got %v", tr.Context["app_token"])
	}
}
