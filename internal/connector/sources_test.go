package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"gopkg.in/yaml.v3"
)

// specOn builds a minimal TriggerSpec for `on: <conn>.<event>`.
func specOn(t *testing.T, y string) config.TriggerSpec {
	t.Helper()
	var s config.TriggerSpec
	if err := yaml.Unmarshal([]byte(y), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// cron
// ---------------------------------------------------------------------------

func TestCronConnector(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  timer:
    use: cron
    schedules:
      tick:    { every: 1h }
      nightly: { cron: "0 4 * * *", run_on_start: true }
`)
	in, ok := reg.Get("timer")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("timer should build enabled, got %+v", in)
	}
	if err := in.Impl.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := in.Impl.DeclaredEvents(); len(got) != 2 || got[0] != "nightly" || got[1] != "tick" {
		t.Fatalf("DeclaredEvents = %v, want [nightly tick]", got)
	}

	// Source: one trigger per schedule lowers cleanly.
	src, err := in.Impl.Source([]CompiledTrigger{
		{Index: 0, Spec: specOn(t, "on: timer.tick\nsteps: [{id: s, type: command, command: [x]}]")},
		{Index: 1, Spec: specOn(t, "on: timer.nightly\nsteps: [{id: s, type: command, command: [x]}]")},
	})
	if err != nil || src == nil {
		t.Fatalf("Source: %v (src=%v)", err, src)
	}
	if err := src.Validate(); err != nil {
		t.Fatalf("lowered cron integration invalid: %v", err)
	}

	// No triggers → no source.
	if src, err := in.Impl.Source(nil); err != nil || src != nil {
		t.Fatalf("empty Source should be nil, got %v, %v", src, err)
	}
	// Unknown schedule names the declared set.
	_, err = in.Impl.Source([]CompiledTrigger{{Spec: specOn(t, "on: timer.hourly")}})
	if err == nil || !strings.Contains(err.Error(), `unknown cron schedule "hourly"`) || !strings.Contains(err.Error(), "nightly, tick") {
		t.Fatalf("unknown schedule: %v", err)
	}
	// A second trigger on one schedule is rejected.
	_, err = in.Impl.Source([]CompiledTrigger{
		{Spec: specOn(t, "on: timer.tick")}, {Spec: specOn(t, "on: timer.tick")},
	})
	if err == nil || !strings.Contains(err.Error(), "one trigger per cron schedule") {
		t.Fatalf("duplicate schedule: %v", err)
	}
	// No verbs.
	if _, err := in.Impl.Invoke(context.Background(), "run", nil); err == nil || !strings.Contains(err.Error(), "no verbs") {
		t.Fatalf("Invoke should refuse: %v", err)
	}
}

// ---------------------------------------------------------------------------
// rss
// ---------------------------------------------------------------------------

func TestRSSConnector(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  news:
    use: rss
    feeds:
      rel:  { url: "https://example.com/releases.atom", interval: 30m }
      blog: { url: "https://example.com/blog.rss" }
`)
	in, ok := reg.Get("news")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("news should build enabled, got %+v", in)
	}
	if err := in.Impl.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := in.Impl.DeclaredEvents(); len(got) != 2 || got[0] != "blog" || got[1] != "rel" {
		t.Fatalf("DeclaredEvents = %v, want [blog rel]", got)
	}

	// Two triggers on one feed coexist (variants), plus one on another feed.
	src, err := in.Impl.Source([]CompiledTrigger{
		{Index: 0, Spec: specOn(t, "on: news.rel")},
		{Index: 1, Spec: specOn(t, "on: news.rel\nname: second")},
		{Index: 2, Spec: specOn(t, "on: news.blog")},
	})
	if err != nil || src == nil {
		t.Fatalf("Source: %v (src=%v)", err, src)
	}
	if err := src.Validate(); err != nil {
		t.Fatalf("lowered rss integration invalid: %v", err)
	}
	_, err = in.Impl.Source([]CompiledTrigger{{Spec: specOn(t, "on: news.nope")}})
	if err == nil || !strings.Contains(err.Error(), `unknown rss feed "nope"`) {
		t.Fatalf("unknown feed: %v", err)
	}
	if _, err := in.Impl.Invoke(context.Background(), "post", nil); err == nil || !strings.Contains(err.Error(), "no verbs") {
		t.Fatalf("Invoke should refuse: %v", err)
	}
}

func TestRSSFilter(t *testing.T) {
	ctx := map[string]any{"item": map[string]any{
		"title": "Go 1.26 released", "summary": "toolchain and runtime updates",
	}}
	cases := []struct {
		name    string
		filters map[string]any
		want    bool
		wantErr string
	}{
		{"no match filter matches all", map[string]any{}, true, ""},
		{"title match, case-insensitive", map[string]any{"match": "go 1\\.26"}, true, ""},
		{"summary match", map[string]any{"match": "runtime"}, true, ""},
		{"no match", map[string]any{"match": "security advisory"}, false, ""},
		{"bad regex errors", map[string]any{"match": "("}, false, "bad regex"},
	}
	for _, c := range cases {
		got, err := rssFilter("rel", c.filters, ctx)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}
