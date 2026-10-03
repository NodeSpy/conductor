package sourcekit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseDuration reads a duration the way conductor's config does: a Go
// duration string ("30m", "720h"), optionally led by a day unit ("7d",
// "1d12h"), or a number of seconds (an integer, or a YAML/JSON number).
// A connector reading an interval from its connection config should use it,
// so `every: 7d` and `interval: 3600` mean what they mean everywhere else.
func ParseDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case string:
		return parseDurationString(x)
	case int:
		return time.Duration(x) * time.Second, nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("want a duration string or seconds, got %T", v)
}

// maxDays keeps days*24h inside time.Duration (it would wrap past ~106751).
const maxDays = int(math.MaxInt64 / int64(24*time.Hour))

func parseDurationString(s string) (time.Duration, error) {
	if i := strings.IndexByte(s, 'd'); i > 0 {
		if days, err := strconv.Atoi(s[:i]); err == nil {
			if days < 0 || days > maxDays {
				return 0, fmt.Errorf("duration %q: day count out of range", s)
			}
			rest := time.Duration(0)
			if tail := s[i+1:]; tail != "" {
				r, err := time.ParseDuration(tail)
				if err != nil {
					return 0, err
				}
				rest = r
			}
			d := time.Duration(days) * 24 * time.Hour
			if rest > 0 && d > time.Duration(math.MaxInt64)-rest {
				return 0, fmt.Errorf("duration %q: out of range", s)
			}
			return d + rest, nil
		}
	}
	return time.ParseDuration(s)
}
