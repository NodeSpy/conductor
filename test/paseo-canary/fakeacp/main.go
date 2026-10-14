// Command fakeacp is a minimal ACP agent for the paseo provider canary: no
// model, no credentials. It answers initialize, session/new|load|close, and
// replies to every prompt with "canary-ok" plus what it can see of the jail
// it runs in.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func main() {
	out := json.NewEncoder(os.Stdout)
	send := func(v any) { _ = out.Encode(v) }
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	n := 0
	for in.Scan() {
		var m msg
		if json.Unmarshal(in.Bytes(), &m) != nil || m.Method == "" || len(m.ID) == 0 {
			continue
		}
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch m.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}})
		case "session/new":
			n++
			reply(map[string]any{"sessionId": fmt.Sprintf("canary-%d", n)})
		case "session/load", "session/close", "session/set_mode", "session/set_config_option":
			reply(map[string]any{})
		case "session/prompt":
			home, _ := os.UserHomeDir()
			seen := "home-hidden"
			if _, err := os.Stat(os.Getenv("CANARY_HOME_MARKER")); err == nil {
				seen = "home-visible"
			}
			send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": p.SessionID,
				"update": map[string]any{"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{"type": "text", "text": fmt.Sprintf("canary-ok uid=%d home=%s %s", os.Getuid(), home, seen)}},
			}})
			reply(map[string]any{"stopReason": "end_turn"})
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
}
