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

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// sweepStub serves the org installation lookup, installation repo list, open-PR
// list, and per-PR mergeable state needed for an `owner/*` sweep.
func sweepStub(t *testing.T) *appAuth {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"t","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/orgs/acme/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":77}`)
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"total_count":2,"repositories":[
			{"full_name":"acme/widget","name":"widget","owner":{"login":"acme"}},
			{"full_name":"acme/gadget","name":"gadget","owner":{"login":"acme"}}]}`)
	})
	// widget has one authored, conflicting PR; gadget has none.
	mux.HandleFunc("/repos/acme/widget/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"number":3,"user":{"login":"me"}}]`)
	})
	mux.HandleFunc("/repos/acme/widget/pulls/3", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"mergeable_state":"dirty","head":{"sha":"h3"},"base":{"ref":"main"}}`)
	})
	mux.HandleFunc("/repos/acme/gadget/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	return &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}
}

func TestSweepOrgGlob(t *testing.T) {
	cfg := Config{
		App:     AppConfig{AppID: 1, PrivateKeyPath: "x", WebhookSecret: "s"},
		Webhook: WebhookConfig{SmeeURL: "https://smee.io/x"},
		Sweep:   SweepConfig{Enabled: true, Repos: []string{"acme/*"}},
		Rules: []Rule{{
			Match:    Match{Repos: []string{"acme/*"}},
			Reviewer: config.Actors{Logins: []string{"me"}}, // makes "me" a self login
			Actions:  map[string]config.Action{"merge_conflict": {Type: "agent", Agent: "fixer"}},
		}},
	}
	g := newTestIntegration(t, cfg)
	g.app = sweepStub(t)
	g.rest = newRESTClient(g.app)

	var got []core.Trigger
	if err := g.sweep(context.Background(), func(_ context.Context, tr core.Trigger) { got = append(got, tr) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "merge_conflict" || got[0].Target.Repo != "acme/widget" {
		t.Fatalf("want one merge_conflict on acme/widget, got %+v", got)
	}
}
