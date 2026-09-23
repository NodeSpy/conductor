package dispatch

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// paseo's command surface changed at 0.9: it gained multi-home daemons and the
// `--home <path>` flag that selects one (default ~/.paseo). PRE-0.9 paseo has NO
// `--home` flag — passing it there is an "unknown option" error that breaks EVERY
// command. So conductor detects the paseo version once (per dispatch surface) and
// only emits `--home` on paseo >= 0.9. Everything else conductor invokes
// (clone/run/ls/send/wait/inspect/archive/logs/workspace and the --dir/--protocol/
// --json flags) is identical across the 0.8↔0.9 boundary, so the version gate has
// exactly one job today; it is also the seam for any future command delta.

// paseoSemver is a parsed `paseo --version`. `known` is false when detection
// failed or the output could not be parsed — in which case atLeast assumes the
// NEWEST behavior (the fleet trends current, and a home is only ever configured
// on 0.9+, so assuming new never regresses a real pre-0.9 box).
type paseoSemver struct {
	major, minor, patch int
	known               bool
}

// String renders the detected version for logs ("0.9.1", or "unknown").
func (v paseoSemver) String() string {
	if !v.known {
		return "unknown"
	}
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

// atLeast reports whether the detected version is >= maj.min.patch. An unknown
// version is treated as newest (returns true), so a detection hiccup does not
// silently strip `--home` from a 0.9+ dispatch that needs it.
func (v paseoSemver) atLeast(maj, min, patch int) bool {
	if !v.known {
		return true
	}
	if v.major != maj {
		return v.major > maj
	}
	if v.minor != min {
		return v.minor > min
	}
	return v.patch >= patch
}

// parsePaseoSemver reads the first dotted numeric triple out of `paseo --version`
// output ("0.9.1", "0.9.0-beta.2\n", "paseo 0.9.1"). err != nil (the command
// itself failed) → unknown.
func parsePaseoSemver(out string, err error) paseoSemver {
	if err != nil {
		return paseoSemver{}
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
		return paseoSemver{}
	}
	maj, e1 := strconv.Atoi(parts[0])
	min, e2 := strconv.Atoi(parts[1])
	patch := 0
	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	if e1 != nil || e2 != nil {
		return paseoSemver{}
	}
	return paseoSemver{major: maj, minor: min, patch: patch, known: true}
}

// paseoVersionCache lazily probes and caches `paseo --version` for one dispatch
// surface (a Dispatcher or a Reaper). It is embedded, not shared, because each
// surface has its own bin/host — a remote runtime may run a different paseo than
// the local one.
type paseoVersionCache struct {
	once sync.Once
	ver  paseoSemver
}

// detect runs `paseo --version` through the same exec seam as every other command
// (so it works locally AND over ssh for a host: runtime), parses it, and caches
// the result. Called at boot for a clean log line and lazily on the first
// home-gated command. onFirst, when non-nil, is invoked once with the detected
// version (for a boot log).
func (c *paseoVersionCache) detect(ctx context.Context, bin string, remote *hosts.Target, onFirst func(paseoSemver)) paseoSemver {
	c.once.Do(func() {
		out, err := paseoCommand(ctx, bin, remote, "--version").Output()
		c.ver = parsePaseoSemver(string(out), err)
		if onFirst != nil {
			onFirst(c.ver)
		}
	})
	return c.ver
}

// homePrefix is the argv prefix that selects the daemon home: ["--home", home]
// when home is set AND paseo is >= 0.9 (the flag exists), else nil. It triggers
// version detection on first use.
func homePrefix(ctx context.Context, bin string, remote *hosts.Target, home string, cache *paseoVersionCache, onFirst func(paseoSemver)) []string {
	if home == "" {
		return nil
	}
	if cache.detect(ctx, bin, remote, onFirst).atLeast(0, 9, 0) {
		return []string{"--home", home}
	}
	return nil
}
