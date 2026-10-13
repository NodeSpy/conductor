package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
)

// TestCallWrapsTransportErrorAtTheClientBoundary is finding 9 (LOW):
// Client.callFor's own transport-layer failures came straight from the acp
// package this client reuses as its wire protocol — an operator reading
// conductor's own error output saw "acp: connection closed" with no
// indication of which plugin, or that "acp" is this plugin's own transport
// rather than some unrelated subsystem. The boundary now names the actual,
// conductor-level thing that happened — "plugin <name> process exited or
// closed its connection" — while keeping the acp-level cause reachable via
// %w (errors.Is/As), and never touching what the acp package itself reports
// for its own runtime messages (acp.ErrClosed's text is untouched).
func TestCallWrapsTransportErrorAtTheClientBoundary(t *testing.T) {
	fc := newFakeConn()
	fc.invoke = func(InvokeRequest) (map[string]any, error) {
		return nil, acp.ErrClosed
	}
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})

	_, err := c.Invoke(context.Background(), InvokeRequest{Instance: "j", Verb: "search"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// The operator-facing PREFIX must be the friendly, plugin-named one —
	// the actual conductor-level thing that happened, not "acp: ...".
	if !strings.HasPrefix(err.Error(), "plugin jira process exited or closed its connection") {
		t.Fatalf("want the boundary message leading, got: %v", err)
	}
	// The cause stays wrapped alongside it (%w, never swallowed) — just no
	// longer the FIRST thing an operator reads.
	if !strings.Contains(err.Error(), "acp: connection closed") {
		t.Fatalf("the acp cause must stay visible, chained after the boundary message, got: %v", err)
	}
	if !errors.Is(err, acp.ErrClosed) {
		t.Fatalf("the original acp cause must still be reachable via %%w (errors.Is), got: %v", err)
	}

	// acp.ErrClosed's OWN text is never touched — only the plugin client
	// boundary wraps it.
	if acp.ErrClosed.Error() != "acp: connection closed" {
		t.Fatalf("acp.ErrClosed's own runtime message must be unchanged, got: %q", acp.ErrClosed.Error())
	}
}

// TestCallDoesNotWrapAJSONRPCErrorResponse: a plugin that answered with a
// real JSON-RPC error (the platform's own 4xx, a bad option) is NOT a
// transport failure — the connection is healthy, the process is alive, and
// wrapping it as "process exited or closed its connection" would be an
// outright lie. Only genuine transport failures (callFor's non-*acp.RPCError
// branch) get the new wrapping.
func TestCallDoesNotWrapAJSONRPCErrorResponse(t *testing.T) {
	fc := newFakeConn()
	fc.invoke = func(InvokeRequest) (map[string]any, error) {
		return nil, acp.NewRPCError(acp.CodeInvalidParams, "bad option: foo")
	}
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})

	_, err := c.Invoke(context.Background(), InvokeRequest{Instance: "j", Verb: "search"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "process exited or closed its connection") {
		t.Fatalf("a real JSON-RPC error response must never be wrapped as a transport failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad option: foo") {
		t.Fatalf("the plugin's own error message must still come through, got: %v", err)
	}
}
