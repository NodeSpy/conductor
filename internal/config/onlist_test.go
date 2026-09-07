package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// normTriggers unmarshals a triggers-bearing config and runs the Load-time
// normalization, returning the first parse or expansion error (nil on
// success) — malformed on: items reject at decode time.
func normTriggers(t *testing.T, y string) (*Config, error) {
	t.Helper()
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		return &c, err
	}
	return &c, c.NormalizeTriggers()
}

// TestOnListExpansion: a multi-source on: expands into one trigger per source
// with shared steps and per-source filters merged over the shared base
// (per-source keys win).
func TestOnListExpansion(t *testing.T) {
	c, err := normTriggers(t, `
triggers:
  - name: fan-in
    on:
      - timer.nightly
      - gh.issue_matched:
          filters: { labels_any: [billing], state: open }
      - manual
    filters: { state: closed, actor: me }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 3 {
		t.Fatalf("expanded to %d triggers, want 3", len(c.Triggers))
	}
	for i, want := range []string{"timer.nightly", "gh.issue_matched", "manual"} {
		tr := c.Triggers[i]
		if tr.On != want || len(tr.OnSources) != 0 {
			t.Fatalf("triggers[%d]: on=%q sources=%v", i, tr.On, tr.OnSources)
		}
		if tr.Name != "fan-in" || len(tr.Steps) != 1 {
			t.Fatalf("triggers[%d] lost shared config: %+v", i, tr)
		}
	}
	// Bare source: the shared base only.
	if f := c.Triggers[0].Filters; f["state"] != "closed" || f["actor"] != "me" || len(f) != 2 {
		t.Fatalf("bare-source filters: %v", f)
	}
	// Per-source block: its keys override the base, other base keys remain.
	f := c.Triggers[1].Filters
	if f["state"] != "open" || f["actor"] != "me" {
		t.Fatalf("per-source override: %v", f)
	}
	if la, ok := f["labels_any"].([]any); !ok || la[0] != "billing" {
		t.Fatalf("per-source own key: %v", f)
	}
	// The base map itself is untouched by the merge.
	if c.Triggers[0].Filters["state"] != "closed" {
		t.Fatalf("shared base mutated: %v", c.Triggers[0].Filters)
	}
}

// TestOnListPerSourcePolicyAndHooks: a per-source block's policy merges as
// the innermost scope over the trigger's, and its hooks append AFTER the
// shared hooks — on that source's expanded trigger only.
func TestOnListPerSourcePolicyAndHooks(t *testing.T) {
	c, err := normTriggers(t, `
triggers:
  - name: fan-in
    on:
      - timer.nightly
      - gh.new_comment:
          policy: { reply_to_bots: "off" }
          hooks:
            - { at: start, uses: gh.react, options: { emoji: eyes } }
    policy: { reply_to_bots: full, pause_label: hold }
    hooks:
      - { at: done, uses: slack.post, options: { text: done } }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	bare, over := c.Triggers[0], c.Triggers[1]

	// Bare source: the trigger's own policy and hooks, untouched.
	if got := *bare.Policy.ReplyToBots; got != "full" {
		t.Fatalf("bare policy: %s", got)
	}
	if len(bare.Hooks) != 1 || bare.Hooks[0].Uses != "slack.post" {
		t.Fatalf("bare hooks: %+v", bare.Hooks)
	}

	// Per-source: reply_to_bots overridden, non-overridden field kept.
	if got := *over.Policy.ReplyToBots; got != "off" {
		t.Fatalf("per-source policy override: %s", got)
	}
	if over.Policy.PauseLabel == nil || *over.Policy.PauseLabel != "hold" {
		t.Fatalf("trigger policy field lost in merge: %+v", over.Policy)
	}
	// Hooks: shared first, per-source appended second.
	if len(over.Hooks) != 2 || over.Hooks[0].Uses != "slack.post" || over.Hooks[1].Uses != "gh.react" {
		t.Fatalf("per-source hooks append: %+v", over.Hooks)
	}
	// The trigger's own policy/hook values were not mutated by the merge.
	if *bare.Policy.ReplyToBots != "full" || len(bare.Hooks) != 1 {
		t.Fatalf("shared blocks mutated: %+v %+v", bare.Policy, bare.Hooks)
	}
}

// TestOnListScalarUnchanged: the scalar form parses exactly as before.
func TestOnListScalarUnchanged(t *testing.T) {
	c, err := normTriggers(t, `
triggers:
  - on: gh.new_comment
    filters: { author_bot: false }
    steps: [ { uses: svc.post, options: { text: t } } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	tr := c.Triggers[0]
	if tr.On != "gh.new_comment" || len(tr.OnSources) != 0 || tr.Filters["author_bot"] != false {
		t.Fatalf("scalar on: %+v", tr)
	}
}

// TestOnListRejections: malformed lists fail normalization with a reason.
func TestOnListRejections(t *testing.T) {
	cases := []struct{ name, yaml, wantErr string }{
		{"duplicate source", `
triggers:
  - name: x
    on: [gh.new_comment, gh.new_comment]
    steps: [ { uses: svc.post } ]`, `duplicate source "gh.new_comment"`},
		{"empty map item", `
triggers:
  - name: x
    on: [ {} ]
    steps: [ { uses: svc.post } ]`, "one-key map"},
		{"multi-key map item", `
triggers:
  - name: x
    on:
      - gh.new_comment: { filters: { author_bot: false } }
        gh.release: {}
    steps: [ { uses: svc.post } ]`, "one-key map"},
		{"disallowed per-source key", `
triggers:
  - name: x
    on:
      - gh.new_comment: { steps: [ { uses: svc.post } ] }
    steps: [ { uses: svc.post } ]`, `unknown per-source key "steps"`},
		{"per-source value not a block", `
triggers:
  - name: x
    on: [ { gh.new_comment: 5 } ]
    steps: [ { uses: svc.post } ]`, "is a block {filters, policy, hooks}"},
		{"manual needs a name", `
triggers:
  - on: [manual, gh.new_comment]
    steps: [ { uses: svc.post } ]`, "requires a name"},
		{"scalar manual needs a name too", `
triggers:
  - on: manual
    steps: [ { uses: svc.post } ]`, "requires a name"},
		{"manual names must be unique", `
triggers:
  - { name: deploy, on: manual, steps: [ { uses: svc.post } ] }
  - { name: deploy, on: manual, steps: [ { uses: svc.post } ] }`, `manual trigger name "deploy" is not unique`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normTriggers(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestManualValidate: config.Validate accepts `on: manual` without a
// connector, rejects a connector named "manual", and still requires
// <connector>.<event> for everything else.
func TestManualValidate(t *testing.T) {
	base := `
connectors:
  timer: { type: cron, schedules: { nightly: { cron: "0 2 * * *" } } }
`
	var c Config
	if err := yaml.Unmarshal([]byte(base+`
triggers:
  - { name: adhoc, on: manual, steps: [ { uses: timer.x, options: {} } ] }
`), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("manual trigger must pass config validation: %v", err)
	}

	var c2 Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  manual: { type: cron, schedules: { s: { every: 1h } } }
triggers:
  - { on: manual.s, steps: [ { uses: manual.x, options: {} } ] }
`), &c2); err != nil {
		t.Fatal(err)
	}
	if err := c2.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("connector named manual must be rejected: %v", err)
	}

	var c3 Config
	if err := yaml.Unmarshal([]byte(base+`
triggers:
  - { on: bogus, steps: [ { uses: timer.x, options: {} } ] }
`), &c3); err != nil {
		t.Fatal(err)
	}
	if err := c3.Validate(); err == nil || !strings.Contains(err.Error(), "<connector>.<event>") {
		t.Fatalf("bare non-manual on: must be rejected: %v", err)
	}
}
