package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// TestRestOAuth2PollFailsClosedWhenHostAuthHasNoToken is finding 9(b)'s
// regression test: an oauth2 instance whose host.auth can't produce a token
// (no authenticator registered for it at all, the simplest case) must never
// poll the upstream unauthenticated. ApplyAuth (internal/builtins/rest) has
// no static-scheme fallback for auth.type "oauth2" — the daemon strips and
// owns that scheme entirely — so before this fix, a host.auth error just
// left the request with no Authorization header whatsoever and sent it
// anyway. This test proves the upstream never receives a single request.
func TestRestOAuth2PollFailsClosedWhenHostAuthHasNoToken(t *testing.T) {
	var apiHits int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer apiSrv.Close()

	// An empty registry: host.auth refuses "gh" outright (no authenticator
	// registered for it at all).
	authReg := NewAuthRegistry()

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
		"auth":     map[string]any{"type": "oauth2", "grant": "client_credentials", "token_url": "http://unused.invalid", "client_id": "cid"},
		"events": map[string]any{
			"things": map[string]any{
				"poll":    "10ms",
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

	// Several poll ticks' worth of time: the upstream must never see one.
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&apiHits); n != 0 {
		t.Fatalf("upstream received %d request(s) with no managed token at all — an oauth2 instance must skip the poll, not send it unauthenticated", n)
	}
}

// TestRestPollLogsFailureThroughHostLog is finding 9(a)'s regression test:
// pollOnce used to be completely silent on failure. A poll whose response
// can never satisfy its `list:` template (here: the upstream 500s) must be
// reported through host.log so an operator watching the daemon's log can see
// a stuck poller instead of silent, permanent data loss.
func TestRestPollLogsFailureThroughHostLog(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("not json"))
	}))
	defer apiSrv.Close()

	var mu sync.Mutex
	var logged []string
	logf := func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	spec := plugin.Spec{Name: "rest", Kind: plugin.KindConnector, Provides: "rest", InProcess: rest.New()}
	cl := plugin.NewClient(spec, plugin.Deps{Log: logf})
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
				"poll":    "10ms",
				"request": map[string]any{"method": "GET", "path": "/"},
				"list":    "{{.response.body.items}}", // body is "not json" — never a map with "items"
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

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(logged)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) == 0 {
		t.Fatal("a poll that can never satisfy its list: template logged nothing through host.log")
	}
	found := false
	for _, l := range logged {
		if strings.Contains(l, "gh") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("logged lines never named the instance: %v", logged)
	}
}
