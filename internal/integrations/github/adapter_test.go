package github

import (
	"reflect"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/pkg/githubkit/ghsource"
)

// The adapter is a type boundary over the kit, and these pin the boundary.

// Two merges run for every resolved variant: the kit merges the fields it
// reads, and MergeRule (through the Ext hook) merges the whole config.Action.
// They must agree, field for field, or the source would evaluate one trigger
// and the engine run another.
func TestKitMergeAgreesWithMergeRule(t *testing.T) {
	yes, no := true, false
	defaults := Rule{
		Reviewer: config.Actors{Logins: []string{"me"}},
		Actions: map[string]config.ActionSet{
			"review_requested": {{Agent: "base", IgnoreChecks: []string{"lint"}, StuckAfter: config.Duration(time.Minute),
				AuthorBot: &no, LabelsAny: []string{"a"}, Gates: map[string]any{"not_draft": true}}},
			"failing_checks": {{Agent: "fixer", Repos: []string{"x/*"}}},
		},
	}
	rule := Rule{
		Match:    Match{Repos: []string{"acme/*"}},
		Assignee: config.Actors{Logins: []string{"bob"}},
		Actions: map[string]config.ActionSet{
			"review_requested": {
				{Name: "one", Prompt: "p", Enabled: &no, FromUsers: []string{"u"}, PollInterval: config.Duration(2 * time.Minute)},
				{Name: "two", AuthorBot: &yes, Exclude: config.Exclude{Title: []string{"WIP"}}, Repos: []string{"acme/w"}},
			},
			"new_comment": {{IgnoreUsers: []string{"ci[bot]"}, IncludePrereleases: true, SoleAssignee: true}},
		},
	}
	legacy := MergeRule(defaults, rule)
	kit := ghsource.MergeRule(defaults.kit(), rule.kit(), mergeExt)

	if !reflect.DeepEqual(kit.Reviewer, actors(legacy.Reviewer)) || !reflect.DeepEqual(kit.Assignee, actors(legacy.Assignee)) {
		t.Fatalf("rule-level actors differ: kit %+v/%+v legacy %+v/%+v", kit.Reviewer, kit.Assignee, legacy.Reviewer, legacy.Assignee)
	}
	if len(kit.Actions) != len(legacy.Actions) {
		t.Fatalf("kinds differ: kit %d legacy %d", len(kit.Actions), len(legacy.Actions))
	}
	for kind, set := range legacy.Actions {
		ks := kit.Actions[kind]
		if len(ks) != len(set) {
			t.Fatalf("%s: %d kit variants, %d legacy", kind, len(ks), len(set))
		}
		for i := range set {
			want := KitAction(set[i])
			got := ks[i]
			// Ext is the merged config.Action itself, and must be exactly
			// what MergeRule produced.
			if !reflect.DeepEqual(got.Ext, set[i]) {
				t.Errorf("%s[%d]: Ext is not MergeRule's action:\n got %+v\nwant %+v", kind, i, got.Ext, set[i])
			}
			got.Ext, want.Ext = nil, nil
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s[%d]: kit merge disagrees with MergeRule:\n kit    %+v\n legacy %+v", kind, i, got, want)
			}
		}
	}
}

// The kit mirrors a handful of conductor constants it cannot import. They are
// values the engine reads back off the trigger, so a drift is a silent
// behavior change.
func TestKitConstantsMatchConductor(t *testing.T) {
	if ghsource.KindClosed != core.KindClosed {
		t.Errorf("KindClosed: kit %q, core %q", ghsource.KindClosed, core.KindClosed)
	}
	if ghsource.CommentKindIssue != store.CommentKindIssue || ghsource.CommentKindReview != store.CommentKindReview {
		t.Errorf("comment kinds: kit %q/%q, store %q/%q", ghsource.CommentKindIssue, ghsource.CommentKindReview,
			store.CommentKindIssue, store.CommentKindReview)
	}
	if ghsource.FilterNotPrefix != config.FilterNotPrefix {
		t.Errorf("FilterNotPrefix: kit %q, config %q", ghsource.FilterNotPrefix, config.FilterNotPrefix)
	}
	for kind := range ghsource.KnownKinds() {
		if ghsource.BranchFixKind(kind) != core.BranchFixKind(kind) {
			t.Errorf("BranchFixKind(%q): kit %v, core %v", kind, ghsource.BranchFixKind(kind), core.BranchFixKind(kind))
		}
	}
}

// A kit trigger converts to a core trigger with nothing dropped, and the
// engine's Action is the merged config.Action.
func TestTriggerConversionCarriesEveryField(t *testing.T) {
	act := config.Action{Name: "v", Agent: "fixer"}
	kt := ghsource.Trigger{
		Source: "github", Instance: "gh", Kind: "merge_conflict", Variant: "v",
		Target: ghsource.Target{Repo: "o/r", Owner: "o", Name: "r", PR: 3, Number: 3, HeadSHA: "h", BaseRef: "main", HTMLURL: "u", Project: "p"},
		Title:  "t", Context: map[string]any{"k": 1}, Dedup: "d", Labels: map[string]string{"l": "1"},
		Action: KitAction(act), TargetTrusted: true, CatchUp: true, Force: true,
	}
	ct := trigger(kt)
	want := core.Trigger{
		Source: "github", Instance: "gh", Kind: "merge_conflict", Variant: "v",
		Target: core.Target{Repo: "o/r", Owner: "o", Name: "r", PR: 3, Number: 3, HeadSHA: "h", BaseRef: "main", HTMLURL: "u", Project: "p"},
		Title:  "t", Context: map[string]any{"k": 1}, Dedup: "d", Labels: map[string]string{"l": "1"},
		Action: act, TargetTrusted: true, CatchUp: true, Force: true,
	}
	if !reflect.DeepEqual(ct, want) {
		t.Fatalf("conversion lost something:\n got %+v\nwant %+v", ct, want)
	}
	// Every exported core.Trigger field is either carried or deliberately
	// daemon-only. A new field fails here until it is classified.
	daemonOnly := map[string]bool{"DispatchID": true, "HistoryID": true}
	carried := map[string]bool{}
	kv := reflect.TypeOf(ghsource.Trigger{})
	for i := 0; i < kv.NumField(); i++ {
		carried[kv.Field(i).Name] = true
	}
	cv := reflect.TypeOf(core.Trigger{})
	for i := 0; i < cv.NumField(); i++ {
		name := cv.Field(i).Name
		if !carried[name] && !daemonOnly[name] {
			t.Errorf("core.Trigger.%s has no kit counterpart and is not classified daemon-only", name)
		}
	}
	if (trigger(ghsource.Trigger{Kind: ghsource.KindClosed})).Action != nil {
		t.Error("a trigger with no matched variant must carry no Action")
	}
}
