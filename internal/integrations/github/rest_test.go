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
)

// stubAPI serves the installation-token and pull endpoints against a settable
// mergeable_state, so merge-state parsing can be tested without the network.
func stubAPI(t *testing.T, mergeableState string) (*httptest.Server, *appAuth) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token":"inst-tok","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/acme/w/pulls/6", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"mergeable_state":%q,"head":{"sha":"h6"},"base":{"ref":"main"},"html_url":"http://x/6"}`, mergeableState)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	app := &appAuth{appID: 1, key: key, httpc: http.DefaultClient, apiBase: srv.URL, now: time.Now, cache: map[int64]cachedToken{}}
	return srv, app
}

func TestMergeStateConflict(t *testing.T) {
	_, app := stubAPI(t, "dirty")
	g := newTestIntegration(t, richConfig())
	g.app = app
	g.rest = newRESTClient(app)

	body := `{"action":"opened","installation":{"id":42},
		"repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h6"},"base":{"ref":"main"}}}`
	trs := g.triggersFor(context.Background(), "pull_request", []byte(body))
	if len(trs) != 1 || trs[0].Kind != "merge_conflict" {
		t.Fatalf("want merge_conflict, got %+v", trs)
	}
	// App token was injected via the stubbed token endpoint.
	if trs[0].Context["app_token"] != "inst-tok" {
		t.Fatalf("app token not injected: %+v", trs[0].Context)
	}
}

func TestMergeStateBehind(t *testing.T) {
	_, app := stubAPI(t, "behind")
	g := newTestIntegration(t, richConfig())
	g.app = app
	g.rest = newRESTClient(app)

	body := `{"action":"synchronize","installation":{"id":42},
		"repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h6"},"base":{"ref":"main"}}}`
	trs := g.triggersFor(context.Background(), "pull_request", []byte(body))
	if len(trs) != 1 || trs[0].Kind != "pr_behind" {
		t.Fatalf("want pr_behind, got %+v", trs)
	}
}

func TestMergeStateCleanNoTrigger(t *testing.T) {
	_, app := stubAPI(t, "clean")
	g := newTestIntegration(t, richConfig())
	g.app = app
	g.rest = newRESTClient(app)

	body := `{"action":"opened","installation":{"id":42},
		"repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h6"},"base":{"ref":"main"}}}`
	if trs := g.triggersFor(context.Background(), "pull_request", []byte(body)); len(trs) != 0 {
		t.Fatalf("clean PR should not trigger, got %+v", trs)
	}
}
