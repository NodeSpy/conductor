// Command acme-echo is a REFERENCE external connector plugin for conductor
// (#54). It speaks conductor's plugin protocol — newline-delimited JSON-RPC 2.0
// on stdin/stdout — and provides a connector type "acme-echo" with one verb,
// echo, that returns its input. It exists to exercise and document the
// out-of-process plugin path end-to-end; it makes no network calls.
//
// Generic example only (acme/…) — not a real service.
//
// Protocol (server side of internal/plugin):
//
//	plugin.describe -> Decl
//	plugin.invoke {instance, verb, options, connection} -> {outputs}
//
// stdout is the RPC transport; all logging goes to stderr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/plugin"
)

type handler struct{}

func (handler) HandleRequest(_ context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case plugin.MethodDescribe:
		return plugin.Decl{
			ProtocolVersion: plugin.ProtocolVersion,
			Type:            "acme-echo",
			Desc:            "reference echo connector (example plugin)",
			Connection: plugin.Schema{
				"token": {Type: "string", Desc: "an example credential the plugin receives per-call"},
			},
			Verbs: []plugin.Verb{{
				Name: "echo",
				Desc: "return the given message",
				Options: plugin.Schema{
					"message": {Type: "string", Required: true, Desc: "text to echo back"},
					"leak":    {Type: "boolean", Desc: "if true, log the received token to stderr (to exercise daemon redaction)"},
				},
				Outputs: plugin.Schema{
					"message":        {Type: "string", Required: true},
					"received_token": {Type: "boolean", Required: true},
				},
			}},
			Capabilities: plugin.Capabilities{}, // no egress, no fs, no spawn
		}, nil

	case plugin.MethodInvoke:
		var req plugin.InvokeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, err.Error())
		}
		if req.Verb != "echo" {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, "unknown verb "+req.Verb)
		}
		token, _ := req.Connection["token"].(string)
		// A buggy/malicious plugin might log its credential; the daemon redacts
		// the plugin's stderr, so this must never appear in the daemon log.
		if leak, _ := req.Options["leak"].(bool); leak {
			fmt.Fprintf(os.Stderr, "echo: DEBUG received token=%s\n", token)
		}
		fmt.Fprintf(os.Stderr, "echo: invoked verb=%s instance=%s\n", req.Verb, req.Instance)
		msg, _ := req.Options["message"].(string)
		return plugin.InvokeResult{Outputs: map[string]any{
			"message":        msg,
			"received_token": token != "",
		}}, nil
	}
	return nil, acp.NewRPCError(acp.CodeMethodNotFound, "unknown method "+method)
}

func (handler) HandleNotification(context.Context, string, json.RawMessage) {}

func main() {
	// Support a --oversize self-test mode used by the negative e2e: emit a
	// giant describe result to prove the daemon's size cap rejects it.
	if len(os.Args) > 1 && os.Args[1] == "--oversize" {
		big := make([]byte, 32<<20)
		for i := range big {
			big[i] = 'a'
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":0,"result":{"protocol_version":1,"type":"acme-echo","desc":%q}}`+"\n", string(big))
		return
	}
	conn := acp.NewConn(os.Stdin, os.Stdout, handler{})
	<-conn.Done()
}
