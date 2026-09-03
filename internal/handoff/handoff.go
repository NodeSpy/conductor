// Package handoff is conductor's portable human↔agent transport: a HandoffChannel
// presents a draft to you, awaits your call, and reports it back as
// approve | revise | discard. It's controller-agnostic — the same channel drives
// an interactive review whether the agent runs under paseo, ACP, or anything the
// controller layer can hold a live Session for — so a runtime that lacks a native
// interactive surface still gets one from conductor.
//
// Two channels ship here: a web-link channel served on conductor's inbound HTTP
// listener (a draft page with approve/revise/discard + a text box), and a Slack
// channel (post the draft to a thread, capture your reply). Both satisfy Channel;
// the review loop (see Review) and the ACP permission/input bridge (see Handler)
// are written against the interface, not either implementation.
package handoff

import (
	"context"
	"strings"
)

// The three terminal calls a human can make on a presented draft.
const (
	ActionApprove = "approve" // submit the draft as-is
	ActionRevise  = "revise"  // send Text back to the agent as the next turn
	ActionDiscard = "discard" // abandon the hand-off; cancel the session
)

// Draft is what gets presented to a human: the proposed content plus enough
// context to make the decision (which PR, what the agent wants to do).
type Draft struct {
	ID      string   // channel-assigned identifier (set by Present when empty)
	Title   string   // one-line summary, e.g. "Review for o/r#12"
	Body    string   // the proposed content (review text, a permission detail, a question)
	PRKey   string   // "owner/name#n" the hand-off belongs to (for logs/threading)
	Repo    string   // owner/name (optional, display only)
	Number  int      // PR/issue number (optional, display only)
	Options []string // extra choices beyond approve/revise/discard (e.g. ACP permission options)
}

// Decision is the human's terminal call on a presented draft.
type Decision struct {
	Action string // ActionApprove | ActionRevise | ActionDiscard
	Text   string // the revised content (revise) or a chosen option / free-form answer
}

// Presentation is a draft shown to a human and awaiting their decision. Present
// returns one immediately (so the caller can surface Ref); Await blocks until the
// human decides or ctx is done. This is the present → await → decision split.
type Presentation interface {
	// Ref is a human-facing pointer to where the draft is waiting (a URL for the
	// web channel, a thread reference for Slack), so the caller can tell you where
	// to look.
	Ref() string
	// Await blocks until the human decides, or ctx is cancelled.
	Await(ctx context.Context) (Decision, error)
	// Close releases the presentation's resources (removes the pending page /
	// stops listening for the reply). Safe to call more than once.
	Close()
}

// Channel presents a draft and yields a Presentation to await the decision on.
type Channel interface {
	Present(ctx context.Context, d Draft) (Presentation, error)
}

// parseReply maps a free-form human reply (a Slack thread message, say) onto a
// Decision: a bare approve/discard keyword is that call; anything else is a
// revision carrying the text (a leading "revise:" prefix is stripped). This is
// what lets a plain chat reply drive the same approve/revise/discard loop the web
// buttons do.
func parseReply(text string) Decision {
	t := strings.TrimSpace(text)
	switch strings.ToLower(t) {
	case "approve", "approved", "lgtm", "ship it", "shipit", "👍", "+1":
		return Decision{Action: ActionApprove}
	case "discard", "cancel", "reject", "no", "stop", "👎", "-1":
		return Decision{Action: ActionDiscard}
	}
	if i := strings.IndexByte(t, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(t[:i]), "revise") {
		t = strings.TrimSpace(t[i+1:])
	}
	return Decision{Action: ActionRevise, Text: t}
}
