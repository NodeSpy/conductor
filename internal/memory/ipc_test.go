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
func mcpPipe(t *testing.T, call MCPCaller, src Source) (func(msg string), func() map[string]any) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeMCP(inR, outW, call, src) }()
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
	send, recv := mcpPipe(t, call, src)

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
	// tools/list publishes both tools.
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools := recv()["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
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
}
