package config

import (
	"testing"
	"time"
)

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
		Runtimes: map[string]RuntimeConfig{"rt": {Use: "paseo", Bin: "/x", Default: true}},
	}
	merged := c.MergedControllers()
	if len(merged) != 1 || merged["rt"].Bin != "/x" {
		t.Fatalf("merged: %+v", merged)
	}
	if c.DefaultRuntimeName() != "rt" {
		t.Fatalf("default runtime: %q", c.DefaultRuntimeName())
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
