package callable

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// runMCP feeds newline-delimited JSON-RPC requests through ServeMCP and returns
// the decoded responses in order.
func runMCP(t *testing.T, d MCPDeps, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out strings.Builder
	if err := ServeMCP(in, &out, d); err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestMCPToolsListScopedToCallable(t *testing.T) {
	d := MCPDeps{
		Tools: []MCPTool{{Name: "triage"}, {Name: "audit", Description: "run the audit"}},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return nil, nil
		},
	}
	resps := runMCP(t, d,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	result := resps[1]["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2 (only callable workflows)", len(tools))
	}
	// Sorted by name: audit, triage.
	names := []string{tools[0].(map[string]any)["name"].(string), tools[1].(map[string]any)["name"].(string)}
	if names[0] != "audit" || names[1] != "triage" {
		t.Fatalf("names = %v, want [audit triage]", names)
	}
}

func TestMCPToolCallInvokes(t *testing.T) {
	var gotName string
	var gotInput map[string]any
	d := MCPDeps{
		Tools: []MCPTool{{Name: "triage"}},
		Invoke: func(_ context.Context, name string, input map[string]any) (map[string]any, error) {
			gotName, gotInput = name, input
			return map[string]any{"run_id": "r1", "status": "ok", "outputs": map[string]any{"s": map[string]any{"k": "v"}}}, nil
		},
	}
	resps := runMCP(t, d,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"triage","arguments":{"input":{"pr":7}}}}`,
	)
	if gotName != "triage" {
		t.Fatalf("invoked %q, want triage", gotName)
	}
	if gotInput["pr"] != float64(7) {
		t.Fatalf("input = %v, want pr=7", gotInput)
	}
	result := resps[0]["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("ok run should not be an error result: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"status":"ok"`) {
		t.Fatalf("tool result text = %q, want the structured result", text)
	}
}

func TestMCPToolCallUnknownRefused(t *testing.T) {
	called := false
	d := MCPDeps{
		Tools: []MCPTool{{Name: "triage"}},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			called = true
			return nil, nil
		},
	}
	resps := runMCP(t, d,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deploy","arguments":{}}}`,
	)
	if called {
		t.Fatal("Invoke ran for a non-advertised (non-callable) tool")
	}
	result := resps[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("unknown tool should be an isError result: %v", result)
	}
}

func TestMCPFailedRunIsError(t *testing.T) {
	d := MCPDeps{
		Tools: []MCPTool{{Name: "triage"}},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"run_id": "r1", "status": "failed", "error": "boom"}, nil
		},
	}
	resps := runMCP(t, d,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"triage","arguments":{}}}`,
	)
	result := resps[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("a failed run should surface as isError: %v", result)
	}
}
