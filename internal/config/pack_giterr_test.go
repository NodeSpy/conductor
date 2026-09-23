package config

import "testing"

func TestGitErrDetail(t *testing.T) {
	// The exact spew from a transient GitHub TLS hiccup during a partial clone —
	// progress, detached-HEAD advice, and post-failure hints that bury the cause.
	noisy := `Cloning into '/tmp/conductor-pack-2129536880'...
warning: refs/tags/pr-autopilot/v1.0.0 01f34e37f9c58f399f122b1e40d189d81b779145 is not a commit!
Note: switching to 'b6a2039a289db3d20c36971f6e24bb4e20af9ed8'.

You are in 'detached HEAD' state. You can look around, make experimental
changes and commit them, and you can discard any commits you make in this
state without impacting any branches by switching back to a branch.
fatal: unable to access 'https://github.com/NodeSpy/conductor-packs/': SSL: certificate subject name 'dotcom.glb' does not match target hostname 'github.com'
fatal: could not fetch 961e633f58fb05a7ddca791ce44d58642567e3f2 from promisor remote
warning: Clone succeeded, but checkout failed.
You can inspect what was checked out with 'git status'
error: remote origin already exists.`

	got := gitErrDetail(noisy)
	want := "fatal: unable to access 'https://github.com/NodeSpy/conductor-packs/': SSL: certificate subject name 'dotcom.glb' does not match target hostname 'github.com'"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	// error: line when there's no fatal:.
	if got := gitErrDetail("Cloning into 'x'...\nerror: pathspec 'v9' did not match\n"); got != "error: pathspec 'v9' did not match" {
		t.Fatalf("error-line: got %q", got)
	}

	// No fatal:/error: → last non-empty line, and it stays single-line.
	if got := gitErrDetail("first line\n\nlast meaningful line\n"); got != "last meaningful line" {
		t.Fatalf("fallback: got %q", got)
	}

	// Empty output → empty.
	if got := gitErrDetail("\n  \n"); got != "" {
		t.Fatalf("empty: got %q", got)
	}

	// A runaway single line is capped (and never multi-line).
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if got := gitErrDetail(string(long)); len(got) != 200+len("…") {
		t.Fatalf("cap: len=%d", len(got))
	}
}
