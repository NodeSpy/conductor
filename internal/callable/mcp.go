package callable

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sort"
)

// The MCP tool face (#36 §13.4): `conductor mcp callable` is a minimal MCP
// (Model Context Protocol) server over stdio — newline-delimited JSON-RPC 2.0
// — that exposes each callable workflow as a tool. An MCP client (an agent, an
// IDE, a desktop assistant) sees the workflows the operator opted in with
// `callable: true` and nothing else; calling a tool fires that workflow and
// returns its structured result.
//
// It is scoped callable-only by construction — the tool list is exactly the
// `callable: true` triggers, so a manual trigger that did not opt in is never
// exposed. The transport is the daemon's same-user control socket (the launch
// wiring lives in cmd/conductor), the same privilege boundary `conductor run`
// uses; there is no separate bearer token on this local face.
//
// Hand-rolled for the same reasons as the memory server: initialize,
// tools/list, tools/call, ping are the whole surface a tools-only server
// needs, and a dependency-free loop keeps the zero-cgo single-binary build.

const mcpProtocolVersion = "2025-06-18"

// MCPTool is one callable workflow advertised to the MCP client.
type MCPTool struct {
	Name        string
	Description string
}

// MCPDeps wires the stdio server to the daemon. Invoke fires a workflow and
// blocks (bounded) for its structured result — the same result body the HTTP
// GET /runs/<id> returns.
type MCPDeps struct {
	Tools  []MCPTool
	Invoke func(ctx context.Context, name string, input map[string]any) (map[string]any, error)
	Log    func(string, ...any)
}

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeMCP runs the stdio loop until r reaches EOF.
func ServeMCP(r io.Reader, w io.Writer, d MCPDeps) error {
	if d.Log == nil {
		d.Log = func(string, ...any) {}
	}
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(w)

	reply := func(id json.RawMessage, result any, rpcErr *mcpError) error {
		msg := map[string]any{"jsonrpc": "2.0", "id": id}
		if rpcErr != nil {
			msg["error"] = rpcErr
		} else {
			msg["result"] = result
		}
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if _, err := out.Write(append(b, '\n')); err != nil {
			return err
		}
		return out.Flush()
	}

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg mcpMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = reply(nil, nil, &mcpError{Code: -32700, Message: "parse error: " + err.Error()})
			continue
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue // a notification needs no reply
		}
		switch msg.Method {
		case "initialize":
			if err := reply(msg.ID, map[string]any{
				"protocolVersion": negotiatedVersion(msg.Params),
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "conductor-callable", "title": "Conductor callable workflows"},
			}, nil); err != nil {
				return err
			}
		case "ping":
			if err := reply(msg.ID, map[string]any{}, nil); err != nil {
				return err
			}
		case "tools/list":
			if err := reply(msg.ID, map[string]any{"tools": mcpTools(d.Tools)}, nil); err != nil {
				return err
			}
		case "tools/call":
			if err := reply(msg.ID, d.toolCall(msg.Params), nil); err != nil {
				return err
			}
		default:
			if err := reply(msg.ID, nil, &mcpError{Code: -32601, Message: "method not found: " + msg.Method}); err != nil {
				return err
			}
		}
	}
	return in.Err()
}

// mcpTools shapes the callable workflows as MCP tool descriptors. Each takes a
// single free-form `input` object — the same body the HTTP invoke endpoint
// accepts, so the two faces stay congruent.
func mcpTools(tools []MCPTool) []map[string]any {
	sorted := append([]MCPTool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	out := make([]map[string]any, 0, len(sorted))
	for _, t := range sorted {
		desc := t.Description
		if desc == "" {
			desc = "Run the conductor callable workflow " + t.Name + " and return its structured result."
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": desc,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "object",
						"description": "inputs passed to the workflow (available under .inputs and at the top level in templates)",
					},
				},
			},
		})
	}
	return out
}

// toolCall executes one tools/call: fire the named workflow, return its result.
// A tool failure is an isError result (the MCP convention), not a protocol error.
func (d MCPDeps) toolCall(params json.RawMessage) map[string]any {
	fail := func(msg string) map[string]any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true}
	}
	var p struct {
		Name string `json:"name"`
		Args struct {
			Input map[string]any `json:"input"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return fail("bad tool arguments: " + err.Error())
	}
	// Only advertised (callable-only) tools are invocable.
	known := false
	for _, t := range d.Tools {
		if t.Name == p.Name {
			known = true
			break
		}
	}
	if !known {
		return fail("unknown or non-callable workflow: " + p.Name)
	}
	input := p.Args.Input
	if input == nil {
		input = map[string]any{}
	}
	res, err := d.Invoke(context.Background(), p.Name, input)
	if err != nil {
		d.Log("callable mcp: invoke %q failed: %v", p.Name, err)
		return fail("invoke failed: " + err.Error())
	}
	body, _ := json.Marshal(res)
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}
	if s, _ := res["status"].(string); s == "failed" {
		out["isError"] = true // a failed run surfaces as a tool error
	}
	return out
}

// negotiatedVersion echoes the client's protocol version when it sent one, else
// ours (the shapes here are stable across published versions).
func negotiatedVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		return p.ProtocolVersion
	}
	return mcpProtocolVersion
}
