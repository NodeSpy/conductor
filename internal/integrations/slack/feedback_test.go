package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// apiCall records one request a mock Slack server received.
type apiCall struct {
	path string
	body map[string]any
}

// callRecorder collects apiCalls made during a test, safe for concurrent use
// (the httptest.Server handler runs on its own goroutine).
type callRecorder struct {
	mu    sync.Mutex
	calls []apiCall
}

func (r *callRecorder) record(c apiCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *callRecorder) all() []apiCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]apiCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *callRecorder) paths() []string {
	var out []string
	for _, c := range r.all() {
		out = append(out, c.path)
	}
	return out
}

// mockSlack starts an httptest server recording every call, and points the
// package's Slack Web API URL vars at it for the duration of the test (undone
// via t.Cleanup — mirrors the notify package's pushoverURL override).
func mockSlack(t *testing.T) *callRecorder {
	t.Helper()
	rec := &callRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.record(apiCall{path: r.URL.Path, body: body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	origMsg, origEph, origReact := chatPostMessageURL, chatPostEphemeralURL, reactionsAddURL
	chatPostMessageURL = srv.URL + "/chat.postMessage"
	chatPostEphemeralURL = srv.URL + "/chat.postEphemeral"
	reactionsAddURL = srv.URL + "/reactions.add"
	t.Cleanup(func() {
		chatPostMessageURL, chatPostEphemeralURL, reactionsAddURL = origMsg, origEph, origReact
	})
	return rec
}

func cfgWithAck(ack *Feedback) Config {
	return Config{
		AppToken: "xapp-1", BotToken: "xoxb-1",
		Rules: []Rule{
			{On: "app_mention", Ack: ack, Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
			{On: "slash_command", Command: "/conductor", Ack: ack, Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
		},
	}
}

func mentionEvent() json.RawMessage {
	return json.RawMessage(`{"event":{"type":"app_mention","text":"do it","user":"U1","channel":"C1","ts":"1.1"}}`)
}

func TestAckReactOnly(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(&Feedback{React: "eyes"}))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	calls := rec.all()
	if len(calls) != 1 || calls[0].path != "/reactions.add" {
		t.Fatalf("want one reactions.add call, got %+v", calls)
	}
	if calls[0].body["channel"] != "C1" || calls[0].body["timestamp"] != "1.1" || calls[0].body["name"] != "eyes" {
		t.Fatalf("reactions.add body wrong: %+v", calls[0].body)
	}
}

func TestAckSayDefaultsInThread(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(&Feedback{Say: "on it"}))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	calls := rec.all()
	if len(calls) != 1 || calls[0].path != "/chat.postMessage" {
		t.Fatalf("want one chat.postMessage call, got %+v", calls)
	}
	if calls[0].body["text"] != "on it" || calls[0].body["thread_ts"] != "1.1" {
		t.Fatalf("expected threaded reply, got %+v", calls[0].body)
	}
}

func TestAckSayInThreadFalsePostsToChannel(t *testing.T) {
	rec := mockSlack(t)
	f := false
	g := newTest(t, cfgWithAck(&Feedback{Say: "on it", InThread: &f}))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	calls := rec.all()
	if len(calls) != 1 || calls[0].path != "/chat.postMessage" {
		t.Fatalf("want one chat.postMessage call, got %+v", calls)
	}
	if _, hasThread := calls[0].body["thread_ts"]; hasThread {
		t.Fatalf("in_thread:false must not set thread_ts, got %+v", calls[0].body)
	}
}

func TestAckEphemeralTargetsTriggeringUser(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(&Feedback{Say: "just for you", Ephemeral: true}))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	calls := rec.all()
	if len(calls) != 1 || calls[0].path != "/chat.postEphemeral" {
		t.Fatalf("want one chat.postEphemeral call, got %+v", calls)
	}
	if calls[0].body["user"] != "U1" || calls[0].body["channel"] != "C1" || calls[0].body["text"] != "just for you" {
		t.Fatalf("chat.postEphemeral body wrong: %+v", calls[0].body)
	}
}

func TestAckReactAndSayTogether(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(&Feedback{React: "eyes", Say: "on it"}))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	if paths := rec.paths(); len(paths) != 2 {
		t.Fatalf("want react + say, got %+v", paths)
	}
}

func TestAckOmittedIsSilent(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(nil))
	emit, _ := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())

	if calls := rec.all(); len(calls) != 0 {
		t.Fatalf("omitted ack must be silent, got %+v", calls)
	}
}

// TestSlashCommandReactIsSafeNoOp proves a slash command (no message to react
// to) doesn't crash and skips the reaction, while `say` still posts.
func TestSlashCommandReactIsSafeNoOp(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithAck(&Feedback{React: "eyes", Say: "on it"}))
	emit, _ := collect()
	g.handleSlash(context.Background(), emit, json.RawMessage(
		`{"command":"/conductor","text":"deploy","channel_id":"C1","user_id":"U1"}`))

	calls := rec.all()
	if len(calls) != 1 || calls[0].path != "/chat.postMessage" {
		t.Fatalf("want only the say call (react is a no-op with no message), got %+v", calls)
	}
}

func cfgWithDoneFail(onDone, onFail *Feedback) Config {
	return Config{
		AppToken: "xapp-1", BotToken: "xoxb-1",
		Rules: []Rule{
			{On: "app_mention", OnDone: onDone, OnFail: onFail, Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
		},
	}
}

func TestOnDoneFiresOnSuccessOutcomes(t *testing.T) {
	for _, outcome := range []string{"ok", "adopted", "queued"} {
		t.Run(outcome, func(t *testing.T) {
			rec := mockSlack(t)
			g := newTest(t, cfgWithDoneFail(&Feedback{React: "white_check_mark"}, &Feedback{React: "x"}))
			emit, got := collect()
			g.handleEvent(context.Background(), emit, mentionEvent())
			if len(*got) != 1 {
				t.Fatalf("want 1 trigger, got %d", len(*got))
			}
			g.HandleCompletion((*got)[0], outcome)

			calls := rec.all()
			if len(calls) != 1 || calls[0].path != "/reactions.add" || calls[0].body["name"] != "white_check_mark" {
				t.Fatalf("want on_done reaction for outcome %q, got %+v", outcome, calls)
			}
		})
	}
}

func TestOnFailFiresOnFailedOutcome(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, cfgWithDoneFail(&Feedback{React: "white_check_mark"}, &Feedback{React: "x", Say: "couldn't finish"}))
	emit, got := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())
	g.HandleCompletion((*got)[0], "failed")

	calls := rec.all()
	if len(calls) != 2 {
		t.Fatalf("want on_fail react + say, got %+v", calls)
	}
	var sawReact, sawSay bool
	for _, c := range calls {
		if c.path == "/reactions.add" && c.body["name"] == "x" {
			sawReact = true
		}
		if c.path == "/chat.postMessage" && c.body["text"] == "couldn't finish" {
			sawSay = true
		}
	}
	if !sawReact || !sawSay {
		t.Fatalf("missing expected on_fail calls: %+v", calls)
	}
}

// TestCompletionAggregatesAllVariants proves feedback fires exactly once per
// Slack event, after every action variant that event dispatched has reported,
// and that a single failure among several variants routes to on_fail.
func TestCompletionAggregatesAllVariants(t *testing.T) {
	rec := mockSlack(t)
	cfg := Config{
		AppToken: "xapp-1", BotToken: "xoxb-1",
		Rules: []Rule{{
			On:     "app_mention",
			OnDone: &Feedback{React: "white_check_mark"},
			OnFail: &Feedback{React: "x"},
			Actions: config.ActionSet{
				{Type: "agent", Name: "one", Agent: "fixer"},
				{Type: "agent", Name: "two", Agent: "fixer"},
			},
		}},
	}
	g := newTest(t, cfg)
	emit, got := collect()
	g.handleEvent(context.Background(), emit, mentionEvent())
	if len(*got) != 2 {
		t.Fatalf("want 2 triggers (one per variant), got %d", len(*got))
	}

	// First variant reports success; feedback must not fire yet.
	g.HandleCompletion((*got)[0], "ok")
	if calls := rec.all(); len(calls) != 0 {
		t.Fatalf("feedback fired before every variant reported: %+v", calls)
	}

	// Second variant fails; the event as a whole is now "any failed" -> on_fail.
	g.HandleCompletion((*got)[1], "failed")
	calls := rec.all()
	if len(calls) != 1 || calls[0].body["name"] != "x" {
		t.Fatalf("want on_fail after aggregation, got %+v", calls)
	}
}

// TestCompletionUnknownDedupIsNoOp proves a completion for a trigger with no
// on_done/on_fail configured (nothing stashed) never crashes or posts.
func TestCompletionUnknownDedupIsNoOp(t *testing.T) {
	rec := mockSlack(t)
	g := newTest(t, baseCfg())
	g.HandleCompletion(core.Trigger{Source: "slack", Instance: "test", Dedup: "nope"}, "ok")
	if calls := rec.all(); len(calls) != 0 {
		t.Fatalf("unknown dedup must be a no-op, got %+v", calls)
	}
}

func TestValidateFeedback(t *testing.T) {
	valid := func(f *Feedback) Config {
		return Config{AppToken: "x", Rules: []Rule{{On: "app_mention", Ack: f, Actions: config.ActionSet{{Type: "agent"}}}}}
	}
	if err := newTest(t, valid(&Feedback{})).Validate(); err == nil {
		t.Fatal("an empty feedback block (neither react nor say) should fail validate")
	}
	if err := newTest(t, valid(&Feedback{Ephemeral: true})).Validate(); err == nil {
		t.Fatal("ephemeral with no say should fail validate")
	}
	if err := newTest(t, valid(&Feedback{React: "eyes"})).Validate(); err != nil {
		t.Fatalf("react-only feedback should be valid: %v", err)
	}
	if err := newTest(t, valid(&Feedback{Say: "hi", Ephemeral: true})).Validate(); err != nil {
		t.Fatalf("ephemeral with say should be valid: %v", err)
	}
	if err := newTest(t, valid(nil)).Validate(); err != nil {
		t.Fatalf("omitted feedback should be valid: %v", err)
	}
}
