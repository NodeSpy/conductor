package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// oauth2TokenServer is a minimal client_credentials token endpoint that
// always mints a fresh, distinguishable token (so a test can tell which
// authenticator answered).
func oauth2TokenServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("%s-%d", prefix, n), "expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestAuthenticator(t *testing.T, name, tokenURL string) *authenticator {
	t.Helper()
	au, err := newAuthenticator(context.Background(), name,
		authConfig{Type: "oauth2", Grant: "client_credentials", TokenURL: tokenURL, ClientID: "cid"},
		secrets.New(), nil)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}
	return au
}

// TestValidateBuildDoesNotChangeLiveHostAuth is finding 4's first regression
// test: a throwaway validation/dry-run build's own AuthRegistry must never
// affect what the LIVE daemon's host.auth (HostAuthProvider, what every
// in-process builtin's shared plugin.Client is wired to) answers — even when
// it registers an authenticator for the SAME instance name the live stack
// already has one for.
func TestValidateBuildDoesNotChangeLiveHostAuth(t *testing.T) {
	liveSrv := oauth2TokenServer(t, "live")
	validateSrv := oauth2TokenServer(t, "validate")

	live := NewAuthRegistry()
	live.register("gh", newTestAuthenticator(t, "gh", liveSrv.URL))
	SetLiveAuthRegistry(live)
	t.Cleanup(func() { SetLiveAuthRegistry(nil) })

	ctx := context.Background()
	tok, err := HostAuthProvider.AccessToken(ctx, "gh", false)
	if err != nil || tok != "live-1" {
		t.Fatalf("live host.auth before any validate build: tok=%q err=%v, want live-1", tok, err)
	}

	// A throwaway validation build's OWN registry, naming the SAME instance
	// — simulating cmd/conductor/plugins.go's pendingPluginRetry →
	// validateConfigFile → buildFlowStack(dryRun=true) → connector.Build,
	// which never calls connector.SetLiveAuthRegistry for its registry.
	validateReg := NewAuthRegistry()
	validateReg.register("gh", newTestAuthenticator(t, "gh", validateSrv.URL))
	_ = validateReg // never promoted live — that is the whole point

	tok, err = HostAuthProvider.AccessToken(ctx, "gh", false)
	if err != nil || tok != "live-1" {
		t.Fatalf("live host.auth AFTER a validate build registered the same instance: tok=%q err=%v, want unchanged live-1", tok, err)
	}
}

// TestReconfiguredInstanceLosesManagedAuthOnRebuild is finding 4's second
// regression test: when a live stack is REBUILT (config reload) and an
// instance no longer has managed (oauth2) auth configured, the new live
// registry — built fresh, containing only what THIS build registered — must
// not still answer for it just because an older registry once did.
func TestReconfiguredInstanceLosesManagedAuthOnRebuild(t *testing.T) {
	srv := oauth2TokenServer(t, "gh")
	first := NewAuthRegistry()
	first.register("gh", newTestAuthenticator(t, "gh", srv.URL))
	SetLiveAuthRegistry(first)
	t.Cleanup(func() { SetLiveAuthRegistry(nil) })

	ctx := context.Background()
	if _, err := HostAuthProvider.AccessToken(ctx, "gh", false); err != nil {
		t.Fatalf("host.auth before reconfigure: %v", err)
	}

	// The instance is reconfigured with no managed auth at all: the rebuild's
	// connector.Build loop calls register(name, nil) (buildManagedAuth
	// returns a nil authenticator for a plain/no-auth connection), which is
	// a deliberate no-op — "gh" is simply never in the new registry.
	second := NewAuthRegistry()
	second.register("gh", nil)
	SetLiveAuthRegistry(second)

	if _, err := HostAuthProvider.AccessToken(ctx, "gh", false); err == nil {
		t.Fatal("host.auth answered OK for an instance reconfigured to no managed auth")
	}
}

// TestHostAuthRefreshIsCooldownThrottled is finding 6's regression test:
// host.auth {refresh:true} must honor a per-instance cooldown instead of
// hitting the token endpoint on every call — within the cooldown it returns
// the cached token unchanged; once the cooldown elapses, refresh:true forces
// a genuinely fresh one.
func TestHostAuthRefreshIsCooldownThrottled(t *testing.T) {
	oldCooldown := refreshCooldown
	refreshCooldown = 50 * time.Millisecond
	t.Cleanup(func() { refreshCooldown = oldCooldown })

	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&mints, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("tok-%d", n), "expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)

	reg := NewAuthRegistry()
	au := newTestAuthenticator(t, "gh", srv.URL)
	reg.register("gh", au)
	ctx := context.Background()

	tok1, err := reg.accessToken(ctx, "gh", true)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if atomic.LoadInt32(&mints) != 1 {
		t.Fatalf("mints = %d after the first refresh, want 1", mints)
	}

	// A second refresh:true WITHIN the cooldown must not hit the token
	// endpoint again — the cached token comes back unchanged.
	tok2, err := reg.accessToken(ctx, "gh", true)
	if err != nil {
		t.Fatalf("second refresh (within cooldown): %v", err)
	}
	if atomic.LoadInt32(&mints) != 1 {
		t.Fatalf("mints = %d after a second refresh within the cooldown, want still 1 (throttled)", mints)
	}
	if tok2 != tok1 {
		t.Fatalf("token changed within the cooldown: %q -> %q", tok1, tok2)
	}

	// Once the cooldown elapses, refresh:true forces a genuinely fresh token.
	time.Sleep(2 * refreshCooldown)
	tok3, err := reg.accessToken(ctx, "gh", true)
	if err != nil {
		t.Fatalf("third refresh (past cooldown): %v", err)
	}
	if atomic.LoadInt32(&mints) != 2 {
		t.Fatalf("mints = %d after the cooldown elapsed, want 2", mints)
	}
	if tok3 == tok1 {
		t.Fatal("refresh past the cooldown did not force a fresh token")
	}
}
