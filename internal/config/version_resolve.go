package config

import "strings"

// BestMatch and SatisfiesConstraint are the exported entry points to the shared
// version-constraint resolver, for other packages (e.g. plugin remote fetch).
func BestMatch(tags []string, prefix, constraint string) (string, bool) {
	return bestMatch(tags, prefix, constraint)
}

// SatisfiesConstraint reports whether version satisfies an AND-ed constraint.
func SatisfiesConstraint(version, constraint string) bool {
	return satisfiesConstraint(version, constraint)
}

// Shared version-constraint resolution for sourced dependencies (packs and
// plugins). A `version:` constraint is Terraform/gems style — comma- or
// space-separated parts, AND-ed — and drives which git tag a dependency
// resolves to. Reuses the semver primitives in pack_version.go.

// constraintParts splits an AND-ed constraint into op+version parts, tolerating
// both conventions: comma- or space-separated ANDs ("">=1.2, <2.0"", ">=1.2
// <2.0"), and a space between an operator and its version ("~> 1.2", ">= 1.2").
// A lone-operator token reattaches to the following version token.
func constraintParts(c string) []string {
	toks := strings.FieldsFunc(c, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var parts []string
	for i := 0; i < len(toks); i++ {
		op, rest := splitOp(toks[i])
		if op != "" && rest == "" && i+1 < len(toks) {
			parts = append(parts, op+toks[i+1]) // "~>" "1.2" -> "~>1.2"
			i++
			continue
		}
		parts = append(parts, toks[i])
	}
	return parts
}

// splitOp is splitConstraintOp plus ~> (pessimistic / "twiddle-wakka").
func splitOp(s string) (op, rest string) {
	for _, o := range []string{"~>", ">=", "<=", "==", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(s, o) {
			return o, strings.TrimSpace(s[len(o):])
		}
	}
	return "", s
}

// semverComponents counts the dotted components a version literal specified
// (v1.2 → 2, 1.2.3 → 3), before any pre-release/build metadata. ~> and ~ need
// it to know which component is allowed to grow.
func semverComponents(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return 0
	}
	return strings.Count(s, ".") + 1
}

// matchOp reports whether `have` satisfies `op want`, where want was written
// with `comps` dotted components (matters for ~> and ~). This is the single
// boolean core both the requires.conductor check and constraint resolution use.
func matchOp(have semver, op string, want semver, comps int) bool {
	cmp := compareSemver(have, want)
	switch op {
	case ">=", "": // bare version means >=
		return cmp >= 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case "<":
		return cmp < 0
	case "=", "==":
		return cmp == 0
	case "^": // compatible-with, npm-style (0.x treated as unstable)
		switch {
		case want.major > 0:
			return have.major == want.major && cmp >= 0
		case want.minor > 0:
			return have.major == 0 && have.minor == want.minor && cmp >= 0
		default:
			return have.major == 0 && have.minor == 0 && have.patch == want.patch
		}
	case "~": // approximately: same major.minor, >= want
		return have.major == want.major && have.minor == want.minor && cmp >= 0
	case "~>": // pessimistic: fix all but the last written component, >= want
		if cmp < 0 {
			return false
		}
		if comps >= 3 { // ~> 1.2.3  => >=1.2.3, <1.3.0
			return have.major == want.major && have.minor == want.minor
		}
		return have.major == want.major // ~> 1 / ~> 1.2  => same major, <next major
	default:
		return false
	}
}

// satisfiesConstraint reports whether a concrete version satisfies an AND-ed
// constraint. An empty constraint matches anything; an unparseable version or
// constraint part does not match.
func satisfiesConstraint(version, constraint string) bool {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return true
	}
	have, err := parseSemver(version)
	if err != nil {
		return false
	}
	for _, part := range constraintParts(constraint) {
		op, rest := splitOp(part)
		want, err := parseSemver(rest)
		if err != nil {
			return false
		}
		if !matchOp(have, op, want, semverComponents(rest)) {
			return false
		}
	}
	return true
}

// bestMatch returns the highest tag that satisfies the constraint. Tags may
// carry a prefix (e.g. "sentry/") stripped before the semver parse; tags that
// don't parse as semver are skipped. ok=false when nothing matches.
func bestMatch(tags []string, prefix, constraint string) (string, bool) {
	var bestTag string
	var best semver
	for _, t := range tags {
		if prefix != "" && !strings.HasPrefix(t, prefix) {
			continue // a component-prefixed query ignores other components' tags
		}
		raw := strings.TrimPrefix(t, prefix)
		sv, err := parseSemver(raw)
		if err != nil {
			continue
		}
		if !satisfiesConstraint(raw, constraint) {
			continue
		}
		if bestTag == "" || compareSemver(sv, best) > 0 {
			bestTag, best = t, sv
		}
	}
	return bestTag, bestTag != ""
}
