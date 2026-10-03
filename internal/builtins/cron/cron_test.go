package cron

import (
	"testing"
	"time"
)

// `every:` takes the config duration grammar — a day unit and plain seconds
// included — so a schedule means what the same value means everywhere else.
func TestEveryTakesTheConfigDurationGrammar(t *testing.T) {
	got, problems := schedules(map[string]any{"schedules": map[string]any{
		"weekly":  map[string]any{"every": "7d"},
		"hourly":  map[string]any{"every": 3600},
		"nightly": map[string]any{"cron": "0 3 * * *"},
	}})
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	if got["weekly"].spec != "@every "+(7*24*time.Hour).String() || got["hourly"].spec != "@every 1h0m0s" {
		t.Fatalf("specs: %+v", got)
	}
	if _, problems := schedules(map[string]any{"schedules": map[string]any{"x": map[string]any{"every": "soon"}}}); len(problems) == 0 {
		t.Fatal("a bad duration was accepted")
	}
}
