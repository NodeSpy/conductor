package connector

import (
	"strings"
	"testing"
	"time"
)

// TestScalarCoercions: the option-coercion helpers behind verb calls.
func TestScalarCoercions(t *testing.T) {
	if toInt(3) != 3 || toInt(int64(4)) != 4 || toInt(uint64(9)) != 9 || toInt(5.0) != 5 || toInt("6") != 0 || toInt(nil) != 0 {
		t.Fatal("toInt table")
	}
	if got := toStrings([]any{"a", 1, "b"}); len(got) != 3 || got[1] != "1" {
		t.Fatalf("toStrings []any coerces non-strings: %v", got)
	}
	if got := toStrings([]string{"x"}); len(got) != 1 {
		t.Fatalf("toStrings []string: %v", got)
	}
	if got := toStrings("solo"); len(got) != 1 || got[0] != "solo" {
		t.Fatalf("toStrings scalar: %v", got)
	}
	if toStrings(7) != nil {
		t.Fatal("toStrings other")
	}
	if !truthy(true) || truthy(false) || !truthy("yes") || truthy("") || truthy(nil) {
		t.Fatal("truthy table")
	}
	if d, err := toDuration("5m"); err != nil || d != 5*time.Minute {
		t.Fatalf("toDuration string: %v %v", d, err)
	}
	if d, err := toDuration(nil); err != nil || d != 0 {
		t.Fatalf("toDuration nil: %v %v", d, err)
	}
	if _, err := toDuration("bogus"); err == nil {
		t.Fatal("bad duration must error")
	}
	if d, err := toDuration(30); err != nil || d != 30*time.Second {
		t.Fatalf("toDuration number: %v %v", d, err)
	}
}

// TestRegistryNamesAndDeclHelpers: Names ordering, EventNames/ContextKeys.
func TestRegistryNamesAndDeclHelpers(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  b-conn: { type: command }
  a-conn: { type: cron, schedules: { tick: { every: 1h } } }
`)
	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("names: %v", names)
	}
	in, _ := reg.Get("b-conn")
	if evs := in.Decl.EventNames(); len(evs) != 0 {
		t.Fatalf("command has no events: %v", evs)
	}
	gh, ok := TypeDeclFor("github")
	if !ok {
		t.Fatal("github type decl")
	}
	if evs := gh.EventNames(); len(evs) == 0 || evs[0] > evs[len(evs)-1] {
		t.Fatalf("github event names sorted: %v", evs)
	}
	ev, _ := gh.Event("new_comment")
	keys := ev.Context.ContextKeys()
	if len(keys) == 0 || !keys["author_is_bot"] {
		t.Fatalf("context keys: %v", keys)
	}
}

// TestValidateCallOptions: unknown keys, missing required (unless a connector
// default supplies it), enum enforcement.
func TestValidateCallOptions(t *testing.T) {
	opts := Schema{
		"text": {Type: TString, Required: true},
		"as":   {Type: TString, Enum: []string{"me", "bot"}},
	}
	if err := ValidateCallOptions("w", opts, map[string]any{"text": "hi"}, nil); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := ValidateCallOptions("w", opts, map[string]any{"text": "hi", "nope": 1}, nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown key: %v", err)
	}
	if err := ValidateCallOptions("w", opts, map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "text") {
		t.Fatalf("missing required: %v", err)
	}
	// A connector default satisfies the requirement.
	if err := ValidateCallOptions("w", opts, map[string]any{}, map[string]any{"text": "default"}); err != nil {
		t.Fatalf("default satisfies required: %v", err)
	}
	if err := ValidateCallOptions("w", opts, map[string]any{"text": "t", "as": "ghost"}, nil); err == nil || !strings.Contains(err.Error(), "me|bot") {
		t.Fatalf("enum: %v", err)
	}
}

// TestVerbOnlyDeclaredEventsAndSources: verb-only connectors expose no events
// and build no source integrations.
func TestVerbOnlyDeclaredEventsAndSources(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  box: { type: command }
  alerts: { type: ntfy, topic: t }
  pager: { type: pushover, token: x, user: u }
  relay: { type: notifiarr, api_key: k }
  disc: { type: discord, bot_token: b }
`)
	for _, name := range []string{"box", "alerts", "pager", "relay", "disc"} {
		in, ok := reg.Get(name)
		if !ok || in.DisabledReason != "" {
			t.Fatalf("%s: %+v", name, in)
		}
		if evs := in.Impl.DeclaredEvents(); evs != nil {
			t.Fatalf("%s declared events: %v", name, evs)
		}
		src, err := in.Impl.Source(nil)
		if err != nil || src != nil {
			t.Fatalf("%s source: %v %v", name, src, err)
		}
	}
	// Discord duck-typed wiring surfaces.
	in, _ := reg.Get("disc")
	if dc, ok := in.Impl.(interface{ BotToken() string }); !ok || dc.BotToken() != "b" {
		t.Fatal("discord BotToken surface")
	}
}

func TestAskFirstLine(t *testing.T) {
	if got := firstLine("  \n  first real line\nrest"); got != "first real line" {
		t.Fatalf("firstLine: %q", got)
	}
	long := strings.Repeat("y", 130)
	if got := firstLine(long); len(got) <= 120 && !strings.HasSuffix(got, "…") {
		t.Fatalf("long first line truncated: %q", got)
	}
	if got := firstLine("   "); got != "   " {
		t.Fatalf("blank falls through: %q", got)
	}
}
