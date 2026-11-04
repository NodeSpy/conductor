// Command acme-slow is a TEST external plugin for conductor's call-deadline
// handling. It provides a connector type "acme-slow" that streams a tick
// every 50ms on start_source and has four verbs:
//
//   - echo: answers at once.
//   - sleep: waits `ms` milliseconds, or until the daemon cancels the call.
//     When cancelled it writes "cancelled" to the file named by the `mark`
//     option, so a test can see plugin.cancel reached the verb. With
//     `ignore_cancel` it sleeps the full time and answers late.
//   - slow: like sleep, but declares call_timeout 5s, so a daemon whose
//     default bound is shorter still waits for it.
//   - wedge: stops the whole process (SIGSTOP to itself). It answers
//     nothing after that, pings included.
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

type slowHandler struct{}

func (slowHandler) Describe() plugin.Decl {
	sleepOpts := plugin.Schema{"ms": {Type: "number"}, "mark": {Type: "string"}, "ignore_cancel": {Type: "boolean"}}
	return plugin.Decl{
		Type: "acme-slow",
		Desc: "test plugin for call deadlines",
		Verbs: []plugin.Verb{
			{Name: "echo", Options: plugin.Schema{"message": {Type: "string"}}},
			{Name: "sleep", Options: sleepOpts},
			{Name: "slow", Options: sleepOpts, Semantics: &plugin.VerbSemantics{
				CallTimeout: "5s", Optional: []string{"call_timeout"},
			}},
			{Name: "wedge"},
		},
		Events: []plugin.Event{{Name: "tick"}},
	}
}

func (h slowHandler) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return h.InvokeContext(context.Background(), req)
}

func (slowHandler) InvokeContext(ctx context.Context, req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	switch req.Verb {
	case "echo":
		return plugin.InvokeResult{Outputs: map[string]any{"message": req.Options["message"], "pid": os.Getpid()}}, nil
	case "sleep", "slow":
		ms, _ := req.Options["ms"].(float64)
		if ignore, _ := req.Options["ignore_cancel"].(bool); ignore {
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return plugin.InvokeResult{Outputs: map[string]any{"slept": ms, "pid": os.Getpid()}}, nil
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
			return plugin.InvokeResult{Outputs: map[string]any{"slept": ms, "pid": os.Getpid()}}, nil
		case <-ctx.Done():
			if mark, _ := req.Options["mark"].(string); mark != "" {
				_ = os.WriteFile(mark, []byte("cancelled"), 0o600)
			}
			return plugin.InvokeResult{}, ctx.Err()
		}
	case "wedge":
		_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
		select {} // never answer, even in the instant before the stop lands
	}
	return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "unknown verb "+req.Verb)
}

// StartSource ticks every 50ms until the daemon goes away.
func (slowHandler) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if err := emit(map[string]any{
			"source": "acme-slow", "instance": req.Instance, "kind": "tick",
			"title": fmt.Sprintf("tick %d", i), "pid": os.Getpid(),
		}); err != nil {
			return err
		}
	}
}

func main() {
	if err := plugin.Serve(slowHandler{}); err != nil {
		fmt.Fprintf(os.Stderr, "acme-slow: serve: %v\n", err)
		os.Exit(1)
	}
}
