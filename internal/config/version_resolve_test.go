package config

import (
	"sort"
	"testing"
)

func TestSatisfiesConstraint(t *testing.T) {
	cases := []struct {
		version, constraint string
		want                bool
	}{
		{"1.2.3", "", true}, // empty = any
		{"1.2.3", ">=1.0", true},
		{"0.9.0", ">=1.0", false},
		{"1.5.0", ">=1.2, <2.0", true},
		{"2.0.0", ">=1.2, <2.0", false},
		{"1.5.0", ">=1.2 <2.0", true}, // space-separated AND
		{"1.2.0", "^1.0", true},
		{"2.0.0", "^1.0", false},
		{"1.2.5", "~1.2", true}, // same major.minor, >=
		{"1.3.0", "~1.2", false},
		// ~> (pessimistic)
		{"1.9.0", "~> 1.2", true}, // ~>1.2 => >=1.2, <2.0
		{"2.0.0", "~> 1.2", false},
		{"1.2.9", "~> 1.2.3", true}, // ~>1.2.3 => >=1.2.3, <1.3.0
		{"1.3.0", "~> 1.2.3", false},
		{"1.2.2", "~> 1.2.3", false}, // below the floor
		{"v1.2.3", ">=1.0", true},    // v-prefix tolerated
	}
	for _, c := range cases {
		if got := satisfiesConstraint(c.version, c.constraint); got != c.want {
			t.Errorf("satisfiesConstraint(%q, %q) = %v, want %v", c.version, c.constraint, got, c.want)
		}
	}
}

func TestBestMatch(t *testing.T) {
	tags := []string{"sentry/v1.0.0", "sentry/v1.1.0", "sentry/v1.2.0", "sentry/v2.0.0", "sentry/nightly"}
	// ~> 1.1 picks the highest 1.x >= 1.1, not 2.0.0.
	if got, ok := bestMatch(tags, "sentry/", "~> 1.1"); !ok || got != "sentry/v1.2.0" {
		t.Fatalf("~>1.1: got %q ok=%v, want sentry/v1.2.0", got, ok)
	}
	// >=1.0 with no upper bound picks the newest overall.
	if got, ok := bestMatch(tags, "sentry/", ">=1.0"); !ok || got != "sentry/v2.0.0" {
		t.Fatalf(">=1.0: got %q ok=%v, want sentry/v2.0.0", got, ok)
	}
	// No match.
	if _, ok := bestMatch(tags, "sentry/", ">=3.0"); ok {
		t.Fatal(">=3.0 should not match")
	}
	// Empty prefix.
	if got, ok := bestMatch([]string{"v1.0.0", "v1.1.0"}, "", ""); !ok || got != "v1.1.0" {
		t.Fatalf("empty prefix/constraint: got %q ok=%v, want v1.1.0", got, ok)
	}
}

// TestCompareVersionsReleaseBeatsPrerelease is the reviewer's exact case
// for finding 3 (MEDIUM): major.minor.patch ties between a release and a
// pre-release of the identical core ("v1.2.3" vs "v1.2.3-alpha"), and the
// release must win — in BOTH directions, so the comparator is a real
// antisymmetric order, not a one-sided special case.
func TestCompareVersionsReleaseBeatsPrerelease(t *testing.T) {
	if c := CompareVersions("v1.2.3", "v1.2.3-alpha"); c <= 0 {
		t.Fatalf("CompareVersions(v1.2.3, v1.2.3-alpha) = %d, want > 0 (release beats pre-release)", c)
	}
	if c := CompareVersions("v1.2.3-alpha", "v1.2.3"); c >= 0 {
		t.Fatalf("CompareVersions(v1.2.3-alpha, v1.2.3) = %d, want < 0", c)
	}
	if c := CompareVersions("v1.2.3", "v1.2.3"); c != 0 {
		t.Fatalf("CompareVersions(v1.2.3, v1.2.3) = %d, want 0", c)
	}
}

// TestBestMatchReleaseBeatsPrerelease is bestMatch's half of finding 3: the
// SAME comparator CompareVersions uses must decide bestMatch's tie too, so
// an unconstrained resolve over a release and a pre-release tagging the
// identical core picks the release — regardless of which one the tags
// slice lists first (order-independence is covered separately below).
func TestBestMatchReleaseBeatsPrerelease(t *testing.T) {
	if got, ok := bestMatch([]string{"v1.2.3-alpha", "v1.2.3"}, "", ""); !ok || got != "v1.2.3" {
		t.Fatalf("got %q ok=%v, want v1.2.3 (release beats pre-release)", got, ok)
	}
	if got, ok := bestMatch([]string{"v1.2.3", "v1.2.3-alpha"}, "", ""); !ok || got != "v1.2.3" {
		t.Fatalf("got %q ok=%v, want v1.2.3 (release beats pre-release, reversed input order)", got, ok)
	}
}

// TestBestMatchOrderIndependent is finding 3's core claim: bestMatch picks
// the max under ONE total order regardless of input order. Before the fix,
// a semver TIE (compareSemver == 0, pre-release dropped by parseSemver)
// left bestMatch's "replace only on strictly greater" running-max keeping
// whichever tied tag it happened to visit FIRST — an order-dependent
// result for exactly the cases CompareVersions (text tie-break) and
// bestMatch (first-seen) could disagree on.
func TestBestMatchOrderIndependent(t *testing.T) {
	tags := []string{"v1.2.3-alpha", "v1.2.3-beta", "v1.2.3", "v1.0.0", "v1.2.3-alpha.1"}
	want, ok := bestMatch(tags, "", "")
	if !ok {
		t.Fatal("expected a match")
	}
	if want != "v1.2.3" {
		t.Fatalf("got %q, want v1.2.3 (the release beats every pre-release of its core)", want)
	}
	// Every permutation (a handful of fixed shuffles, not exhaustive) must
	// land on the SAME tag — the defining property of a true total order's
	// max, which a first-seen-wins tie-break does not have.
	perms := [][]string{
		{"v1.2.3", "v1.2.3-alpha", "v1.2.3-beta", "v1.0.0", "v1.2.3-alpha.1"},
		{"v1.0.0", "v1.2.3-beta", "v1.2.3-alpha.1", "v1.2.3-alpha", "v1.2.3"},
		{"v1.2.3-alpha.1", "v1.2.3-alpha", "v1.0.0", "v1.2.3", "v1.2.3-beta"},
		{"v1.2.3-beta", "v1.2.3", "v1.2.3-alpha", "v1.2.3-alpha.1", "v1.0.0"},
	}
	for i, p := range perms {
		got, ok := bestMatch(p, "", "")
		if !ok || got != want {
			t.Fatalf("permutation %d (%v): got %q ok=%v, want %q", i, p, got, ok, want)
		}
	}
}

// TestBestMatchOrdersPrereleasesByIdentifier covers the "pre-releases
// compare by suffix per semver rules where feasible" half of finding 3:
// with no release present, the highest PRE-RELEASE under semver's own
// identifier precedence rules wins, consistently regardless of order.
func TestBestMatchOrdersPrereleasesByIdentifier(t *testing.T) {
	// alpha < beta (alphanumeric, ASCII order); alpha < alpha.1 (a longer
	// identifier list outranks being a strict prefix of it, semver §11).
	tags := []string{"v1.2.3-alpha", "v1.2.3-alpha.1", "v1.2.3-beta"}
	got, ok := bestMatch(tags, "", "")
	if !ok || got != "v1.2.3-beta" {
		t.Fatalf("got %q ok=%v, want v1.2.3-beta (highest pre-release by identifier precedence)", got, ok)
	}
	// Reversed order must not change the answer.
	rev := []string{"v1.2.3-beta", "v1.2.3-alpha.1", "v1.2.3-alpha"}
	// no-op guard: rev really is the reverse of tags, so this exercises the
	// opposite traversal order.
	if rev[0] != tags[2] || rev[2] != tags[0] {
		t.Fatal("test setup: rev must be tags reversed")
	}
	if got, ok := bestMatch(rev, "", ""); !ok || got != "v1.2.3-beta" {
		t.Fatalf("reversed order: got %q ok=%v, want v1.2.3-beta", got, ok)
	}
}

// TestGetAllVersionsConsistentWithBestMatch is finding 3's closing
// requirement: Get/AllVersions (config.CompareVersions-sorted) and
// BestMatch must agree on which tag is "highest" — the whole point of
// defining one shared comparator. AllVersions-ascending's LAST element
// must always equal bestMatch's unconstrained pick over the identical set.
func TestGetAllVersionsConsistentWithBestMatch(t *testing.T) {
	sets := [][]string{
		{"v1.0.0", "v1.2.3-alpha", "v1.2.3"},
		{"v1.2.3-alpha", "v1.2.3-alpha.1", "v1.2.3-beta"},
		{"v1.9.0", "v1.10.0", "v1.2.0"},
		{"v1.0.0", "v2.0.0-alpha"},
	}
	for _, tags := range sets {
		sorted := append([]string(nil), tags...)
		sort.Slice(sorted, func(i, j int) bool { return CompareVersions(sorted[i], sorted[j]) < 0 })
		highestBySort := sorted[len(sorted)-1]

		highestByMatch, ok := bestMatch(tags, "", "")
		if !ok {
			t.Fatalf("%v: expected a match", tags)
		}
		if highestByMatch != highestBySort {
			t.Fatalf("%v: CompareVersions-sort picks %q as highest, bestMatch picks %q — must agree", tags, highestBySort, highestByMatch)
		}
	}
}
