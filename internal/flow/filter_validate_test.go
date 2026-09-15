package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// Load-time validation of the unified `filter:`
// (docs/design/unified-filter.md §Validation). A filter is the one thing
// between an event and a dispatch, so a typo in it is silent by nature: an
// unknown fact resolves to nil (falsy) and inverts the operator's intent
// without ever logging anything. These tests are what make it loud.

const filterBaseCfg = `
connectors:
  gh: { use: github, token: x, repos: [acme/web] }
  slack: { use: slack, bot_token: x }
`

func filterTrigger(body string) string {
	return filterBaseCfg + `
triggers:
  - name: review
    on: gh.review_requested
` + body + `
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
}

// validateFilterCfg runs the same load + validate path the daemon does.
func validateFilterCfg(t *testing.T, y string) error {
	t.Helper()
	cfg := loadConfig(t, y)
	return Validate(cfg, buildRegistry(t, cfg))
}

func TestFilterValidationAccepts(t *testing.T) {
	cases := []struct{ name, body string }{
		{"expr over declared facts", `    filter: "!is_draft && !contains(title, 'Release ')"`},
		{"the #5590 spelling, parentheses and all",
			`    filter: "!is_draft && !( (head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release ') )"`},
		{"an object of match keys", `    filter: { not_draft: true, authors: [dependabot] }`},
		{"the reserved expr: key alongside siblings",
			`    filter: { not_draft: true, expr: "!contains(title, 'Release')" }`},
		{"a list of alternatives",
			"    filter:\n      - { authors: [dependabot], not_draft: true }\n      - { labels_any: [urgent, security] }\n      - \"!is_draft\""},
		{"startswith, the recommended replacement for the title substring footgun",
			`    filter: "startswith(title, 'Release ')"`},
		{"repos: routing stays legal next to a filter",
			"    filters: { repos: [acme/web] }\n    filter: \"!is_draft\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateFilterCfg(t, filterTrigger(c.body)); err != nil {
				t.Fatalf("should validate: %v", err)
			}
		})
	}
}

func TestFilterValidationRejects(t *testing.T) {
	cases := []struct{ name, body, wantErr string }{
		{
			"a typo'd fact — the silent-falsy case this check exists for",
			`    filter: "!is_drafft"`,
			`publishes no fact "is_drafft"`,
		},
		{
			"a fact this event does not publish",
			`    filter: "merge_state == 'CLEAN'"`,
			`publishes no fact "merge_state"`,
		},
		{
			"a fact nested inside a list branch",
			"    filter:\n      - \"!is_draft\"\n      - \"reviewer == 'alice'\"",
			`publishes no fact "reviewer"`,
		},
		{
			"an unknown match key",
			`    filter: { labels_nay: [urgent] }`,
			`has no match key "labels_nay"`,
		},
		{
			"a match key whose underlying fact this event lacks",
			`    filter: { from_users: [alice] }`,
			`has no match key "from_users"`,
		},
		{
			"a match key nested inside a list branch",
			"    filter:\n      - { not_draft: true }\n      - { sole_assignee: true }",
			`has no match key "sole_assignee"`,
		},
		{
			"a list-typed key given a bool",
			`    filter: { labels_any: true }`,
			`key "labels_any"`,
		},
		{
			"a bool-typed key given a list",
			`    filter: { not_draft: [yes] }`,
			`key "not_draft"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateFilterCfg(t, filterTrigger(c.body))
			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want an error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// TestFilterUnsupportedEvent: phase 1 wires five events. On any other one a
// `filter:` would be decoded, validated, and then never evaluated — so it is
// refused rather than silently ignored.
func TestFilterUnsupportedEvent(t *testing.T) {
	y := filterBaseCfg + `
triggers:
  - name: conflict
    on: gh.merge_conflict
    filter: "!is_draft"
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	err := validateFilterCfg(t, y)
	if err == nil || !strings.Contains(err.Error(), "not supported on gh.merge_conflict") {
		t.Fatalf("want a not-supported error, got %v", err)
	}
}

// TestFilterPerSourceValidatesAgainstItsOwnSource: a per-source `filter:` is
// checked against THAT source's facts, like per-source `filters:`.
func TestFilterPerSourceValidatesAgainstItsOwnSource(t *testing.T) {
	ok := filterBaseCfg + `
triggers:
  - name: fan-in
    on:
      - gh.review_requested: { filter: "!is_draft" }
      - gh.new_comment: { filter: "contains(comment_body, 'please review')" }
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	if err := validateFilterCfg(t, ok); err != nil {
		t.Fatalf("each per-source filter should validate against its own source: %v", err)
	}

	// is_draft is a review_requested fact; new_comment does not publish it.
	bad := filterBaseCfg + `
triggers:
  - name: fan-in
    on:
      - gh.review_requested
      - gh.new_comment: { filter: "!is_draft" }
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	err := validateFilterCfg(t, bad)
	if err == nil || !strings.Contains(err.Error(), `publishes no fact "is_draft"`) {
		t.Fatalf("a per-source filter must be checked against its own source: %v", err)
	}
}

// TestFilterAndLegacyFiltersConflict: a trigger states its predicate ONE way.
// This runs through config.Load, where the check lives.
func TestFilterAndLegacyFiltersConflict(t *testing.T) {
	load := func(t *testing.T, y string) error {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "conductor.yaml")
		if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(p)
		return err
	}

	conflict := filterTrigger("    filters: { gates: { not_draft: true } }\n    filter: \"!is_draft\"")
	err := load(t, conflict)
	if err == nil {
		t.Fatal("setting both `filter:` and a legacy predicate key must fail")
	}
	for _, want := range []string{"filter:", "gates"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %q; got %v", want, err)
		}
	}

	// Several legacy keys are all named, so one fix round clears them.
	err = load(t, filterTrigger("    filters: { gates: { not_draft: true }, exclude: { title: ['Release '] } }\n    filter: \"!is_draft\""))
	if err == nil || !strings.Contains(err.Error(), "exclude, gates") {
		t.Errorf("every conflicting legacy key should be listed; got %v", err)
	}

	// The routing keys are not predicates and stay legal.
	if err := load(t, filterTrigger("    filters: { repos: [acme/web], exclude_repos: [acme/old] }\n    filter: \"!is_draft\"")); err != nil {
		t.Errorf("repos/exclude_repos must stay legal alongside `filter:`: %v", err)
	}

	// A per-source block hits the same rule after normalization.
	perSource := filterBaseCfg + `
triggers:
  - name: fan
    on:
      - gh.review_requested: { filter: "!is_draft", filters: { gates: { not_draft: true } } }
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	if err := load(t, perSource); err == nil {
		t.Error("a per-source block setting both must fail too")
	}
}
