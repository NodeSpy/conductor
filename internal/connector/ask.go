package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/handoff"
)

// AskChanneler is implemented by connector types that can present an
// interactive hand-off (slack/discord/web): they build a handoff.Channel from
// options — typically the connector's default options — so a background agent
// step's `handoff: <connector>` review rides the same machinery as the
// connector's own ask verb.
type AskChanneler interface {
	AskChannel(opts map[string]any) (handoff.Channel, error)
}

// askOutputs is the shared output schema of every `ask` verb: what the human
// decided and the text they replied with (the revision, or the approved draft).
func askOutputs() Schema {
	return Schema{
		"action": {Type: TString, Enum: []string{"approve", "revise", "discard"}, Desc: "the human's decision"},
		"text":   {Type: TString, Desc: "their reply text (a revision), or the draft on approve"},
		"ref":    {Type: TString, Desc: "where the question was presented (URL / channel ts)"},
	}
}

// askOptionBase are the option keys every ask verb shares; channel-specific
// keys (to/user/channel) are added per connector type.
func askOptionBase() Schema {
	return Schema{
		"prompt":  {Type: TString, Required: true, Desc: "the question to present"},
		"draft":   {Type: TString, Desc: "editable draft text presented with the question"},
		"title":   {Type: TString, Desc: "presentation title (default: the prompt's first line)"},
		"timeout": {Type: TDuration, Desc: "how long to wait for an answer (default 1h)"},
	}
}

// defaultAskTimeout bounds an unanswered ask so a workflow goroutine can't
// wait forever on a human who never replies.
const defaultAskTimeout = time.Hour

// runAsk presents a draft on a hand-off channel and blocks for the decision —
// the request-response half of the verb surface. The hand-off machinery
// (present → await → decision, tunnels, reply capture) is the existing
// internal/handoff implementation; `ask` is its verb face.
func runAsk(ctx context.Context, ch handoff.Channel, opts map[string]any) (map[string]any, error) {
	prompt, _ := opts["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("ask: options.prompt is required")
	}
	title, _ := opts["title"].(string)
	if title == "" {
		title = firstLine(prompt)
	}
	body, _ := opts["draft"].(string)
	if body == "" {
		body = prompt
	} else if title == firstLine(prompt) && prompt != body {
		// Keep the question visible when the body is a separate draft.
		title = firstLine(prompt)
	}
	timeout := defaultAskTimeout
	if d, err := toDuration(opts["timeout"]); err != nil {
		return nil, fmt.Errorf("ask: options.timeout: %w", err)
	} else if d > 0 {
		timeout = d
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pres, err := ch.Present(ctx, handoff.Draft{Title: title, Body: body})
	if err != nil {
		return nil, fmt.Errorf("ask: present: %w", err)
	}
	defer pres.Close()
	dec, err := pres.Await(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ask: no answer within %s", timeout)
		}
		return nil, fmt.Errorf("ask: %w", err)
	}
	text := dec.Text
	if dec.Action == handoff.ActionApprove && text == "" {
		text = body
	}
	return map[string]any{"action": string(dec.Action), "text": text, "ref": pres.Ref()}, nil
}

// firstLine trims a string to its first non-empty line (for titles).
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			if len(t) > 120 {
				return t[:120] + "…"
			}
			return t
		}
	}
	return s
}
