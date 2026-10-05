package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSanitizeVersionDirRejectsDotAndDotDot is finding 5 (MEDIUM-HIGH,
// security): sanitizeVersionDir mapped "/" and "\\" but let "." and ".."
// through unchanged — a single path segment with no separator in it, so the
// replacer never touches it. BinDirForVersion would then resolve to the
// plugin's own key directory (for ".") or that key directory's PARENT (for
// ".."), and GCVersions' RemoveAll on that path takes out every version of
// every plugin under connectors/ or runtimes/ in one pass. A real release
// tag is never exactly "." or "..", so a value this function sees here is
// already a tampered or corrupt record; it must never produce a bare "."
// or "..".
func TestSanitizeVersionDirRejectsDotAndDotDot(t *testing.T) {
	for _, in := range []string{".", "..", ""} {
		got := sanitizeVersionDir(in)
		if got == "." || got == ".." {
			t.Fatalf("sanitizeVersionDir(%q) = %q, must never be a bare %q", in, got, got)
		}
	}
	// Still deterministic and distinct per input — not collapsed to one
	// value that would silently alias two different (corrupt) records onto
	// the same directory.
	if sanitizeVersionDir(".") == sanitizeVersionDir("..") {
		t.Fatalf("sanitizeVersionDir(\".\") and sanitizeVersionDir(\"..\") must not collide: both map to %q", sanitizeVersionDir("."))
	}
	// An ordinary release tag is untouched.
	if got := sanitizeVersionDir("v1.2.3"); got != "v1.2.3" {
		t.Fatalf("an ordinary tag must pass through unchanged, got %q", got)
	}
}

// TestGCVersionsReviewerCaseDotDotDoesNotEscape is the reviewer's exact
// reproduction for finding 5: a tampered installed.yaml record whose
// Resolved is ".." must never make GCVersions remove anything outside that
// ONE version's own directory — in particular, never the whole connectors/
// directory (every other plugin's every installed version) the way
// BinDirForVersion(dir, "connectors/widget", "..") would resolve to before
// the fix (the plugin's key directory's own PARENT).
func TestGCVersionsReviewerCaseDotDotDoesNotEscape(t *testing.T) {
	dir := t.TempDir()
	s := LoadInstallState(dir)

	// A sibling plugin, installed normally, living in the SAME parent
	// (connectors/) the ".." record would otherwise have RemoveAll'd.
	sibling := Installed{Key: "connectors/other", Kind: "connector", Name: "other", Resolved: "other/v1.0.0",
		Path: filepath.Join(BinDirForVersion(dir, "connectors/other", "other/v1.0.0"), "conductor-other")}
	// The tampered record: Resolved is literally "..".
	tampered := Installed{Key: "connectors/widget", Kind: "connector", Name: "widget", Resolved: "..",
		Path: filepath.Join(BinDirFor(dir, "connectors/widget"), "conductor-widget")}

	for _, in := range []Installed{sibling, tampered} {
		if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Path, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.Put(in)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Nothing is kept — the tampered record, in particular, resolves to
	// nothing any live config could reference, so a real GC pass would try
	// to drop it exactly like this.
	keep := map[VersionKey]bool{
		{Key: sibling.Key, Resolved: sibling.Resolved}: true,
	}

	connectorsDir := filepath.Join(dir, "connectors")
	before, err := os.ReadDir(connectorsDir)
	if err != nil {
		t.Fatal(err)
	}

	// GCVersions is allowed to refuse and return an error over a tampered
	// record (the defense-in-depth guard catching what sanitizeVersionDir
	// was SUPPOSED to have already prevented) — that is a safe outcome too.
	// What must NEVER happen, whether it errors or not, is anything
	// outside widget's own version directory being removed.
	_, _ = s.GCVersions(keep)

	if _, err := os.Stat(sibling.Path); err != nil {
		t.Fatalf("an unrelated sibling plugin must survive a tampered record's GC: %v", err)
	}
	after, err := os.ReadDir(connectorsDir)
	if err != nil {
		t.Fatalf("connectors/ itself must still exist: %v", err)
	}
	if len(after) < len(before) {
		t.Fatalf("connectors/ lost entries during GC of an unrelated tampered record: before=%v after=%v", before, after)
	}
}

// TestIsStrictlyWithin is a focused unit test of GCVersions' own
// defense-in-depth guard, independent of sanitizeVersionDir — the LAST line
// of defense before RemoveAll, so it must correctly refuse a parent-equal
// or escaping path and accept a genuine child, on its own.
func TestIsStrictlyWithin(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "connectors", "widget")
	cases := []struct {
		name  string
		child string
		want  bool
	}{
		{"a real child", filepath.Join(parent, "v1.0.0"), true},
		{"a nested child", filepath.Join(parent, "v1.0.0", "sub"), true},
		{"the parent itself", parent, false},
		{"the parent's own parent", filepath.Join(base, "connectors"), false},
		{"an unrelated sibling key", filepath.Join(base, "connectors", "other"), false},
		{"an escape via .. baked into the path", filepath.Join(parent, ".."), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStrictlyWithin(parent, tc.child); got != tc.want {
				t.Fatalf("isStrictlyWithin(%q, %q) = %v, want %v", parent, tc.child, got, tc.want)
			}
		})
	}
}
