package engine

import (
	"context"
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/core/coretest"
)

// The fixture chat connector stands in for a real chat plugin (e.g. the
// official slack plugin): its declared app_mention event carries the
// option_hooks semantic (plugin-contract.md §2.2) that restores a trigger's
// own ack/on_done/on_fail feedback generically, through the ordinary flow
// hook machinery — no connector-specific completion hook in the engine.
func init() { connector.RegisterInProcessConnector(coretest.Chat) }

const feedbackCfg = `
connectors:
  chat: { use: slack, bot_token: x }
triggers:
  - on: chat.app_mention
    options:
      ack: { react: "eyes" }
      on_done: { react: "white_check_mark" }
      on_fail: { react: "x" }
    steps:
      - { id: p, uses: chat.post, options: { channel: "C1", text: "ok" } }
  - on: chat.reaction_added
    steps:
      - { id: p, uses: chat.post, options: { channel: "C1", text: "ok" } }
`

// feedbackTrigger builds a trigger for kind on the chat fixture connector,
// with the message facts every option_hooks Args template in the fixture
// decl reads (channel/ts/user/thread_ts).
func feedbackTrigger(kind, dedup, flowRef string) core.Trigger {
	return core.Trigger{
		Source: "slack", Instance: "chat", Kind: kind,
		Target: core.Target{Repo: "", Number: 0},
		Title:  "mention",
		Dedup:  dedup,
		Context: map[string]any{
			"slack": map[string]any{"channel": "C1", "ts": "100.1", "user": "U1", "thread_ts": "100.1"},
		},
		Action: config.Action{FlowRef: flowRef},
		// The fixture's app_mention event declares target.assigned: true
		// (testdata/chat-decl.json) — Slack told us the channel/user
		// directly, exactly what TargetTrusted means (core/event.go). The
		// real pluginsource.go lowering sets this from that declaration;
		// this hand-built trigger must say so itself, or
		// connector.OptionHooks' assigned-target-only defense (§2.2) skips
		// every option_hooks entry here as if the target were
		// sender-controlled, which it is not.
		TargetTrusted: true,
	}
}

// verbOutputs returns the right-shaped output for the chat fixture's verbs,
// so the generic strict output-schema check (ValidateSchema) never trips on
// an extra key a one-size-fits-all mock would otherwise return.
func verbOutputs(verb string) map[string]any {
	if verb == "post" {
		return map[string]any{"ts": "1", "channel": "C1"}
	}
	return map[string]any{"ok": true}
}

// feedbackCalls filters coretest.Chat's recorded calls to just the "feedback"
// verb invocations (ack/on_done/on_fail), in order.
func feedbackCalls() []sdk.InvokeRequest {
	var out []sdk.InvokeRequest
	for _, c := range coretest.Chat.Calls() {
		if c.Verb == "feedback" {
			out = append(out, c)
		}
	}
	return out
}

func TestOptionHooksAckFiresAtStart(t *testing.T) {
	coretest.Chat.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		return sdk.InvokeResult{Outputs: verbOutputs(req.Verb)}, nil
	})
	t.Cleanup(func() { coretest.Chat.Respond(nil) })

	eng, _, _, _ := buildFlowEngine(t, feedbackCfg)
	eng.process(context.Background(), feedbackTrigger("app_mention", "d1", "0:chat.app_mention"))

	waitCond(t, "ack feedback call", func() bool { return len(feedbackCalls()) >= 1 })
	calls := feedbackCalls()
	if calls[0].Options["react"] != "eyes" {
		t.Fatalf("ack: want react=eyes, got %v", calls[0].Options)
	}
	if calls[0].Options["channel"] != "C1" || calls[0].Options["ts"] != "100.1" {
		t.Fatalf("ack: the event's own message coordinates weren't carried: %v", calls[0].Options)
	}

	waitCond(t, "the on_done feedback call", func() bool { return len(feedbackCalls()) >= 2 })
	if got := feedbackCalls()[1].Options["react"]; got != "white_check_mark" {
		t.Fatalf("on_done: want react=white_check_mark, got %v", got)
	}
}

func TestOptionHooksOnFailFiresAcrossSteps(t *testing.T) {
	// A two-step workflow where the SECOND step fails: the fail hook must
	// still fire exactly once, after both steps have had their chance — the
	// same join an explicit `hooks: [{at: fail, ...}]` already uses, now
	// reached through the synthesized option_hooks entry instead of a
	// connector-specific pending-outcome map.
	cfg := `
connectors:
  chat: { use: slack, bot_token: x }
triggers:
  - on: chat.app_mention
    options:
      on_done: { react: "white_check_mark" }
      on_fail: { react: "x" }
    steps:
      - { id: a, uses: chat.post, options: { channel: "C1", text: "first" } }
      - { id: b, uses: chat.post, options: { channel: "C1", text: "second" } }
`
	calls := 0
	coretest.Chat.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		if req.Verb == "post" {
			calls++
			if calls == 2 {
				return sdk.InvokeResult{}, sdk.Fail(sdk.CodeUpstream, "boom", map[string]any{"retryable": false})
			}
		}
		return sdk.InvokeResult{Outputs: verbOutputs(req.Verb)}, nil
	})
	t.Cleanup(func() { coretest.Chat.Respond(nil) })

	eng, st, _, _ := buildFlowEngine(t, cfg)
	eng.process(context.Background(), feedbackTrigger("app_mention", "d2", "0:chat.app_mention"))

	waitCond(t, "the on_fail feedback call", func() bool { return len(feedbackCalls()) >= 1 })
	if got := feedbackCalls()[0].Options["react"]; got != "x" {
		t.Fatalf("want on_fail (react=x) after the second step failed, got %v", got)
	}
	if len(feedbackCalls()) != 1 {
		t.Fatalf("want exactly one feedback call (on_fail only, no on_done), got %d", len(feedbackCalls()))
	}
	st.mu.Lock()
	var failed bool
	for _, a := range st.audits {
		if a["event"] == "workflow_failed" {
			failed = true
		}
	}
	st.mu.Unlock()
	if !failed {
		t.Fatal("want the workflow recorded as failed")
	}
}

func TestOptionHooksAbsentOptionsFireNothing(t *testing.T) {
	// chat.reaction_added's trigger sets no options at all: the event
	// declares option_hooks, but nothing is wired up without an operator
	// opting in per trigger.
	coretest.Chat.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		return sdk.InvokeResult{Outputs: verbOutputs(req.Verb)}, nil
	})
	t.Cleanup(func() { coretest.Chat.Respond(nil) })

	eng, _, _, _ := buildFlowEngine(t, feedbackCfg)
	eng.process(context.Background(), feedbackTrigger("reaction_added", "d3", "1:chat.reaction_added"))

	waitCond(t, "the post call", func() bool {
		for _, c := range coretest.Chat.Calls() {
			if c.Verb == "post" {
				return true
			}
		}
		return false
	})
	if n := len(feedbackCalls()); n != 0 {
		t.Fatalf("want no feedback calls when options are unset, got %d", n)
	}
}

func TestOptionHooksFailingFeedbackVerbDoesNotFailTheRun(t *testing.T) {
	coretest.Chat.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		if req.Verb == "feedback" {
			return sdk.InvokeResult{}, sdk.Fail(sdk.CodeUpstream, "feedback upstream boom", map[string]any{"retryable": false})
		}
		return sdk.InvokeResult{Outputs: verbOutputs(req.Verb)}, nil
	})
	t.Cleanup(func() { coretest.Chat.Respond(nil) })

	eng, st, _, _ := buildFlowEngine(t, feedbackCfg)
	eng.process(context.Background(), feedbackTrigger("app_mention", "d4", "0:chat.app_mention"))

	waitCond(t, "the post call despite a failing ack hook", func() bool {
		for _, c := range coretest.Chat.Calls() {
			if c.Verb == "post" {
				return true
			}
		}
		return false
	})
	// Give the done-phase feedback (also failing) a moment to run.
	waitCond(t, "the on_done feedback attempt", func() bool { return len(feedbackCalls()) >= 2 })

	st.mu.Lock()
	var failed bool
	for _, a := range st.audits {
		if a["event"] == "workflow_failed" {
			failed = true
		}
	}
	st.mu.Unlock()
	if failed {
		t.Fatal("a failing feedback verb must not fail the run (hooks are best-effort)")
	}
}
