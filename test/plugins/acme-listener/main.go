// Command acme-listener is a REFERENCE external SOURCE plugin for conductor,
// built against the public plugin SDK (github.com/NodeSpy/conductor/pkg/plugin).
// It demonstrates the `listeners` connection semantic
// (docs/design/plugin-contract.md §2.4) end to end: it runs a real inbound
// HTTP listener on its config field `listen`, and declares that config field
// `expose` names an exposure connector the engine should open `listen`
// through, handing the public URL back in config field `public_url` before
// this plugin's start_source ever runs.
//
// Proof of both halves is simple and observable: the FIRST event this plugin
// emits (before it even binds the listener) echoes `public_url` straight
// into its context, so a daemon that didn't open the exposure (or didn't
// pass the URL along) shows up as an empty field; and every HTTP POST that
// reaches the bound listener — whether sent directly, or forwarded through
// whatever `expose` named — fires its own `delivery` event.
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

type acmeListenerHandler struct{}

func (acmeListenerHandler) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "acme-listener",
		Desc: "reference source connector demonstrating the listeners connection semantic (example plugin)",
		Connection: plugin.Schema{
			"listen":     {Type: "string", Required: true, Desc: "inbound HTTP address to bind"},
			"expose":     {Type: "string", Desc: "an exposure connector (one declaring an exposes verb) to make `listen` reachable from outside"},
			"public_url": {Type: "string", Desc: "filled in by the engine at instance start when `expose` is set"},
		},
		Semantics: &plugin.ConnSemantics{
			Listeners: []plugin.Listener{{Listen: "listen", Expose: "expose", URLTo: "public_url"}},
		},
		Events: []plugin.Event{
			{Name: "ready", Desc: "the source started; context.public_url carries whatever the engine filled in (empty when expose is unset)"},
			{Name: "delivery", Desc: "an HTTP POST reached the listener"},
		},
	}
}

func (acmeListenerHandler) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "acme-listener has no verbs")
}

// StartSource emits `ready` (carrying whatever public_url the engine set)
// and then binds `listen`, emitting one `delivery` event per HTTP request
// until ctx is cancelled.
func (acmeListenerHandler) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	listen, _ := req.Config["listen"].(string)
	if listen == "" {
		return fmt.Errorf("acme-listener: config.listen is required")
	}
	// Bind BEFORE emitting ready: a consumer that sees `ready` must be able
	// to reach the listener immediately (no window where the public URL is
	// known but the port isn't actually accepting yet).
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("acme-listener: listen %s: %w", listen, err)
	}

	publicURL, _ := req.Config["public_url"].(string)
	if err := emit(map[string]any{
		"event":   "ready",
		"title":   "acme-listener ready",
		"dedup":   req.Instance + "-ready",
		"context": map[string]any{"public_url": publicURL},
	}); err != nil {
		ln.Close()
		return err
	}

	var mu sync.Mutex
	var n int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n++
		seq := n
		mu.Unlock()
		_ = emit(map[string]any{
			"event":   "delivery",
			"title":   fmt.Sprintf("delivery %d", seq),
			"dedup":   req.Instance + "-delivery-" + strconv.FormatInt(seq, 10),
			"context": map[string]any{"body": string(body), "seq": seq},
		})
		w.WriteHeader(http.StatusAccepted)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return ctx.Err()
	}
	return err
}

func main() {
	if err := plugin.Serve(acmeListenerHandler{}); err != nil {
		fmt.Fprintf(os.Stderr, "acme-listener: serve: %v\n", err)
		os.Exit(1)
	}
}
