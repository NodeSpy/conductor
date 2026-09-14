package plugin

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncWriter is an io.Writer a test can read back safely while serve
// writes to it from several goroutines.
type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// §8: dispatch ran INLINE in the stdin read loop, so one slow verb — a
// paseo `wait` on a long agent, an unreachable API — blocked every later
// call behind it and the daemon saw the whole plugin as hung. StartSource
// already had a goroutine for exactly this reason.
func TestASlowInvokeDoesNotBlockOtherCalls(t *testing.T) {
	release := make(chan struct{})
	h := ConnectorFunc(
		func() Decl { return Decl{Type: "t"} },
		func(req InvokeRequest) (InvokeResult, error) {
			if req.Verb == "slow" {
				<-release
			}
			return InvokeResult{Outputs: map[string]any{"verb": req.Verb}}, nil
		},
	)
	pr, pw := io.Pipe()
	out := &syncWriter{}
	served := make(chan error, 1)
	go func() { served <- serve(pr, out, h) }()

	// A slow call, then a fast one behind it in the same stream.
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.invoke","params":{"verb":"slow"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.describe"}`,
	} {
		if _, err := io.WriteString(pw, line+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// The fast one must answer while the slow one is still parked.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"id":2`) {
		if time.Now().After(deadline) {
			close(release)
			_ = pw.Close()
			t.Fatal("a slow verb blocked the read loop — the second call never answered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(release)
	_ = pw.Close()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	// …and the slow one still answers: serve drains in-flight handlers
	// before returning, so its response is not lost.
	if !strings.Contains(out.String(), `"id":1`) {
		t.Fatalf("the slow call's response was dropped:\n%s", out.String())
	}
}

// §22: a panic in third-party plugin code is a protocol ERROR, not a dead
// process. Letting it escape killed the plugin mid-conversation and took
// every in-flight call with it.
func TestAPanickingVerbBecomesAnRPCError(t *testing.T) {
	h := ConnectorFunc(
		func() Decl { return Decl{Type: "t"} },
		func(req InvokeRequest) (InvokeResult, error) { panic("boom") },
	)
	var out syncWriter
	err := serve(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"plugin.invoke","params":{"verb":"x"}}`+"\n"), &out, h)
	if err != nil {
		t.Fatalf("a panicking verb must not take the process down: %v", err)
	}
	var m wireMessage
	if derr := json.NewDecoder(strings.NewReader(out.String())).Decode(&m); derr != nil {
		t.Fatalf("no response written: %v (%q)", derr, out.String())
	}
	if m.Error == nil || m.Error.Code != CodeInternalError || !strings.Contains(m.Error.Message, "panicked") {
		t.Fatalf("want an internal-error response naming the panic, got %+v", m.Error)
	}
}
