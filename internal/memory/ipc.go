package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// maxHTTPBody bounds a remote tool request body: tool calls are small JSON
// (options, a query, a step), never a payload upload.
const maxHTTPBody = 1 << 20 // 1 MiB

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
	// Token is the skill session token (#36 §12), obtained by exchanging the
	// dispatch-time claim code (op token_claim). The broker ops authorize by
	// it (plus the connection's peer credentials) ALONE — unlike Source it is
	// unguessable, and the daemon maps it server-side to the real dispatched
	// profile and its skill: policy.
	Token string `json:"token,omitempty"`
	// Claim is the one-shot claim code token_claim exchanges for a session
	// token. The daemon bakes it into the tool subprocess's environment —
	// never argv — and it is single-use with a short TTL, so a code scraped
	// from a process listing or /proc later is already dead.
	Claim string `json:"claim,omitempty"`
	// Secret is the named secret secret_issue requests.
	Secret string `json:"secret,omitempty"`
	// Grant is the grant id secret_redeem redeems.
	Grant string `json:"grant,omitempty"`
	// Uses / Options are the verb op's payload (#36 §12 verb tools): one
	// connector verb invoked with LITERAL options, gated per profile.
	Uses    string         `json:"uses,omitempty"`
	Options map[string]any `json:"options,omitempty"`
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
	// ClaimToken exchanges a one-shot claim code for a session token, binding
	// the session to the claiming connection's peer process (#36 §12 / #122).
	ClaimToken func(claim string, peer Peer) (token string, err error)
	// IssueSecret / RedeemSecret are the secret broker (#36 §12), wired from
	// internal/skill at boot. Plain funcs so this package stays free of the
	// skill dependency. nil → the broker ops report unavailable. Every
	// token-authorized op carries the calling connection's peer identity —
	// the broker refuses a token presented by a different process than the
	// one that claimed it.
	IssueSecret  func(token, name string, peer Peer) (grant string, expires time.Time, err error)
	RedeemSecret func(token, grant string, peer Peer) (value string, err error)
	// SkillVerbs / RunVerb are the verb-tool surface (#36 §12): the catalog
	// of verbs the token's profile exposes (MCP tool declarations), and one
	// gated verb execution. Both authorize by token + peer server-side.
	SkillVerbs func(token string, peer Peer) ([]map[string]any, error)
	RunVerb    func(ctx context.Context, token, uses string, options map[string]any, peer Peer) (map[string]any, error)
	// Identify resolves a session token to the dispatch provenance the broker
	// holds for it (the real Agent/Repo/Trigger + PR/issue number), authorizing
	// by token + peer. The CLI and remote-HTTP faces carry a token but no baked
	// Source, so the memory / run_step ops derive provenance from HERE rather
	// than trusting a body-supplied Source — a caller cannot spoof another
	// run's identity. nil → those ops fall back to the body Source (the MCP
	// path, where the daemon baked Source into the tool command's flags).
	Identify func(token string, peer Peer) (src Source, number int, ok bool)
}

// Peer is the socket-peer identity of the calling process (Linux
// SO_PEERCRED + /proc start time; zero value on platforms without one).
// Mirrored by internal/skill so this package stays dependency-free.
type Peer struct {
	PID       int
	StartTime uint64
	UID       uint32 // SO_PEERCRED uid — the uid-bound session token authorizes by this
	Valid     bool
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
	// The peer credentials come from the KERNEL, not the request — the skill
	// ops bind and verify sessions against them.
	writeResp(conn, handleIPC(m, req, peerInfo(conn), audit, log))
}

func writeResp(conn net.Conn, resp IPCResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		b = []byte(`{"ok":false,"error":"memory: unencodable response"}`)
	}
	_, _ = conn.Write(append(b, '\n'))
}

// handleIPC executes one tool call against the manager. m may be nil when
// the socket is up for the skill surface alone (broker/run_step); only the
// memory ops need it.
func handleIPC(m *Manager, req IPCRequest, peer Peer, audit func(map[string]any), log func(string, ...any)) IPCResponse {
	if log == nil {
		log = func(string, ...any) {}
	}
	aud := func(e map[string]any) {
		if audit != nil {
			audit(e)
		}
	}
	if m == nil && (req.Op == "remember" || req.Op == "recall") {
		return IPCResponse{Error: "memory: not configured"}
	}
	// Provenance binding for the token-carrying faces (CLI / remote HTTP): the
	// ops below trust the dispatched identity the broker holds for the token,
	// never a Source in the request body. When a token is present and the
	// broker can identify it, its provenance is authoritative; when the broker
	// rejects the token, the op is denied rather than falling back to a
	// spoofable body Source. A request with no token (the MCP tool subprocess,
	// which bakes Source into its flags) keeps the body Source unchanged.
	if req.Token != "" && (req.Op == "remember" || req.Op == "recall" || req.Op == "run_step") {
		if ops := getLiveOps(); ops.Identify != nil {
			src, number, ok := ops.Identify(req.Token, peer)
			if !ok {
				return IPCResponse{Error: "memory: unknown or unauthorized session token"}
			}
			req.Source, req.Number = src, number
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
	case "token_claim":
		ops := getLiveOps()
		if ops.ClaimToken == nil {
			return IPCResponse{Error: "token_claim: the skill surface is not available on this daemon"}
		}
		tok, err := ops.ClaimToken(req.Claim, peer)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Result: map[string]any{"token": tok}}
	case "secret_issue":
		// The broker audits every outcome itself (it knows the real identity
		// behind the token); nothing to add at this layer.
		ops := getLiveOps()
		if ops.IssueSecret == nil {
			return IPCResponse{Error: "secret_issue: no secret broker on this daemon"}
		}
		id, exp, err := ops.IssueSecret(req.Token, req.Secret, peer)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Result: map[string]any{
			"grant": id, "expires": exp.UTC().Format(time.RFC3339),
		}}
	case "secret_redeem":
		ops := getLiveOps()
		if ops.RedeemSecret == nil {
			return IPCResponse{Error: "secret_redeem: no secret broker on this daemon"}
		}
		v, err := ops.RedeemSecret(req.Token, req.Grant, peer)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Result: map[string]any{"value": v}}
	case "verb_list":
		// The verb-tool catalog for THIS token's profile. The runner audits
		// executions; listing is read-only.
		ops := getLiveOps()
		if ops.SkillVerbs == nil {
			return IPCResponse{Error: "verb_list: the skill verb surface is not available on this daemon"}
		}
		tools, err := ops.SkillVerbs(req.Token, peer)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		list := make([]any, len(tools))
		for i, tl := range tools {
			list[i] = tl
		}
		return IPCResponse{OK: true, Result: map[string]any{"tools": list}}
	case "verb":
		ops := getLiveOps()
		if ops.RunVerb == nil {
			return IPCResponse{Error: "verb: the skill verb surface is not available on this daemon"}
		}
		out, err := ops.RunVerb(context.Background(), req.Token, req.Uses, req.Options, peer)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Result: out}
	}
	return IPCResponse{Error: fmt.Sprintf("memory: unknown tool op %q", req.Op)}
}

// HTTPHandler is the remote face of the tool surface: the same ops ServeIPC
// serves on the local unix socket, reachable over HTTP for an agent running on
// another machine (reverse-proxied / tunnelled with TLS terminated upstream —
// see the daemon wiring). One POST is one op: the body is the IPCRequest JSON,
// and the session token rides the `Authorization: Bearer` header, NOT the body,
// so a caller cannot smuggle a different session's token in the payload.
//
// A remote connection has no kernel peer credentials, so the caller is
// presented to the broker as Peer{} (Valid:false); a uid-bound session token
// authorizes by the bearer token alone (its uid check has no local identity to
// compare against — that is the deliberate TLS-bearer model). token_claim is
// refused here: the one-shot claim flow is peer-bound and local-only; a remote
// agent receives its session token directly in its environment.
func HTTPHandler(m *Manager, audit func(map[string]any), log func(string, ...any)) http.Handler {
	if log == nil {
		log = func(string, ...any) {}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpJSON(w, http.StatusMethodNotAllowed, IPCResponse{Error: "skill: POST only"})
			return
		}
		tok := bearerToken(r.Header.Get("Authorization"))
		if tok == "" {
			httpJSON(w, http.StatusUnauthorized, IPCResponse{Error: "skill: missing bearer token"})
			return
		}
		var req IPCRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxHTTPBody)).Decode(&req); err != nil {
			httpJSON(w, http.StatusBadRequest, IPCResponse{Error: "skill: bad tool request: " + err.Error()})
			return
		}
		// The token is the header's, never the body's.
		req.Token = tok
		if req.Op == "token_claim" {
			httpJSON(w, http.StatusForbidden, IPCResponse{Error: "skill: token_claim is not available over the remote endpoint"})
			return
		}
		// No kernel peer identity on a remote connection.
		resp := handleIPC(m, req, Peer{}, audit, log)
		httpJSON(w, http.StatusOK, resp)
	})
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header
// (case-insensitive scheme), returning "" when absent or malformed.
func bearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func httpJSON(w http.ResponseWriter, code int, resp IPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, err := json.Marshal(resp)
	if err != nil {
		b = []byte(`{"ok":false,"error":"skill: unencodable response"}`)
	}
	_, _ = w.Write(append(b, '\n'))
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
