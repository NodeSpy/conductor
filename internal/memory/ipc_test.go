package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startIPC serves the tool socket for one test and returns the socket path
// and the captured audit entries.
func startIPC(t *testing.T, m *Manager) (string, func() []map[string]any) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "memory.sock")
	l, err := ListenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	var audits []map[string]any
	go ServeIPC(ctx, l, m, func(e map[string]any) {
		mu.Lock()
		audits = append(audits, e)
		mu.Unlock()
	}, nil)
	return sock, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), audits...)
	}
}

func TestIPCRememberRecallRoundTrip(t *testing.T) {
	m := testManager(t, NewMemBackend())
	sock, audits := startIPC(t, m)
	src := Source{Agent: "gemini", Repo: "o/r", Trigger: "issue_matched"}

	resp, err := IPCCall(sock, IPCRequest{Op: "remember", Text: "socket note", Tags: []string{"live"}, Scope: "repo", Source: src})
	if err != nil || !resp.OK || resp.Entry == nil {
		t.Fatalf("remember: %+v %v", resp, err)
	}
	if resp.Entry.Scope != "repo:o/r" || resp.Entry.Source.Agent != "gemini" {
		t.Fatalf("entry: %+v", resp.Entry)
	}

	resp, err = IPCCall(sock, IPCRequest{Op: "recall", Tags: []string{"live"}, Scope: "repo", Limit: 5, Source: src})
	if err != nil || !resp.OK || len(resp.Entries) != 1 || resp.Entries[0].Text != "socket note" {
		t.Fatalf("recall: %+v %v", resp, err)
	}

	// Errors come back in-band.
	resp, err = IPCCall(sock, IPCRequest{Op: "remember", Source: src})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "text is required") {
		t.Fatalf("bad remember: %+v %v", resp, err)
	}
	resp, err = IPCCall(sock, IPCRequest{Op: "drop"})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "unknown tool op") {
		t.Fatalf("unknown op: %+v %v", resp, err)
	}

	// Tool calls audit like the verb surface.
	got := audits()
	var remembered, recalled, failed int
	for _, e := range got {
		switch {
		case e["event"] == "memory_remember" && e["outcome"] == "ok":
			remembered++
			if e["agent"] != "gemini" || e["via"] != "tool" {
				t.Errorf("remember audit: %+v", e)
			}
		case e["event"] == "memory_remember" && e["outcome"] == "failed":
			failed++
		case e["event"] == "memory_recall":
			recalled++
		}
	}
	if remembered != 1 || recalled != 1 || failed != 1 {
		t.Fatalf("audits: remembered=%d recalled=%d failed=%d (%+v)", remembered, recalled, failed, got)
	}
}

func TestIPCCallNoDaemon(t *testing.T) {
	if _, err := IPCCall(filepath.Join(t.TempDir(), "nope.sock"), IPCRequest{Op: "recall"}); err == nil {
		t.Fatal("dial to a missing socket must error")
	}
}

// mcpPipe runs ServeMCP against in-memory pipes and returns a send/receive
// pair driving it.
func mcpPipe(t *testing.T, call MCPCaller, mc MCPConfig) (func(msg string), func() map[string]any) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeMCP(inR, outW, call, mc) }()
	t.Cleanup(func() {
		inW.Close()
		if err := <-done; err != nil {
			t.Errorf("ServeMCP: %v", err)
		}
		outW.Close()
	})
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	send := func(msg string) {
		if _, err := inW.Write([]byte(msg + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() map[string]any {
		if !sc.Scan() {
			t.Fatalf("mcp: no response: %v", sc.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("mcp: bad response %q: %v", sc.Text(), err)
		}
		return m
	}
	return send, recv
}

func TestMCPServerLoop(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Agent: "gemini", Repo: "o/r"}
	var gotReq IPCRequest
	call := func(req IPCRequest) (IPCResponse, error) {
		gotReq = req
		return IPCResponse{}, nil // overwritten per-case below via closure state
	}
	// Route through the real handler for realistic responses.
	call = func(req IPCRequest) (IPCResponse, error) {
		gotReq = req
		return handleIPC(m, req, nil, nil), nil
	}
	send, recv := mcpPipe(t, call, MCPConfig{Source: src, Number: 7})

	// initialize handshake echoes the client's protocol version.
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientCapabilities":{}}}`)
	res := recv()["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize: %+v", res)
	}
	// notifications need no reply; ping does.
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := recv(); r["result"] == nil {
		t.Fatalf("ping: %+v", r)
	}
	// tools/list publishes the memory pair + the plan surface.
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools := recv()["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools: %+v", tools)
	}
	if name := tools[0].(map[string]any)["name"]; name != "memory_remember" {
		t.Fatalf("first tool: %v", name)
	}
	// tools/call memory_remember persists through the caller with provenance.
	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_remember","arguments":{"text":"live note","tags":["t"],"scope":"repo"}}}`)
	r := recv()["result"].(map[string]any)
	if r["isError"] != false {
		t.Fatalf("remember call: %+v", r)
	}
	if gotReq.Op != "remember" || gotReq.Source.Agent != "gemini" || gotReq.Scope != "repo" {
		t.Fatalf("caller request: %+v", gotReq)
	}
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "remembered ") || !strings.Contains(text, "repo:o/r") {
		t.Fatalf("remember text: %q", text)
	}
	// tools/call memory_recall returns the entries as JSON text.
	send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_recall","arguments":{"tags":["t"]}}}`)
	r = recv()["result"].(map[string]any)
	text = r["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "live note") {
		t.Fatalf("recall text: %q", text)
	}
	// A tool failure is an isError result, not a protocol error.
	send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_remember","arguments":{}}}`)
	r = recv()["result"].(map[string]any)
	if r["isError"] != true {
		t.Fatalf("failed call must set isError: %+v", r)
	}
	// Unknown tool name.
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`)
	if r = recv()["result"].(map[string]any); r["isError"] != true {
		t.Fatalf("unknown tool: %+v", r)
	}
	// Unknown method is a JSON-RPC error.
	send(`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`)
	if e := recv()["error"]; e == nil {
		t.Fatal("unknown method must return a JSON-RPC error")
	}
	// Parse errors respond without killing the loop.
	send(`{nope`)
	if e := recv()["error"]; e == nil {
		t.Fatal("parse error must respond with an error")
	}
	send(`{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	if r := recv(); r["result"] == nil {
		t.Fatalf("loop must survive a parse error: %+v", r)
	}

	// run_step routes through the live ops with provenance + number; without
	// a wired runner it fails in-band.
	send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"run_step","arguments":{"step":{"uses":"svc.post"}}}}`)
	r = recv()["result"].(map[string]any)
	if r["isError"] != true || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "not available") {
		t.Fatalf("unwired run_step: %+v", r)
	}
	SetLiveOps(LiveOps{
		RunStep: func(_ context.Context, src Source, number int, step map[string]any) (map[string]any, error) {
			if src.Agent != "gemini" || number != 7 || step["uses"] != "svc.post" {
				t.Errorf("run_step wiring: src=%+v number=%d step=%v", src, number, step)
			}
			return map[string]any{"executed": 1}, nil
		},
		ListWorkflows: func() map[string]any { return map[string]any{"count": 2} },
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })
	send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"run_step","arguments":{"step":{"uses":"svc.post"}}}}`)
	r = recv()["result"].(map[string]any)
	text = r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] != false || !strings.Contains(text, `"executed": 1`) {
		t.Fatalf("run_step: %+v", r)
	}
	send(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"workflow_list","arguments":{}}}`)
	r = recv()["result"].(map[string]any)
	if !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), `"count": 2`) {
		t.Fatalf("workflow_list: %+v", r)
	}
}

// The broker ops (#36 §12) ride the same socket surface. A daemon without a
// memory: section still serves them (m == nil); the memory ops refuse.
func TestIPCSecretBrokerOps(t *testing.T) {
	if resp := handleIPC(nil, IPCRequest{Op: "remember", Text: "x"}, nil, nil); resp.Error != "memory: not configured" {
		t.Fatalf("remember without memory: %+v", resp)
	}
	if resp := handleIPC(nil, IPCRequest{Op: "recall"}, nil, nil); resp.Error != "memory: not configured" {
		t.Fatalf("recall without memory: %+v", resp)
	}
	if resp := handleIPC(nil, IPCRequest{Op: "secret_issue", Secret: "k"}, nil, nil); !strings.Contains(resp.Error, "no secret broker") {
		t.Fatalf("unwired secret_issue: %+v", resp)
	}
	if resp := handleIPC(nil, IPCRequest{Op: "secret_redeem", Grant: "g"}, nil, nil); !strings.Contains(resp.Error, "no secret broker") {
		t.Fatalf("unwired secret_redeem: %+v", resp)
	}

	exp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	SetLiveOps(LiveOps{
		IssueSecret: func(token, name string) (string, time.Time, error) {
			if token != "tok-1" || name != "deploy_key" {
				t.Errorf("issue wiring: token=%q name=%q", token, name)
			}
			return "grant-abc", exp, nil
		},
		RedeemSecret: func(token, grant string) (string, error) {
			if token != "tok-1" || grant != "grant-abc" {
				t.Errorf("redeem wiring: token=%q grant=%q", token, grant)
			}
			return "s3cretvalue", nil
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })

	resp := handleIPC(nil, IPCRequest{Op: "secret_issue", Token: "tok-1", Secret: "deploy_key"}, nil, nil)
	if !resp.OK || resp.Result["grant"] != "grant-abc" || resp.Result["expires"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("secret_issue: %+v", resp)
	}
	resp = handleIPC(nil, IPCRequest{Op: "secret_redeem", Token: "tok-1", Grant: "grant-abc"}, nil, nil)
	if !resp.OK || resp.Result["value"] != "s3cretvalue" {
		t.Fatalf("secret_redeem: %+v", resp)
	}
}

// The MCP layer: broker tools are advertised only when the daemon minted a
// session token; the token rides the launch flags SERVER-SIDE — a tool call
// cannot substitute its own; --no-memory hides the memory tools.
func TestMCPBrokerTools(t *testing.T) {
	var got []IPCRequest
	call := func(req IPCRequest) (IPCResponse, error) {
		if req.Op == "verb_list" {
			// tools/list also fetches the dynamic verb catalog when a token
			// is present — not under test here.
			return IPCResponse{Error: "verb_list: not available"}, nil
		}
		got = append(got, req)
		switch req.Op {
		case "secret_issue":
			return IPCResponse{OK: true, Result: map[string]any{"grant": "grant-abc", "expires": "2026-01-02T03:04:05Z"}}, nil
		case "secret_redeem":
			return IPCResponse{OK: true, Result: map[string]any{"value": "s3cretvalue"}}, nil
		}
		return IPCResponse{Error: "unexpected op " + req.Op}, nil
	}
	send, recv := mcpPipe(t, call, MCPConfig{Token: "tok-1", NoMemory: true})

	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	names := map[string]bool{}
	for _, tool := range recv()["result"].(map[string]any)["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["secret_issue"] || !names["secret_redeem"] {
		t.Fatalf("broker tools missing with a token: %v", names)
	}
	if names["memory_remember"] || names["memory_recall"] {
		t.Fatalf("--no-memory must hide the memory tools: %v", names)
	}

	// The token comes from the daemon-baked config; a call trying to smuggle
	// its own "token" argument does not override it.
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"secret_issue","arguments":{"name":"deploy_key","token":"forged"}}}`)
	r := recv()["result"].(map[string]any)
	if r["isError"] != false {
		t.Fatalf("secret_issue: %+v", r)
	}
	if len(got) != 1 || got[0].Op != "secret_issue" || got[0].Token != "tok-1" || got[0].Secret != "deploy_key" {
		t.Fatalf("issue request wiring: %+v", got)
	}
	if text := r["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "grant-abc") {
		t.Fatalf("issue text: %q", text)
	}

	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"secret_redeem","arguments":{"grant":"grant-abc"}}}`)
	r = recv()["result"].(map[string]any)
	if got[1].Op != "secret_redeem" || got[1].Token != "tok-1" || got[1].Grant != "grant-abc" {
		t.Fatalf("redeem request wiring: %+v", got[1])
	}
	if text := r["content"].([]any)[0].(map[string]any)["text"].(string); text != "s3cretvalue" {
		t.Fatalf("redeem must return the raw value to the agent, got %q", text)
	}
}

// Without a token the broker tools are not advertised at all.
func TestMCPBrokerToolsHiddenWithoutToken(t *testing.T) {
	call := func(req IPCRequest) (IPCResponse, error) { return IPCResponse{}, nil }
	send, recv := mcpPipe(t, call, MCPConfig{})
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, tool := range recv()["result"].(map[string]any)["tools"].([]any) {
		n := tool.(map[string]any)["name"].(string)
		if n == "secret_issue" || n == "secret_redeem" {
			t.Fatalf("broker tool %q advertised without a session token", n)
		}
	}
}

// The verb ops (#36 §12): verb_list serves the token's tool catalog, verb
// executes one gated verb — both through the daemon-wired live ops.
func TestIPCVerbOps(t *testing.T) {
	if resp := handleIPC(nil, IPCRequest{Op: "verb", Uses: "svc.post"}, nil, nil); !strings.Contains(resp.Error, "not available") {
		t.Fatalf("unwired verb: %+v", resp)
	}
	if resp := handleIPC(nil, IPCRequest{Op: "verb_list"}, nil, nil); !strings.Contains(resp.Error, "not available") {
		t.Fatalf("unwired verb_list: %+v", resp)
	}
	SetLiveOps(LiveOps{
		SkillVerbs: func(token string) ([]map[string]any, error) {
			if token != "tok-1" {
				t.Errorf("verb_list token: %q", token)
			}
			return []map[string]any{{"name": "svc_post", "uses": "svc.post", "description": "d",
				"inputSchema": map[string]any{"type": "object"}}}, nil
		},
		RunVerb: func(_ context.Context, token, uses string, options map[string]any) (map[string]any, error) {
			if token != "tok-1" || uses != "svc.post" || options["text"] != "hi" {
				t.Errorf("verb wiring: token=%q uses=%q options=%v", token, uses, options)
			}
			return map[string]any{"id": 42}, nil
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })

	resp := handleIPC(nil, IPCRequest{Op: "verb_list", Token: "tok-1"}, nil, nil)
	if !resp.OK || len(resp.Result["tools"].([]any)) != 1 {
		t.Fatalf("verb_list: %+v", resp)
	}
	resp = handleIPC(nil, IPCRequest{Op: "verb", Token: "tok-1", Uses: "svc.post",
		Options: map[string]any{"text": "hi"}}, nil, nil)
	if !resp.OK || resp.Result["id"] != 42 {
		t.Fatalf("verb: %+v", resp)
	}
}

// The MCP layer serves the DYNAMIC verb toolset: tools/list appends the
// daemon-computed catalog, and a tools/call by tool name dispatches op verb
// with the raw arguments as literal options (token attached server-side).
func TestMCPVerbTools(t *testing.T) {
	var got []IPCRequest
	call := func(req IPCRequest) (IPCResponse, error) {
		got = append(got, req)
		switch req.Op {
		case "verb_list":
			return IPCResponse{OK: true, Result: map[string]any{"tools": []any{
				map[string]any{"name": "svc_post", "uses": "svc.post", "description": "post it",
					"inputSchema": map[string]any{"type": "object"}},
			}}}, nil
		case "verb":
			return IPCResponse{OK: true, Result: map[string]any{"id": 42}}, nil
		}
		return IPCResponse{Error: "unexpected op " + req.Op}, nil
	}
	send, recv := mcpPipe(t, call, MCPConfig{Token: "tok-1", NoMemory: true})

	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	names := map[string]bool{}
	for _, tool := range recv()["result"].(map[string]any)["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["svc_post"] {
		t.Fatalf("verb tool missing from tools/list: %v", names)
	}

	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"svc_post","arguments":{"text":"hi","meta":{"k":1}}}}`)
	r := recv()["result"].(map[string]any)
	if r["isError"] != false {
		t.Fatalf("svc_post call: %+v", r)
	}
	last := got[len(got)-1]
	if last.Op != "verb" || last.Uses != "svc.post" || last.Token != "tok-1" {
		t.Fatalf("verb request wiring: %+v", last)
	}
	if last.Options["text"] != "hi" || last.Options["meta"].(map[string]any)["k"] != float64(1) {
		t.Fatalf("options must pass through raw: %+v", last.Options)
	}
	if text := r["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, `"id": 42`) {
		t.Fatalf("verb result text: %q", text)
	}

	// An unknown tool name stays an error even after a re-fetch.
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope_tool","arguments":{}}}`)
	r = recv()["result"].(map[string]any)
	if r["isError"] != true {
		t.Fatalf("unknown tool must error: %+v", r)
	}
}

// Without a token, tools/list never fetches the verb catalog.
func TestMCPVerbToolsHiddenWithoutToken(t *testing.T) {
	call := func(req IPCRequest) (IPCResponse, error) {
		if req.Op == "verb_list" {
			t.Errorf("verb_list must not be fetched without a token")
		}
		return IPCResponse{}, nil
	}
	send, recv := mcpPipe(t, call, MCPConfig{})
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	recv()
}
