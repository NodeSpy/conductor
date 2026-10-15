// Command fakeopencode is a hermetic stand-in for the `opencode` CLI's HTTP server
// — installed on PATH as `opencode`, driven by conductor's opencode native-transport
// controller (internal/controller/opencode.go), which execs `opencode serve
// --hostname 127.0.0.1 --port 0` IN the conductor-provisioned PR worktree, scrapes
// the listen URL from stdout, then POSTs /session and /session/{id}/message.
//
// On the message turn it performs the shared fixer edit+commit+push (package fixer)
// in the session's `directory` (the worktree), so the opencode-native row lands a
// real commit on the forge — with NO LLM and NO secrets.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/NodeSpy/conductor/test/e2e/services/fixer"
)

var (
	seq  int64
	mu   sync.Mutex
	dirs = map[string]string{} // session id → working directory
)

func main() {
	// The controller invokes `opencode serve --hostname 127.0.0.1 --port 0`; any
	// other subcommand (e.g. a live `opencode acp`) is out of scope for the fake.
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "fakeopencode: only `serve` is supported")
		os.Exit(1)
	}

	// Bind the port asked for (`--port 0`: an ephemeral one; a sandboxed
	// server is given a fixed one) and print the real URL the controller
	// scrapes from stdout (it waits for a token containing "http://").
	port := "0"
	for i := 2; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--port" {
			port = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeopencode: listen:", err)
		os.Exit(1)
	}
	fmt.Printf("opencode server listening on http://%s\n", ln.Addr().String())
	os.Stdout.Sync()

	mux := http.NewServeMux()
	mux.HandleFunc("/session", handleSession) // POST /session → create
	mux.HandleFunc("/session/", handleSessionSub)
	mux.HandleFunc("/x-probe", handleProbe)
	var h http.Handler = mux
	if pw := os.Getenv("OPENCODE_SERVER_PASSWORD"); pw != "" {
		// Like the real server: with a password set, every request needs it.
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != "opencode" || p != pw {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		})
	}
	_ = http.Serve(ln, h)
}

// handleSession creates a session, remembering the worktree from `directory`.
func handleSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Directory string `json:"directory"`
		Title     string `json:"title"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	// Unique across servers, like opencode's own ids: each dispatch runs its
	// own server, and conductor keys sessions by id.
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	id := fmt.Sprintf("ses_%x_%d", rnd, atomic.AddInt64(&seq, 1))
	mu.Lock()
	dirs[id] = body.Directory
	mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "title": body.Title})
}

// handleSessionSub handles /session/{id}/message and /session/{id}/abort.
func handleSessionSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/session/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "abort":
		writeJSON(w, map[string]any{})
	default: // "message"
		var body struct {
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		prompt := ""
		for _, p := range body.Parts {
			if p.Type == "text" && p.Text != "" {
				prompt = p.Text
				break
			}
		}
		mu.Lock()
		dir := dirs[id]
		mu.Unlock()
		if dir == "" {
			dir, _ = os.Getwd()
		}
		_ = fixer.Apply(dir, "opencode-native", prompt)
		writeJSON(w, map[string]any{
			"info":  map[string]any{"role": "assistant", "cost": 0},
			"parts": []any{map[string]any{"type": "text", "text": "opencode: done"}},
		})
	}
}

// handleProbe reports what the server process can see from where it runs
// (the agent sandbox, under conductor's tests): its HOME, and for each
// ?read= path whether it can be read.
func handleProbe(w http.ResponseWriter, r *http.Request) {
	reads := map[string]string{}
	for _, p := range r.URL.Query()["read"] {
		if _, err := os.ReadFile(p); err == nil {
			reads[p] = "ok"
		} else if _, err := os.ReadDir(p); err == nil {
			reads[p] = "ok"
		} else {
			reads[p] = "denied"
		}
	}
	writeJSON(w, map[string]any{"home": os.Getenv("HOME"), "read": reads,
		"conductor": os.Getenv("CONDUCTOR_ENDPOINT") != "" && os.Getenv("CONDUCTOR_SKILL_TOKEN") != "",
		"creds":     os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("SSH_AUTH_SOCK") != ""})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
