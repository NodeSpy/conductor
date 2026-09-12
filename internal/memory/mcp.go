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
// MCPConfig is the per-dispatch wiring the daemon baked into the tool
// subprocess's flags and environment at injection time.
type MCPConfig struct {
	Source Source
	Number int
	// Claim is the one-shot claim code (#36 §12 / #122) the daemon put in
	// this subprocess's ENVIRONMENT — never argv. ServeMCP exchanges it for
	// the session token over the socket at startup (single-use, short TTL),
	// binding the session to THIS process; the token then lives only in this
	// process's memory.
	Claim string
	// Token is the claimed skill session token. "" = this dispatch has no
	// skill surface: the broker/verb tools are not advertised, and the
	// daemon would deny their calls anyway (deny by default, authorized by
	// token + peer alone).
	Token string
	// Secrets advertises the broker tools (secret_issue/secret_redeem). It
	// tracks the profile's skill.secrets_via, NOT the presence of a
	// credential: every dispatch has a credential, and only some may ask for
	// secrets.
	Secrets bool
	// NoMemory hides the memory tools when the daemon serves the socket for
	// the skill surface without a memory: section.
	NoMemory bool
}

func ServeMCP(r io.Reader, w io.Writer, call MCPCaller, mc MCPConfig) error {
	// Exchange the claim code immediately: it expires fast by design, and a
	// prompt claim shrinks the window in which a scraped code is live. A
	// failed claim leaves the skill tools off (Token "") — the memory tools
	// still serve.
	if mc.Claim != "" && mc.Token == "" {
		if resp, err := call(IPCRequest{Op: "token_claim", Claim: mc.Claim}); err == nil && resp.Error == "" && resp.Result != nil {
			mc.Token, _ = resp.Result["token"].(string)
		}
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

	// The verb toolset (#36 §12) is DYNAMIC: the daemon computes it from the
	// token-bound profile's skill.verbs, so the subprocess fetches it over
	// the socket at tools/list (and re-fetches on an unknown tools/call
	// name). name → uses mapping is remembered for dispatch.
	verbUses := map[string]string{}
	fetchVerbTools := func() []map[string]any {
		if mc.Token == "" {
			return nil
		}
		resp, err := call(IPCRequest{Op: "verb_list", Token: mc.Token})
		if err != nil || resp.Error != "" || resp.Result == nil {
			return nil
		}
		raw, _ := resp.Result["tools"].([]any)
		var tools []map[string]any
		for _, e := range raw {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			uses, _ := m["uses"].(string)
			if name == "" || uses == "" {
				continue
			}
			verbUses[name] = uses
			tools = append(tools, map[string]any{
				"name":        name,
				"description": m["description"],
				"inputSchema": m["inputSchema"],
			})
		}
		return tools
	}
	resolveVerb := func(name string) (string, bool) {
		if u, ok := verbUses[name]; ok {
			return u, true
		}
		fetchVerbTools()
		u, ok := verbUses[name]
		return u, ok
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
			if err := reply(msg.ID, map[string]any{"tools": append(mcpTools(mc), fetchVerbTools()...)}, nil); err != nil {
				return err
			}
		case "tools/call":
			result := mcpToolCall(msg.Params, call, mc, resolveVerb)
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

// mcpTools is the published tool list, shaped by what THIS dispatch was
// wired for: memory tools unless the daemon has no memory: section, broker
// tools only when the daemon minted a skill session token.
func mcpTools(mc MCPConfig) []map[string]any {
	str := map[string]any{"type": "string"}
	strList := map[string]any{"type": "array", "items": str}
	var tools []map[string]any
	if !mc.NoMemory {
		tools = append(tools, memoryTools(str, strList)...)
	}
	tools = append(tools, liveTools()...)
	// The secret tools are advertised on the POLICY, not on the presence of a
	// token. Every dispatch now holds a credential — that is how the socket
	// authenticates provenance (round-12 #1) — so keying on the token would
	// advertise a secret surface to every agent and have the daemon refuse
	// every call to it. A tool an agent can see is a tool it will try.
	if mc.Secrets {
		tools = append(tools, brokerTools(str)...)
	}
	return tools
}

func memoryTools(str, strList map[string]any) []map[string]any {
	return []map[string]any{
		{
			"name":        "memory_remember",
			"description": "Save a durable note to conductor's shared agent memory — future agent runs (yours and other agents') can recall it. Use for lessons that outlive this run: project conventions, gotchas, environment facts.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  map[string]any{"type": "string", "description": "the note to keep"},
					"tags":  strList,
					"scope": map[string]any{"type": "string", "description": "the scope KEY to file this under — omit for the shared set, or name one of the keys listed in your prompt's shared-memory section (e.g. the repo, the workflow, or your own step)"},
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
	}
}

func liveTools() []map[string]any {
	return []map[string]any{
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

// brokerTools is the secret-broker pair (#36 §12): the minimized last resort
// when a raw tool the agent must run needs a credential. Prefer acting
// through conductor (run_step / the verb tools) — then the credential never
// reaches this runtime at all.
func brokerTools(str map[string]any) []map[string]any {
	return []map[string]any{
		{
			"name":        "secret_issue",
			"description": "Request a grant for ONE named conductor secret. Only names your profile's skill.allow_secrets policy lists are issued. The grant is single-use, expires in about a minute, and every issue/use/expiry is audited. Prefer running the action through conductor (run_step / the conductor verb tools) instead — then no credential enters this session at all.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "the secrets: entry to request"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "secret_redeem",
			"description": "Redeem a secret_issue grant for the secret value — exactly once, before the grant expires. Use the value immediately for the one action that needs it; do not store it, echo it, or write it to disk.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"grant": map[string]any{"type": "string", "description": "the grant id secret_issue returned"},
				},
				"required": []string{"grant"},
			},
		},
	}
}

// mcpToolCall executes one tools/call. Tool failures come back as an
// isError result (the MCP convention), not a protocol error.
func mcpToolCall(params json.RawMessage, call MCPCaller, mc MCPConfig, resolveVerb func(string) (string, bool)) map[string]any {
	fail := func(msg string) map[string]any {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": msg}},
			"isError": true,
		}
	}
	var p struct {
		Name string          `json:"name"`
		Raw  json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return fail("bad tool arguments: " + err.Error())
	}
	var args struct {
		Text      string         `json:"text"`
		Tags      []string       `json:"tags"`
		Scope     string         `json:"scope"`
		Substring string         `json:"substring"`
		Limit     int            `json:"limit"`
		Step      map[string]any `json:"step"`
		Name      string         `json:"name"`
		Grant     string         `json:"grant"`
	}
	if len(p.Raw) > 0 {
		if err := json.Unmarshal(p.Raw, &args); err != nil {
			return fail("bad tool arguments: " + err.Error())
		}
	}
	req := IPCRequest{
		Text: args.Text, Tags: args.Tags, Scope: args.Scope,
		Substring: args.Substring, Limit: args.Limit,
		Step: args.Step, Number: mc.Number, Source: mc.Source,
		Token: mc.Token, Secret: args.Name, Grant: args.Grant,
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
	case "secret_issue":
		req.Op = "secret_issue"
	case "secret_redeem":
		req.Op = "secret_redeem"
	default:
		// A dynamic verb tool: the whole arguments object is the verb's
		// options, passed through LITERALLY (the daemon never renders them).
		uses, ok := "", false
		if resolveVerb != nil {
			uses, ok = resolveVerb(p.Name)
		}
		if !ok {
			return fail(fmt.Sprintf("unknown tool %q", p.Name))
		}
		req.Op = "verb"
		req.Uses = uses
		if len(p.Raw) > 0 {
			if err := json.Unmarshal(p.Raw, &req.Options); err != nil {
				return fail("bad tool arguments: " + err.Error())
			}
		}
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
	case "run_step", "workflow_list", "secret_issue", "verb":
		b, _ := json.MarshalIndent(resp.Result, "", "  ")
		text = string(b)
	case "secret_redeem":
		// The redeemed value itself — this is the broker's whole point: the
		// value reaches the agent HERE, once, and nowhere else.
		text, _ = resp.Result["value"].(string)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}
