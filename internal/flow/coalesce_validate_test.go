package flow

import (
	"strings"
	"testing"
)

// github's new_comment batches a comment burst per PR by default (its event
// declares connector.EventDecl.Coalesce), so a trigger with no group: of its
// own may read {{.group.*}}; `group: { enabled: false }` opts out of batching
// and takes .group out of scope with it.
func TestDefaultBatchingPutsGroupInScope(t *testing.T) {
	trigger := func(group string) string {
		return filterBaseCfg + `
triggers:
  - name: comments
    on: gh.new_comment
` + group + `    steps:
      - uses: slack.post
        options: { channel: C1, text: "{{.group.count}} comment(s)" }
`
	}
	if err := validateFilterCfg(t, trigger("")); err != nil {
		t.Fatalf("new_comment with no group: should see its default batch: %v", err)
	}
	err := validateFilterCfg(t, trigger("    group: { enabled: false }\n"))
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("opted-out trigger reading .group: err = %v, want an out-of-scope reference", err)
	}
	// A kind with no default batching is unchanged: .group needs a group:.
	other := strings.Replace(trigger(""), "gh.new_comment", "gh.merge_conflict", 1)
	if err := validateFilterCfg(t, other); err == nil {
		t.Fatal("merge_conflict reading .group with no group: validated")
	}
}
