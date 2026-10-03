// Command acme-instance is a REFERENCE external connector plugin for
// conductor, built against the PUBLIC plugin SDK
// (github.com/NodeSpy/conductor/pkg/plugin) — it imports NO internal daemon
// package, exactly as a third-party plugin in its own module would.
//
// It exists to exercise the per-instance declaration path (Q6,
// plugin-contract.md §1.4): its TYPE-level Describe() never names any verb
// or event (the rest/graphql shape the design doc calls out), but it
// implements InstanceDescriber and answers plugin.describe {instance} with a
// verb whose shape depends on the `variant` build flag below — so two builds
// of this SAME binary can disagree about what one configured instance may do
// while looking identical at the type level.
//
// cmd/conductor/reload_test.go compiles this twice, with
// `-ldflags "-X main.variant=v2"` for the "new build", to drive conductor's
// in-place plugin hot-reload path end-to-end over the real wire.
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"context"
	"fmt"
	"os"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

// variant selects this build's per-instance declaration shape. Overridden at
// build time with `-ldflags "-X main.variant=v2"`; a plain build is "v1".
var variant = "v1"

func describe() plugin.Decl {
	// No verbs, no events at the type level — a rest/graphql-shaped plugin:
	// every verb this instance may call is declared per-instance only, via
	// DescribeInstance below.
	return plugin.Decl{
		Type: "acme-instance",
		Desc: "reference per-instance-decl connector (example plugin)",
		Connection: plugin.Schema{
			"token": {Type: "string", Desc: "an example credential"},
		},
		Capabilities: plugin.Capabilities{},
	}
}

type handler struct{}

func (handler) Describe() plugin.Decl { return describe() }

func (handler) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	if req.Verb != "go" {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "unknown verb "+req.Verb)
	}
	return plugin.InvokeResult{Outputs: map[string]any{"ok": true}}, nil
}

// DescribeInstance answers Q6: the declared "go" verb's option/output shape
// differs between "v1" and "v2" builds, while the type-level Describe() above
// stays byte-identical across both — the exact gap finding 1 closes.
func (handler) DescribeInstance(_ context.Context, instance string, config map[string]any) (plugin.Decl, error) {
	d := describe()
	opts := plugin.Schema{"a": {Type: "string"}}
	if variant == "v2" {
		// A new option a v1 instance never declared — the kind of change an
		// in-place swap must not serve silently.
		opts["b"] = plugin.Field{Type: "string"}
	}
	d.Verbs = []plugin.Verb{{
		Name:    "go",
		Desc:    "do the thing (instance " + instance + ", variant " + variant + ")",
		Options: opts,
		Outputs: plugin.Schema{"ok": {Type: "boolean", Required: true}},
	}}
	return d, nil
}

func main() {
	if err := plugin.Serve(handler{}); err != nil {
		fmt.Fprintf(os.Stderr, "acme-instance: serve: %v\n", err)
		os.Exit(1)
	}
}
