package handoff

import (
	"context"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/controller"
)

// Handler bridges an agent's mid-turn permission and input requests (the ACP
// client-side callbacks the controller layer surfaces via controller.Handler)
// onto a HandoffChannel: a permission grant or a question becomes a draft you
// approve/revise/discard, so any controller's agent can ask for a human decision
// through the same portable channel — no runtime-native prompt UI required.
type Handler struct {
	ch     Channel
	notify func(ref string) // optional: told where the draft is waiting (a URL/thread)
}

// NewHandler builds a Handler over a channel. notify (may be nil) is called with
// the presentation Ref so the caller can surface "a decision is waiting here".
func NewHandler(ch Channel, notify func(ref string)) *Handler {
	return &Handler{ch: ch, notify: notify}
}

// Ensure the bridge satisfies the controller-side callback contract.
var _ controller.Handler = (*Handler)(nil)

// present shows a draft and blocks for the decision, surfacing the ref first.
func (h *Handler) present(ctx context.Context, d Draft) (Decision, error) {
	pres, err := h.ch.Present(ctx, d)
	if err != nil {
		return Decision{}, err
	}
	if h.notify != nil {
		h.notify(pres.Ref())
	}
	defer pres.Close()
	return pres.Await(ctx)
}

// RequestPermission presents a gated action for approval. Approve grants it
// (selecting the agent's first allow-option, or a matching option named in the
// reply); revise/discard reject it.
func (h *Handler) RequestPermission(ctx context.Context, req controller.PermissionRequest) (controller.PermissionOutcome, error) {
	title := "Permission: " + req.Tool
	if req.Tool == "" {
		title = "Permission request"
	}
	dec, err := h.present(ctx, Draft{
		Title:   title,
		Body:    req.Detail,
		PRKey:   req.SessionID,
		Options: req.Options,
	})
	if err != nil {
		return controller.PermissionOutcome{}, err
	}
	approved := dec.Action == ActionApprove
	return controller.PermissionOutcome{
		Selected: selectedOption(dec, req.Options, approved),
		Approved: approved,
	}, nil
}

// selectedOption resolves which of the agent's offered options the decision maps
// to: an explicit option named in the reply text wins; otherwise an approval
// takes the first offered option (conventionally the "allow" choice).
func selectedOption(dec Decision, options []string, approved bool) string {
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(dec.Text), o) {
			return o
		}
	}
	if approved && len(options) > 0 {
		return options[0]
	}
	return ""
}

// RequestInput presents a free-form question. The reply text is the answer;
// discard cancels the request.
func (h *Handler) RequestInput(ctx context.Context, req controller.InputRequest) (controller.InputOutcome, error) {
	dec, err := h.present(ctx, Draft{
		Title: "Input requested",
		Body:  req.Prompt,
		PRKey: req.SessionID,
	})
	if err != nil {
		return controller.InputOutcome{}, err
	}
	if dec.Action == ActionDiscard {
		return controller.InputOutcome{Cancelled: true}, nil
	}
	return controller.InputOutcome{Text: dec.Text}, nil
}
