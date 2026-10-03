package plugin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

type inproc struct {
	host chan *sdk.HostConn
}

func (inproc) Describe() sdk.Decl {
	return sdk.Decl{Type: "tick", Verbs: []sdk.Verb{{Name: "echo"}}, Events: []sdk.Event{{Name: "tick"}}}
}
func (inproc) Invoke(r sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{Outputs: map[string]any{"v": r.Options["v"]}}, nil
}
func (inproc) StartSource(_ context.Context, r sdk.StartSourceRequest, emit func(any) error) error {
	return emit(sdk.SourceEvent{Event: "tick", Instance: r.Instance})
}
func (inproc) Poll(context.Context, sdk.PollRequest) (sdk.PollResult, error) {
	return sdk.PollResult{Events: []sdk.SourceEvent{{Event: "tick"}}}, nil
}
func (p inproc) SetHost(h *sdk.HostConn) { p.host <- h }

// A builtin served in-process takes exactly the spawned-plugin path: the
// same client, transport, routing and host callbacks — nothing private.
func TestInProcessPluginSpeaksTheContract(t *testing.T) {
	st, err := OpenStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := inproc{host: make(chan *sdk.HostConn, 1)}
	c := NewClient(Spec{Name: "tick", Kind: KindConnector, Provides: "tick", InProcess: h}, Deps{State: st})
	defer c.Close()
	ctx := context.Background()
	d, err := c.Describe(ctx)
	if err != nil || d.Type != "tick" {
		t.Fatalf("describe: %+v %v", d, err)
	}
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "t", Verb: "echo", Options: map[string]any{"v": 7}})
	if err != nil || out["v"] != float64(7) {
		t.Fatalf("invoke: %v %v", out, err)
	}
	got := make(chan json.RawMessage, 1)
	if err := c.StartSource(ctx, StartSourceRequest{Instance: "t"}, func(r json.RawMessage) { got <- r }); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		var ev sdk.SourceEvent
		_ = json.Unmarshal(r, &ev)
		if ev.Event != "tick" || ev.Instance != "t" {
			t.Fatalf("event: %s", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no source event over the in-process transport")
	}
	if evs, err := c.Poll(ctx, sdk.PollRequest{Instance: "t", Mode: sdk.PollNow}); err != nil || len(evs) != 1 {
		t.Fatalf("poll: %v %v", evs, err)
	}
	host := <-h.host
	if err := host.State("t").Put(ctx, "k", "v", ""); err != nil {
		t.Fatalf("host.state over the pipe: %v", err)
	}
	if v, _ := host.State("t").Get(ctx, "k"); v != "v" {
		t.Fatalf("host.state get = %v", v)
	}
}
