package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
)

// engineSpec is a step-engine plugin's Spec, the shape SpecFromRef builds for
// a step's `use:`.
func engineSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{Name: "acme-engine", Kind: KindStep, Provides: "acme-engine",
		BinPath: writeBin(t, t.TempDir(), "b", []byte("x"), 0o755), Local: true}
}

// engineConn is a transport that answers plugin.run by calling BACK through
// the client's own request handler — which is exactly what the real
// subprocess does over acp.Conn, and is the only way to exercise the run_id
// check on the path it actually sits on.
type engineConn struct {
	done chan struct{}
	decl *Decl
	// run is invoked for plugin.run and may issue host callbacks through back.
	run  func(req RunRequest, back func(method string, params any) HostResult) map[string]any
	back func(method string, params json.RawMessage) (any, *acp.RPCError)
}

func newEngineConn(decl *Decl) *engineConn {
	return &engineConn{done: make(chan struct{}), decl: decl}
}

func (e *engineConn) Call(_ context.Context, method string, params, result any) error {
	switch method {
	case MethodDescribe:
		*result.(*Decl) = *e.decl
		return nil
	case MethodRun:
		req := params.(RunRequest)
		// Round-trip the result through JSON: outputs cross a wire in real
		// life, so a test that skipped it would let a Go-only value pass.
		b, err := json.Marshal(RunResult{Outputs: e.run(req, e.host)})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result.(*RunResult))
	}
	return acp.NewRPCError(acp.CodeMethodNotFound, method)
}

// host is the plugin→daemon direction: marshal the request the way the wire
// would, hand it to the daemon's handler, and decode the answer.
func (e *engineConn) host(method string, params any) HostResult {
	raw, _ := json.Marshal(params)
	v, rpcErr := e.back(method, raw)
	if rpcErr != nil {
		return HostResult{Error: rpcErr.Message}
	}
	// Round-trip through JSON so the test sees what the plugin would see.
	b, _ := json.Marshal(v)
	var res HostResult
	_ = json.Unmarshal(b, &res)
	return res
}

func (e *engineConn) Close() error {
	select {
	case <-e.done:
	default:
		close(e.done)
	}
	return nil
}
func (e *engineConn) Done() <-chan struct{} { return e.done }

func engineClient(t *testing.T, ec *engineConn) *Client {
	t.Helper()
	c := NewClient(engineSpec(t), Deps{dial: func(_ context.Context, _ Spec, d Deps) (transport, func(), error) {
		ec.back = func(method string, params json.RawMessage) (any, *acp.RPCError) {
			return d.onRequest(context.Background(), method, params)
		}
		return ec, func() { ec.Close() }, nil
	}})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The plugin.run round trip, with a host.* callback served DURING the run and
// routed into the handler the caller supplied.
func TestEngineRunRoutesHostCallbacks(t *testing.T) {
	var seen []HostRequest
	ec := newEngineConn(&Decl{ProtocolVersion: ProtocolVersion, Kind: KindStep, ABI: EngineABI, Type: "acme-engine"})
	ec.run = func(req RunRequest, back func(string, any) HostResult) map[string]any {
		res := back(MethodHostKV, HostRequest{RunID: req.RunID, Kind: "kv", Op: "get", Resource: "cache", Args: []any{"ns", "k"}})
		return map[string]any{"value": res.Value, "ok": res.OK, "inputs": req.Inputs["x"]}
	}
	c := engineClient(t, ec)

	out, err := c.Run(context.Background(),
		RunRequest{Instance: "acme-engine", Inputs: map[string]any{"x": "in"}},
		func(req HostRequest) HostResult {
			seen = append(seen, req)
			return HostResult{OK: true, Value: "from-host"}
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out["value"] != "from-host" || out["ok"] != true || out["inputs"] != "in" {
		t.Fatalf("outputs = %+v", out)
	}
	if len(seen) != 1 || seen[0].Kind != "kv" || seen[0].Op != "get" || seen[0].Resource != "cache" {
		t.Fatalf("host handler saw %+v", seen)
	}
}

// SECURITY. The run_id is a capability, not a name: a callback presenting the
// wrong one, an empty one, or one whose run has ENDED is refused before the
// handler is consulted at all. Neutralizing the check in runFor must fail
// this test.
func TestHostCallbackWithoutAValidRunIDIsRefused(t *testing.T) {
	var reached int
	ec := newEngineConn(&Decl{ProtocolVersion: ProtocolVersion, Kind: KindStep, ABI: EngineABI, Type: "acme-engine"})
	var stolen string
	ec.run = func(req RunRequest, back func(string, any) HostResult) map[string]any {
		stolen = req.RunID
		bad := back(MethodHostKV, HostRequest{RunID: "0000", Kind: "kv", Op: "get", Resource: "c", Args: []any{"n", "k"}})
		none := back(MethodHostKV, HostRequest{Kind: "kv", Op: "get", Resource: "c", Args: []any{"n", "k"}})
		good := back(MethodHostKV, HostRequest{RunID: req.RunID, Kind: "kv", Op: "get", Resource: "c", Args: []any{"n", "k"}})
		return map[string]any{"bad": bad, "none": none, "good": good}
	}
	c := engineClient(t, ec)
	host := func(HostRequest) HostResult { reached++; return HostResult{OK: true, Value: "v"} }

	out, err := c.Run(context.Background(), RunRequest{}, host)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{"bad", "none"} {
		got, _ := out[name].(map[string]any)
		if got["ok"] == true {
			t.Errorf("%s run_id was ACCEPTED: %+v", name, got)
		}
		if got["refused"] != true || !strings.Contains(got["error"].(string), "run_id") {
			t.Errorf("%s run_id: want a refusal naming run_id, got %+v", name, got)
		}
	}
	if good, _ := out["good"].(map[string]any); good["ok"] != true {
		t.Errorf("the run's OWN token was refused: %+v", good)
	}
	if reached != 1 {
		t.Fatalf("the data-plane handler was reached %d times — only the authenticated call may reach it", reached)
	}

	// And the capability dies with the run: the same token, presented after
	// Run returned, is now nobody's.
	if c.runFor(stolen) != nil {
		t.Fatal("the run's token still resolves after the run ended")
	}
	res, rpcErr := c.handleRequest(context.Background(), MethodHostKV, mustJSON(HostRequest{RunID: stolen, Kind: "kv", Op: "get"}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	if hr := res.(HostResult); hr.OK || !hr.Refused {
		t.Fatalf("a token replayed after its run was accepted: %+v", hr)
	}
	if reached != 1 {
		t.Fatalf("the replayed token reached the handler (%d calls)", reached)
	}
}

// A CONNECTOR plugin has no run registered — it is never given a plugin.run —
// so every host.* call it could make is refused. This is the fleet-safety
// property from the other side: the new direction grants a plugin nothing it
// was not handed a token for.
func TestAConnectorPluginCannotUseTheDataPlane(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{})
	res, rpcErr := c.handleRequest(context.Background(), MethodHostKV, mustJSON(HostRequest{RunID: "anything", Kind: "kv", Op: "get"}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	if hr := res.(HostResult); hr.OK || !hr.Refused {
		t.Fatalf("a connector plugin reached the data plane: %+v", hr)
	}
	// And a method that is not a host callback is still method-not-found, the
	// answer the daemon has always given.
	if _, rpcErr := c.handleRequest(context.Background(), "daemon.please", nil); rpcErr == nil || rpcErr.Code != acp.CodeMethodNotFound {
		t.Fatalf("want method-not-found for a non-callback, got %v", rpcErr)
	}
}

// The method IS the kind. A host.kv request whose body claims sql is refused
// rather than reconciled.
func TestHostRequestKindMustMatchItsMethod(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{})
	_, rpcErr := c.handleRequest(context.Background(), MethodHostKV, mustJSON(HostRequest{RunID: "x", Kind: "sql", Op: "exec"}))
	if rpcErr == nil || rpcErr.Code != acp.CodeInvalidParams {
		t.Fatalf("want invalid-params for a kind/method mismatch, got %v", rpcErr)
	}
}

// FLEET SAFETY at the daemon's acceptance point (Describe): a Decl with
// ProtocolVersion 1 and NO ABI — every connector and runtime plugin in the
// field — loads exactly as it did before engines existed, and its ABI is not
// consulted.
func TestExistingPluginsLoadUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl Decl
		spec func() Spec
	}{
		{"connector with no kind and no abi (pre-#54 shape)",
			Decl{ProtocolVersion: 1, Type: "jira", Verbs: []Verb{{Name: "search"}}},
			connectorSpec},
		{"connector that declares its kind but no abi",
			Decl{ProtocolVersion: 1, Kind: KindConnector, Type: "jira"},
			connectorSpec},
		{"runtime with no abi",
			Decl{ProtocolVersion: 1, Kind: KindRuntime, Type: "modal"},
			func() Spec { return Spec{Name: "modal", Kind: KindRuntime, Provides: "modal", Local: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeConn()
			d := tc.decl
			fc.describe = &d
			sp := tc.spec()
			sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
			c := NewClient(sp, Deps{dial: fakeDial(fc)})
			defer c.Close()
			got, err := c.Describe(context.Background())
			if err != nil {
				t.Fatalf("an existing plugin stopped loading: %v", err)
			}
			if got.ABI != 0 {
				t.Fatalf("ABI was invented for a plugin that sent none: %d", got.ABI)
			}
		})
	}
}

// An ENGINE's ABI, by contrast, IS checked — and only an engine's.
func TestEngineABIIsNegotiated(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Kind: KindStep, ABI: EngineABI + 1, Type: "acme-engine"}
	sp := engineSpec(t)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	defer c.Close()
	_, err := c.Describe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "step-engine ABI") {
		t.Fatalf("want an ABI refusal, got %v", err)
	}
	// The refusal must NOT be phrased as a protocol-version problem: the wire
	// protocol is unchanged and an operator told otherwise would go looking in
	// the wrong place.
	if strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("an ABI mismatch was reported as a protocol mismatch: %v", err)
	}
}

// Concurrent runs on one engine each get their own token, and each token
// reaches only its own run's handler. Run with -race.
func TestConcurrentRunsGetDistinctTokens(t *testing.T) {
	ec := newEngineConn(&Decl{ProtocolVersion: ProtocolVersion, Kind: KindStep, ABI: EngineABI, Type: "acme-engine"})
	ec.run = func(req RunRequest, back func(string, any) HostResult) map[string]any {
		res := back(MethodHostKV, HostRequest{RunID: req.RunID, Kind: "kv", Op: "get", Resource: "c", Args: []any{"n", "k"}})
		return map[string]any{"who": res.Value, "token": req.RunID}
	}
	c := engineClient(t, ec)

	var mu sync.Mutex
	tokens := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			me := string(rune('a' + i))
			out, err := c.Run(context.Background(), RunRequest{Inputs: map[string]any{"i": i}},
				func(HostRequest) HostResult { return HostResult{OK: true, Value: me} })
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			if out["who"] != me {
				t.Errorf("run %d got another run's host handler: %+v", i, out)
			}
			mu.Lock()
			tok, _ := out["token"].(string)
			if tok == "" || tokens[tok] {
				t.Errorf("run %d: token %q is empty or reused", i, tok)
			}
			tokens[tok] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	c.mu.Lock()
	left := len(c.runs)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d run token(s) outlived their run", left)
	}
}

// A run with NO host handler is granted no data plane at all: no token is
// minted, and the engine is told so by an empty run_id.
func TestRunWithoutAHostGrantsNoToken(t *testing.T) {
	ec := newEngineConn(&Decl{ProtocolVersion: ProtocolVersion, Kind: KindStep, ABI: EngineABI, Type: "acme-engine"})
	ec.run = func(req RunRequest, _ func(string, any) HostResult) map[string]any {
		return map[string]any{"token": req.RunID}
	}
	c := engineClient(t, ec)
	out, err := c.Run(context.Background(), RunRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["token"] != "" {
		t.Fatalf("a run with no host handler was given a token: %+v", out)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
