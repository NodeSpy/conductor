package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// scriptedRunAgentBackend is an in-process Backend double that answers
// RunAgent calls from a scripted queue (one func per call, in order) and
// records every call's Args so a test can assert exactly what argv paseo()
// actually sent — whether --output-schema was attempted, what the prompt
// carried, and how many physical runs happened. Every other Backend method
// panics: the output_schema AUTO path (checkout:none, an explicit Workspace,
// a schema present so queueOrAdopt is skipped) must never reach them, and a
// panic catches a wrong assumption loudly instead of silently returning a
// zero value.
type scriptedRunAgentBackend struct {
	calls      []RunAgentOptions
	responses  []func(opts RunAgentOptions) (RunAgentResult, error)
	lastPrompt string
	lastOutput string
}

func (b *scriptedRunAgentBackend) RunAgent(_ context.Context, opts RunAgentOptions) (RunAgentResult, error) {
	i := len(b.calls)
	b.calls = append(b.calls, opts)
	if len(opts.Args) > 1 {
		b.lastPrompt = opts.Args[1]
	}
	if i >= len(b.responses) {
		return RunAgentResult{}, errors.New("scriptedRunAgentBackend: unscripted call")
	}
	res, err := b.responses[i](opts)
	b.lastOutput = res.Output
	return res, err
}

// AgentLog mirrors real paseo: `paseo logs <id>` prints the `[User]` prompt
// echo (which for a soft run carries the injected schema document) followed by
// the assistant's message. The soft path must recover the answer from here —
// paseo run itself returns only the launch envelope — and must pick the
// answer over the echoed schema. Synthesizing the echo lets this test prove
// exactly that.
func (b *scriptedRunAgentBackend) AgentLog(_ context.Context, _ string, _ int) (string, error) {
	return "[User] " + b.lastPrompt + "\n" + b.lastOutput, nil
}

func (b *scriptedRunAgentBackend) ListAgents(context.Context, map[string]string) ([]AgentInfo, error) {
	panic("output_schema path must not list agents (OutputSchema skips queueOrAdopt)")
}
func (b *scriptedRunAgentBackend) Inspect(context.Context, string) (AgentDetail, error) {
	panic("not used")
}
func (b *scriptedRunAgentBackend) ArchiveAgent(context.Context, string) error { panic("not used") }
func (b *scriptedRunAgentBackend) ArchiveWorkspace(context.Context, string) error {
	panic("not used")
}
func (b *scriptedRunAgentBackend) CreateWorktree(context.Context, CreateWorktreeOptions) (CreateWorktreeResult, error) {
	panic("checkout:none must not create a worktree")
}
func (b *scriptedRunAgentBackend) CreateWorkspace(context.Context, CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	panic("an explicit req.Workspace must skip scratch-workspace resolution")
}
func (b *scriptedRunAgentBackend) ListWorkspaces(context.Context) ([]WorkspaceInfo, error) {
	panic("not used")
}
func (b *scriptedRunAgentBackend) Clone(context.Context, CloneOptions) error { panic("not used") }
func (b *scriptedRunAgentBackend) Send(context.Context, SendOptions) (SendResult, error) {
	panic("not used")
}
func (b *scriptedRunAgentBackend) Wait(context.Context, string) error { panic("not used") }

// schemaTestRequest builds a foreground agent Request carrying schema, pinned
// to an explicit workspace (skips scratch resolution) with checkout:none
// (skips worktree creation) — isolating the test to exactly the AUTO
// native/soft decision in dispatchOutputSchema.
func schemaTestRequest(schema map[string]any) Request {
	return Request{
		Wait: true,
		Trigger: core.Trigger{Kind: "assess",
			Target: core.Target{Repo: "a/w", PR: 1, Number: 1}},
		Action:   config.Action{Type: "agent", Prompt: "assess this pr", Checkout: "none", OutputSchema: schema},
		Step:     config.Step{Runtime: "claude-relay"},
		Provider: "claude", Model: "claude-x",
		Workspace: "wks1",
	}
}

func okObjResult(obj map[string]any) func(RunAgentOptions) (RunAgentResult, error) {
	b, _ := json.Marshal(obj)
	return func(RunAgentOptions) (RunAgentResult, error) {
		return RunAgentResult{Output: string(b), AgentID: "a1"}, nil
	}
}

func textResult(s string) func(RunAgentOptions) (RunAgentResult, error) {
	return func(RunAgentOptions) (RunAgentResult, error) {
		return RunAgentResult{Output: s, AgentID: "a1"}, nil
	}
}

func schemaFailResult() func(RunAgentOptions) (RunAgentResult, error) {
	return func(RunAgentOptions) (RunAgentResult, error) {
		return RunAgentResult{}, errors.New(`paseo run: exit status 1: OUTPUT_SCHEMA_FAILED: Failed to handle agent.timeline.list_prompts.request`)
	}
}

// (a) A provider whose native schema fails is served via the SOFT fallback:
// the prompt carries the schema directive, --output-schema is NOT attempted
// on that run, and the validated object comes back as the step's output.
func TestOutputSchemaFallsBackToSoftOnNativeFailure(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		schemaFailResult(),
		okObjResult(map[string]any{"decision": "approve"}),
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)

	ref, err := d.paseo(context.Background(), schemaTestRequest(schema))
	if err != nil {
		t.Fatalf("expected the soft fallback to succeed: %v", err)
	}
	if len(fb.calls) != 2 {
		t.Fatalf("expected exactly 2 physical runs (native + soft), got %d", len(fb.calls))
	}
	if strings.Contains(strings.Join(fb.calls[1].Args, " "), "--output-schema") {
		t.Fatal("the soft run must NOT carry --output-schema")
	}
	if !strings.Contains(fb.calls[1].Args[1], "Respond with ONLY a single JSON object") {
		t.Fatal("the soft run's prompt must carry the schema directive")
	}
	if !strings.Contains(fb.calls[1].Args[1], `"decision"`) {
		t.Fatal("the soft run's prompt must carry the compact schema")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(ref.Output), &out); err != nil || out["decision"] != "approve" {
		t.Fatalf("ref.Output should be the validated object: %q (err=%v)", ref.Output, err)
	}
}

// (b) The capability cache: once native has failed once for a given
// runtime|provider|model, the SECOND dispatch for that triple skips the
// native attempt entirely and goes straight to soft — exactly one physical
// run, with no --output-schema.
func TestOutputSchemaCapabilityCacheSkipsNativeAfterFirstFailure(t *testing.T) {
	schema := map[string]any{"type": "object"}
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		schemaFailResult(),                      // dispatch 1: native fails
		okObjResult(map[string]any{"ok": true}), // dispatch 1: soft succeeds
		okObjResult(map[string]any{"ok": true}), // dispatch 2: soft directly
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)

	if _, err := d.paseo(context.Background(), schemaTestRequest(schema)); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if len(fb.calls) != 2 {
		t.Fatalf("dispatch 1 should take 2 physical runs (native fail + soft), got %d", len(fb.calls))
	}

	if _, err := d.paseo(context.Background(), schemaTestRequest(schema)); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if len(fb.calls) != 3 {
		t.Fatalf("dispatch 2 should skip native and take exactly 1 more physical run, got %d total calls", len(fb.calls))
	}
	if strings.Contains(strings.Join(fb.calls[2].Args, " "), "--output-schema") {
		t.Fatal("a cached-unsupported provider must never attempt --output-schema again")
	}
}

// (c) Tolerant extraction recovers a schema object out of a ```json fenced
// block and out of prose surrounding a bare object.
func TestTolerantExtractObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // expected "decision" value
	}{
		{"plain", `{"decision":"approve"}`, "approve"},
		{"fenced json", "```json\n{\"decision\":\"approve\"}\n```", "approve"},
		{"bare fence", "```\n{\"decision\":\"approve\"}\n```", "approve"},
		{"prose wrapped", "Here you go:\n{\"decision\":\"approve\"}\nHope that helps!", "approve"},
		{"envelope", `{"output":{"decision":"approve"}}`, "approve"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj, ok := tolerantExtractObject(c.in)
			if !ok {
				t.Fatalf("expected extraction to succeed for %q", c.in)
			}
			if obj["decision"] != c.want {
				t.Fatalf("got %v, want decision=%q", obj, c.want)
			}
		})
	}
	if _, ok := tolerantExtractObject("not json at all, no braces here"); ok {
		t.Fatal("expected extraction to fail on non-JSON prose with no object")
	}
}

// (d) The validator catches enum, required, and type violations, and accepts
// a conforming object.
func TestValidateSchema(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"required":             []any{"decision"},
		"additionalProperties": false,
		"properties": map[string]any{
			"decision": map[string]any{"type": "string", "enum": []any{"approve", "reject"}},
			"score":    map[string]any{"type": "integer"},
		},
	}
	if err := validateSchema(schema, map[string]any{"decision": "approve", "score": float64(3)}); err != nil {
		t.Fatalf("expected a conforming object to validate: %v", err)
	}
	if err := validateSchema(schema, map[string]any{"score": float64(3)}); err == nil {
		t.Fatal("expected a missing required property to fail")
	}
	if err := validateSchema(schema, map[string]any{"decision": "maybe"}); err == nil {
		t.Fatal("expected an out-of-enum value to fail")
	}
	if err := validateSchema(schema, map[string]any{"decision": "approve", "score": "3"}); err == nil {
		t.Fatal("expected a wrong-typed property (string, not integer) to fail")
	}
	if err := validateSchema(schema, map[string]any{"decision": "approve", "extra": true}); err == nil {
		t.Fatal("expected an additionalProperties:false violation to fail")
	}
}

// (e) The soft path takes exactly one bounded corrective retry: if the
// corrective reply is STILL not valid JSON matching the schema, the step
// errors rather than looping.
func TestOutputSchemaSoftPathExactlyOneCorrectiveRetry(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		schemaFailResult(),           // native fails
		textResult("nope, not json"), // soft attempt: still not JSON
		textResult("still not json"), // corrective retry: still not JSON
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)

	_, err := d.paseo(context.Background(), schemaTestRequest(schema))
	if err == nil {
		t.Fatal("expected an error after the corrective retry also fails")
	}
	if len(fb.calls) != 3 {
		t.Fatalf("expected exactly 3 physical runs (native + soft + one corrective retry), got %d", len(fb.calls))
	}
	if !strings.Contains(fb.calls[2].Args[1], "Your previous reply was not valid JSON") {
		t.Fatal("the corrective retry's prompt must explain what was wrong")
	}
	if !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("error should name the output_schema contract: %v", err)
	}
}

// Native success needs no fallback at all: exactly one physical run, and
// ref.Output is left untouched (flow.extractOutputs already handles it).
func TestOutputSchemaNativeSuccessNoFallback(t *testing.T) {
	schema := map[string]any{"type": "object"}
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		okObjResult(map[string]any{"decision": "approve"}),
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)

	ref, err := d.paseo(context.Background(), schemaTestRequest(schema))
	if err != nil {
		t.Fatalf("native success: %v", err)
	}
	if len(fb.calls) != 1 {
		t.Fatalf("native success should take exactly 1 physical run, got %d", len(fb.calls))
	}
	if !strings.Contains(strings.Join(fb.calls[0].Args, " "), "--output-schema") {
		t.Fatal("the first attempt must be native (--output-schema present)")
	}
	if !strings.Contains(ref.Output, "approve") {
		t.Fatalf("ref.Output unexpected: %q", ref.Output)
	}
}

// An unrelated run failure (not a schema signal) must propagate as an
// ordinary dispatch error — no fallback attempt, no cache mutation.
func TestOutputSchemaUnrelatedFailureDoesNotFallBack(t *testing.T) {
	schema := map[string]any{"type": "object"}
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		func(RunAgentOptions) (RunAgentResult, error) {
			return RunAgentResult{}, errors.New(`paseo run: exit status 1: RUN_FAILED: network partition`)
		},
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)

	_, err := d.paseo(context.Background(), schemaTestRequest(schema))
	if err == nil || !strings.Contains(err.Error(), "network partition") {
		t.Fatalf("expected the unrelated failure to propagate untouched: %v", err)
	}
	if len(fb.calls) != 1 {
		t.Fatalf("must not attempt a soft fallback for an unrelated failure, got %d calls", len(fb.calls))
	}
	if !d.nativeSchemaSupported(schemaCacheKey(schemaTestRequest(schema))) {
		t.Fatal("an unrelated failure must not poison the capability cache")
	}
}

// EnforceSchema is the runtime-agnostic sibling of the paseo soft path used by
// the controller runtimes (cli/acp/opencode): it operates on an already-
// produced answer plus a "run one more turn" primitive, and owns the
// extract/validate/canonicalize + single-corrective-retry contract.
func TestEnforceSchemaFirstAnswerValidates(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	// The answer wraps the object in prose and even echoes the schema doc; the
	// LAST validating object is the agent's real reply.
	answer := "Here is my verdict.\n" + `{"type":"object"}` + "\n" + `{"decision":"approve"}`
	retries := 0
	retry := func(context.Context, string) (string, error) { retries++; return "", nil }

	out, err := EnforceSchema(context.Background(), schema, "base prompt", answer, retry)
	if err != nil {
		t.Fatalf("EnforceSchema: %v", err)
	}
	if retries != 0 {
		t.Fatalf("a valid first answer must not trigger a corrective turn, got %d", retries)
	}
	var got map[string]any
	if json.Unmarshal([]byte(out), &got); got["decision"] != "approve" {
		t.Fatalf("expected canonical JSON of the validated object, got %q", out)
	}
}

func TestEnforceSchemaOneCorrectiveTurnThenSucceeds(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	var gotPrompt string
	retry := func(_ context.Context, p string) (string, error) {
		gotPrompt = p
		return `{"decision":"reject"}`, nil
	}
	out, err := EnforceSchema(context.Background(), schema, "base prompt", "not json at all", retry)
	if err != nil {
		t.Fatalf("EnforceSchema: %v", err)
	}
	if !strings.Contains(gotPrompt, "base prompt") || !strings.Contains(gotPrompt, "Return ONLY the JSON object") {
		t.Fatalf("corrective prompt must re-send the base prompt and the reason: %q", gotPrompt)
	}
	if !strings.Contains(out, "reject") {
		t.Fatalf("expected the corrective turn's object, got %q", out)
	}
}

func TestEnforceSchemaHardErrorAfterOneRetry(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	calls := 0
	retry := func(context.Context, string) (string, error) { calls++; return "still not json", nil }
	_, err := EnforceSchema(context.Background(), schema, "base", "nope", retry)
	if err == nil || !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("expected a hard output_schema error after one failed retry, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("exactly one corrective turn (no unbounded loop), got %d", calls)
	}
}

func TestEnforceSchemaNilRetryIsSingleShot(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	if _, err := EnforceSchema(context.Background(), schema, "base", "nope", nil); err == nil {
		t.Fatal("a nil retry must turn a first miss straight into an error")
	}
	if out, err := EnforceSchema(context.Background(), schema, "base", `{"decision":"ok"}`, nil); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("a nil retry must still accept a valid first answer: out=%q err=%v", out, err)
	}
}
