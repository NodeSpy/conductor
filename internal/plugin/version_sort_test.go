package plugin

import "testing"

// TestAllVersionsSortsNumericallyNotLexically is finding 2 (MEDIUM):
// InstallState.Get and AllVersions used to sort installed records by
// Resolved with a plain lexical byte compare, which orders "v1.10.0" BEFORE
// "v1.9.0" — "1.1" < "1.9" character by character — so the moment a
// plugin's installed versions crossed a double-digit component, Get()
// silently handed every caller the WRONG "highest" record. AllVersions must
// sort with the same semver comparison config.BestMatch resolves
// constraints with instead.
func TestAllVersionsSortsNumericallyNotLexically(t *testing.T) {
	s := LoadInstallState(t.TempDir())
	s.Put(Installed{Key: "connectors/widget", Resolved: "v1.9.0", Sha256: "a"})
	s.Put(Installed{Key: "connectors/widget", Resolved: "v1.10.0", Sha256: "b"})

	all := s.AllVersions("connectors/widget")
	if len(all) != 2 {
		t.Fatalf("AllVersions = %+v, want 2 records", all)
	}
	if all[0].Resolved != "v1.9.0" || all[1].Resolved != "v1.10.0" {
		t.Fatalf("AllVersions ascending order = [%s, %s], want [v1.9.0, v1.10.0] (numeric, not lexical)", all[0].Resolved, all[1].Resolved)
	}

	got, ok := s.Get("connectors/widget")
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.Resolved != "v1.10.0" || got.Sha256 != "b" {
		t.Fatalf("Get (the highest installed) = %+v, want v1.10.0/sha b", got)
	}
}

// TestAllVersionsOrdersPreReleaseDeterministically covers the pre-release
// edge of finding 2: parseSemver deliberately drops "-"/"+" metadata (the
// same rule requires.conductor's own gate uses), so "v1.2.3" and
// "v1.2.3-rc1" compare numerically EQUAL. The sort must still be total and
// repeatable rather than depending on insertion order — config.CompareVersions
// documents the tiebreak as a plain byte-wise compare of the full tag, which
// this pins down and locks in.
func TestAllVersionsOrdersPreReleaseDeterministically(t *testing.T) {
	s := LoadInstallState(t.TempDir())
	s.Put(Installed{Key: "connectors/widget", Resolved: "v1.2.3-rc1"})
	s.Put(Installed{Key: "connectors/widget", Resolved: "v1.2.3"})

	for i := 0; i < 5; i++ {
		all := s.AllVersions("connectors/widget")
		if len(all) != 2 {
			t.Fatalf("round %d: AllVersions = %+v, want 2 records", i, all)
		}
		// "v1.2.3" < "v1.2.3-rc1" byte-wise (shorter string, common prefix) —
		// the documented, deterministic tiebreak, exercised repeatedly to
		// prove it never flips.
		if all[0].Resolved != "v1.2.3" || all[1].Resolved != "v1.2.3-rc1" {
			t.Fatalf("round %d: order = [%s, %s], want a stable [v1.2.3, v1.2.3-rc1]", i, all[0].Resolved, all[1].Resolved)
		}
	}
}

// TestAllVersionsToleratesNonSemverSnapshotKey covers the other edge of
// finding 2: a record whose Resolved is not a parseable semver tag at all
// (a hand-written local-snapshot-style key, or a record a pre-versioning
// install state wrote) must not panic AllVersions/Get, and must still sort
// deterministically against a real semver sibling.
func TestAllVersionsToleratesNonSemverSnapshotKey(t *testing.T) {
	s := LoadInstallState(t.TempDir())
	s.Put(Installed{Key: "connectors/widget", Resolved: "local-abc123"})
	s.Put(Installed{Key: "connectors/widget", Resolved: "v1.0.0"})

	var all []Installed
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("AllVersions panicked on a non-semver Resolved value: %v", r)
			}
		}()
		all = s.AllVersions("connectors/widget")
	}()
	if len(all) != 2 {
		t.Fatalf("AllVersions = %+v, want 2 records", all)
	}
	// "local-abc123" < "v1.0.0" byte-wise ('l' < 'v') — documented fallback,
	// not a semantic "older" claim.
	if all[0].Resolved != "local-abc123" || all[1].Resolved != "v1.0.0" {
		t.Fatalf("order = [%s, %s], want a stable [local-abc123, v1.0.0]", all[0].Resolved, all[1].Resolved)
	}
}
