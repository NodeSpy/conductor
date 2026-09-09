package config

import (
	"fmt"
	"strconv"
	"strings"
)

// checkConductorConstraint validates a pack's requires.conductor constraint
// (§16) against the running daemon version. An empty constraint or a dev build
// ("", "dev") skips the check. Supported forms mirror the issue examples:
//
//	">=0.8"  ">0.8.1"  "<=1.2"  "<2.0"  "=1.0.0"  "^1.2"  "~1.2.3"  "1.0"
//
// Space-separated constraints are AND-ed (">=0.8 <2.0"). An unrecognized
// constraint is a named error rather than a silent pass — a pack that declares
// something we can't evaluate should not be assumed compatible.
func checkConductorConstraint(constraint, version string) error {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	if version == "" || version == "dev" {
		return nil // unversioned local/dev build: don't gate
	}
	have, err := parseSemver(version)
	if err != nil {
		return nil // unparseable running version: don't gate on it
	}
	for _, part := range strings.Fields(constraint) {
		if err := checkOneConstraint(part, have, version, constraint); err != nil {
			return err
		}
	}
	return nil
}

func checkOneConstraint(part string, have semver, version, full string) error {
	op, rest := splitOp(part)
	want, err := parseSemver(rest)
	if err != nil {
		return fmt.Errorf("requires.conductor %q: unrecognized constraint %q", full, part)
	}
	if !matchOp(have, op, want, semverComponents(rest)) {
		return fmt.Errorf("requires conductor %s but this daemon is %s", full, version)
	}
	return nil
}

type semver struct{ major, minor, patch int }

func parseSemver(s string) (semver, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Drop any pre-release/build metadata.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return semver{}, fmt.Errorf("empty version")
	}
	parts := strings.Split(s, ".")
	var v semver
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return v, err
	}
	if len(parts) > 1 {
		if v.minor, err = strconv.Atoi(parts[1]); err != nil {
			return v, err
		}
	}
	if len(parts) > 2 {
		if v.patch, err = strconv.Atoi(parts[2]); err != nil {
			return v, err
		}
	}
	return v, nil
}

func compareSemver(a, b semver) int {
	if a.major != b.major {
		return sign(a.major - b.major)
	}
	if a.minor != b.minor {
		return sign(a.minor - b.minor)
	}
	return sign(a.patch - b.patch)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
