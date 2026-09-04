package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipePair wires a client and a raw agent-side Conn over in-memory pipes.
func pipePair(delegate ClientDelegate, agent Handler) (*Client, *Conn, func()) {
	clientR, agentW := io.Pipe()
	agentR, clientW := io.Pipe()
	c := NewClient(clientR, clientW, delegate)
	a := NewConn(agentR, agentW, agent)
	return c, a, func() {
		c.Close()
		a.Close()
		clientR.Close()
		clientW.Close()
		agentR.Close()
		agentW.Close()
	}
}

type recordingAgent struct {
	mu      sync.Mutex
	notifs  []string
	cancels []string
}

func (a *recordingAgent) HandleRequest(_ context.Context, method string, _ json.RawMessage) (any, *RPCError) {
	return nil, NewRPCError(CodeMethodNotFound, method)
}

func (a *recordingAgent) HandleNotification(_ context.Context, method string, params json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notifs = append(a.notifs, method)
	if method == MethodSessionCancel {
		var p CancelParams
		_ = json.Unmarshal(params, &p)
		a.cancels = append(a.cancels, p.SessionID)
	}
}

// TestClientCancelAndDone: Cancel notifies session/cancel with the id; Done
// closes when the connection shuts down; Conn is exposed.
func TestClientCancelAndDone(t *testing.T) {
	agent := &recordingAgent{}
	c, _, cleanup := pipePair(DelegateFuncs{}, agent)
	defer cleanup()
	if c.Conn() == nil {
		t.Fatal("Conn accessor")
	}
	if err := c.Cancel(context.Background(), "sess-1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		agent.mu.Lock()
		n := len(agent.cancels)
		agent.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.mu.Lock()
	if len(agent.cancels) != 1 || agent.cancels[0] != "sess-1" {
		t.Fatalf("cancel notification: %v", agent.cancels)
	}
	agent.mu.Unlock()

	c.Close()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done never closed")
	}
	// A call on a closed conn errors with ErrClosed (err() path).
	if _, err := c.Initialize(context.Background(), DefaultInitializeParams(Implementation{Name: "t"})); err == nil {
		t.Fatal("closed conn must refuse calls")
	}
}

// TestDelegateDefaultsAndDispatch: nil hooks drop updates and cancel
// permissions; the client routes both delegate callbacks.
func TestDelegateDefaultsAndDispatch(t *testing.T) {
	var got []string
	var mu sync.Mutex
	del := DelegateFuncs{
		OnUpdate: func(_ context.Context, n SessionNotification) error {
			mu.Lock()
			got = append(got, "update:"+n.SessionID)
			mu.Unlock()
			return nil
		},
		OnPermission: func(context.Context, RequestPermissionParams) (RequestPermissionOutcome, error) {
			mu.Lock()
			got = append(got, "perm")
			mu.Unlock()
			return SelectedOutcome("allow"), nil
		},
	}
	c, agentConn, cleanup := pipePair(del, &recordingAgent{})
	defer cleanup()

	// The agent notifies a session update and requests a permission.
	if err := agentConn.Notify(context.Background(), MethodSessionUpdate, SessionNotification{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	var out RequestPermissionResult
	if err := agentConn.Call(context.Background(), MethodRequestPermission, RequestPermissionParams{}, &out); err != nil {
		t.Fatal(err)
	}
	_ = c
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	joined := strings.Join(got, ",")
	mu.Unlock()
	if !strings.Contains(joined, "update:s1") || !strings.Contains(joined, "perm") {
		t.Fatalf("delegate dispatch: %s", joined)
	}

	// Nil-hook defaults: SessionUpdate returns nil, RequestPermission cancels.
	var d DelegateFuncs
	if err := d.SessionUpdate(context.Background(), SessionNotification{}); err != nil {
		t.Fatal(err)
	}
	outc, err := d.RequestPermission(context.Background(), RequestPermissionParams{})
	if err != nil || outc.Outcome != OutcomeCancelled {
		t.Fatalf("nil permission hook must cancel: %+v %v", outc, err)
	}
}

// TestSpawnClient runs a real subprocess speaking one initialize round trip.
func TestSpawnClient(t *testing.T) {
	// A minimal stdin/stdout agent: reads one request line, answers it.
	script := `read line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id"`
	c, cmd, err := Spawn(context.Background(), DelegateFuncs{}, "bash", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Initialize(context.Background(), DefaultInitializeParams(Implementation{Name: "t"}))
	if err != nil || res == nil {
		t.Fatalf("initialize over spawn: %v", err)
	}
	c.Close()
	_ = cmd.Wait()

	if _, _, err := Spawn(context.Background(), DelegateFuncs{}, "/nonexistent/agent"); err == nil {
		t.Fatal("missing binary must error")
	}
}

func TestRPCErrorAndMarshalParams(t *testing.T) {
	e := NewRPCError(CodeMethodNotFound, "nope")
	if e.Error() == "" || !strings.Contains(e.Error(), "nope") {
		t.Fatalf("rpc error text: %q", e.Error())
	}
	if p, err := marshalParams(nil); err != nil || p != nil {
		t.Fatal("nil params marshal to nil")
	}
	if _, err := marshalParams(func() {}); err == nil {
		t.Fatal("unmarshalable params must error")
	}
}
