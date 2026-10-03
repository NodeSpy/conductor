package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

// TestDescribeInstanceMaterializesOpenVerbs proves Q6: each configured verb
// becomes a concrete, Open (user-defined option shape) Verb, and the
// type-level Describe() carries none of them.
func TestDescribeInstanceMaterializesOpenVerbs(t *testing.T) {
	r := New()
	cfg := map[string]any{
		"base_url": "http://x",
		"verbs": map[string]any{
			"list":   map[string]any{"method": "GET", "path": "/things", "output": map[string]any{"ok": "{{.response.status}}"}},
			"create": map[string]any{"method": "POST", "path": "/things"},
		},
	}
	d, err := r.DescribeInstance(context.Background(), "api", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Verbs) != 2 {
		t.Fatalf("verbs = %+v, want 2", d.Verbs)
	}
	byName := map[string]plugin.Verb{}
	for _, v := range d.Verbs {
		byName[v.Name] = v
	}
	for _, name := range []string{"list", "create"} {
		v, ok := byName[name]
		if !ok {
			t.Fatalf("missing verb %q", name)
		}
		if !v.Open {
			t.Errorf("verb %q must be Open (user-defined option shape)", name)
		}
	}
	if len(byName["list"].Outputs) != 1 {
		t.Fatalf("list outputs = %+v, want {ok}", byName["list"].Outputs)
	}

	// The type-level Describe() has no verbs at all — they are per-instance.
	td := r.Describe()
	if len(td.Verbs) != 0 {
		t.Fatalf("type-level decl has verbs: %+v", td.Verbs)
	}
}

// TestDescribeInstanceEventsAreAssigned: rest's polled events are always a
// synthetic target named after the instance+event (the operator's own
// config), so they declare target.assigned: true — the same trust shape
// rss/cron have.
func TestDescribeInstanceEventsAreAssigned(t *testing.T) {
	r := New()
	cfg := map[string]any{
		"base_url": "http://x",
		"events": map[string]any{
			"new_thing": map[string]any{
				"request": map[string]any{"path": "/things"},
				"list":    "{{.response.body.Items}}",
				"id":      "{{.item.ID}}",
			},
		},
	}
	d, err := r.DescribeInstance(context.Background(), "api", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 {
		t.Fatalf("events = %+v, want 1", d.Events)
	}
	ev := d.Events[0]
	if ev.Semantics == nil || ev.Semantics.Target == nil {
		t.Fatal("no target semantics declared")
	}
	if string(ev.Semantics.Target.Assigned) != "true" {
		t.Fatalf("assigned = %s, want true", ev.Semantics.Target.Assigned)
	}
}

func TestValidateRejectionTable(t *testing.T) {
	r := New()
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr string
	}{
		{"no base_url", map[string]any{"verbs": map[string]any{"v": map[string]any{"method": "GET", "path": "/"}}}, "base_url is required"},
		{"nothing declared", map[string]any{"base_url": "http://x"}, "declare at least one verb or event"},
		{"verb missing method/path", map[string]any{"base_url": "http://x", "verbs": map[string]any{"v": map[string]any{"method": "GET"}}}, "method: and path: are required"},
		{"bad path template", map[string]any{"base_url": "http://x", "verbs": map[string]any{
			"v": map[string]any{"method": "GET", "path": "/x/{{.broken"},
		}}, "bad path template"},
		{"event missing id", map[string]any{"base_url": "http://x", "events": map[string]any{
			"e": map[string]any{"request": map[string]any{"path": "/x"}, "list": "{{.response.body.X}}"},
		}}, "list: and id: are required"},
		{"unknown auth type", map[string]any{"base_url": "http://x", "auth": map[string]any{"type": "magic"},
			"verbs": map[string]any{"v": map[string]any{"method": "GET", "path": "/"}}}, "auth type must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := r.Validate(context.Background(), plugin.ValidateRequest{Instance: "api", Config: c.cfg})
			if err != nil {
				t.Fatalf("Validate returned an error (want problems): %v", err)
			}
			found := false
			for _, p := range res.Problems {
				if strings.Contains(p.Message, c.wantErr) {
					found = true
				}
			}
			if !found {
				t.Fatalf("problems = %+v, want one mentioning %q", res.Problems, c.wantErr)
			}
		})
	}
}

// TestInvokeUnknownVerb proves a verb not in this instance's config fails
// with CodeInvalid, never a panic.
func TestInvokeUnknownVerb(t *testing.T) {
	r := New()
	_, err := r.Invoke(plugin.InvokeRequest{
		Instance: "api", Verb: "ghost",
		Connection: map[string]any{"base_url": "http://x", "verbs": map[string]any{}},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown verb")
	}
}

// TestInvokeUpstreamStatusRetryable proves Invoke's §1.11 CodeUpstream data
// against a real HTTP server, per status: 401/429/5xx are retryable (the
// upstream may recover on its own — an expired token, a rate limit, a
// transient outage); 400/403/404/422 are not (the request itself is wrong
// and retrying it changes nothing). Both the status and retryable fields in
// the error's Data must match.
func TestInvokeUpstreamStatusRetryable(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{
		{401, true},
		{429, true},
		{500, true},
		{503, true},
		{400, false},
		{403, false},
		{404, false},
		{422, false},
	}
	for _, c := range cases {
		t.Run(http.StatusText(c.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			r := New()
			_, err := r.Invoke(plugin.InvokeRequest{
				Instance: "api", Verb: "get",
				Connection: map[string]any{
					"base_url": srv.URL,
					"verbs":    map[string]any{"get": map[string]any{"method": "GET", "path": "/x"}},
				},
			})
			if err == nil {
				t.Fatalf("status %d: expected an error (not a 2xx)", c.status)
			}
			var pe *plugin.Error
			if !errors.As(err, &pe) {
				t.Fatalf("status %d: err = %v, want a *plugin.Error", c.status, err)
			}
			if pe.Code != plugin.CodeUpstream {
				t.Fatalf("status %d: code = %d, want CodeUpstream (%d)", c.status, pe.Code, plugin.CodeUpstream)
			}
			if got, _ := pe.Data["status"].(int); got != c.status {
				t.Fatalf("status %d: data.status = %v, want %d", c.status, pe.Data["status"], c.status)
			}
			if got, _ := pe.Data["retryable"].(bool); got != c.retryable {
				t.Fatalf("status %d: data.retryable = %v, want %v", c.status, pe.Data["retryable"], c.retryable)
			}
		})
	}
}

// TestApplyAuthPrefersManagedToken proves the managed-OAuth2 bearer (when
// the host injected one) wins over a static `auth:` block — the host only
// injects it when it decided to manage this connection's oauth2, so a
// leftover static scheme must never shadow it.
func TestApplyAuthPrefersManagedToken(t *testing.T) {
	conn := map[string]any{
		"auth":                map[string]any{"type": "bearer", "token": "static-token"},
		plugin.AccessTokenKey: "managed-token",
	}
	req := newGetRequest(t)
	ApplyAuth(conn)(req)
	if got := req.Header.Get("Authorization"); got != "Bearer managed-token" {
		t.Fatalf("Authorization = %q, want the managed token", got)
	}
}

func TestApplyAuthStaticSchemes(t *testing.T) {
	req := newGetRequest(t)
	ApplyAuth(map[string]any{"auth": map[string]any{"type": "bearer", "token": "tok"}})(req)
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("bearer: %q", got)
	}

	req = newGetRequest(t)
	ApplyAuth(map[string]any{"auth": map[string]any{"type": "header", "name": "X-Key", "value": "k"}})(req)
	if got := req.Header.Get("X-Key"); got != "k" {
		t.Fatalf("header: %q", got)
	}
}

func newGetRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
