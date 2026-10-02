package main

import (
	"errors"
	"testing"
)

// The release check reports changed only when the newest published tag
// moves, and surfaces a lookup failure.
func TestReleaseCheckerReportsMovement(t *testing.T) {
	tags := []string{"v1.0.0", "v1.0.0", "v1.1.0"}
	i := 0
	rc := &releaseChecker{latest: func() (string, error) { i++; return tags[i-1], nil }}
	for n, want := range []bool{true, false, true} {
		tag, changed, err := rc.check()
		if err != nil || changed != want || tag != tags[n] {
			t.Fatalf("check %d: tag=%q changed=%v err=%v, want changed=%v", n, tag, changed, err, want)
		}
	}
	bad := &releaseChecker{latest: func() (string, error) { return "", errors.New("unreachable") }}
	if _, _, err := bad.check(); err == nil {
		t.Fatal("a failed lookup must be reported")
	}
}

func TestNewerRelease(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		changed bool
		running string
		want    bool
	}{
		{"nothing new", "", false, "v0.5.23", false},
		{"changed but same tag", "v0.5.23", true, "v0.5.23", false},
		{"changed and newer", "v0.5.24", true, "v0.5.23", true},
		{"changed but empty tag", "", true, "v0.5.23", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newerRelease(c.tag, c.changed, c.running); got != c.want {
				t.Fatalf("newerRelease(%q,%v,%q) = %v, want %v", c.tag, c.changed, c.running, got, c.want)
			}
		})
	}
}
