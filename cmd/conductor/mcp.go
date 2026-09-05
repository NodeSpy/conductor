package main

import (
	"fmt"
	"os"

	"github.com/NodeSpy/conductor/internal/memory"
)

// cmdMCP implements `conductor mcp memory` — the stdio MCP server the daemon
// injects into agent sessions on runtimes that support live tools (ACP's
// session/new mcpServers). It is launched BY a runtime, not by hand: the
// daemon bakes the socket path and the dispatch's provenance into the flags
// (see memory.SetToolCommand), and every remember the agent makes mid-run
// carries them.
func cmdMCP(args []string) error {
	if len(args) < 1 || args[0] != "memory" {
		return fmt.Errorf("usage: conductor mcp memory --socket <path> [--agent <name>] [--repo <owner/repo>] [--trigger <kind>] [--run <id>] [--number <n>]")
	}
	var socket string
	var number int
	var src memory.Source
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
			src.Agent = next()
		case "--repo":
			src.Repo = next()
		case "--trigger":
			src.Trigger = next()
		case "--run":
			src.Run = next()
		case "--number":
			fmt.Sscanf(next(), "%d", &number)
		default:
			return fmt.Errorf("mcp memory: unknown flag %q", rest[i])
		}
	}
	if socket == "" {
		return fmt.Errorf("mcp memory: --socket is required (the daemon's memory.sock)")
	}
	call := func(req memory.IPCRequest) (memory.IPCResponse, error) {
		return memory.IPCCall(socket, req)
	}
	return memory.ServeMCP(os.Stdin, os.Stdout, call, src, number)
}
