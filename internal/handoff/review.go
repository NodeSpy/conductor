package handoff

import (
	"context"
	"strings"

	"github.com/NodeSpy/conductor/internal/controller"
)

// submitPrompt is the follow-up turn sent to the agent when you approve a draft:
// finalize and post it as-is.
const submitPrompt = "The reviewer approved this draft as-is — submit/post it now without further changes."

// Review runs the interactive review hand-off loop over a live session, entirely
// controller-agnostic (it touches only controller.Session and the Channel):
//
//	present the draft → await your call →
//	   approve : tell the session to submit, return
//	   revise  : send your text back as the next turn; its output is the new draft; loop
//	   discard : cancel the session, return
//
// The draft's Body is refreshed from each revision turn's streamed output, so you
// iterate with the agent through the same channel until you approve or discard.
// notify (may be nil) is called with each presentation's Ref so the caller can
// tell you where the draft is waiting. Returns the terminal Decision.
func Review(ctx context.Context, sess controller.Session, ch Channel, draft Draft, notify func(ref string)) (Decision, error) {
	for {
		pres, err := ch.Present(ctx, draft)
		if err != nil {
			return Decision{}, err
		}
		if notify != nil {
			notify(pres.Ref())
		}
		dec, err := pres.Await(ctx)
		pres.Close()
		if err != nil {
			return Decision{}, err
		}
		switch dec.Action {
		case ActionApprove:
			if _, err := promptTurn(ctx, sess, submitPrompt); err != nil {
				return dec, err
			}
			return dec, nil
		case ActionDiscard:
			_ = sess.Cancel(ctx)
			return dec, nil
		default: // ActionRevise
			body, err := promptTurn(ctx, sess, dec.Text)
			if err != nil {
				return dec, err
			}
			if strings.TrimSpace(body) != "" {
				draft.Body = body
			}
		}
	}
}

// promptTurn sends one turn to the session and returns its concatenated streamed
// output (a native controller emits a single terminal update). A streamed error
// ends the turn.
func promptTurn(ctx context.Context, sess controller.Session, text string) (string, error) {
	ch, err := sess.Prompt(ctx, controller.Message{Text: text})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for up := range ch {
		if up.Output != "" {
			b.WriteString(up.Output)
		}
		if up.Err != nil {
			return b.String(), up.Err
		}
	}
	return b.String(), nil
}
