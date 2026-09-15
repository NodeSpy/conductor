package config

import (
	"strings"
	"testing"
)

// The retired `filters:` block (docs/design/unified-filter-phase2.md). Phase 1
// shipped `filter:` as a SIBLING of `filters:`, so a trigger could carry both
// — the exact two-keys-one-letter-apart confusion the unification existed to
// end. Phase 2 deleted the old key from the schema.
//
// Rejecting it is not enough on its own: the strict parser would refuse it as
// an unknown field, but "field filters not found" reads like a typo, and a
// config carrying `filters:` is not a typo — it is a config from before the
// rename that needs migrating. So every surface that used to accept the block
// names it, and says what to write instead.
func TestLegacyFiltersKeyIsRejectedEverywhere(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"on a trigger", `
connectors: { gh: { use: github, token: x } }
triggers:
  - on: gh.review_requested
    filters: { repos: [acme/web] }
    steps: [{ id: s, type: command, command: ["true"] }]
`},
		{"in a per-source block of an `on:` list", `
connectors: { gh: { use: github, token: x } }
triggers:
  - name: fan
    on:
      - gh.review_requested:
          filters: { repos: [acme/web] }
    steps: [{ id: s, type: command, command: ["true"] }]
`},
		{"in a pack trigger arm", `
connectors: { gh: { use: github, token: x } }
packs:
  kit:
    source: ./src/kit
    triggers:
      review: { enabled: true, repos: [acme/web], filters: { labels: [ready] } }
`},
		{"in a pack `on:` overlay", `
connectors: { gh: { use: github, token: x } }
packs:
  kit:
    source: ./src/kit
    on:
      review: { filters: { labels: [ready] } }
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadDoc(t, c.doc)
			if err == nil {
				t.Fatal("`filters:` must be refused — it is no longer in the schema")
			}
			// The error has to name the key that REPLACED it, or an operator
			// reading it learns only that something is wrong.
			if !strings.Contains(err.Error(), "`filters:` was removed") {
				t.Fatalf("the error should name the retired key: %v", err)
			}
			if !strings.Contains(err.Error(), "`filter:`") {
				t.Fatalf("the error should point at `filter:`: %v", err)
			}
		})
	}
}

// The replacement spellings all parse — the error above is actionable only if
// what it tells you to write actually works.
func TestRetiredFiltersKeysHaveUnifiedSpellings(t *testing.T) {
	for _, doc := range []string{
		`filter: {repo: [acme/web], not_repo: [acme/legacy]}`,
		`filter: {not_branch: [staging, prod]}`,
		`filter: {not_label_any: [wip]}`,
		`filter: {not_title: ['Release ']}`,
		`filter: {not_draft: true}`,
		`filter: {label_any: [urgent], label_all: [triaged]}`,
		`filter: {author: [dependabot], not_comment_author: [ci-bot]}`,
		`filter: {comment_author: [alice]}`,
	} {
		if f, err := decodeFilter(t, doc); err != nil || f == nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
}
