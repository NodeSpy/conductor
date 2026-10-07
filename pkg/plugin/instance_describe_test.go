package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// instanceDescriberHandler is a fake connector plugin whose declared verbs
// depend on the instance's own config — the rest/graphql shape Q6 exists for.
type instanceDescriberHandler struct {
	// perInstance, when non-nil, is called from DescribeInstance; nil means
	// "this plugin does not implement InstanceDescriber" (the zero-method
	// case tested separately by plainHandler below).
	perInstance func(instance string, config map[string]any) (Decl, error)
}

func (h instanceDescriberHandler) Describe() Decl {
	return Decl{Type: "rest", Verbs: []Verb{{Name: "generic-placeholder"}}}
}

func (h instanceDescriberHandler) Invoke(InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, Errorf(CodeMethodNotFound, "unused in this test")
}

func (h instanceDescriberHandler) DescribeInstance(_ context.Context, instance string, config map[string]any) (Decl, error) {
	return h.perInstance(instance, config)
}

// TestInstanceDescribeRoundTrip drives plugin.describe{instance, config} over
// the real wire (ServeConn-equivalent via the serve() loop) and checks that a
// plugin implementing InstanceDescriber returns a declaration that depends on
// the instance's own config — the rest/graphql case Q6 exists for.
func TestInstanceDescribeRoundTrip(t *testing.T) {
	h := instanceDescriberHandler{
		perInstance: func(instance string, config map[string]any) (Decl, error) {
			verbs, _ := config["verbs"].(map[string]any)
			d := Decl{Type: "rest"}
			for name := range verbs {
				d.Verbs = append(d.Verbs, Verb{Name: name})
			}
			d.Desc = "instance:" + instance
			return d, nil
		},
	}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"instance":"a","config":{"verbs":{"create_issue":{}}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.describe","params":{"instance":"b","config":{"verbs":{"list_repos":{},"get_repo":{}}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.describe"}`, // type-level: no instance
	}, "\n") + "\n"

	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}

	byID := decodeResponses(t, out.String())

	var da Decl
	if err := json.Unmarshal(byID["1"].Result, &da); err != nil {
		t.Fatalf("instance a describe: %v (raw %s)", err, byID["1"].Result)
	}
	if len(da.Verbs) != 1 || da.Verbs[0].Name != "create_issue" {
		t.Fatalf("instance a verbs = %+v, want [create_issue]", da.Verbs)
	}
	if da.Desc != "instance:a" {
		t.Fatalf("instance a desc = %q", da.Desc)
	}

	var db Decl
	if err := json.Unmarshal(byID["2"].Result, &db); err != nil {
		t.Fatalf("instance b describe: %v", err)
	}
	if len(db.Verbs) != 2 {
		t.Fatalf("instance b verbs = %+v, want 2", db.Verbs)
	}

	var dtype Decl
	if err := json.Unmarshal(byID["3"].Result, &dtype); err != nil {
		t.Fatalf("type-level describe: %v", err)
	}
	if len(dtype.Verbs) != 1 || dtype.Verbs[0].Name != "generic-placeholder" {
		t.Fatalf("type-level decl changed: %+v, want the static placeholder", dtype.Verbs)
	}
}

// TestInstanceDescribeUnsupportedIsMethodNotFound proves the negotiation: a
// plugin that does NOT implement InstanceDescriber answers CodeMethodNotFound
// for an instance-scoped describe (never silently ignores instance/config and
// returns the type-level Decl, which would be indistinguishable from "yes, I
// support it, and every instance gets the same answer").
func TestInstanceDescribeUnsupportedIsMethodNotFound(t *testing.T) {
	h := ConnectorFunc(
		func() Decl { return Decl{Type: "acme", Verbs: []Verb{{Name: "echo"}}} },
		func(req InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil },
	)
	in := `{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"instance":"a","config":{}}}` + "\n"
	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	m := byID["1"]
	if m.Error == nil || m.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected CodeMethodNotFound for an instance describe on a plugin with no InstanceDescriber, got %+v / result=%s", m.Error, m.Result)
	}
}

// TestInstanceDescribeErrorPropagates proves a handler-returned *Error
// survives the wire with its own code.
func TestInstanceDescribeErrorPropagates(t *testing.T) {
	h := instanceDescriberHandler{
		perInstance: func(string, map[string]any) (Decl, error) {
			return Decl{}, Fail(CodeInvalid, "bad instance config", nil)
		},
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"instance":"a"}}` + "\n"
	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	m := byID["1"]
	if m.Error == nil || m.Error.Code != CodeInvalid {
		t.Fatalf("expected CodeInvalid, got %+v", m.Error)
	}
}

// TestInstanceDescribePlainErrorIsWrappedInternal proves the OTHER half of
// the propagation rule: a handler that returns a plain error (not a *Error
// the handler deliberately coded) is wrapped as CodeInternalError rather
// than leaking an untyped error over the wire or panicking serve.
func TestInstanceDescribePlainErrorIsWrappedInternal(t *testing.T) {
	h := instanceDescriberHandler{
		perInstance: func(string, map[string]any) (Decl, error) {
			return Decl{}, errors.New("database connection refused")
		},
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"plugin.describe","params":{"instance":"a"}}` + "\n"
	var out strings.Builder
	if err := serve(strings.NewReader(in), &out, h); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	m := byID["1"]
	if m.Error == nil || m.Error.Code != CodeInternalError {
		t.Fatalf("expected a plain error wrapped as CodeInternalError (%d), got %+v", CodeInternalError, m.Error)
	}
	if !strings.Contains(m.Error.Message, "database connection refused") {
		t.Fatalf("the wrapped error must keep the original message, got %q", m.Error.Message)
	}
}

// decodeResponses reads every wireMessage out of raw, keyed by its id.
func decodeResponses(t *testing.T, raw string) map[string]wireMessage {
	t.Helper()
	byID := map[string]wireMessage{}
	dec := json.NewDecoder(strings.NewReader(raw))
	for {
		var m wireMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.ID != nil {
			byID[string(*m.ID)] = m
		}
	}
	return byID
}
