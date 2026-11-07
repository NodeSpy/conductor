package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
)

// serve serves one request and returns the recorder.
func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

// decodeJSON fails unless the answer is JSON (by header and body).
func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON (body %.80q)", ct, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v (%.80q)", err, rr.Body.String())
	}
	return out
}

func errCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %v", body)
	}
	if m, _ := e["message"].(string); m == "" {
		t.Errorf("error has no message: %v", e)
	}
	c, _ := e["code"].(string)
	return c
}

// With no bots configured the bot API's list reads answer an empty list and
// configured: false — a first visit, not the SPA's index.html (which the
// client failed to parse) and not an error.
func TestBotsUnconfiguredListsAreEmptyJSON(t *testing.T) {
	s, _, _ := newTestServer(t)
	for path, key := range map[string]string{
		"/api/v1/bots":          "bots",
		"/api/v1/teams":         "teams",
		"/api/v1/conversations": "conversations",
		"/api/v1/tickets":       "tickets",
		"/api/v1/proposals":     "proposals",
	} {
		t.Run(key, func(t *testing.T) {
			rr := serve(t, s.Handler(), http.MethodGet, path)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200 (%s)", path, rr.Code, rr.Body)
			}
			body := decodeJSON(t, rr)
			list, ok := body[key].([]any)
			if !ok || len(list) != 0 {
				t.Errorf("%s: %s = %#v, want an empty list (not null)", path, key, body[key])
			}
			if c, ok := body["configured"].(bool); !ok || c {
				t.Errorf("%s: configured = %#v, want false", path, body["configured"])
			}
		})
	}
}

// The roster says whether this dashboard can stage the first bot: only one
// started with a config path can edit it.
func TestBotsUnconfiguredRosterSaysWhetherItCanCreate(t *testing.T) {
	s, _, _ := newTestServer(t) // no ConfigPath: read-only
	body := decodeJSON(t, serve(t, s.Handler(), http.MethodGet, "/api/v1/bots"))
	if teams, ok := body["teams"].([]any); !ok || len(teams) != 0 {
		t.Errorf("teams = %#v, want []", body["teams"])
	}
	if e, ok := body["editable"].(bool); !ok || e {
		t.Errorf("read-only dashboard: editable = %#v, want false", body["editable"])
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("connectors:\n  box: { use: command }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ed := New(Deps{Cfg: &config.Config{}, Store: s.deps.Store, Events: flow.NewEventHub(), StateDir: dir,
		ConfigPath: cfgPath, Validate: func(string) error { return nil }, Reload: func() error { return nil }})
	body = decodeJSON(t, serve(t, ed.Handler(), http.MethodGet, "/api/v1/bots"))
	if e, ok := body["editable"].(bool); !ok || !e {
		t.Errorf("editable dashboard: editable = %#v, want true", body["editable"])
	}
}

// Anything else on the bot paths — one bot, a stream, a write — is a JSON 404
// that says there are no bots yet.
func TestBotsUnconfiguredOtherPathsAreJSON404(t *testing.T) {
	s, _, _ := newTestServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/bots/atlas"},
		{http.MethodGet, "/api/v1/teams/platform"},
		{http.MethodGet, "/api/v1/tickets/stream"},
		{http.MethodGet, "/api/v1/conversations/stream"},
		{http.MethodPost, "/api/v1/tickets"},
		{http.MethodPost, "/api/v1/conversations/bot:atlas/messages"},
	} {
		rr := serve(t, s.Handler(), c.method, c.path)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", c.method, c.path, rr.Code)
			continue
		}
		if code := errCode(t, decodeJSON(t, rr)); code != "no_bots" {
			t.Errorf("%s %s: code %q, want no_bots", c.method, c.path, code)
		}
	}
}

// An /api path nobody serves is a JSON 404; only non-API paths fall back to
// the SPA.
func TestUnknownAPIPathIsJSON404NotTheSPA(t *testing.T) {
	s, _, _ := newTestServer(t)
	for _, p := range []string{"/api/v1/nope", "/api/v2/bots", "/api/"} {
		rr := serve(t, s.Handler(), http.MethodGet, p)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rr.Code)
			continue
		}
		if code := errCode(t, decodeJSON(t, rr)); code != "not_found" {
			t.Errorf("%s: code %q, want not_found", p, code)
		}
	}
	rr := serve(t, s.Handler(), http.MethodGet, "/bots")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") {
		t.Errorf("/bots: %d %q, want the SPA's index.html", rr.Code, rr.Header().Get("Content-Type"))
	}
}

// With bots configured, a path under the bot API's prefixes that it doesn't
// serve is a JSON 404 too, not the mux's plain-text one.
func TestBotAPIUnservedPathIsJSON404(t *testing.T) {
	s, st, _ := newTestServer(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"bots": []any{}, "teams": []any{}})
	})
	b := New(Deps{Cfg: s.deps.Cfg, Store: st, Events: flow.NewEventHub(),
		BotRoutes: []BotRoute{{Pattern: "GET /v1/bots", Handler: ok}}})
	if rr := serve(t, b.Handler(), http.MethodGet, "/api/v1/bots"); rr.Code != http.StatusOK {
		t.Fatalf("/api/v1/bots: %d, want 200", rr.Code)
	}
	rr := serve(t, b.Handler(), http.MethodGet, "/api/v1/teams")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/teams: %d, want 404", rr.Code)
	}
	if code := errCode(t, decodeJSON(t, rr)); code != "not_found" {
		t.Errorf("code %q, want not_found", code)
	}
}

// The connections read model says each runtime's built-in kind, so a screen
// can tell which runtimes a bot's workers can run on (the cli ones).
func TestConnectionsSaysRuntimeType(t *testing.T) {
	_, st, _ := newTestServer(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("runtimes:\n  agent: { use: cli, tool: claude-code }\n  ext: { use: acme/rt }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Runtimes: config.RuntimeSet{"agent": {Use: "cli"}, "ext": {Use: "acme/rt"}}}
	c := New(Deps{Cfg: cfg, Store: st, Events: flow.NewEventHub(), StateDir: dir, ConfigPath: cfgPath,
		Validate: func(string) error { return nil }, Reload: func() error { return nil }})
	body := decodeJSON(t, serve(t, c.Handler(), http.MethodGet, "/api/v1/connections"))
	got := map[string]string{}
	for _, r := range body["runtimes"].([]any) {
		m := r.(map[string]any)
		typ, _ := m["type"].(string)
		got[m["name"].(string)] = typ
	}
	if got["agent"] != "cli" || got["ext"] != "" || len(got) != 2 {
		t.Errorf("runtime types = %v, want agent=cli and ext untyped", got)
	}
}
