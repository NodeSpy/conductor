package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The done+output contract: a schema step's agent delivers its result via
// `conductor call step.done --json '{"output": …}'` — one atomic final action,
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
	d.markNativeUnsupported(schemaCacheKey(req))

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
	res, err := d.dispatchOutputSchema(context.Background(), req, []string{"run", "assess this pr", "--json"}, "assess this pr", "", &ref, true)
	if err != nil {
		t.Fatalf("verb-delivered output must satisfy the schema step: %v", err)
	}
	if len(fb.calls) != 1 {
		t.Fatalf("delivered output must need NO corrective retry; got %d runs", len(fb.calls))
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
	if out, ok := d.takeDeliveredOutput("d1"); !ok || out["decision"] != "request_changes" {
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
