package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// The live-tool transport. The `conductor mcp memory` subprocess an agent
// runtime attaches (see mcp.go) runs OUTSIDE the daemon, but the memory
// backend lives inside it (an in-process map, a bolt file the daemon holds
// locked, an injected prompt cache) — so tool calls cross a unix socket the
// daemon serves next to its state file. The protocol is one JSON request
// line per connection, one JSON response line back.

// IPCRequest is one live-tool call: the memory pair (remember/recall) plus
// the agent-driven-workflow surface (#36 §11) — run_step executes ONE
// agent-authored step through the flow runner under policy.agent_authored,
// and workflow_list returns the choosable catalog.
type IPCRequest struct {
	Op        string   `json:"op"` // remember | recall | run_step | workflow_list
	Text      string   `json:"text,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Substring string   `json:"substring,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	// Step is the run_step payload: one step in the normal grammar.
	Step map[string]any `json:"step,omitempty"`
	// Number is the dispatch target's PR/issue number (run_step's trigger
	// reconstruction; baked into the tool flags like Source).
	Number int `json:"number,omitempty"`
	// Source is the dispatch provenance the daemon baked into the tool
	// command's flags at injection time — the agent cannot spoof a different
	// run's identity beyond what its own launch carried.
	Source Source `json:"source,omitempty"`
}

// IPCResponse is the daemon's reply.
type IPCResponse struct {
	OK      bool           `json:"ok"`
	Error   string         `json:"error,omitempty"`
	Entry   *Entry         `json:"entry,omitempty"`   // remember
	Entries []Entry        `json:"entries,omitempty"` // recall
	Result  map[string]any `json:"result,omitempty"`  // run_step / workflow_list
}

// LiveOps are the agent-driven-workflow handlers the daemon plugs in at boot
// (the flow runner lives above this package). nil ops → those tools report
// unavailable.
type LiveOps struct {
	// RunStep validates, guards (policy.agent_authored), and executes one
	// agent-authored step, returning its outputs.
	RunStep func(ctx context.Context, src Source, number int, step map[string]any) (map[string]any, error)
	// ListWorkflows returns the workflow catalog (workflow.list's shape).
	ListWorkflows func() map[string]any
}

var (
	liveMu  sync.RWMutex
	liveOps LiveOps
)

// SetLiveOps installs the run_step/workflow_list handlers (boot, tests).
func SetLiveOps(ops LiveOps) {
	liveMu.Lock()
	liveOps = ops
	liveMu.Unlock()
}

func getLiveOps() LiveOps {
	liveMu.RLock()
	defer liveMu.RUnlock()
	return liveOps
}

// ListenSocket opens the daemon-side unix socket, replacing a stale one.
func ListenSocket(path string) (net.Listener, error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("memory: tool socket %s: %w", path, err)
	}
	// Same-user only: the socket accepts memory writes with provenance.
	_ = os.Chmod(path, 0o600)
	return l, nil
}

// ServeIPC accepts tool calls until ctx ends or the listener closes. Writes
// audit like the verb surface does (via: tool).
func ServeIPC(ctx context.Context, l net.Listener, m *Manager, audit func(map[string]any), log func(string, ...any)) {
	if log == nil {
		log = func(string, ...any) {}
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			return // listener closed (shutdown)
		}
		go serveConn(conn, m, audit, log)
	}
}

func serveConn(conn net.Conn, m *Manager, audit func(map[string]any), log func(string, ...any)) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var req IPCRequest
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		writeResp(conn, IPCResponse{Error: "memory: bad tool request: " + err.Error()})
		return
	}
	writeResp(conn, handleIPC(m, req, audit, log))
}

func writeResp(conn net.Conn, resp IPCResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		b = []byte(`{"ok":false,"error":"memory: unencodable response"}`)
	}
	_, _ = conn.Write(append(b, '\n'))
}

// handleIPC executes one tool call against the manager.
func handleIPC(m *Manager, req IPCRequest, audit func(map[string]any), log func(string, ...any)) IPCResponse {
	if m == nil {
		return IPCResponse{Error: "memory: not configured"}
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	aud := func(e map[string]any) {
		if audit != nil {
			audit(e)
		}
	}
	switch req.Op {
	case "remember":
		// The same write guard as the harvest path: the IPC tool is driven
		// by live agents and must not persist tracked secret material.
		if gerr := m.checkGuard(req.Text); gerr != nil {
			aud(map[string]any{"event": "memory_remember", "via": "tool", "outcome": "blocked",
				"agent": req.Source.Agent, "repo": req.Source.Repo, "error": gerr.Error()})
			return IPCResponse{Error: gerr.Error()}
		}
		e, err := m.Remember(req.Text, req.Tags, req.Scope, req.Source)
		if err != nil {
			aud(map[string]any{"event": "memory_remember", "via": "tool", "outcome": "failed",
				"agent": req.Source.Agent, "repo": req.Source.Repo, "error": err.Error()})
			return IPCResponse{Error: err.Error()}
		}
		log("memory: agent %q remembered %s (scope %s)", req.Source.Agent, e.ID, e.Scope)
		aud(map[string]any{"event": "memory_remember", "via": "tool", "outcome": "ok",
			"agent": req.Source.Agent, "repo": req.Source.Repo, "id": e.ID, "scope": e.Scope})
		return IPCResponse{OK: true, Entry: &e}
	case "recall":
		q := Query{Tags: req.Tags, Substring: req.Substring, Limit: req.Limit}
		if req.Scope != "" {
			resolved, err := ResolveScope(req.Scope, req.Source)
			if err != nil {
				return IPCResponse{Error: err.Error()}
			}
			q.Scopes = []string{resolved}
		}
		entries, err := m.Recall(q)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		aud(map[string]any{"event": "memory_recall", "via": "tool",
			"agent": req.Source.Agent, "repo": req.Source.Repo, "count": len(entries)})
		// Recalled text goes straight into the calling agent's context:
		// redact like the prompt-injection path.
		for i := range entries {
			entries[i].Text = m.redactText(entries[i].Text)
		}
		return IPCResponse{OK: true, Entries: entries}
	case "run_step":
		ops := getLiveOps()
		if ops.RunStep == nil {
			return IPCResponse{Error: "run_step: the live plan runner is not available on this daemon"}
		}
		if len(req.Step) == 0 {
			return IPCResponse{Error: "run_step: step is required"}
		}
		out, err := ops.RunStep(context.Background(), req.Source, req.Number, req.Step)
		if err != nil {
			aud(map[string]any{"event": "plan_live_step", "via": "tool", "outcome": "failed",
				"agent": req.Source.Agent, "repo": req.Source.Repo, "error": err.Error()})
			return IPCResponse{Error: err.Error()}
		}
		aud(map[string]any{"event": "plan_live_step", "via": "tool", "outcome": "ok",
			"agent": req.Source.Agent, "repo": req.Source.Repo})
		return IPCResponse{OK: true, Result: out}
	case "workflow_list":
		ops := getLiveOps()
		if ops.ListWorkflows == nil {
			return IPCResponse{Error: "workflow_list: not available on this daemon"}
		}
		return IPCResponse{OK: true, Result: ops.ListWorkflows()}
	}
	return IPCResponse{Error: fmt.Sprintf("memory: unknown tool op %q", req.Op)}
}

// IPCCall dials the daemon socket for one tool call (the subprocess side).
func IPCCall(socket string, req IPCRequest) (IPCResponse, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return IPCResponse{}, fmt.Errorf("memory: daemon socket %s: %w", socket, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	b, err := json.Marshal(req)
	if err != nil {
		return IPCResponse{}, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return IPCResponse{}, err
	}
	var resp IPCResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return IPCResponse{}, fmt.Errorf("memory: read tool response: %w", err)
	}
	return resp, nil
}
