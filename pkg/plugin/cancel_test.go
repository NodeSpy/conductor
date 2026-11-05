package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

type ctxInvoker struct {
	started chan struct{}
	stopped chan error
}

func (ctxInvoker) Describe() Decl { return Decl{Type: "t", Verbs: []Verb{{Name: "wait"}}} }
func (ctxInvoker) Invoke(InvokeRequest) (InvokeResult, error) {
	panic("Serve must call InvokeContext on a ContextInvoker")
}
func (h ctxInvoker) InvokeContext(ctx context.Context, _ InvokeRequest) (InvokeResult, error) {
	close(h.started)
	<-ctx.Done()
	h.stopped <- ctx.Err()
	return InvokeResult{}, ctx.Err()
}

// TestServeCancelReachesInvokeContext: a plugin.cancel notification naming an
// in-flight request cancels that request's ctx, and plugin.ping answers.
func TestServeCancelReachesInvokeContext(t *testing.T) {
	h := ctxInvoker{started: make(chan struct{}), stopped: make(chan error, 1)}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = serve(inR, outW, h); _ = outW.Close() }()
	defer inW.Close()
	send := func(s string) {
		if _, err := io.WriteString(inW, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	responses := make(chan wireMessage, 4)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m wireMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				responses <- m
			}
		}
	}()

	send(`{"jsonrpc":"2.0","id":7,"method":"plugin.invoke","params":{"verb":"wait"}}`)
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("InvokeContext was never called")
	}
	send(`{"jsonrpc":"2.0","id":8,"method":"plugin.ping"}`)
	select {
	case m := <-responses:
		if string(*m.ID) != "8" || m.Error != nil {
			t.Fatalf("ping answer = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ping was not answered while a verb was running")
	}
	// A cancel naming some other id does nothing.
	send(`{"jsonrpc":"2.0","method":"plugin.cancel","params":{"id":99}}`)
	select {
	case err := <-h.stopped:
		t.Fatalf("a cancel for another id stopped the verb: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	send(`{"jsonrpc":"2.0","method":"plugin.cancel","params":{"id": 7}}`)
	select {
	case err := <-h.stopped:
		if err != context.Canceled {
			t.Fatalf("verb ctx ended with %v, want Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plugin.cancel did not cancel the verb's ctx")
	}
}
