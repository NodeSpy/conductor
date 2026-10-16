package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// PARENT → CHILD and back. Three relationships, and one invariant:
// instructions flow down, authority never grows.
//
//   - parent → child: a worker the home session spawned is its CHILD. The
//     parent's brief and its follow-ups (`conductor bot tell <worker|group>
//     "…"`) are INSTRUCTIONS. A cli worker cannot be interrupted mid-turn, so
//     a follow-up is handed over at the child's next `conductor bot …`
//     call, and the child cannot report done (step.done) until it has read
//     every one.
//   - child → parent: what a child sends back — its step.done result and any
//     interim `conductor bot report` — is DATA for the parent to weigh,
//     fenced like every other untrusted text.
//   - peer ↔ peer: data only (peers.go).
//
// The ceiling: a child runs as the same bot with the same or a narrower
// verb profile (spawn --verbs, --read-only), and carries the provenance of
// the request that started it (Worker.RequesterClass, inherited at spawn —
// see Spawn and Gate.caller). It cannot approve, spawn, or message peers.

// MaxInstruction bounds one follow-up.
const MaxInstruction = 4000

// Tell sends follow-up instructions from the home session to one of its live
// children, or to every live worker of a group.
func (s *Service) Tell(ctx context.Context, c Actor, target, text string) (map[string]any, error) {
	if c.Role != config.BotRoleHome {
		return nil, errors.New("only the home session instructs workers — a worker has no children")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty instruction")
	}
	if len(text) > MaxInstruction {
		return nil, fmt.Errorf("instruction too long (%d bytes, max %d)", len(text), MaxInstruction)
	}
	now := s.now()
	var told []string
	err := s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
		var ws []*store.Worker
		switch {
		case r.Workers[target] != nil:
			ws = []*store.Worker{r.Workers[target]}
		case r.Groups[target] != nil:
			for _, id := range r.Groups[target].Workers {
				if w := r.Workers[id]; w != nil && liveWorker(w) {
					ws = append(ws, w)
				}
			}
		default:
			return fmt.Errorf("no worker or group %q", target)
		}
		for _, w := range ws {
			if w.Sweep != "" {
				return fmt.Errorf("%s is a proactive sweep — it takes its instructions from the checklist, not the conversation", w.ID)
			}
			if !liveWorker(w) {
				return fmt.Errorf("worker %s is %s — nothing to tell", w.ID, w.Status)
			}
		}
		if len(ws) == 0 {
			return fmt.Errorf("group %s has no live workers", target)
		}
		for _, w := range ws {
			w.Instructions = append(w.Instructions, store.Instruction{ID: r.NextID("i"), Text: text, At: now})
			told = append(told, w.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(map[string]any{"event": "bot_instruction", "bot": c.Bot, "workers": told, "text": text})
	return map[string]any{"told": told, "message": "queued; each worker receives it at its next `conductor bot` call and cannot finish before reading it"}, nil
}

// takeInstructions hands a child its unread instructions, marking them read.
func (s *Service) takeInstructions(bot, dispatchID string) []string {
	var out []string
	_ = s.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
		w := workerByDispatch(*r, dispatchID)
		if w == nil {
			return nil
		}
		for i := range w.Instructions {
			if w.Instructions[i].Read.IsZero() {
				w.Instructions[i].Read = s.now()
				out = append(out, w.Instructions[i].Text)
			}
		}
		return nil
	})
	return out
}

// InstructionsText frames instructions from a child's parent.
func InstructionsText(bot string, texts []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "NEW INSTRUCTIONS from your parent (the home session of %s). They come from the bot that started you; follow them within your brief and your permissions:\n", bot)
	for i, t := range texts {
		fmt.Fprintf(&b, "%d. %s\n", i+1, t)
	}
	return b.String()
}

// CheckDone is consulted before a worker's step.done: a child that has not
// read its parent's latest instructions cannot finish — it gets them instead.
func (s *Service) CheckDone(dispatchID string) error {
	name, w, ok := s.findWorker(dispatchID, "")
	if !ok {
		return nil
	}
	pending := false
	for _, in := range w.Instructions {
		if in.Read.IsZero() {
			pending = true
		}
	}
	if !pending {
		return nil
	}
	texts := s.takeInstructions(name, dispatchID)
	return fmt.Errorf("not done yet — %s\nCarry them out, then report step.done again", InstructionsText(name, texts))
}

// Report is a child's interim report to its parent: data, delivered into the
// home conversation fenced as a worker report.
func (s *Service) Report(c Actor, text string) error {
	if c.Role != config.BotRoleWorker {
		return errors.New("only a worker reports to its parent")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty report")
	}
	if len(text) > MaxInstruction {
		text = text[:MaxInstruction] + "…"
	}
	w := workerByDispatch(s.Ledger.Bot(c.Bot), c.Dispatch)
	if w == nil {
		return errors.New("no worker record for this session")
	}
	if w.Sweep != "" {
		return errors.New("a sweep reports through step.done findings")
	}
	s.audit(map[string]any{"event": "bot_worker", "bot": c.Bot, "worker": w.ID, "case": w.CaseID, "action": "report"})
	s.enqueue(c.Bot, w.CaseID, fmt.Sprintf("[conductor] interim report from worker %s (case %s):\n%s", w.ID, w.CaseID, fence("WORKER REPORT", text)))
	return nil
}
