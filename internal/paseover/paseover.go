// Package paseover parses `paseo --version` and answers version-gate
// questions about paseo's command surface.
//
// paseo's surface changed at 0.9: it gained multi-home daemons and the
// `--home <path>` flag that selects one (default ~/.paseo). PRE-0.9 paseo has
// NO `--home` flag — passing it there is an "unknown option" error that breaks
// EVERY command. So conductor detects the paseo version and only emits
// `--home` on paseo >= 0.9.
//
// This lives in its own package because BOTH callers of paseo need the gate
// and neither may import the other: internal/dispatch launches agents, and
// internal/models enumerates providers for model resolution (dispatch already
// imports models, so the dependency can only run one way). Before the split,
// only dispatch had it — and the models-side paseo lister silently talked to
// the DEFAULT home while dispatch talked to the configured one, so discovery
// came back empty on every box with a `runtimes.paseo.home:`.
package paseover

import (
	"strconv"
	"strings"
)

// HomeFlagMajor/Minor/Patch is the first paseo release carrying `--home`.
const (
	HomeFlagMajor = 0
	HomeFlagMinor = 9
	HomeFlagPatch = 0
)

// Semver is a parsed `paseo --version`. Known is false when detection failed
// or the output could not be parsed — in which case AtLeast assumes the NEWEST
// behavior (the fleet trends current, and a home is only ever configured on
// 0.9+, so assuming new never regresses a real pre-0.9 box).
type Semver struct {
	Major, Minor, Patch int
	Known               bool
}

// String renders the detected version for logs ("0.9.1", or "unknown").
func (v Semver) String() string {
	if !v.Known {
		return "unknown"
	}
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// AtLeast reports whether the detected version is >= maj.min.patch. An unknown
// version is treated as newest (returns true), so a detection hiccup does not
// silently strip `--home` from a 0.9+ call that needs it.
func (v Semver) AtLeast(maj, min, patch int) bool {
	if !v.Known {
		return true
	}
	if v.Major != maj {
		return v.Major > maj
	}
	if v.Minor != min {
		return v.Minor > min
	}
	return v.Patch >= patch
}

// HasHomeFlag reports whether this paseo understands `--home`.
func (v Semver) HasHomeFlag() bool {
	return v.AtLeast(HomeFlagMajor, HomeFlagMinor, HomeFlagPatch)
}

// Parse reads the first dotted numeric triple out of `paseo --version` output
// ("0.9.1", "0.9.0-beta.2\n", "paseo 0.9.1"). err != nil (the command itself
// failed) → unknown.
func Parse(out string, err error) Semver {
	if err != nil {
		return Semver{}
	}
	// First whitespace-delimited token that starts with a digit, e.g. skip a
	// leading "paseo " prefix if some build prints one.
	line := strings.TrimSpace(out)
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = strings.TrimSpace(line[:nl])
	}
	tok := line
	for _, f := range strings.Fields(line) {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' {
			tok = f
			break
		}
	}
	// Drop a pre-release/build suffix ("-beta.2", "+meta").
	if i := strings.IndexAny(tok, "-+"); i >= 0 {
		tok = tok[:i]
	}
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return Semver{}
	}
	maj, e1 := strconv.Atoi(parts[0])
	min, e2 := strconv.Atoi(parts[1])
	patch := 0
	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	if e1 != nil || e2 != nil {
		return Semver{}
	}
	return Semver{Major: maj, Minor: min, Patch: patch, Known: true}
}
