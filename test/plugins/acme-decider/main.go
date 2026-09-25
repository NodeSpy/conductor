// Command acme-decider is a REFERENCE decision-runtime plugin: a runtime that
// declares the system_one/v1 decision protocol and serves the two verbs a
// decision runtime needs — `decide` and `models` — answering from canned data.
// Built against the PUBLIC plugin SDK only (github.com/NodeSpy/conductor/pkg/plugin),
// exactly as a third-party decision runtime in its own module would be.
//
// It exists so the daemon's side of the decision-runtime contract —
// classification at boot, the decide/models round trip over the real
// transport, and validation of an untrusted runtime's answers — is provable
// without depending on any real decision API.
//
// Per-call Connection fields (all optional):
//
//	api_key:  a stand-in credential; each calls_log line records it, so a
//	          test can prove the daemon resolved and delivered it
//	noul:     the probability every noul question is answered with (default 0.25)
//	bad:      "range" answers every noul with 1.5; "missing" omits every answer —
//	          so the daemon's refusal of an invalid answer is exercisable
//	calls_log: a file path; one "<verb> pid=<pid> key=<api_key> <options-json>"
//	          line is appended per call (the pid proves process reuse)
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

func describe() plugin.Decl {
	return plugin.Decl{
		Kind:      plugin.KindRuntime,
		Type:      "acme-decider",
		Desc:      "reference decision runtime (example plugin)",
		Protocols: []string{plugin.ProtocolSystemOneV1},
		Connection: plugin.Schema{
			"api_key":   {Type: "string", Desc: "stand-in credential, echoed into calls_log"},
			"noul":      {Type: "string", Desc: "probability every noul is answered with"},
			"bad":       {Type: "string", Desc: "range | missing: answer invalidly"},
			"calls_log": {Type: "string", Desc: "file to append one line per call to"},
		},
		Verbs: []plugin.Verb{
			{Name: plugin.VerbDecide, Desc: "answer a system_one/v1 request"},
			{Name: plugin.VerbModels, Desc: "list the models this runtime offers"},
		},
		Capabilities: plugin.Capabilities{FS: []string{"calls_log"}},
	}
}

var logMu sync.Mutex

func record(conn map[string]any, verb string, opts map[string]any) {
	path, _ := conn["calls_log"].(string)
	if path == "" {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	raw, _ := json.Marshal(opts)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	key, _ := conn["api_key"].(string)
	fmt.Fprintf(f, "%s pid=%d key=%s %s\n", verb, os.Getpid(), key, raw)
}

func invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	record(req.Connection, req.Verb, req.Options)
	switch req.Verb {
	case plugin.VerbModels:
		return plugin.InvokeResult{Outputs: map[string]any{"models": []any{
			map[string]any{"id": "acme-2", "name": "Acme 2", "released": "2026-09-01"},
			map[string]any{"id": "acme-1", "name": "Acme 1", "released": "2026-01-01"},
		}}}, nil
	case plugin.VerbDecide:
		return decide(req)
	}
	return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, fmt.Sprintf("unknown verb %q", req.Verb))
}

func decide(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	if p, _ := req.Options["protocol"].(string); p != plugin.ProtocolSystemOneV1 {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, fmt.Sprintf("unsupported protocol %q", p))
	}
	qs, _ := req.Options["questions"].(map[string]any)
	if len(qs) == 0 {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "no questions")
	}
	p := 0.25
	if s, _ := req.Connection["noul"].(string); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			p = f
		}
	}
	bad, _ := req.Connection["bad"].(string)
	answers := map[string]any{}
	for name, raw := range qs {
		q, _ := raw.(map[string]any)
		switch q["type"] {
		case "noul":
			v := p
			if bad == "range" {
				v = 1.5
			}
			answers[name] = map[string]any{"type": "noul", "noul": v}
		case "choice":
			crit, _ := q["criteria"].(map[string]any)
			labels := make([]string, 0, len(crit))
			for l := range crit {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			probs := map[string]any{}
			for i, l := range labels {
				if i == 0 {
					probs[l] = 1.0
				} else {
					probs[l] = 0.0
				}
			}
			answers[name] = map[string]any{"type": "choice", "choice": labels[0], "confidence": 1.0, "probabilities": probs}
		case "score":
			levels, _ := q["criteria"].([]any)
			probs := map[string]any{}
			for i := range levels {
				probs[strconv.Itoa(i)] = 0.0
			}
			probs["0"] = 1.0
			answers[name] = map[string]any{"type": "score", "score": 0.0, "confidence": 1.0, "probabilities": probs}
		}
	}
	if bad == "missing" {
		answers = map[string]any{}
	}
	model, _ := req.Options["model"].(string)
	if model == "" || model == "acme-latest" {
		model = "acme-2"
	}
	return plugin.InvokeResult{Outputs: map[string]any{
		"model": model, "answers": answers,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 0},
	}}, nil
}

func main() {
	if err := plugin.Serve(plugin.ConnectorFunc(func() plugin.Decl { return describe() }, invoke)); err != nil {
		fmt.Fprintln(os.Stderr, "acme-decider:", err)
		os.Exit(1)
	}
}
