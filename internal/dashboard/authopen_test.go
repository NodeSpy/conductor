package dashboard

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// noOpServer builds a Server with no operators configured, optionally with a
// state dir (so auth CAN be wired) and/or allow_open set.
func noOpServer(t *testing.T, withStateDir, allowOpen bool) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{Dashboard: config.DashboardConfig{Enabled: true, Auth: config.DashboardAuth{AllowOpen: allowOpen}}}
	deps := Deps{Cfg: cfg, Store: st, Events: flow.NewEventHub(), Operators: func() map[string]string { return nil }}
	if withStateDir {
		deps.StateDir = filepath.Join(dir, "dashboard")
	}
	return New(deps)
}

func getLocal(s *Server, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	s.listenerHandler(auth.ListenerLocalhost).ServeHTTP(rr, r)
	return rr
}

// rt2 #10: with no operators configured, the dashboard API must refuse
// requests by default — not serve them openly — regardless of whether a
// sign-in store could even be built (no state dir) or was (state dir present
// but nobody has enrolled yet). Only dashboard.auth.allow_open: true restores
// the old open-by-default behavior.
func TestDashboardRefusesAPIWithNoOperatorsByDefault(t *testing.T) {
	for _, withStateDir := range []bool{false, true} {
		s := noOpServer(t, withStateDir, false)
		if s.authEnforced() {
			t.Fatalf("withStateDir=%v: authEnforced should be false with no operators", withStateDir)
		}
		rr := getLocal(s, "/api/v1/status")
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("withStateDir=%v: /api/v1/status = %d, want %d (refused)", withStateDir, rr.Code, http.StatusServiceUnavailable)
		}
		// The SPA shell itself still loads (it's public, no API data).
		rr = getLocal(s, "/")
		if rr.Code == http.StatusServiceUnavailable {
			t.Fatalf("withStateDir=%v: the SPA shell must still load", withStateDir)
		}
	}
}

// allow_open: true restores serving the API with no operators configured —
// an explicit operator choice, never the default.
func TestDashboardAllowOpenServesAPIWithNoOperators(t *testing.T) {
	s := noOpServer(t, false, true)
	rr := getLocal(s, "/api/v1/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/v1/status with allow_open: true = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}
