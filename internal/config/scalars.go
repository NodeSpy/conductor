package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a Go duration string
// ("30m", "720h") or a plain number of seconds.
type Duration time.Duration

// UnmarshalYAML parses a duration string or integer seconds.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err == nil && s != "" {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var secs int64
	if err := n.Decode(&secs); err != nil {
		return fmt.Errorf("duration must be a string or seconds: %w", err)
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String renders the duration (or empty when zero).
func (d Duration) String() string {
	if d == 0 {
		return ""
	}
	return time.Duration(d).String()
}

// ByteSize is a size in bytes that unmarshals from strings like "50MB", "1GB",
// "512kb", or a plain integer number of bytes.
type ByteSize int64

var unitTable = []struct {
	suffix string
	mult   int64
}{
	{"gb", 1 << 30}, {"g", 1 << 30},
	{"mb", 1 << 20}, {"m", 1 << 20},
	{"kb", 1 << 10}, {"k", 1 << 10},
	{"b", 1},
}

// UnmarshalYAML parses a byte-size string or a plain integer.
func (s *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	var i int64
	if err := n.Decode(&i); err == nil {
		*s = ByteSize(i)
		return nil
	}
	var str string
	if err := n.Decode(&str); err != nil {
		return fmt.Errorf("byte size must be a string or integer: %w", err)
	}
	str = strings.TrimSpace(strings.ToLower(str))
	for _, u := range unitTable {
		if strings.HasSuffix(str, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(str, u.suffix))
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return fmt.Errorf("invalid byte size %q: %w", str, err)
			}
			*s = ByteSize(f * float64(u.mult))
			return nil
		}
	}
	return fmt.Errorf("invalid byte size %q", str)
}

// Bytes returns the size in bytes.
func (s ByteSize) Bytes() int64 { return int64(s) }
