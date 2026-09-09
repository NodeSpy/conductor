package plugin

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestServeDescribeInvoke drives the serve loop with the exact wire framing the
// daemon uses (newline-delimited JSON-RPC 2.0) and checks the responses — a
// transport-level check independent of the daemon integration test.
func TestServeDescribeInvoke(t *testing.T) {
	h := ConnectorFunc(
		func() Decl {
			return Decl{Type: "t", Verbs: []Verb{{Name: "echo"}}}
		},
		func(req InvokeRequest) (InvokeResult, error) {
			if req.Verb != "echo" {
				return InvokeResult{}, Errorf(CodeInvalidParams, "unknown verb")
			}
			return InvokeResult{Outputs: map[string]any{"msg": req.Options["msg"]}}, nil
		},
	)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.describe"}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.invoke","params":{"verb":"echo","options":{"msg":"hi"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.invoke","params":{"verb":"nope"}}`,
		`{"jsonrpc":"2.0","method":"someNotification"}`, // ignored (no id)
	}, "\n") + "\n"

	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil && err != io.EOF {
		t.Fatalf("serve: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(out.String()))
	var m1, m2, m3 wireMessage
	if err := dec.Decode(&m1); err != nil {
		t.Fatalf("decode describe resp: %v", err)
	}
	var decl Decl
	if err := json.Unmarshal(m1.Result, &decl); err != nil || decl.Type != "t" {
		t.Fatalf("describe result = %s (%v)", m1.Result, err)
	}
	if decl.ProtocolVersion != ProtocolVersion {
		t.Fatalf("Serve did not stamp ProtocolVersion: got %d", decl.ProtocolVersion)
	}
	if string(*m1.ID) != "1" {
		t.Fatalf("describe id echoed as %s, want 1", *m1.ID)
	}

	if err := dec.Decode(&m2); err != nil {
		t.Fatalf("decode invoke resp: %v", err)
	}
	var res InvokeResult
	if err := json.Unmarshal(m2.Result, &res); err != nil || res.Outputs["msg"] != "hi" {
		t.Fatalf("invoke result = %s (%v)", m2.Result, err)
	}

	if err := dec.Decode(&m3); err != nil {
		t.Fatalf("decode error resp: %v", err)
	}
	if m3.Error == nil || m3.Error.Code != CodeInvalidParams {
		t.Fatalf("expected InvalidParams error, got %+v", m3.Error)
	}

	// Exactly three responses — the notification produced none.
	if err := dec.Decode(new(wireMessage)); err != io.EOF {
		t.Fatalf("expected 3 responses (notification ignored), got a 4th: %v", err)
	}
}
