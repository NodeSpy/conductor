package graphql

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
	g := New()
	cfg := map[string]any{
		"endpoint": "http://x",
		"verbs": map[string]any{
			"create_order": map[string]any{
				"query":     "mutation { ok }",
				"variables": map[string]any{"id": "{{.options.id}}"},
				"output":    map[string]any{"ok": "{{.response.data.ok}}"},
			},
		},
	}
	d, err := g.DescribeInstance(context.Background(), "shop", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Verbs) != 1 {
		t.Fatalf("verbs = %+v, want 1", d.Verbs)
	}
	v := d.Verbs[0]
	if v.Name != "create_order" || !v.Open {
		t.Fatalf("verb = %+v, want Open create_order", v)
	}
	if len(v.Outputs) != 1 {
		t.Fatalf("outputs = %+v, want {ok}", v.Outputs)
	}

	td := g.Describe()
	if len(td.Verbs) != 0 {
		t.Fatalf("type-level decl has verbs: %+v", td.Verbs)
	}
}

func TestValidateRejectionTable(t *testing.T) {
	g := New()
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr string
	}{
		{"no endpoint", map[string]any{"verbs": map[string]any{"v": map[string]any{"query": "query { x }"}}}, "endpoint is required"},
		{"no verbs", map[string]any{"endpoint": "http://x"}, "declare at least one verb"},
		{"verb no query", map[string]any{"endpoint": "http://x", "verbs": map[string]any{
			"v": map[string]any{"output": map[string]any{"a": "{{.response.data.a}}"}},
		}}, "query: is required"},
		{"unknown auth type", map[string]any{"endpoint": "http://x", "auth": map[string]any{"type": "magic"},
			"verbs": map[string]any{"v": map[string]any{"query": "query { x }"}}}, "auth type must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := g.Validate(context.Background(), plugin.ValidateRequest{Instance: "shop", Config: c.cfg})
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

func TestInvokeUnknownVerb(t *testing.T) {
	g := New()
	_, err := g.Invoke(plugin.InvokeRequest{
		Instance: "shop", Verb: "ghost",
		Connection: map[string]any{"endpoint": "http://x", "verbs": map[string]any{}},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown verb")
	}
}

// TestInvokeUpstreamStatusRetryable proves Invoke's §1.11 CodeUpstream data
// against a real HTTP server, per status: 401/429/5xx are retryable;
// 400/403/404/422 are not. Both status and retryable in the error's Data
// must match.
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

			g := New()
			_, err := g.Invoke(plugin.InvokeRequest{
				Instance: "shop", Verb: "q",
				Connection: map[string]any{
					"endpoint": srv.URL,
					"verbs":    map[string]any{"q": map[string]any{"query": "{ ok }"}},
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
