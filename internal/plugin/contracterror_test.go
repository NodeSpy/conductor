package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// An answered JSON-RPC error (plugin-contract.md §1.11) must never tear the
// plugin process down: the plugin is up and answered, the transport is
// healthy — only a transport failure or a timeout does that (TestClientCallTimeout
// covers the timeout half). This is the #164 behavior docs/design/
// plugin-contract.md §1.11 says is "kept"; this test is what keeps it kept.
func TestAnsweredJSONRPCErrorDoesNotRestartProcess(t *testing.T) {
	fc := newFakeConn()
	fc.invoke = func(InvokeRequest) (map[string]any, error) {
		return nil, &acp.RPCError{Code: sdk.CodeInvalid, Message: "nope"}
	}
	dialAttempts := 0
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: func(ctx context.Context, s Spec, d Deps) (transport, func(), error) {
		dialAttempts++
		return fakeDial(fc)(ctx, s, d)
	}})
	ctx := context.Background()

	_, err := c.Invoke(ctx, InvokeRequest{Instance: "i", Verb: "x"})
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != sdk.CodeInvalid {
		t.Fatalf("want the answered RPC error returned, got %v", err)
	}
	c.mu.Lock()
	up := c.conn != nil
	c.mu.Unlock()
	if !up {
		t.Fatal("an answered JSON-RPC error must not tear the process down")
	}
	if dialAttempts != 1 {
		t.Fatalf("dialAttempts = %d after the first call, want 1 (no restart)", dialAttempts)
	}

	// A second call must reuse the SAME process: no re-dial.
	if _, err := c.Invoke(ctx, InvokeRequest{Instance: "i", Verb: "x"}); err == nil {
		t.Fatal("expected the same scripted error again")
	}
	if dialAttempts != 1 {
		t.Fatalf("dialAttempts = %d after a second answered error, want 1 (still no restart)", dialAttempts)
	}
}
