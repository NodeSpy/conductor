package config

import (
	"testing"
	"time"
)

func TestNotifyWantsRoute(t *testing.T) {
	n := Notify{}
	if !n.WantsRoute(NotifyRoute{}, "escalate") {
		t.Fatal("empty route on: defers to the block policy")
	}
	r := NotifyRoute{On: []string{"escalate", "digest"}}
	if !n.WantsRoute(r, "digest") || n.WantsRoute(r, "dispatch") {
		t.Fatal("route on: restricts events")
	}
}

func TestRetryDefaults(t *testing.T) {
	if (Retry{}).Attempts() != 3 || (Retry{Max: -1}).Attempts() != 0 || (Retry{Max: 5}).Attempts() != 5 {
		t.Fatal("Attempts table")
	}
	if (Retry{}).BackoffDur() != 10*time.Second {
		t.Fatal("Backoff default")
	}
	if (Retry{Backoff: Duration(time.Second)}).BackoffDur() != time.Second {
		t.Fatal("Backoff explicit")
	}
}

func TestMergedControllersAndDefaultRuntime(t *testing.T) {
	c := &Config{
		Controllers: map[string]ControllerConfig{"legacy": {Type: "cli", Tool: "codex"}},
		Runtimes:    map[string]RuntimeConfig{"rt": {Type: "paseo", Bin: "/x", Default: true}},
	}
	merged := c.MergedControllers()
	if len(merged) != 2 || merged["rt"].Bin != "/x" || merged["legacy"].Tool != "codex" {
		t.Fatalf("merged: %+v", merged)
	}
	if c.DefaultRuntimeName() != "rt" {
		t.Fatalf("default runtime: %q", c.DefaultRuntimeName())
	}
	// Falls back to a default:true controller when no runtime claims it.
	c2 := &Config{Controllers: map[string]ControllerConfig{"lc": {Type: "paseo", Default: true}}}
	if c2.DefaultRuntimeName() != "lc" {
		t.Fatalf("controller fallback: %q", c2.DefaultRuntimeName())
	}
	if (&Config{}).DefaultRuntimeName() != "" {
		t.Fatal("no default")
	}
}

func TestActionKindHelpers(t *testing.T) {
	on := true
	off := false
	if !(Action{}).IsEnabled() || !(Action{Enabled: &on}).IsEnabled() || (Action{Enabled: &off}).IsEnabled() {
		t.Fatal("IsEnabled")
	}
	if (Action{}).StuckAfterDur() != 30*time.Minute {
		t.Fatal("StuckAfter default")
	}
	if (Action{StuckAfter: Duration(time.Hour)}).StuckAfterDur() != time.Hour {
		t.Fatal("StuckAfter explicit")
	}
	if (Action{}).PollIntervalDur() != 15*time.Minute {
		t.Fatal("PollInterval default")
	}
	if (StepRetry{}).RetryInterval() != time.Minute || (StepRetry{}).RetryTimeout() != 15*time.Minute {
		t.Fatal("StepRetry defaults")
	}
	if (StepRetry{Interval: Duration(time.Second), Timeout: Duration(time.Minute)}).RetryInterval() != time.Second {
		t.Fatal("StepRetry explicit")
	}
}

func TestExcludeMatches(t *testing.T) {
	e := Exclude{Branches: []string{"release/*"}, Labels: []string{"hold"}, Title: []string{"WIP"}}
	if e.Empty() || !(Exclude{}).Empty() {
		t.Fatal("Empty")
	}
	if !e.Matches("release/1.2", "t", nil) {
		t.Fatal("branch glob")
	}
	if !e.Matches("main", "t", []string{"HOLD"}) {
		t.Fatal("label case-insensitive")
	}
	if !e.Matches("main", "wip: draft", nil) {
		t.Fatal("title substring case-insensitive")
	}
	if e.Matches("main", "ready", []string{"go"}) {
		t.Fatal("no match")
	}
}

func TestActorsHasLogin(t *testing.T) {
	a := Actors{Logins: []string{"Octocat"}}
	if !a.HasLogin("octocat") || a.HasLogin("other") {
		t.Fatal("HasLogin case-insensitive")
	}
}
