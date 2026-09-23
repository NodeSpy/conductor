package dispatch

import (
	"context"
	"sync"

	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/paseover"
)

// The version gate itself lives in internal/paseover, because internal/models
// needs the same gate for provider discovery and cannot import this package
// (dispatch imports models, not the other way round). What stays here is the
// dispatch-surface CACHE: each surface has its own bin/host, so each probes
// once for itself.
//
// Everything else conductor invokes (clone/run/ls/send/wait/inspect/archive/
// logs/workspace and the --dir/--protocol/--json flags) is identical across the
// 0.8↔0.9 boundary, so the gate has exactly one job today; it is also the seam
// for any future command delta.

type paseoSemver = paseover.Semver

func parsePaseoSemver(out string, err error) paseoSemver { return paseover.Parse(out, err) }

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
	if cache.detect(ctx, bin, remote, onFirst).HasHomeFlag() {
		return []string{"--home", home}
	}
	return nil
}
