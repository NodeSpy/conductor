package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// The live runtime tool surface: `conductor mcp memory` is a minimal MCP
// (Model Context Protocol) server over stdio — newline-delimited JSON-RPC
// 2.0 — exposing two tools, memory_remember and memory_recall, that proxy to
// the daemon over the unix socket (ipc.go). Runtimes that can attach MCP
// servers to an agent session get live mid-run memory: ACP passes it via
// session/new mcpServers. Runtimes without live-tool injection (today's
// paseo CLI dispatch, plain cli/command runtimes) fall back to the output
// contract and workflow steps — same memory, later write.
//
// The implementation is deliberately hand-rolled: initialize, tools/list,
// tools/call, and ping are the whole protocol surface a tools-only server
// needs, and a dependency-free loop keeps the zero-cgo single-binary build.

const mcpProtocolVersion = "2025-06-18"

// MCPCaller executes one tool call (production: IPCCall to the daemon).
type MCPCaller func(req IPCRequest) (IPCResponse, error)

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

// ServeMCP runs the stdio loop until r reaches EOF. src is the dispatch
// provenance baked into the launch flags; every remember carries it.
func ServeMCP(r io.Reader, w io.Writer, call MCPCaller, src Source, number int) error {
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
			continue // a notification (notifications/initialized, …) needs no reply
		}
		switch msg.Method {
		case "initialize":
			if err := reply(msg.ID, map[string]any{
				"protocolVersion": negotiatedVersion(msg.Params),
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "conductor-memory", "title": "Conductor shared memory"},
			}, nil); err != nil {
				return err
			}
		case "ping":
			if err := reply(msg.ID, map[string]any{}, nil); err != nil {
				return err
			}
		case "tools/list":
			if err := reply(msg.ID, map[string]any{"tools": mcpTools()}, nil); err != nil {
				return err
			}
		case "tools/call":
			result := mcpToolCall(msg.Params, call, src, number)
			if err := reply(msg.ID, result, nil); err != nil {
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

// negotiatedVersion echoes a protocol version we can speak: the client's own
// when it sent one (the shapes this server uses are stable across published
// versions), else ours.
func negotiatedVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		return p.ProtocolVersion
	}
	return mcpProtocolVersion
}

// mcpTools is the published tool list.
func mcpTools() []map[string]any {
	str := map[string]any{"type": "string"}
	strList := map[string]any{"type": "array", "items": str}
	return []map[string]any{
		{
			"name":        "memory_remember",
			"description": "Save a durable note to conductor's shared agent memory — future agent runs (yours and other agents') can recall it. Use for lessons that outlive this run: project conventions, gotchas, environment facts.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  map[string]any{"type": "string", "description": "the note to keep"},
					"tags":  strList,
					"scope": map[string]any{"type": "string", "description": "global (default) | repo (this run's repo) | agent (your own notes) | repo:<owner/repo> | agent:<name>"},
				},
				"required": []string{"text"},
			},
		},
		{
			"name":        "memory_recall",
			"description": "Search conductor's shared agent memory: notes left by earlier runs, filtered by tags/scope/substring, newest first.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags":      strList,
					"scope":     str,
					"substring": str,
					"limit":     map[string]any{"type": "integer"},
				},
			},
		},
		{
			"name":        "run_step",
			"description": "Author and run ONE conductor workflow step right now (the normal step grammar: uses/run/type/workflow…). Validated and guarded by policy.agent_authored before it runs; returns the step's outputs. Use it to build a plan interactively; batch the rest as a ```plan block in your final output.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"step": map[string]any{"type": "object", "description": "one step, e.g. {\"uses\": \"gh.comment\", \"options\": {…}}"},
				},
				"required": []string{"step"},
			},
		},
		{
			"name":        "workflow_list",
			"description": "The workflow catalog: every config + saved workflow's name, description, inputs, and health — pick a fit and run it (a workflow step via run_step, or workflow.run in a plan) instead of authoring from scratch.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// mcpToolCall executes one tools/call. Tool failures come back as an
// isError result (the MCP convention), not a protocol error.
func mcpToolCall(params json.RawMessage, call MCPCaller, src Source, number int) map[string]any {
	fail := func(msg string) map[string]any {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": msg}},
			"isError": true,
		}
	}
	var p struct {
		Name string `json:"name"`
		Args struct {
			Text      string         `json:"text"`
			Tags      []string       `json:"tags"`
			Scope     string         `json:"scope"`
			Substring string         `json:"substring"`
			Limit     int            `json:"limit"`
			Step      map[string]any `json:"step"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return fail("bad tool arguments: " + err.Error())
	}
	req := IPCRequest{
		Text: p.Args.Text, Tags: p.Args.Tags, Scope: p.Args.Scope,
		Substring: p.Args.Substring, Limit: p.Args.Limit,
		Step: p.Args.Step, Number: number, Source: src,
	}
	switch p.Name {
	case "memory_remember":
		req.Op = "remember"
	case "memory_recall":
		req.Op = "recall"
	case "run_step":
		req.Op = "run_step"
	case "workflow_list":
		req.Op = "workflow_list"
	default:
		return fail(fmt.Sprintf("unknown tool %q", p.Name))
	}
	resp, err := call(req)
	if err != nil {
		return fail(err.Error())
	}
	if resp.Error != "" {
		return fail(resp.Error)
	}
	text := ""
	switch req.Op {
	case "remember":
		text = fmt.Sprintf("remembered %s (scope %s)", resp.Entry.ID, resp.Entry.Scope)
	case "recall":
		if len(resp.Entries) == 0 {
			text = "no matching memories"
		} else {
			b, _ := json.MarshalIndent(resp.Entries, "", "  ")
			text = string(b)
		}
	case "run_step", "workflow_list":
		b, _ := json.MarshalIndent(resp.Result, "", "  ")
		text = string(b)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}
