package graphql

import (
	"context"
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
