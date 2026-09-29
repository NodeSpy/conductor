package targets

import "testing"

func fixerTarget() Target {
	return Target{Repo: "acme/app", Number: 42, IsPR: true, HeadBranch: "fix/thing"}
}

func TestCheckWriteOwnPRCommentOK(t *testing.T) {
	r := New()
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "acme/app", 42); reason != "" {
		t.Fatalf("own-PR comment should be allowed, got %q", reason)
	}
}

func TestCheckWriteOtherPRRefusedExactReason(t *testing.T) {
	r := New()
	got := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "acme/app", 43)
	want := "target: write to acme/app#43 but dispatch target is #42"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestCheckWriteOtherRepoRefused(t *testing.T) {
	r := New()
	got := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "acme/other", 1)
	want := "target: write to acme/other#1 but dispatch target is acme/app#42"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestCheckWriteOtherTargetsAllows(t *testing.T) {
	r := New()
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{OtherTargets: true}, "comment", "acme/app", 43); reason != "" {
		t.Fatalf("OtherTargets should allow a different PR, got %q", reason)
	}
}

func TestCheckWriteCreatePRRefusedByDefault(t *testing.T) {
	r := New()
	got := r.CheckWrite(fixerTarget(), WritePolicy{}, "create_pr", "acme/app", 0)
	want := "target: opening a PR is refused (writes are bound to the dispatch's own target)"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{CreatePR: true}, "create_pr", "acme/app", 0); reason != "" {
		t.Fatalf("CreatePR: true should allow it, got %q", reason)
	}
}

func TestCheckWriteCreateIssueRefusedByDefault(t *testing.T) {
	r := New()
	got := r.CheckWrite(fixerTarget(), WritePolicy{}, "create_issue", "acme/app", 0)
	want := "target: opening an issue is refused (writes are bound to the dispatch's own target)"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{CreateIssue: true}, "create_issue", "acme/app", 0); reason != "" {
		t.Fatalf("CreateIssue: true should allow it, got %q", reason)
	}
}

func TestCheckWriteMergeNeedsGrant(t *testing.T) {
	r := New()
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{}, "merge", "acme/app", 42); reason == "" {
		t.Fatal("merge on own target should refuse without Merge: true")
	}
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{Merge: true}, "merge", "acme/app", 42); reason != "" {
		t.Fatalf("Merge: true should allow merging own target, got %q", reason)
	}
	for _, kind := range []string{"close", "reopen"} {
		if reason := r.CheckWrite(fixerTarget(), WritePolicy{}, kind, "acme/app", 42); reason == "" {
			t.Fatalf("%s on own target should refuse without Merge: true", kind)
		}
		if reason := r.CheckWrite(fixerTarget(), WritePolicy{Merge: true}, kind, "acme/app", 42); reason != "" {
			t.Fatalf("%s with Merge: true should be allowed, got %q", kind, reason)
		}
	}
}

func TestCheckWriteReadOnlyRefusesEverything(t *testing.T) {
	r := New()
	pol := WritePolicy{ReadOnly: true, CreatePR: true, CreateIssue: true, OtherTargets: true, Merge: true}
	cases := []struct {
		kind   string
		repo   string
		number int
	}{
		{"comment", "acme/app", 42},
		{"create_pr", "acme/app", 0},
		{"create_issue", "acme/app", 0},
		{"merge", "acme/app", 42},
		{"comment", "acme/other", 1},
	}
	for _, c := range cases {
		if reason := r.CheckWrite(fixerTarget(), pol, c.kind, c.repo, c.number); reason != readOnlyReason {
			t.Errorf("%s: reason = %q, want %q", c.kind, reason, readOnlyReason)
		}
	}
}

func TestCheckWriteClosedTargetRefusesEverything(t *testing.T) {
	r := New()
	r.MarkClosed("acme/app", 42, true)
	pol := WritePolicy{CreatePR: true, CreateIssue: true, OtherTargets: true, Merge: true}
	for _, c := range []struct {
		kind   string
		repo   string
		number int
	}{
		{"comment", "acme/app", 42},
		{"merge", "acme/app", 42},
	} {
		got := r.CheckWrite(fixerTarget(), pol, c.kind, c.repo, c.number)
		want := "target: acme/app#42 is merged — writes refused"
		if got != want {
			t.Errorf("%s: reason = %q, want %q", c.kind, got, want)
		}
	}
}

func TestCheckWriteClosedReasonUsesOutcome(t *testing.T) {
	r := New()
	r.MarkClosed("acme/app", 42, false)
	got := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "acme/app", 42)
	want := "target: acme/app#42 is closed — writes refused"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestCheckWriteRepoCaseInsensitive(t *testing.T) {
	r := New()
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "ACME/APP", 42); reason != "" {
		t.Fatalf("repo comparison should be case-insensitive, got %q", reason)
	}
	r.MarkClosed("ACME/APP", 42, true)
	if _, ok := r.Closed("acme/app", 42); !ok {
		t.Fatal("Closed lookup should be case-insensitive")
	}
}

func TestReopenClearsClosedMark(t *testing.T) {
	r := New()
	r.MarkClosed("acme/app", 42, true)
	if _, ok := r.Closed("acme/app", 42); !ok {
		t.Fatal("expected closed after MarkClosed")
	}
	r.Reopen("acme/app", 42)
	if _, ok := r.Closed("acme/app", 42); ok {
		t.Fatal("Reopen should clear the closed mark")
	}
	if reason := r.CheckWrite(fixerTarget(), WritePolicy{}, "comment", "acme/app", 42); reason != "" {
		t.Fatalf("write should be allowed again after reopen, got %q", reason)
	}
}

// --- CheckPush ---

func TestCheckPushOwnBranchOK(t *testing.T) {
	r := New()
	if reason := r.CheckPush(fixerTarget(), WritePolicy{}, "acme/app", "fix/thing", false, false); reason != "" {
		t.Fatalf("push to own head branch should be allowed, got %q", reason)
	}
}

func TestCheckPushBranchGlobs(t *testing.T) {
	r := New()
	pol := WritePolicy{Branches: []string{"scratch/*"}}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/app", "scratch/notes", false, false); reason != "" {
		t.Fatalf("push matching a Branches glob should be allowed, got %q", reason)
	}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/app", "other/thing", false, false); reason == "" {
		t.Fatal("push to a branch matching no glob and not the head branch should be refused")
	}
}

func TestCheckPushDefaultBranchForceDeleteRefused(t *testing.T) {
	r := New()
	pol := WritePolicy{Branches: []string{"*"}}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/app", "", false, false); reason == "" {
		t.Fatal("push to the default branch (empty branch) should be refused")
	}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/app", "fix/thing", true, false); reason == "" {
		t.Fatal("force push should be refused")
	}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/app", "fix/thing", false, true); reason == "" {
		t.Fatal("branch delete should be refused")
	}
}

func TestCheckPushOtherRepoNeedsOtherTargetsAndGlob(t *testing.T) {
	r := New()
	if reason := r.CheckPush(fixerTarget(), WritePolicy{Branches: []string{"*"}}, "acme/other", "main2", false, false); reason == "" {
		t.Fatal("push to another repo should be refused without OtherTargets")
	}
	pol := WritePolicy{OtherTargets: true, Branches: []string{"scratch/*"}}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/other", "scratch/x", false, false); reason != "" {
		t.Fatalf("OtherTargets + matching glob should allow a cross-repo push, got %q", reason)
	}
	if reason := r.CheckPush(fixerTarget(), pol, "acme/other", "main2", false, false); reason == "" {
		t.Fatal("OtherTargets alone (no glob match) should still refuse")
	}
}

func TestCheckPushClosedTargetRefused(t *testing.T) {
	r := New()
	r.MarkClosed("acme/app", 42, true)
	if reason := r.CheckPush(fixerTarget(), WritePolicy{}, "acme/app", "fix/thing", false, false); reason == "" {
		t.Fatal("push to a closed/merged target's own branch should be refused")
	}
}

func TestCheckPushReadOnly(t *testing.T) {
	r := New()
	if reason := r.CheckPush(fixerTarget(), WritePolicy{ReadOnly: true}, "acme/app", "fix/thing", false, false); reason != readOnlyReason {
		t.Fatalf("reason = %q, want %q", reason, readOnlyReason)
	}
}
