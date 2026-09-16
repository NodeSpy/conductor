package plugin

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// FLEET SAFETY. The protocol grew a direction (host.*), a method (plugin.run)
// and a Decl field (ABI). None of that may change one byte of what a
// CONNECTOR plugin puts on the wire, because every connector and runtime
// already in the field is one.
//
// The assertion is exact rather than approximate: the describe result must
// still be the same JSON object it was before engines existed — no "abi", no
// "kind", no envelope additions — and the invoke result must still be the
// same. A new field that is merely omitempty-clean at the struct level is not
// enough; this checks the bytes.
func TestConnectorWireIsUnchangedByTheEngineAdditions(t *testing.T) {
	h := ConnectorFunc(
		func() Decl { return Decl{Type: "acme", Verbs: []Verb{{Name: "echo"}}} },
		func(r InvokeRequest) (InvokeResult, error) {
			return InvokeResult{Outputs: map[string]any{"message": r.Options["message"]}}, nil
		})
	in := `{"jsonrpc":"2.0","id":0,"method":"plugin.describe"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"plugin.invoke","params":{"instance":"e1","verb":"echo","options":{"message":"hi"}}}` + "\n"

	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}

	// Responses complete concurrently, so match on the echoed id (as the
	// daemon's own client does) rather than on position.
	got := map[string]string{}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m wireMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.ID == nil {
			t.Fatalf("a connector plugin wrote a message with no id — it must issue no requests:\n%s", out.String())
		}
		if m.Method != "" {
			t.Fatalf("a connector plugin wrote a REQUEST (%s) — the new direction must stay opt-in:\n%s", m.Method, out.String())
		}
		got[string(*m.ID)] = string(m.Result)
	}
	want := map[string]string{
		"0": `{"protocol_version":1,"type":"acme","verbs":[{"name":"echo"}],"capabilities":{}}`,
		"1": `{"outputs":{"message":"hi"}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d responses, want %d:\n%s", len(got), len(want), out.String())
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("response %s changed shape:\n got %s\nwant %s", id, got[id], w)
		}
	}
}

// A Decl with no ABI is what every plugin built before engines emits. It must
// survive a marshal/unmarshal round trip with the field simply absent — the
// zero value MEANS "pre-engine plugin", so a daemon reading it gets 0 and a
// plugin that never heard of it writes nothing.
func TestDeclWithoutABIStaysAbsentOnTheWire(t *testing.T) {
	raw, err := json.Marshal(Decl{ProtocolVersion: ProtocolVersion, Type: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"abi"`) {
		t.Fatalf("a Decl with no ABI emitted the field: %s", raw)
	}
	var back Decl
	if err := json.Unmarshal([]byte(`{"protocol_version":1,"type":"acme"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.ABI != 0 || back.Kind != "" || back.ProtocolVersion != 1 {
		t.Fatalf("a pre-engine decl decoded as %+v", back)
	}
}

// fakeDaemon drives a plugin's Serve loop the way conductor does: it writes
// requests, reads whatever comes back, answers the plugin's host.* REQUESTS,
// and routes responses to whoever is waiting. It is the minimum honest peer —
// bidirectional, concurrent, and unordered.
type fakeDaemon struct {
	toPlugin   *io.PipeWriter
	fromPlugin *json.Decoder

	mu    sync.Mutex
	enc   *json.Encoder
	resps map[string]chan wireMessage

	// host answers each host.* request.
	host func(method string, req HostRequest) HostResult
}

func newFakeDaemon(t *testing.T, h Handler, host func(string, HostRequest) HostResult) *fakeDaemon {
	t.Helper()
	inR, inW := io.Pipe()   // daemon → plugin
	outR, outW := io.Pipe() // plugin → daemon
	d := &fakeDaemon{
		toPlugin:   inW,
		fromPlugin: json.NewDecoder(outR),
		enc:        json.NewEncoder(inW),
		resps:      map[string]chan wireMessage{},
		host:       host,
	}
	served := make(chan error, 1)
	go func() { served <- serve(inR, outW, h); _ = outW.Close() }()
	go d.pump()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serve did not return after stdin closed")
		}
	})
	return d
}

// pump reads everything the plugin writes: responses go to their waiter,
// requests are answered inline from the host hook.
func (d *fakeDaemon) pump() {
	for {
		var m wireMessage
		if err := d.fromPlugin.Decode(&m); err != nil {
			return
		}
		if m.Method != "" && m.ID != nil {
			req := HostRequest{}
			_ = json.Unmarshal(m.Params, &req)
			res := d.host(m.Method, req)
			raw, _ := json.Marshal(res)
			d.mu.Lock()
			_ = d.enc.Encode(wireMessage{JSONRPC: "2.0", ID: m.ID, Result: raw})
			d.mu.Unlock()
			continue
		}
		if m.ID == nil {
			continue
		}
		d.mu.Lock()
		ch := d.resps[string(*m.ID)]
		d.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

// call sends one daemon→plugin request and returns its response. Ids are sent
// as JSON strings (quoted) and matched on the echoed raw form, so an id can be
// anything readable without being a number.
func (d *fakeDaemon) call(id, method string, params any) wireMessage {
	raw, _ := json.Marshal(params)
	ch := make(chan wireMessage, 1)
	id = strconv.Quote(id)
	d.mu.Lock()
	d.resps[id] = ch
	idRaw := json.RawMessage(id)
	_ = d.enc.Encode(wireMessage{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: raw})
	d.mu.Unlock()
	return <-ch
}

// The whole engine loop over the real transport: the daemon sends plugin.run,
// the plugin calls back with host.kv DURING that run, and the outputs carry
// what the host answered.
func TestEngineRunWithHostCallback(t *testing.T) {
	var seen []HostRequest
	var mu sync.Mutex
	h := EngineFunc(
		func() Decl { return Decl{Kind: KindStep, ABI: EngineABI, Type: "acme-engine"} },
		func(ctx context.Context, req RunRequest, host *Host) (RunResult, error) {
			if err := host.KV().Set(ctx, "cache", "ns", "k", req.Inputs["v"]); err != nil {
				return RunResult{}, err
			}
			got, err := host.KV().Get(ctx, "cache", "ns", "k")
			if err != nil {
				return RunResult{}, err
			}
			return RunResult{Outputs: map[string]any{"readback": got, "code": req.Code}}, nil
		})

	store := map[string]any{}
	d := newFakeDaemon(t, h, func(method string, req HostRequest) HostResult {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		if method != MethodHostKV {
			return HostResult{Error: "wrong method"}
		}
		switch req.Op {
		case "set":
			store[req.Args[1].(string)] = req.Args[2]
			return HostResult{OK: true}
		case "get":
			return HostResult{OK: true, Value: store[req.Args[1].(string)]}
		}
		return HostResult{Error: "no op " + req.Op}
	})

	resp := d.call("1", MethodRun, RunRequest{RunID: "tok", Code: "print(1)", Inputs: map[string]any{"v": "hello"}})
	if resp.Error != nil {
		t.Fatalf("plugin.run errored: %+v", resp.Error)
	}
	var res RunResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Outputs["readback"] != "hello" || res.Outputs["code"] != "print(1)" {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("want 2 host calls, got %d", len(seen))
	}
	for _, r := range seen {
		if r.RunID != "tok" {
			t.Errorf("host call did not carry the run's token: %+v", r)
		}
		if r.Kind != HostKindKV || r.Resource != "cache" {
			t.Errorf("host call lost its kind/resource: %+v", r)
		}
	}
}

// A refusal is IN-BAND and TYPED: the engine can tell "conductor will not let
// this step do that" from "the store is down" without matching a string.
func TestHostRefusalIsTypedAndNotAnError(t *testing.T) {
	var refused, plain bool
	h := EngineFunc(
		func() Decl { return Decl{Kind: KindStep, ABI: EngineABI, Type: "e"} },
		func(ctx context.Context, req RunRequest, host *Host) (RunResult, error) {
			_, err := host.KV().Get(ctx, "locked", "ns", "k")
			refused = IsRefused(err)
			_, err = host.KV().Get(ctx, "broken", "ns", "k")
			plain = err != nil && !IsRefused(err)
			return RunResult{}, nil
		})
	d := newFakeDaemon(t, h, func(_ string, req HostRequest) HostResult {
		if req.Resource == "locked" {
			return HostResult{Refused: true, Error: "kv: store locked is not in this step's allowlist"}
		}
		return HostResult{Error: "kv: store is down"}
	})
	if resp := d.call("1", MethodRun, RunRequest{RunID: "tok"}); resp.Error != nil {
		t.Fatalf("run errored: %+v", resp.Error)
	}
	if !refused {
		t.Error("a refused host call did not surface as a *Refusal")
	}
	if !plain {
		t.Error("a plain host failure was misreported as a refusal")
	}
}

// A run granted no data plane (empty run_id) must not make the engine guess:
// Host.Available says so, and a call attempted anyway fails locally rather
// than putting an unauthenticated request on the wire.
func TestNoRunIDMeansNoDataPlane(t *testing.T) {
	var avail bool
	var callErr error
	h := EngineFunc(
		func() Decl { return Decl{Kind: KindStep, ABI: EngineABI, Type: "e"} },
		func(ctx context.Context, req RunRequest, host *Host) (RunResult, error) {
			avail = host.Available()
			_, callErr = host.KV().Get(ctx, "cache", "ns", "k")
			return RunResult{}, nil
		})
	var wrote int
	d := newFakeDaemon(t, h, func(string, HostRequest) HostResult {
		wrote++
		return HostResult{OK: true}
	})
	if resp := d.call("1", MethodRun, RunRequest{}); resp.Error != nil {
		t.Fatalf("run errored: %+v", resp.Error)
	}
	if avail {
		t.Error("Available() lied about a run with no token")
	}
	if callErr == nil || !strings.Contains(callErr.Error(), "no ctx data plane") {
		t.Errorf("want a local no-data-plane error, got %v", callErr)
	}
	if wrote != 0 {
		t.Errorf("the SDK put %d unauthenticated host call(s) on the wire", wrote)
	}
}

// The bidirectional loop must not deadlock: while a run is parked waiting on
// its host response, the daemon's OTHER requests still get serviced, and many
// concurrent runs each doing host calls all complete. Run with -race.
func TestInterleavedDaemonRequestsAndHostCallsDoNotDeadlock(t *testing.T) {
	const runs = 8
	release := make(chan struct{})
	h := engineAndConnector{
		run: func(ctx context.Context, req RunRequest, host *Host) (RunResult, error) {
			// Park the first run on a host call the daemon will not answer
			// until every other request has been serviced.
			if req.Inputs["park"] == true {
				<-release
			}
			v, err := host.KV().Get(ctx, "cache", "ns", req.Code)
			if err != nil {
				return RunResult{}, err
			}
			return RunResult{Outputs: map[string]any{"v": v}}, nil
		},
	}
	d := newFakeDaemon(t, h, func(_ string, req HostRequest) HostResult {
		return HostResult{OK: true, Value: req.Args[1]}
	})

	// One parked run…
	parked := make(chan wireMessage, 1)
	go func() {
		parked <- d.call("100", MethodRun, RunRequest{RunID: "t", Inputs: map[string]any{"park": true}, Code: "parked"})
	}()

	// …a describe and an invoke behind it, which must answer anyway…
	if resp := d.call("1", MethodDescribe, struct{}{}); resp.Error != nil {
		t.Fatalf("describe blocked behind a parked run: %+v", resp.Error)
	}
	if resp := d.call("2", MethodInvoke, InvokeRequest{Verb: "ping"}); resp.Error != nil {
		t.Fatalf("invoke blocked behind a parked run: %+v", resp.Error)
	}

	// …and many concurrent runs each doing their own host round trip.
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + string(rune('a'+i))
			resp := d.call("2"+string(rune('a'+i)), MethodRun, RunRequest{RunID: "t", Code: key})
			var res RunResult
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			if res.Outputs["v"] != key {
				t.Errorf("run %d: outputs = %+v", i, res.Outputs)
			}
		}(i)
	}
	wg.Wait()
	close(release)
	if resp := <-parked; resp.Error != nil {
		t.Fatalf("the parked run never completed: %+v", resp.Error)
	}
}

// engineAndConnector is a handler that is BOTH — the surfaces coexist on one
// plugin, which is what makes "additive" true rather than asserted.
type engineAndConnector struct {
	run func(context.Context, RunRequest, *Host) (RunResult, error)
}

func (engineAndConnector) Describe() Decl { return Decl{Kind: KindStep, ABI: EngineABI, Type: "e"} }
func (engineAndConnector) Invoke(r InvokeRequest) (InvokeResult, error) {
	return InvokeResult{Outputs: map[string]any{"verb": r.Verb}}, nil
}
func (h engineAndConnector) Run(ctx context.Context, r RunRequest, host *Host) (RunResult, error) {
	return h.run(ctx, r, host)
}

// plugin.run on a plugin that is not an engine is a clean method-not-found,
// not a crash — the same answer start_source gives a non-source.
func TestRunOnANonEngineIsMethodNotFound(t *testing.T) {
	h := ConnectorFunc(func() Decl { return Decl{Type: "c"} },
		func(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil })
	var out strings.Builder
	if err := serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"plugin.run"}`+"\n"), &out, h); err != nil {
		t.Fatal(err)
	}
	var m wireMessage
	if err := json.NewDecoder(strings.NewReader(out.String())).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Error == nil || m.Error.Code != CodeMethodNotFound {
		t.Fatalf("want method-not-found, got %+v", m.Error)
	}
}

// The stdin cap is PER MESSAGE, not cumulative. It used to be an
// io.LimitReader over the whole stream, which would retire a long-lived
// engine after 32 MiB of lifetime traffic — reported as a clean shutdown.
func TestStdinCapIsPerMessageNotCumulative(t *testing.T) {
	// One frame well under the cap, repeated past a small cap: with the old
	// cumulative reader the second would never be decoded.
	lr := &msgLimitReader{r: strings.NewReader(`{"a":1}` + "\n" + `{"a":2}` + "\n"), limit: 12}
	dec := json.NewDecoder(lr)
	for i := 0; i < 2; i++ {
		var v map[string]int
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("message %d: %v (a cumulative cap would do exactly this)", i, err)
		}
		lr.reset()
	}
}
