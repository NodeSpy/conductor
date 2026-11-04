package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

// TestCallAbandonNotifiesAndDropsLateAnswer: a Call whose ctx ends sends the
// configured abandon notification naming its id, and the peer's late answer
// for that id is dropped instead of reaching the next Call.
func TestCallAbandonNotifiesAndDropsLateAnswer(t *testing.T) {
	peerR, connW := io.Pipe()
	connR, peerW := io.Pipe()
	c := NewConn(connR, connW, nil)
	defer c.Close()
	c.SetAbandonNotify("x.cancel")

	msgs := make(chan wireMessage, 8)
	go func() {
		sc := bufio.NewScanner(peerR)
		for sc.Scan() {
			var m wireMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
	}()
	reply := func(id json.RawMessage, result string) {
		_, _ = io.WriteString(peerW, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":`+result+"}\n")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "slow", nil, nil) }()
	req := <-msgs
	if err := <-errc; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	select {
	case n := <-msgs:
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(n.Params, &p)
		if n.Method != "x.cancel" || n.ID != nil || string(p.ID) != string(*req.ID) {
			t.Fatalf("abandon notification = %+v (params %s), want x.cancel for id %s", n, n.Params, *req.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no abandon notification")
	}

	// The late answer to the abandoned id, then a real call.
	reply(*req.ID, `"late"`)
	var got string
	go func() {
		m := <-msgs
		reply(*m.ID, `"fresh"`)
	}()
	if err := c.Call(context.Background(), "next", nil, &got); err != nil || got != "fresh" {
		t.Fatalf("next call got %q, %v; the late answer must be dropped", got, err)
	}
}
