package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func newTest(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("test", func(v any) error { *(v.(*Config)) = cfg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func collect() (core.EmitFunc, *[]core.Trigger) {
	var got []core.Trigger
	return func(_ context.Context, tr core.Trigger) { got = append(got, tr) }, &got
}

func baseCfg() Config {
	return Config{
		AppToken: "xapp-1", BotToken: "xoxb-1",
		Rules: []Rule{
			{On: "app_mention", Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
			{On: "reaction_added", Reaction: "eyes", Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
			{On: "slash_command", Command: "/conductor", Actions: config.ActionSet{{Type: "agent", Agent: "fixer"}}},
		},
	}
}

func TestAppMentionEmits(t *testing.T) {
	g := newTest(t, baseCfg())
	emit, got := collect()
	raw := json.RawMessage(`{"event":{"type":"app_mention","text":"hey fix RosterStream","user":"U1","channel":"C1","ts":"1.1"}}`)
	g.handleEvent(context.Background(), emit, raw)
	if len(*got) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(*got))
	}
	tr := (*got)[0]
	if tr.Kind != "app_mention" || tr.Title != "hey fix RosterStream" {
		t.Fatalf("unexpected kind/title: %q / %q", tr.Kind, tr.Title)
	}
	if !strings.HasPrefix(tr.Target.Repo, "slack:C1") {
		t.Fatalf("expected synthetic slack channel repo, got %q", tr.Target.Repo)
	}
	if tr.Action.(config.Action).Checkout != "none" {
		t.Fatal("slack triggers should force checkout none")
	}
	if tr.Context["slack_bot_token"] != "xoxb-1" {
		t.Fatal("bot token should be exposed to actions via context")
	}
	s := tr.Context["slack"].(map[string]any)
	if s["channel"] != "C1" || s["user"] != "U1" {
		t.Fatalf("slack context wrong: %+v", s)
	}
}

func TestReactionFilter(t *testing.T) {
	g := newTest(t, baseCfg())
	emit, got := collect()
	// Wrong emoji → no fire.
	g.handleEvent(context.Background(), emit, json.RawMessage(
		`{"event":{"type":"reaction_added","reaction":"tada","user":"U1","item":{"channel":"C1","ts":"2.2"}}}`))
	if len(*got) != 0 {
		t.Fatalf("non-matching reaction should not fire, got %d", len(*got))
	}
	// Right emoji → fire.
	g.handleEvent(context.Background(), emit, json.RawMessage(
		`{"event":{"type":"reaction_added","reaction":"eyes","user":"U1","item":{"channel":"C1","ts":"3.3"}}}`))
	if len(*got) != 1 || (*got)[0].Kind != "reaction_added" {
		t.Fatalf("matching reaction should fire once, got %+v", *got)
	}
}

func TestSlashCommandFilter(t *testing.T) {
	g := newTest(t, baseCfg())
	emit, got := collect()
	g.handleSlash(context.Background(), emit, json.RawMessage(
		`{"command":"/other","text":"x","channel_id":"C1","user_id":"U1"}`))
	if len(*got) != 0 {
		t.Fatalf("non-matching command should not fire, got %d", len(*got))
	}
	g.handleSlash(context.Background(), emit, json.RawMessage(
		`{"command":"/conductor","text":"deploy please","channel_id":"C1","user_id":"U1"}`))
	if len(*got) != 1 || (*got)[0].Kind != "slash_command" {
		t.Fatalf("matching command should fire, got %+v", *got)
	}
}

func TestDedupRedelivery(t *testing.T) {
	g := newTest(t, baseCfg())
	emit, got := collect()
	raw := json.RawMessage(`{"event":{"type":"app_mention","text":"hi","user":"U1","channel":"C1","ts":"9.9"}}`)
	g.handleEvent(context.Background(), emit, raw)
	g.handleEvent(context.Background(), emit, raw) // redelivery of the same ts
	if len(*got) != 1 {
		t.Fatalf("redelivered event should emit once, got %d", len(*got))
	}
}

func TestValidate(t *testing.T) {
	if err := newTest(t, Config{Rules: []Rule{{On: "app_mention", Actions: config.ActionSet{{Type: "agent"}}}}}).Validate(); err == nil {
		t.Fatal("missing app_token should fail validate")
	}
	if err := newTest(t, Config{AppToken: "x", Rules: []Rule{{On: "bogus", Actions: config.ActionSet{{Type: "agent"}}}}}).Validate(); err == nil {
		t.Fatal("bogus `on` should fail validate")
	}
	if err := newTest(t, baseCfg()).Validate(); err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
}
