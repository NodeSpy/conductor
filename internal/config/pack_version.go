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
	have, err := parseSemver(trimVersionPrefix(version))
	if err != nil {
		// Deliberately NOT silent. Returning nil here meant an
		// incompatible version loaded clean with no signal at all — which
		// is worse than either gating or complaining. "" and dev builds
		// are handled above; anything else that reaches here is a version
		// string we were handed and cannot judge, and the operator should
		// know that the constraint they wrote is not being enforced.
		return errUngatableVersion{version: version, constraint: constraint}
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

// errUngatableVersion reports a version string the resolver could not
// parse, so a caller can decide between failing and warning. A pack load
// warns (degraded-boot: an unjudgeable version must not crash-loop a box);
// the message names the string so it can be fixed.
type errUngatableVersion struct{ version, constraint string }

func (e errUngatableVersion) Error() string {
	return fmt.Sprintf("version %q cannot be parsed as semver, so the constraint %q is NOT enforced", e.version, e.constraint)
}

// Ungatable reports whether an error is the unparseable-version case.
func Ungatable(err error) bool {
	_, ok := err.(errUngatableVersion)
	return ok
}

// trimVersionPrefix drops a monorepo tag's component prefix
// ("jira-connector/v1.0.0" -> "v1.0.0"). A plugin released from a
// subdirectory keeps that prefix in its tag, and without this the whole
// version reads as unparseable — which is how an incompatible connector
// slipped past its constraint entirely.
func trimVersionPrefix(v string) string {
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[i+1:]
	}
	return v
}

// semver is major.minor.patch plus the raw pre-release identifier string
// (pre), when present — "" for a release. pre is kept ONLY for precedence
// ordering (compareVersionPrecedence, below): matchOp/satisfiesConstraint's
// range matching (>=, <, ~>, …) still compares major.minor.patch alone via
// compareSemver, exactly as before this field existed — a `version:
// >=1.2.3` constraint does not newly start rejecting "1.2.3-rc1" just
// because precedence ordering elsewhere now knows releases beat
// pre-releases of the same core. Build metadata ("+…") carries no
// precedence under semver at all and is dropped entirely, never retained
// anywhere.
type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(s string) (semver, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Build metadata ("+…") is dropped first and unconditionally — it carries
	// no precedence under semver, unlike a pre-release ("-…"), which does.
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.Index(s, "-"); i >= 0 {
		pre = s[i+1:]
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
	v.pre = pre
	return v, nil
}

// compareSemver orders by major.minor.patch ALONE — pre-release is not
// considered, by design: this is what matchOp's range operators (>=, <,
// ~>, ^, …) compare with, and a constraint must keep matching a
// pre-release of a satisfying core exactly as it always has. Ordering that
// DOES care which of two same-core versions is "newer" (one a release, one
// a pre-release) uses compareVersionPrecedence instead.
func compareSemver(a, b semver) int {
	if a.major != b.major {
		return sign(a.major - b.major)
	}
	if a.minor != b.minor {
		return sign(a.minor - b.minor)
	}
	return sign(a.patch - b.patch)
}

// compareVersionPrecedence orders two parsed versions by full semver
// precedence (semver.org §11), the ONE comparator config.CompareVersions
// and bestMatch both order by (finding 3, MEDIUM): major.minor.patch first
// (compareSemver); when those tie, a release (no pre-release suffix)
// always beats a pre-release of the identical core version — "v1.2.3" >
// "v1.2.3-alpha" — never a coin flip decided by iteration order or byte
// comparison of the tag text, which is what let bestMatch and
// CompareVersions disagree on this exact case before; when BOTH are
// pre-releases of the identical core, their dot-separated identifiers are
// compared per semver's own rules where feasible (comparePrerelease).
// Equal precedence (0) is not a claim the two tags are interchangeable —
// the caller breaks a genuine tie by the full tag text as a last resort
// (CompareVersions, and bestMatch via it).
func compareVersionPrecedence(a, b semver) int {
	if c := compareSemver(a, b); c != 0 {
		return c
	}
	switch {
	case a.pre == "" && b.pre == "":
		return 0
	case a.pre == "": // release beats pre-release of the same core
		return 1
	case b.pre == "":
		return -1
	default:
		return comparePrerelease(a.pre, b.pre)
	}
}

// comparePrerelease orders two pre-release identifier strings ("alpha",
// "rc.1", "beta.11") per semver precedence where feasible: split on ".",
// compare each pair of identifiers left to right (comparePrereleaseIdent),
// and when every compared pair ties, the LONGER identifier list has higher
// precedence ("1.0.0-alpha" < "1.0.0-alpha.1" — more fields narrows a
// pre-release further along toward the release it precedes).
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePrereleaseIdent(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return sign(len(as) - len(bs))
}

// comparePrereleaseIdent orders one pair of dot-separated pre-release
// identifiers per semver precedence: numeric identifiers compare
// numerically; a numeric identifier always has LOWER precedence than an
// alphanumeric one (semver.org §11.4.3); two alphanumeric identifiers
// compare by plain ASCII byte order (semver's own "ASCII sort order" rule,
// the feasible approximation of full Unicode collation).
func comparePrereleaseIdent(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return sign(an - bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
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
