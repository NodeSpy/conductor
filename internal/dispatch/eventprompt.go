package dispatch

import (
	"encoding/json"

	"github.com/NodeSpy/conductor/internal/core"
)

// eventPromptExcludedContext are the Trigger.Context keys that are credential or
// internal plumbing channels, never event data — kept out of the event an agent
// sees. Mirrors the credential channels templateData() refuses to redact/expose
// (see dispatch.go): the dispatch tokens, the deprecated secrets/vaults scopes,
// and the github installation id.
var eventPromptExcludedContext = map[string]bool{
	"app_token":       true,
	"gh_token":        true,
	"installation_id": true,
	"secrets":         true,
	"vaults":          true,
}

// EventPrompt is the task handed to a `type: agent` step that sets no prompt of
// its own: the event IS the instruction. It is CONNECTOR-NEUTRAL — assembled
// only from the trigger every source populates — and names no tools or
// connector-specific fields, so the agent reads the event and works out an
// appropriate response with whatever this environment actually provides. An
// explicit step prompt always overrides it.
//
// json.Marshal sorts map keys, so the serialized event (and thus the prompt) is
// deterministic for a given trigger.
func EventPrompt(t core.Trigger) string {
	ev := map[string]any{}
	if t.Source != "" {
		ev["source"] = t.Source
	}
	if t.Kind != "" {
		ev["kind"] = t.Kind
	}
	if t.Variant != "" {
		ev["variant"] = t.Variant
	}
	if t.Title != "" {
		ev["title"] = t.Title
	}
	if tgt := eventTarget(t.Target); len(tgt) > 0 {
		ev["target"] = tgt
	}
	if ctx := eventContext(t.Context); len(ctx) > 0 {
		ev["context"] = ctx
	}
	body, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		// A trigger that can't marshal is pathological; a bare instruction
		// still beats dispatching an empty prompt.
		return "Act on this event."
	}
	return "Act on this event:\n\n" + string(body)
}

// eventTarget is the non-empty subset of a target's fields.
func eventTarget(t core.Target) map[string]any {
	out := map[string]any{}
	if t.Repo != "" {
		out["repo"] = t.Repo
	}
	if t.Owner != "" {
		out["owner"] = t.Owner
	}
	if t.Name != "" {
		out["name"] = t.Name
	}
	if t.PR != 0 {
		out["pr"] = t.PR
	}
	if t.Issue != 0 {
		out["issue"] = t.Issue
	}
	if t.Number != 0 {
		out["number"] = t.Number
	}
	if t.HeadSHA != "" {
		out["head"] = t.HeadSHA
	}
	if t.BaseRef != "" {
		out["base"] = t.BaseRef
	}
	if t.HTMLURL != "" {
		out["url"] = t.HTMLURL
	}
	return out
}

// eventContext is the trigger context minus credential/plumbing keys and empty
// values — the event data an agent may legitimately see.
func eventContext(ctx map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range ctx {
		if eventPromptExcludedContext[k] {
			continue
		}
		if v == nil || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}
