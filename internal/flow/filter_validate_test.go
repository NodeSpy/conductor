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
		{"an object of match keys", `    filter: { not_draft: true, author: [dependabot] }`},
		{"the reserved expr: key alongside siblings",
			`    filter: { not_draft: true, expr: "!contains(title, 'Release')" }`},
		{"a list of alternatives",
			"    filter:\n      - { author: [dependabot], not_draft: true }\n      - { label_any: [urgent, security] }\n      - \"!is_draft\""},
		{"startswith, the recommended replacement for the title substring footgun",
			`    filter: "startswith(title, 'Release ')"`},
		{"routing and predicate are conjuncts of the ONE filter",
			"    filter: { repo: [acme/web], not_repo: [acme/old], expr: \"!is_draft\" }"},
		{"the not_ twin of every declared key validates without being declared",
			"    filter: { not_branch: [staging], not_title: ['Release '], not_label_any: [wip], not_author: [bot], not_draft: true }"},
		{"not_expr negates a condition over declared facts",
			`    filter: { not_expr: "contains(title, 'Release ')" }`},
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
			`    filter: { comment_author: [alice] }`,
			`has no match key "comment_author"`,
		},
		{
			"the not_ twin of an undeclared key is refused by its BASE name",
			`    filter: { not_comment_author: [alice] }`,
			`has no match key "comment_author"`,
		},
		{
			"a retired key is refused by name rather than silently accepted",
			`    filter: { labels_any: [urgent] }`,
			`has no match key "labels_any"`,
		},
		{
			"a match key nested inside a list branch",
			"    filter:\n      - { not_draft: true }\n      - { sole_assignee: true }",
			`has no match key "sole_assignee"`,
		},
		{
			"a list-typed key given a bool",
			`    filter: { label_any: true }`,
			`key "label_any"`,
		},
		{
			"a bool-typed key given a list",
			`    filter: { not_draft: [yes] }`,
			`key "draft"`,
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

// TestFilterOnAPredicatelessEvent: five github events publish predicate facts.
// The rest publish none, and their keep-conditions evaluate nothing — so a
// PREDICATE on one would be decoded, validated, and then never consulted, and
// is refused rather than silently ignored.
//
// Routing is different in kind: `repo`/`not_repo` are hoisted into the
// structural gate emit() applies before any keep-condition, so they work on
// every event — which is the whole reason routing could move into `filter:`
// and let `filters:` go.
func TestFilterOnAPredicatelessEvent(t *testing.T) {
	conflict := func(body string) string {
		return filterBaseCfg + `
triggers:
  - name: conflict
    on: gh.merge_conflict
` + body + `
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	}
	err := validateFilterCfg(t, conflict(`    filter: "!is_draft"`))
	if err == nil || !strings.Contains(err.Error(), `publishes no fact "is_draft"`) {
		t.Fatalf("a predicate on a factless event must be refused, got %v", err)
	}
	err = validateFilterCfg(t, conflict(`    filter: { label_any: [urgent] }`))
	if err == nil || !strings.Contains(err.Error(), `has no match key "label_any"`) {
		t.Fatalf("a predicate match key on a factless event must be refused, got %v", err)
	}
	for _, body := range []string{
		`    filter: { repo: [acme/web] }`,
		`    filter: { repo: [acme/*], not_repo: [acme/old] }`,
	} {
		if err := validateFilterCfg(t, conflict(body)); err != nil {
			t.Errorf("routing must be legal on every github event; %s: %v", body, err)
		}
	}
}

// TestFilterPerSourceValidatesAgainstItsOwnSource: a per-source `filter:` is
// checked against THAT source's facts, not the union.
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

// TestFilterIsTheOnlyFilterKey: phase 2's headline. Phase 1 let `filter:` and
// `filters:` sit side by side on one trigger — two keys a letter apart, which
// is the ambiguity the unification existed to remove. The old key is gone from
// the schema, and the error says what replaced it.
func TestFilterIsTheOnlyFilterKey(t *testing.T) {
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

	for _, body := range []string{
		"    filters: { gates: { not_draft: true } }",
		"    filters: { repos: [acme/web] }",
		"    filters: { gates: { not_draft: true } }\n    filter: \"!is_draft\"",
		"    filters: { repos: [acme/web], exclude_repos: [acme/old] }\n    filter: \"!is_draft\"",
	} {
		err := load(t, filterTrigger(body))
		if err == nil {
			t.Fatalf("`filters:` must be refused; accepted %q", body)
		}
		if !strings.Contains(err.Error(), "`filters:` was removed") || !strings.Contains(err.Error(), "`filter:`") {
			t.Errorf("the error should name the retired key and its replacement; got %v", err)
		}
	}

	// A per-source block is the same surface, so it refuses the same way.
	perSource := filterBaseCfg + `
triggers:
  - name: fan
    on:
      - gh.review_requested: { filters: { gates: { not_draft: true } } }
    steps:
      - uses: slack.post
        options: { channel: C1, text: hi }
`
	if err := load(t, perSource); err == nil || !strings.Contains(err.Error(), "`filters:` was removed") {
		t.Errorf("a per-source `filters:` must be refused too; got %v", err)
	}

	// And the thing it told you to write loads.
	ok := filterTrigger("    filter: { repo: [acme/web], not_repo: [acme/old], not_draft: true }")
	if err := load(t, ok); err != nil {
		t.Errorf("the replacement spelling must load: %v", err)
	}
}
