package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

// output_schema is a CONDUCTOR-owned contract: ONE behavior, ALWAYS, ZERO
// config (v0.9.2). paseo's native `--output-schema` works for some providers
// (gemini, codex) but fails outright for others (the claude ACP relay:
// OUTPUT_SCHEMA_FAILED / "Failed to handle agent.timeline.list_prompts.request"),
// and conductor used to just forward the flag and inherit that gap. Now every
// output_schema step gets:
//
//  1. NATIVE: pass `--output-schema` (today's behavior) unless the capability
//     cache already knows native is unsupported for this runtime|provider|model.
//  2. On a native schema/structured-output failure — the run errors with a
//     schema signal, OR it succeeds but the output isn't valid JSON matching
//     the schema — record native-unsupported in the cache and fall back to SOFT.
//  3. SOFT: re-run without `--output-schema`, with the schema injected into the
//     prompt, then tolerantly extract + validate the JSON conductor-side, with
//     one bounded corrective retry.
//  4. Validate the final object against output_schema on BOTH paths (defense
//     in depth).
//
// No config field exists for this and none should be added — it is always on
// whenever a step sets output_schema:.

// schemaCacheKey identifies a (runtime, provider, model) triple for the
// native-schema capability cache. Runtime is the config `runtimes:` name the
// step resolved to (req.Step.Runtime); provider/model are the resolved model
// catalog values already carried on Request. Any of the three may be empty
// (a bare launch) — that's still a valid, stable key.
func schemaCacheKey(req Request) string {
	return req.Step.Runtime + "|" + req.Provider + "|" + req.Model
}

// nativeSchemaSupported reports whether the capability cache has already
// learned that native --output-schema does NOT work for key. Absent (the
// common case, including a nil map) means "unknown — try native".
func (d *Dispatcher) nativeSchemaSupported(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.nativeSchemaUnsupported[key]
}

// markNativeUnsupported records that native --output-schema failed for key,
// so every later dispatch for it skips straight to the SOFT fallback.
func (d *Dispatcher) markNativeUnsupported(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nativeSchemaUnsupported == nil {
		d.nativeSchemaUnsupported = map[string]bool{}
	}
	d.nativeSchemaUnsupported[key] = true
}

// dispatchOutputSchema runs a foreground agent step that carries an
// OutputSchema, implementing the AUTO native→soft contract above. nativeArgv
// is the fully-assembled argv paseo() already built (it carries
// `--output-schema <schema> --json`, or `--background --json`); prompt is the
// rendered prompt BEFORE that schema flag was appended, so the soft path can
// re-inject the schema directive into it instead. ref is updated in place
// when the actually-run argv differs from nativeArgv (soft), so audit/report
// reflects what really executed rather than the optimistic preview.
func (d *Dispatcher) dispatchOutputSchema(ctx context.Context, req Request, nativeArgv []string, prompt, cwd string, ref *RunRef, verbDelivery bool) (RunAgentResult, error) {
	schema := req.Action.OutputSchema
	key := schemaCacheKey(req)

	// Verb-delivered output (the done+output contract): when the agent holds
	// CLI creds, its structured output arrives via `conductor call step.done
	// --json '{"output": …}'` — validated daemon-side at the call, correlated
	// back here by dispatch id. Register the slot for the WHOLE dispatch
	// (native attempt included): an agent may deliver via the verb on any path,
	// and a filled slot always wins over reply-text extraction.
	if verbDelivery && req.DispatchID != "" {
		d.registerOutputSlot(req.DispatchID, schema)
		defer d.dropOutputSlot(req.DispatchID)
	}

	if !d.nativeSchemaSupported(key) {
		return d.runSoftSchema(ctx, req, nativeArgv, prompt, cwd, schema, ref, verbDelivery)
	}

	res, err := d.backend().RunAgent(ctx, RunAgentOptions{Args: nativeArgv, Cwd: cwd})
	if err != nil {
		if isNativeSchemaError(err.Error()) {
			d.markNativeUnsupported(key)
			return d.runSoftSchema(ctx, req, nativeArgv, prompt, cwd, schema, ref, verbDelivery)
		}
		// An unrelated failure (network, run error) — propagate as an
		// ordinary dispatch failure. Falling back here would spend a second
		// run on a problem the fallback can't fix, and would wrongly teach
		// the cache "native unsupported" from an outage rather than a real
		// capability gap.
		return res, err
	}
	if obj, ok := tolerantExtractObject(res.Output); ok {
		if verr := validateSchema(schema, obj); verr == nil {
			return res, nil // native worked — ref.Output/Argv stay as-is
		}
	}
	// A verb-delivered output wins even when the native reply was unusable —
	// the agent already handed us the validated object.
	if out, ok := d.takeDeliveredOutput(req.DispatchID); ok {
		res.Output = marshalCanonical(out)
		return res, nil
	}
	// Native ran clean (exit 0) but its output isn't valid JSON matching the
	// schema — per the contract that counts as a native failure too.
	d.markNativeUnsupported(key)
	return d.runSoftSchema(ctx, req, nativeArgv, prompt, cwd, schema, ref, verbDelivery)
}

// isNativeSchemaError reports whether a failed paseo run's error text names a
// schema/structured-output capability gap (as opposed to an ordinary run
// failure) — the two concrete signals seen in the wild plus the general
// OUTPUT_SCHEMA_FAILED code.
func isNativeSchemaError(errText string) bool {
	lo := strings.ToLower(errText)
	for _, sig := range []string{
		"output_schema_failed",
		"failed to handle agent.timeline.list_prompts.request",
	} {
		if strings.Contains(lo, sig) {
			return true
		}
	}
	return false
}

// runSoftSchema is the SOFT fallback: re-run without --output-schema, the
// schema injected into the prompt instead, then tolerantly extract + validate
// the JSON conductor-side with one bounded corrective retry. Reuses the same
// cwd/worktree/session the native attempt (if any) already used — only the
// argv (prompt + absence of --output-schema) changes.
func (d *Dispatcher) runSoftSchema(ctx context.Context, req Request, nativeArgv []string, prompt string, cwd string, schema map[string]any, ref *RunRef, verbDelivery bool) (RunAgentResult, error) {
	softArgv := stripOutputSchemaFlag(nativeArgv)
	// With CLI creds in the session, the output rides the done call
	// (verbSchemaDirective): one atomic final action delivers the result AND
	// signals done, validated at the verb boundary — no chat-reply JSON
	// fishing and no instruction conflict with other guidance. Without creds
	// the classic reply-is-the-JSON directive remains.
	directive := schemaDirective(schema)
	if verbDelivery && req.DispatchID != "" {
		directive = verbSchemaDirective(schema)
	}
	augmented := prompt + directive
	if err := checkPromptSize(augmented); err != nil {
		return RunAgentResult{}, fmt.Errorf("output_schema: soft fallback: %w", err)
	}
	argv := replacePromptArg(softArgv, augmented)
	ref.Argv = append([]string{d.PaseoBin}, argv...)

	res, err := d.backend().RunAgent(ctx, RunAgentOptions{Args: argv, Cwd: cwd})
	if err != nil {
		return res, err
	}
	if res.AgentID != "" {
		ref.AgentID = res.AgentID
	}
	// A verb-delivered output was already validated at the call; it wins.
	if out, ok := d.takeDeliveredOutput(req.DispatchID); ok {
		res.Output = marshalCanonical(out)
		return res, nil
	}
	if obj, ok := d.captureSchemaAnswer(ctx, res, schema); ok {
		res.Output = marshalCanonical(obj)
		return res, nil
	}
	return d.correctiveRetry(ctx, req, argv, augmented, cwd, schema, "response was not valid JSON matching the schema", ref)
}

// softLogTail is how many `paseo logs` entries the soft path reads back to find
// the agent's final message. A single triage/review turn is one entry; a small
// tail absorbs any `[User]` echo or trailing note without pulling unrelated
// history.
const softLogTail = 20

// captureSchemaAnswer recovers a soft-path agent's answer. `paseo run` returns
// only the launch envelope (agentId/status), never the message, so the answer
// is read from `paseo logs <id> --tail n` and taken as the LAST object there
// that validates against schema — which distinguishes the agent's instance
// from the schema DOCUMENT echoed back in the `[User]` prompt line (a schema is
// not an instance of itself, so it never validates).
func (d *Dispatcher) captureSchemaAnswer(ctx context.Context, res RunAgentResult, schema map[string]any) (map[string]any, bool) {
	if res.AgentID == "" {
		return nil, false
	}
	logText, err := d.backend().AgentLog(ctx, res.AgentID, softLogTail)
	if err != nil || strings.TrimSpace(logText) == "" {
		return nil, false
	}
	return extractSchemaMatch(logText, schema)
}

// correctiveRetry is the SOFT path's single bounded retry: told exactly why
// its last reply didn't satisfy the contract, the agent gets one more turn.
// A second miss is a hard error — no unbounded retry loop.
func (d *Dispatcher) correctiveRetry(ctx context.Context, req Request, prevArgv []string, prevPrompt string, cwd string, schema map[string]any, reason string, ref *RunRef) (RunAgentResult, error) {
	corrective := prevPrompt + "\n\nYour previous reply was not valid JSON matching the schema (" + reason + "). Return ONLY the JSON object."
	if err := checkPromptSize(corrective); err != nil {
		return RunAgentResult{}, fmt.Errorf("output_schema: soft fallback corrective retry: %w", err)
	}
	argv := replacePromptArg(prevArgv, corrective)
	ref.Argv = append([]string{d.PaseoBin}, argv...)

	res, err := d.backend().RunAgent(ctx, RunAgentOptions{Args: argv, Cwd: cwd})
	if err != nil {
		return res, err
	}
	if res.AgentID != "" {
		ref.AgentID = res.AgentID
	}
	if out, ok := d.takeDeliveredOutput(req.DispatchID); ok {
		res.Output = marshalCanonical(out)
		return res, nil
	}
	obj, ok := d.captureSchemaAnswer(ctx, res, schema)
	if !ok {
		return res, fmt.Errorf("output_schema: soft fallback: response is not valid JSON matching the schema after one corrective retry")
	}
	res.Output = marshalCanonical(obj)
	return res, nil
}

// allBalancedObjects returns every top-level balanced {...} substring of s, in
// order — used to sift an agent's `paseo logs` tail, which interleaves the
// echoed prompt (carrying the schema document) with the agent's answer.
func allBalancedObjects(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '{')
		if start < 0 {
			return out
		}
		depth, inStr, esc, end := 0, false, false, -1
		for i := start; i < len(s); i++ {
			c := s[i]
			if inStr {
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			return out // unterminated trailing brace — nothing more to find
		}
		out = append(out, s[start:end+1])
		s = s[end+1:]
	}
}

// extractSchemaMatch returns the LAST object in text that parses AND validates
// against schema. Scanning last-first past the echoed schema document (which is
// not an instance of itself) lands on the agent's actual answer.
func extractSchemaMatch(text string, schema map[string]any) (map[string]any, bool) {
	objs := allBalancedObjects(text)
	for i := len(objs) - 1; i >= 0; i-- {
		obj, ok := tryUnmarshalObject(objs[i])
		if !ok {
			continue
		}
		obj = unwrapEnvelope(obj)
		if validateSchema(schema, obj) == nil {
			return obj, true
		}
	}
	return nil, false
}

// EnforceSchema applies the conductor output_schema contract to a CONTROLLER
// runtime's already-produced answer — the runtime-agnostic sibling of the
// paseo dispatcher's own soft path (the pure extract/validate/canonicalize
// helpers are shared; only the "run a turn" primitive differs per runtime).
//
// If `answer` already carries a schema-validating object, EnforceSchema returns
// that object's canonical JSON. Otherwise it runs ONE corrective turn via
// `retry` — a fresh turn told exactly why the last reply missed — and validates
// that. A second miss is a hard error; there is no unbounded retry loop.
//
// `basePrompt` is the already-rendered prompt (schema directive included) that
// produced `answer`; the corrective turn re-sends it with an appended reason so
// the agent has the full contract in front of it. A nil `retry` disables the
// corrective turn (used where a second turn isn't available), turning a first
// miss straight into the error.
func EnforceSchema(ctx context.Context, schema map[string]any, basePrompt, answer string, retry func(context.Context, string) (string, error)) (string, error) {
	if obj, ok := extractSchemaMatch(answer, schema); ok {
		return marshalCanonical(obj), nil
	}
	if retry == nil {
		return "", fmt.Errorf("output_schema: response is not valid JSON matching the schema")
	}
	corrective := basePrompt + "\n\nYour previous reply was not valid JSON matching the schema. Return ONLY the JSON object."
	text, err := retry(ctx, corrective)
	if err != nil {
		return "", err
	}
	if obj, ok := extractSchemaMatch(text, schema); ok {
		return marshalCanonical(obj), nil
	}
	return "", fmt.Errorf("output_schema: response is not valid JSON matching the schema after one corrective retry")
}

// stripOutputSchemaFlag removes a "--output-schema <value>" pair from argv, if
// present, leaving everything else (including --json) untouched.
func stripOutputSchemaFlag(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--output-schema" && i+1 < len(argv) {
			i++ // also skip its value
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

// replacePromptArg swaps argv[1] (the prompt positional — argv[0] is always
// "run", set by paseo()) for newPrompt, leaving the rest of argv untouched.
func replacePromptArg(argv []string, newPrompt string) []string {
	out := append([]string(nil), argv...)
	if len(out) > 1 {
		out[1] = newPrompt
	}
	return out
}

// schemaDirective is the exact instruction appended to the prompt on the SOFT
// path, carrying the compact schema so the model has something concrete to
// satisfy without any provider-native structured-output support.
func schemaDirective(schema map[string]any) string {
	b, err := json.Marshal(schema)
	if err != nil {
		b = []byte("{}")
	}
	return "\n\nRespond with ONLY a single JSON object matching this JSON Schema. " +
		"No prose, no markdown fences, just the JSON:\n" + string(b)
}

// marshalCanonical serializes obj as flat JSON with no envelope, so
// flow.extractOutputs (which only unwraps a top-level output/result/outputs
// key when it maps to another object) hands the step's outputs the object
// itself, matching what a clean native response would have produced.
func marshalCanonical(obj map[string]any) string {
	b, err := json.Marshal(obj)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ---- tolerant extraction ----------------------------------------------------

var codeFenceRe = regexp.MustCompile("(?s)^```[a-zA-Z0-9_-]*\\s*\\n(.*)\\n```\\s*$")

// tolerantExtractObject pulls a JSON object out of raw agent output that may
// be wrapped in a paseo/flow envelope ({"output": {...}}), fenced in markdown
// (```json ... ```), or surrounded by prose. Used identically by the native
// and soft paths so both are held to the same bar.
func tolerantExtractObject(raw string) (map[string]any, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, false
	}
	if m := codeFenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	if obj, ok := tryUnmarshalObject(s); ok {
		return unwrapEnvelope(obj), true
	}
	if body, ok := firstBalancedObject(s); ok {
		if obj, ok2 := tryUnmarshalObject(body); ok2 {
			return unwrapEnvelope(obj), true
		}
	}
	return nil, false
}

func tryUnmarshalObject(s string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, false
	}
	return m, true
}

// unwrapEnvelope mirrors flow.extractOutputs's wrapper-key unwrap so schema
// validation sees the same object shape the step's outputs eventually will.
func unwrapEnvelope(m map[string]any) map[string]any {
	for _, k := range []string{"output", "result", "outputs"} {
		if inner, ok := m[k].(map[string]any); ok {
			return inner
		}
	}
	return m
}

// firstBalancedObject scans s for the first balanced {...} object (honoring
// quoted strings and escapes), so a JSON object wrapped in prose ("Here you
// go: {...}\nHope that helps!") is still recoverable.
func firstBalancedObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// ---- hand-rolled JSON Schema subset validator -------------------------------
//
// No new dependency (CGO stays off): covers the shapes conductor's own packs
// use — object/string/boolean/number/integer/array types, required[],
// additionalProperties:false, nested properties, and enum. An unrecognized
// keyword is a no-op (forward compatible with a richer schema than we bother
// enforcing), never a spurious validation failure.

// validateSchema checks a decoded JSON value (map[string]any / []any /
// string / float64 / bool / nil) against schema.
func validateSchema(schema map[string]any, value any) error {
	return validateNode(schema, value, "$")
}

func validateNode(schema map[string]any, value any, path string) error {
	if schema == nil {
		return nil
	}
	if enumRaw, ok := schema["enum"]; ok {
		if !enumContains(enumRaw, value) {
			return fmt.Errorf("%s: value %v not in enum", path, value)
		}
	}
	t, _ := schema["type"].(string)
	switch t {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, value)
		}
		return validateObject(schema, obj, path)
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", path, value)
		}
		return validateArray(schema, arr, path)
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string, got %T", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: expected number, got %T", path, value)
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || n != math.Trunc(n) {
			return fmt.Errorf("%s: expected integer, got %v", path, value)
		}
	case "":
		// No declared type: if it happens to be an object, still walk
		// properties/required — a schema commonly omits "type: object" at
		// the root when it's implied by having "properties".
		if obj, ok := value.(map[string]any); ok {
			if _, hasProps := schema["properties"]; hasProps {
				return validateObject(schema, obj, path)
			}
		}
	}
	return nil
}

func validateObject(schema map[string]any, obj map[string]any, path string) error {
	props, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			key, _ := r.(string)
			if key == "" {
				continue
			}
			if _, present := obj[key]; !present {
				return fmt.Errorf("%s: missing required property %q", path, key)
			}
		}
	}
	if addl, ok := schema["additionalProperties"]; ok {
		if allow, isBool := addl.(bool); isBool && !allow {
			for k := range obj {
				if _, known := props[k]; !known {
					return fmt.Errorf("%s: additional property %q not allowed", path, k)
				}
			}
		}
	}
	for k, sub := range props {
		subSchema, ok := sub.(map[string]any)
		if !ok {
			continue
		}
		v, present := obj[k]
		if !present {
			continue // required[] above already caught a missing one
		}
		if err := validateNode(subSchema, v, path+"."+k); err != nil {
			return err
		}
	}
	return nil
}

func validateArray(schema map[string]any, arr []any, path string) error {
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return nil
	}
	for i, v := range arr {
		if err := validateNode(items, v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func enumContains(enumRaw any, value any) bool {
	arr, ok := enumRaw.([]any)
	if !ok {
		return true // not a list we understand — don't spuriously fail
	}
	for _, e := range arr {
		if reflect.DeepEqual(e, value) {
			return true
		}
	}
	return false
}

// ---- verb-delivered output (the done+output contract) -------------------------

// outputSlot is one schema dispatch's rendezvous for a verb-delivered output:
// registered before launch, filled by step.done (DeliverOutput), read after the
// run returns. The slot carries the schema so the verb boundary validates with
// a precise, retryable error instead of the corrective-prompt dance.
type outputSlot struct {
	mu     sync.Mutex
	schema map[string]any
	out    map[string]any
	filled bool
}

func (d *Dispatcher) registerOutputSlot(dispatchID string, schema map[string]any) {
	d.outputSlots.Store(dispatchID, &outputSlot{schema: schema})
}

func (d *Dispatcher) dropOutputSlot(dispatchID string) { d.outputSlots.Delete(dispatchID) }

// DeliverOutput is step.done's output half: validate the object against the
// waiting dispatch's schema and park it for the blocked dispatch to collect.
// found=false means no schema dispatch is waiting under that id (a bare done on
// a non-schema step, or the run already resolved) — not an error; the caller's
// done semantics proceed unchanged. A validation failure IS an error, returned
// to the agent verbatim so it can fix the object and call again.
func (d *Dispatcher) DeliverOutput(dispatchID string, output map[string]any) (found bool, err error) {
	if dispatchID == "" || output == nil {
		return false, nil
	}
	v, ok := d.outputSlots.Load(dispatchID)
	if !ok {
		return false, nil
	}
	slot := v.(*outputSlot)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if verr := validateSchema(slot.schema, output); verr != nil {
		return true, fmt.Errorf("output does not match the required schema: %w — fix the object and call step.done again", verr)
	}
	slot.out = output
	slot.filled = true
	return true, nil
}

// takeDeliveredOutput collects a verb-delivered output ("" dispatch id or no
// slot → none). The slot stays registered until the dispatch's deferred drop,
// so a late corrective path can still read an earlier delivery.
func (d *Dispatcher) takeDeliveredOutput(dispatchID string) (map[string]any, bool) {
	if dispatchID == "" {
		return nil, false
	}
	v, ok := d.outputSlots.Load(dispatchID)
	if !ok {
		return nil, false
	}
	slot := v.(*outputSlot)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if !slot.filled {
		return nil, false
	}
	return slot.out, true
}

// verbSchemaDirective is the delivery instruction when the agent holds CLI
// creds: output and done are ONE atomic final action, validated at the verb
// boundary. It deliberately does NOT ask for the JSON in the chat reply, so it
// cannot conflict with any other guidance about the final message.
func verbSchemaDirective(schema map[string]any) string {
	b, _ := json.Marshal(schema)
	return "\n\n---\nDELIVER YOUR RESULT VIA CONDUCTOR: when your work is complete, run\n" +
		"  conductor call step.done --output '<result>'\n" +
		"where <result> is a JSON object matching EXACTLY this schema:\n" + string(b) + "\n" +
		"The call validates the object and replies with a specific error if it does not match — " +
		"fix the object and run the call again until it is accepted. This one call both delivers " +
		"your result and tells conductor you are finished. After it is accepted, simply end your " +
		"turn; your chat reply itself is not the deliverable."
}
