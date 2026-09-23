package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The done+output contract: a schema step's agent delivers its result via
// `conductor call step.done --output '<result JSON>'` — one atomic final action,
// validated at the verb boundary — instead of the fragile reply-is-the-JSON
// dance. These pin the dispatcher half: the rendezvous, validation, the verb
// directive, and that a delivered output wins over whatever the chat said.

func verbTestRequest(schema map[string]any) Request {
	req := schemaTestRequest(schema)
	req.DispatchID = "disp-verb-1"
	return req
}

func TestOutputSchemaVerbDeliveredWins(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	d := &Dispatcher{PaseoBin: "paseo"}
	req := verbTestRequest(schema)
	// NOTE: native is deliberately NOT marked unsupported — with creds the verb
	// path must be taken FIRST, never the native probe (one uniform signal).

	// The "agent": mid-run it calls step.done with the output (DeliverOutput),
	// then its chat reply is USELESS prose — exactly the case that used to fail.
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		func(opts RunAgentOptions) (RunAgentResult, error) {
			found, err := d.DeliverOutput("disp-verb-1", map[string]any{"decision": "approve"})
			if !found || err != nil {
				t.Fatalf("DeliverOutput mid-run: found=%v err=%v", found, err)
			}
			return RunAgentResult{Output: "All done! I delivered my review via step.done.", AgentID: "a1"}, nil
		},
	}}
	d.SetBackend(fb)

	ref := RunRef{}
	res, err := d.dispatchOutputSchema(context.Background(), req, []string{"run", "assess this pr", "--output-schema", "{}", "--json"}, "assess this pr", "", &ref, true)
	if err != nil {
		t.Fatalf("verb-delivered output must satisfy the schema step: %v", err)
	}
	if len(fb.calls) != 1 {
		t.Fatalf("delivered output must need NO corrective retry; got %d runs", len(fb.calls))
	}
	// Native --output-schema is skipped entirely on the creds path: the single
	// run must be the verb-directive run, not a native probe.
	if strings.Contains(strings.Join(fb.calls[0].Args, " "), "--output-schema") {
		t.Fatalf("with creds the native probe must be skipped: %v", fb.calls[0].Args)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil || out["decision"] != "approve" {
		t.Fatalf("step output must be the verb-delivered object, got %q (err=%v)", res.Output, err)
	}
	// The prompt instructed verb delivery, not reply-is-the-JSON.
	if !strings.Contains(fb.lastPrompt, "step.done") {
		t.Fatalf("prompt must carry the verb directive: %q", fb.lastPrompt)
	}
	if strings.Contains(fb.lastPrompt, "Respond with ONLY a single JSON object") {
		t.Fatalf("verb delivery must not also demand JSON in the chat reply: %q", fb.lastPrompt)
	}
	// The slot is dropped once the dispatch resolves.
	if _, ok := d.takeDeliveredOutput("disp-verb-1"); ok {
		t.Fatal("slot must be dropped after the dispatch returns")
	}
}

func TestDeliverOutputValidatesAtTheVerbBoundary(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	d := &Dispatcher{}
	d.registerOutputSlot("d1", schema)

	// Invalid object: the agent gets a precise, retryable error; nothing stored.
	found, err := d.DeliverOutput("d1", map[string]any{"nope": true})
	if !found || err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("invalid output must be refused with a schema error: found=%v err=%v", found, err)
	}
	if _, ok := d.takeDeliveredOutput("d1"); ok {
		t.Fatal("a refused output must not be stored")
	}
	// The agent fixes it and calls again: accepted.
	if found, err := d.DeliverOutput("d1", map[string]any{"decision": "request_changes"}); !found || err != nil {
		t.Fatalf("valid output refused: found=%v err=%v", found, err)
	}
	if out, ok := d.takeDeliveredOutput("d1"); !ok || out.(map[string]any)["decision"] != "request_changes" {
		t.Fatalf("stored output missing: %v %v", out, ok)
	}
	// No schema dispatch waiting: not an error — done semantics proceed.
	if found, err := d.DeliverOutput("unknown", map[string]any{"x": 1}); found || err != nil {
		t.Fatalf("no waiting dispatch must be (false, nil), got (%v, %v)", found, err)
	}
}

func TestOutputSchemaWithoutCredsKeepsReplyContract(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	d := &Dispatcher{PaseoBin: "paseo"}
	req := verbTestRequest(schema)
	d.markNativeUnsupported(schemaCacheKey(req))
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		okObjResult(map[string]any{"decision": "approve"}),
	}}
	d.SetBackend(fb)

	ref := RunRef{}
	if _, err := d.dispatchOutputSchema(context.Background(), req, []string{"run", "assess this pr", "--json"}, "assess this pr", "", &ref, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fb.lastPrompt, "step.done") {
		t.Fatalf("without creds the prompt must not reference step.done: %q", fb.lastPrompt)
	}
	if !strings.Contains(fb.lastPrompt, "Respond with ONLY a single JSON object") {
		t.Fatalf("without creds the classic reply directive must remain: %q", fb.lastPrompt)
	}
}

// Every value shape conductor's validator admits must be deliverable through
// the verb — including falsy scalars, which are real deliveries, not absences.
func TestDeliverOutputSupportsEveryValueShape(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		value  any
		want   string // canonical serialization the step receives
	}{
		{"object", map[string]any{"type": "object", "required": []any{"decision"},
			"properties": map[string]any{"decision": map[string]any{"type": "string"}}},
			map[string]any{"decision": "approve"}, `{"decision":"approve"}`},
		{"array", map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			[]any{"a", "b"}, `["a","b"]`},
		{"string", map[string]any{"type": "string"}, "ship it", `"ship it"`},
		{"empty string", map[string]any{"type": "string"}, "", `""`},
		{"number", map[string]any{"type": "number"}, 3.5, `3.5`},
		{"integer zero", map[string]any{"type": "integer"}, float64(0), `0`},
		{"boolean false", map[string]any{"type": "boolean"}, false, `false`},
		{"enum", map[string]any{"type": "string", "enum": []any{"approve", "reject"}}, "reject", `"reject"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{}
			d.registerOutputSlot("d1", tc.schema)
			found, err := d.DeliverOutput("d1", tc.value)
			if !found || err != nil {
				t.Fatalf("valid %s delivery refused: found=%v err=%v", tc.name, found, err)
			}
			out, ok := d.takeDeliveredOutput("d1")
			if !ok {
				t.Fatalf("%s delivery not stored", tc.name)
			}
			if got := marshalCanonicalValue(out); got != tc.want {
				t.Fatalf("%s: canonical output %q, want %q", tc.name, got, tc.want)
			}
		})
	}

	// And the type mismatch is still refused precisely.
	d := &Dispatcher{}
	d.registerOutputSlot("d1", map[string]any{"type": "array"})
	if _, err := d.DeliverOutput("d1", "not an array"); err == nil {
		t.Fatal("a type mismatch must be refused")
	}
}
