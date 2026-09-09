package config

import "testing"

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
