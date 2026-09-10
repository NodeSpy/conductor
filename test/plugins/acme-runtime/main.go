// Command acme-runtime is a REFERENCE external connector plugin that declares
// the verb set internal/dispatch.Backend drives — the shape a "runtime as a
// verb plugin" takes — and answers each verb from canned data handed to it in
// its Connection. Built against the PUBLIC plugin SDK
// (github.com/NodeSpy/conductor/pkg/plugin) only; it imports NO internal daemon
// package, exactly as a third-party plugin in its own module would.
//
// It exists so the daemon side of that path — dispatch.rpcBackend mapping
// Backend methods onto plugin verbs, its retry loop, and its parsing of
// untrusted verb outputs — is provable over a REAL subprocess and the REAL
// transport without the daemon's tests depending on any particular plugin
// implementation. The concrete paseo plugin lives in the conductor-plugins
// repo and is tested there against its own protocol client; this binary is the
// daemon's half of that contract.
//
// Per-call Connection fields (all optional):
//
//	reply_<verb>: a JSON object string returned as that verb's outputs
//	calls_log:    a file path; one "<verb> <options-json>" line is appended per call
//	fail_<verb>:  an error message; the verb fails with it instead of replying.
//	              A "#N" suffix fails only the first N calls to that verb
//	              (e.g. "fatal: could not lock config file#2"), so a caller's
//	              retry behaviour is exercisable over the real wire. The call
//	              count is read back from calls_log rather than kept in memory,
//	              because the daemon's client tears the subprocess down on any
//	              error response (see internal/plugin.Client.call) — so a retry
//	              arrives at a FRESH process, and an in-memory counter would
//	              reset every time. calls_log is therefore required for "#N".
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

// verbs are the operations internal/dispatch.Backend needs from a runtime
// plugin. Options/outputs are declared loosely (the daemon's rpcBackend owns
// the precise mapping; this double only has to round-trip them).
var verbs = []string{
	"run", "list_agents", "inspect", "archive_agent", "archive_workspace",
	"create_worktree", "create_workspace", "list_workspaces", "clone", "send", "wait",
}

func describe() plugin.Decl {
	d := plugin.Decl{
		Type: "acme-runtime",
		Desc: "reference runtime-shaped connector (example plugin)",
		Connection: plugin.Schema{
			"reply_<verb>": {Type: "string", Desc: "JSON object returned as that verb's outputs"},
			"calls_log":    {Type: "string", Desc: "file to append one line per call to"},
			"fail_<verb>":  {Type: "string", Desc: "make that verb fail with this message (\"msg#N\" = first N calls only)"},
		},
		Capabilities: plugin.Capabilities{FS: []string{"calls_log"}},
	}
	for _, v := range verbs {
		d.Verbs = append(d.Verbs, plugin.Verb{Name: v, Desc: "reference " + v})
	}
	return d
}

func invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	known := false
	for _, v := range verbs {
		if v == req.Verb {
			known = true
			break
		}
	}
	if !known {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "unknown verb "+req.Verb)
	}

	// n is this verb's call number, counted from calls_log so it survives the
	// subprocess restart the daemon performs after an error response.
	n := 1
	if path, _ := req.Connection["calls_log"].(string); path != "" {
		opts, _ := json.Marshal(req.Options)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s\n", req.Verb, opts)
			_ = f.Close()
		}
		if prior, err := os.ReadFile(path); err == nil {
			n = 0
			for _, line := range strings.Split(string(prior), "\n") {
				if strings.HasPrefix(line, req.Verb+" ") {
					n++
				}
			}
		}
	}

	if spec, _ := req.Connection["fail_"+req.Verb].(string); spec != "" {
		msg, limit := spec, 0
		if i := strings.LastIndex(spec, "#"); i >= 0 {
			if k, err := strconv.Atoi(spec[i+1:]); err == nil {
				msg, limit = spec[:i], k
			}
		}
		if limit == 0 || n <= limit {
			return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInternalError, msg)
		}
	}

	raw, _ := req.Connection["reply_"+req.Verb].(string)
	if raw == "" {
		return plugin.InvokeResult{Outputs: map[string]any{}}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams,
			fmt.Sprintf("reply_%s is not a JSON object: %v", req.Verb, err))
	}
	return plugin.InvokeResult{Outputs: out}, nil
}

func main() {
	if err := plugin.Serve(plugin.ConnectorFunc(describe, invoke)); err != nil {
		fmt.Fprintf(os.Stderr, "acme-runtime: serve: %v\n", err)
		os.Exit(1)
	}
}
