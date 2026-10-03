package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/builtins/rest"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// TestRestOAuth2PollSurvivesTokenRotation is the regression test for the bug
// host.auth fixes: a rest connector's events: (a polled source) used to run
// on the connection snapshot captured once at start_source time, so a
// managed OAuth2 token minted (or rotated) AFTER the source started was
// invisible to it — the poller just kept 401ing forever, silently, once
// whatever token that snapshot carried (if any) stopped being accepted.
//
// host.auth fixes this generically: the poller asks the host for the
// instance's CURRENT token on every poll, and again with refresh after a
// 401. This test wires the real pieces — internal/builtins/rest's handler, a
// real internal/plugin.Client driving it in-process (the same path
// RegisterInProcessConnector uses), and a real managed-auth authenticator
// wired into a per-stack AuthRegistry (finding 4) through register — exactly
// the daemon's own wiring (cmd/conductor/plugins.go, internal/connector/
// inprocess.go), not a shortcut mock.
func TestRestOAuth2PollSurvivesTokenRotation(t *testing.T) {
	var (
		mu                              sync.Mutex
		accepted                        string // the upstream's one currently-valid token
		mintCount                       int
		successCount, unauthorizedCount int
	)

	// The token endpoint: every mint is a freshly issued token, always valid
	// upstream the instant it is issued (expires_in is generous — this test
	// is not about natural expiry, it's about a token going stale for a
	// reason the authenticator's own cache can't see: see "revoked" below).
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mintCount++
		tok := fmt.Sprintf("tok-%d", mintCount)
		accepted = tok
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "expires_in": 3600})
	}))
	defer tokSrv.Close()

	// The upstream API: 401s any request whose bearer isn't the one currently
	// "accepted" — a stand-in for the real provider.
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := accepted != "" && r.Header.Get("Authorization") == "Bearer "+accepted
		if ok {
			successCount++
		} else {
			unauthorizedCount++
		}
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer apiSrv.Close()

	au, err := newAuthenticator(context.Background(), "gh",
		authConfig{Type: "oauth2", Grant: "client_credentials", TokenURL: tokSrv.URL, ClientID: "cid"},
		secrets.New(), nil)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}
	authReg := NewAuthRegistry()
	authReg.register("gh", au)

	spec := plugin.Spec{Name: "rest", Kind: plugin.KindConnector, Provides: "rest", InProcess: rest.New()}
	cl := plugin.NewClient(spec, plugin.Deps{Auth: authReg.AuthProvider()})
	defer cl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cl.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	conn := map[string]any{
		"base_url": apiSrv.URL,
		"events": map[string]any{
			"things": map[string]any{
				"poll":    "15ms",
				"request": map[string]any{"method": "GET", "path": "/"},
				"list":    "{{.response.body.items}}",
				"id":      "{{.item.id}}",
			},
		},
	}
	req := plugin.StartSourceRequest{
		Instance: "gh",
		Config:   conn,
		Triggers: []sdk.SourceTrigger{{ID: "t1", Name: "t1", Event: "things"}},
	}
	if err := cl.StartSource(ctx, req, func(json.RawMessage) {}); err != nil {
		t.Fatalf("start_source: %v", err)
	}

	waitFor := func(cond func() bool, msg string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			done := cond()
			mu.Unlock()
			if done {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for: %s", msg)
	}

	// The source works from the very first poll — a live managed token, not
	// one missing or frozen from a start_source-time snapshot.
	waitFor(func() bool { return successCount >= 1 }, "at least one successful poll")

	// Simulate an out-of-band token revocation/rotation: the authenticator's
	// cached token (long TTL, still "valid" by its own bookkeeping) stops
	// being accepted upstream. The pre-host.auth poller had no way to notice
	// this short of the daemon restarting it — it would just 401 forever.
	mu.Lock()
	accepted = "revoked"
	mu.Unlock()

	waitFor(func() bool { return unauthorizedCount >= 1 }, "the rotation to be observed as a 401")
	waitFor(func() bool { return successCount >= 2 }, "the poller to recover with a freshly minted token")
}
