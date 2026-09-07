package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/callable"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
)

// cmdMCP dispatches `conductor mcp <face>`: the memory/skill face injected into
// agent sessions, or the callable face an external MCP client attaches to.
func cmdMCP(args []string) error {
	if len(args) >= 1 && args[0] == "callable" {
		return cmdMCPCallable(args[1:])
	}
	if len(args) < 1 || args[0] != "memory" {
		return fmt.Errorf("usage: conductor mcp memory|callable …")
	}
	return cmdMCPMemory(args)
}

// cmdMCPMemory implements `conductor mcp memory` — the stdio MCP server the
// daemon injects into agent sessions on runtimes that support live tools (ACP's
// session/new mcpServers). It is launched BY a runtime, not by hand: the
// daemon bakes the socket path and the dispatch's provenance into the flags
// (see memory.SetToolCommand), and every remember the agent makes mid-run
// carries them.
func cmdMCPMemory(args []string) error {
	if len(args) < 1 || args[0] != "memory" {
		return fmt.Errorf("usage: conductor mcp memory --socket <path> [--agent <name>] [--repo <owner/repo>] [--trigger <kind>] [--run <id>] [--number <n>] [--no-memory] (skill claim code via $CONDUCTOR_SKILL_CLAIM)")
	}
	var socket string
	var mc memory.MCPConfig
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		next := func() string {
			if i+1 < len(rest) {
				i++
				return rest[i]
			}
			return ""
		}
		switch rest[i] {
		case "--socket":
			socket = next()
		case "--agent":
			mc.Source.Agent = next()
		case "--repo":
			mc.Source.Repo = next()
		case "--trigger":
			mc.Source.Trigger = next()
		case "--run":
			mc.Source.Run = next()
		case "--number":
			fmt.Sscanf(next(), "%d", &mc.Number)
		case "--no-memory":
			mc.NoMemory = true
		default:
			return fmt.Errorf("mcp memory: unknown flag %q", rest[i])
		}
	}
	if socket == "" {
		return fmt.Errorf("mcp memory: --socket is required (the daemon's memory.sock)")
	}
	// The skill claim code (#36 §12 / #122) arrives via the ENVIRONMENT the
	// daemon set on this MCP server — never argv, which any same-user process
	// can read from a process listing. ServeMCP exchanges it (single-use,
	// short-TTL) for the session token over the socket.
	mc.Claim = os.Getenv("CONDUCTOR_SKILL_CLAIM")
	call := func(req memory.IPCRequest) (memory.IPCResponse, error) {
		return memory.IPCCall(socket, req)
	}
	return memory.ServeMCP(os.Stdin, os.Stdout, call, mc)
}

// mcpCallableTimeout bounds how long a tools/call waits for its run to finish
// before returning the last-known (still-running) record.
const mcpCallableTimeout = 5 * time.Minute

// cmdMCPCallable implements `conductor mcp callable` — a stdio MCP server that
// exposes `callable: true` workflows as tools. Unlike `mcp memory`, a human (or
// an MCP client's config) launches it directly; it reads the config to find the
// workflows + the daemon's control socket, and each tool call dispatches through
// that socket and blocks for the structured result.
//
// By default (#36 §13 review, item 2) this face is held to the SAME token model
// as the HTTP surface: it REQUIRES `--token <name>`, exposes only the workflows
// that token is scoped to, and marks each dispatch req.Callable so the daemon
// re-checks the callable opt-in + token scope and writes a `callable_invoke`
// audit. `callable.mcp_local: true` opts out — every `callable: true` workflow
// is exposed with no token and no audit, trusting the same-user boundary alone.
func cmdMCPCallable(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	var tokenName string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--token":
			if i+1 < len(rest) {
				i++
				tokenName = rest[i]
			}
		default:
			return fmt.Errorf("mcp callable: unknown flag %q", rest[i])
		}
	}

	// Resolve the caller identity + its scope. Strong by default: a token is
	// required unless callable.mcp_local explicitly opts out.
	var tok config.CallableToken
	scoped := tokenName != ""
	if scoped {
		t, ok := cfg.Callable.TokenByName(tokenName)
		if !ok {
			return fmt.Errorf("mcp callable: no callable token named %q in config", tokenName)
		}
		tok = t
	} else if !cfg.Callable.MCPLocal {
		return fmt.Errorf("mcp callable: --token <name> is required (set callable.mcp_local: true to expose all callable workflows unscoped)")
	}

	var tools []callable.MCPTool
	for _, t := range cfg.Triggers {
		if !t.IsCallable() || t.Name == "" {
			continue
		}
		if scoped && !tok.Allows(t.Name) {
			continue // deny-by-default: only the token's own workflows
		}
		tools = append(tools, callable.MCPTool{Name: t.Name})
	}
	histDir := historyDirPath(cfg)
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "conductor mcp callable: "+format+"\n", a...) }

	invoke := func(ctx context.Context, name string, input map[string]any) (map[string]any, error) {
		histID := callable.NewRunID()
		req := controlRequest{Cmd: "run", Name: name, Inputs: input, HistoryID: histID}
		if scoped {
			// Mark the dispatch so the daemon enforces the token model + audits
			// it; unscoped (mcp_local) stays on the ungated local-run path.
			req.Callable = true
			req.Caller = tok.Name
		}
		resp, err := sendControl(cfg, req)
		if err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		// Poll the §20 record this run pins until it reaches a terminal status
		// or the deadline — then hand back the same body the HTTP face returns.
		deadline := time.Now().Add(mcpCallableTimeout)
		for {
			if rec, rerr := store.ReadHistory(histDir, histID); rerr == nil && callable.Terminal(rec.Status) {
				return callable.Result(histID, rec), nil
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				rec, _ := store.ReadHistory(histDir, histID)
				rec.ID = histID
				return callable.Result(histID, rec), nil
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	return callable.ServeMCP(os.Stdin, os.Stdout, callable.MCPDeps{Tools: tools, Invoke: invoke, Log: logf})
}
