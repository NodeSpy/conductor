package sourcekit

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want time.Duration
	}{
		{"30m", 30 * time.Minute}, {"7d", 7 * 24 * time.Hour}, {"1d12h", 36 * time.Hour},
		{3600, time.Hour}, {int64(60), time.Minute}, {float64(90), 90 * time.Second},
	} {
		got, err := ParseDuration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseDuration(%v) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []any{"soon", "7dx", true, nil, "999999999d", "-1d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%v) accepted", bad)
		}
	}
}
