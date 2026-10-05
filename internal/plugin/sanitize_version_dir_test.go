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
		Path: filepath.Join(BinDirForVersion(dir, "connectors/other", "other/v1.0.0", ""), "conductor-other")}
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

// TestSanitizeVersionDirInjectiveForReviewerCase is the reviewer's exact
// reproduction: before the fix, sanitizeVersionDir replaced "/" and "\\"
// with "_" and nothing else, so "a/b/widget/v1.0.0" and "a_b/widget/v1.0.0"
// both collapsed to the literal string "a_b_widget_v1.0.0" — two distinct
// component paths sharing ONE on-disk directory. The fix must give them
// distinct directories.
func TestSanitizeVersionDirInjectiveForReviewerCase(t *testing.T) {
	a := "a/b/widget/v1.0.0"
	b := "a_b/widget/v1.0.0"
	gotA, gotB := sanitizeVersionDir(a), sanitizeVersionDir(b)
	if gotA == gotB {
		t.Fatalf("sanitizeVersionDir(%q) and sanitizeVersionDir(%q) must not collide, both got %q", a, b, gotA)
	}
	// Also prove it end to end through BinDirForVersion, which is what
	// actually matters: two instances resolving to these two component
	// paths under the SAME plugin key must land in two different
	// directories.
	dir := t.TempDir()
	dirA := BinDirForVersion(dir, "connectors/widget", a, "")
	dirB := BinDirForVersion(dir, "connectors/widget", b, "")
	if dirA == dirB {
		t.Fatalf("BinDirForVersion must not alias %q and %q onto the same directory, both got %q", a, b, dirA)
	}
}

// TestSanitizeVersionDirSequentialMigrationLeavesNewBinaryInstalled is the
// reviewer's "config migration between those two sources" scenario: a
// config is first resolved with the plugin sourced such that its resolved
// tag text is "a/b/widget/v1.0.0", then the config is changed to a
// DIFFERENT source whose resolved tag text is "a_b/widget/v1.0.0" — the
// exact pair the old "/" "\\" -> "_" replacer aliased onto one directory.
// Before the fix, installing pass 2's version into "the same" directory as
// pass 1's, then pruning pass 1's (now unreferenced) version, would
// RemoveAll the directory pass 2's binary was JUST installed into — since
// they were, bug-for-bug, the same directory. After the fix the two
// versions occupy distinct directories, so a prune of the first never
// touches the second.
func TestSanitizeVersionDirSequentialMigrationLeavesNewBinaryInstalled(t *testing.T) {
	dir := t.TempDir()
	s := LoadInstallState(dir)

	const key = "connectors/widget"
	oldTag := "a/b/widget/v1.0.0"
	newTag := "a_b/widget/v1.0.0"

	// Pass 1: install from the first source.
	oldRec := Installed{Key: key, Kind: "connector", Name: "widget", Resolved: oldTag,
		Sha256: "old-sha", Path: filepath.Join(BinDirForVersion(dir, key, oldTag, ""), "conductor-widget")}
	if err := os.MkdirAll(filepath.Dir(oldRec.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldRec.Path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Put(oldRec)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Pass 2: the config migrates to the second source, which resolves to
	// the OTHER half of the reviewer's colliding pair. Install it, then
	// prune — the first source's version is no longer referenced.
	newRec := Installed{Key: key, Kind: "connector", Name: "widget", Resolved: newTag,
		Sha256: "new-sha", Path: filepath.Join(BinDirForVersion(dir, key, newTag, ""), "conductor-widget")}
	if err := os.MkdirAll(filepath.Dir(newRec.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRec.Path, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Put(newRec)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	keep := map[VersionKey]bool{{Key: key, Resolved: newTag}: true}
	dropped, err := s.GCVersions(keep)
	if err != nil {
		t.Fatalf("GCVersions: %v", err)
	}
	if len(dropped) != 1 || dropped[0].Resolved != oldTag {
		t.Fatalf("expected exactly the old version dropped, got %+v", dropped)
	}

	if _, err := os.Stat(newRec.Path); err != nil {
		t.Fatalf("the newly-installed binary must survive pruning the old source's version: %v", err)
	}
	got, err := os.ReadFile(newRec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("the surviving binary must be the NEW one, got %q", got)
	}
	if _, ok := s.GetVersion(key, oldTag, ""); ok {
		t.Fatal("the old, unreferenced version must be gone from install state")
	}
	if v, ok := s.GetVersion(key, newTag, ""); !ok || v.Sha256 != "new-sha" {
		t.Fatalf("the new version must still be installed and untouched: %+v, ok=%v", v, ok)
	}
}

// TestSanitizeVersionDirPropertyDistinctInputsGiveDistinctOutputs is
// property-style coverage: over a varied set of inputs — ordinary tags,
// component-prefixed tags, inputs that collide under the OLD "/" "\\" -> "_"
// scheme, percent signs, dots, dot-dot, empty, control characters, and
// mixed separators — every pair of DISTINCT inputs must map to DISTINCT
// outputs.
func TestSanitizeVersionDirPropertyDistinctInputsGiveDistinctOutputs(t *testing.T) {
	inputs := []string{
		"v1.0.0",
		"v1.2.3",
		"widget/v1.0.0",
		"a/b/widget/v1.0.0",
		"a_b/widget/v1.0.0",
		"a_b_widget_v1.0.0",
		"a\\b\\widget\\v1.0.0",
		"a/b_widget/v1.0.0",
		".",
		"..",
		"...",
		"",
		"%2E",
		"%2E%2E",
		"%",
		"%%",
		"100%",
		"connectors/widget/v1.0.0",
		"connectors_widget_v1.0.0",
		"v1.0.0-alpha",
		"v1.0.0-alpha/beta",
		"weird\x00byte",
		"weird\x01byte",
		"a b/c",
		"a/b/c",
		"a/b//c",
		"UPPER/lower",
	}
	seen := make(map[string]string, len(inputs))
	for _, in := range inputs {
		out := sanitizeVersionDir(in)
		if out == "." || out == ".." {
			t.Fatalf("sanitizeVersionDir(%q) = %q must never be a bare %q", in, out, out)
		}
		if prevIn, ok := seen[out]; ok && prevIn != in {
			t.Fatalf("collision: sanitizeVersionDir(%q) and sanitizeVersionDir(%q) both produced %q", prevIn, in, out)
		}
		seen[out] = in
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

// TestRemoveVersionDirRefusesEscapingPath closes finding 5's test gap:
// sanitizeVersionDir is now injective and refuses "." / ".." (finding 1),
// so a REAL GCVersions pass can no longer actually hand removeVersionDir a
// dir that escapes keyDir — which is exactly why a test that only ever
// drives this through GCVersions (sanitizeVersionDir always runs first)
// could have the isStrictlyWithin guard deleted from GCVersions entirely
// and still pass. Calling removeVersionDir DIRECTLY with a hand-built
// (keyDir, dir) pair that bypasses sanitization altogether proves the
// guard still does real, independent work as defense in depth — not work
// that upstream sanitization has already made redundant.
func TestRemoveVersionDirRefusesEscapingPath(t *testing.T) {
	base := t.TempDir()
	keyDir := filepath.Join(base, "connectors", "widget")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A sibling plugin's directory — stands in for "everything outside this
	// one plugin version's own directory" — populated with a file so a
	// wrongful RemoveAll is detectable.
	sentinel := filepath.Join(base, "connectors", "other", "v1.0.0")
	if err := os.MkdirAll(sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sentinel, "conductor-other"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	// dir escapes keyDir entirely — exactly the shape sanitizeVersionDir is
	// now relied upon to never produce (finding 1), reconstructed by hand
	// here so removeVersionDir is tested on its OWN, independent of that
	// upstream guarantee ever holding.
	escaping := filepath.Join(keyDir, "..", "other", "v1.0.0")

	if err := removeVersionDir(keyDir, escaping); err == nil {
		t.Fatal("removeVersionDir must refuse a path outside keyDir, not silently remove it")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("a target outside keyDir must survive a refused removeVersionDir call: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sentinel, "conductor-other")); err != nil {
		t.Fatalf("its contents must survive too: %v", err)
	}
}

// TestRemoveVersionDirRemovesARealChild is removeVersionDir's happy path:
// a genuine child of keyDir is removed, so the guard above is a REFUSAL of
// an escape, never a refusal of ordinary GC.
func TestRemoveVersionDirRemovesARealChild(t *testing.T) {
	base := t.TempDir()
	keyDir := filepath.Join(base, "connectors", "widget")
	child := filepath.Join(keyDir, "v1.0.0")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "conductor-widget"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeVersionDir(keyDir, child); err != nil {
		t.Fatalf("removeVersionDir: %v", err)
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("a genuine child must actually be removed, stat err = %v", err)
	}
}
