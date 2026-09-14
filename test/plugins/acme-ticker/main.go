// Command acme-ticker is a REFERENCE external SOURCE plugin for conductor, built
// against the public plugin SDK (github.com/NodeSpy/conductor/pkg/plugin). It
// provides a connector type "acme-ticker" that, on start_source, emits a few
// synthetic events and stops — exercising the source-streaming half of the
// plugin protocol end-to-end. It makes no network calls.
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

// tickerHandler is both a (trivial) verb Handler and a SourceHandler.
type tickerHandler struct{}

func (tickerHandler) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "acme-ticker",
		Desc: "reference source connector (example plugin)",
		Events: []plugin.Event{{
			Name: "tick",
			Desc: "a synthetic periodic event",
		}},
	}
}

func (tickerHandler) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "acme-ticker has no verbs")
}

// StartSource emits three ticks then returns. The count/prefix come from the
// instance config, proving config reaches the source over the wire.
func (tickerHandler) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	n := 3
	if v, ok := req.Config["count"].(float64); ok {
		n = int(v)
	}
	prefix, _ := req.Config["prefix"].(string)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := emit(map[string]any{
			"source":   "acme-ticker",
			"instance": req.Instance,
			"kind":     "tick",
			"title":    fmt.Sprintf("%stick %d", prefix, i),
			"dedup":    fmt.Sprintf("%s-%d", req.Instance, i),
			"context":  map[string]any{"seq": i},
		}); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := plugin.Serve(tickerHandler{}); err != nil {
		fmt.Fprintf(os.Stderr, "acme-ticker: serve: %v\n", err)
		os.Exit(1)
	}
}
