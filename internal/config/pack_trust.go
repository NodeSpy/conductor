package config

import "strings"

// PackTrustConfig is the operator-level pack-source allowlist (§21 Phase A). It
// is the trust surface for WHERE packs may come from — distinct from the
// lockfile, which proves a fetched pack is unchanged but not that its publisher
// is trusted.
//
//	pack_trust:
//	  allow:
//	    - github.com/your-org/*
//	    - github.com/acme/conductor-packs*
type PackTrustConfig struct {
	// Allow lists source globs a remote pack source must match. `*` matches any
	// run of characters (including `/`). An empty/absent Allow with the block
	// present denies all remote sources (fail-closed).
	Allow []string `yaml:"allow,omitempty"`
}

// SourceAllowed reports whether a pack source is permitted by the trust policy.
// Local sources (operator's own filesystem) are always allowed; remote sources
// must match an `allow:` glob. A nil policy allows everything.
func (t *PackTrustConfig) SourceAllowed(source string) bool {
	if t == nil {
		return true
	}
	s := strings.TrimPrefix(strings.TrimSpace(source), "git::")
	// Local sources are the operator's own disk (and nested locals are confined
	// to the config dir elsewhere), so the allowlist governs remote sources only.
	remote := strings.HasPrefix(s, "github.com/") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git://")
	if !remote {
		return true
	}
	// The OFFICIAL pack repo is in the default allowlist, mirroring
	// PluginSourceAllowed. A `packs:` key implies that repo (design §5.1), so
	// an operator who adds an allowlist for third-party packs would otherwise
	// silently break every official pack they already reference by name.
	if s == OfficialPacksSource || strings.HasPrefix(s, OfficialPacksSource+"/") {
		return true
	}
	for _, pat := range t.Allow {
		if globMatch(strings.TrimSpace(pat), s) {
			return true
		}
	}
	return false
}

// PluginSourceAllowed reports whether a PLUGIN source is permitted. It differs
// from SourceAllowed in one way, and deliberately: the OFFICIAL plugin repo is
// in the DEFAULT allowlist, and everything else remote is not.
//
// A plugin is a binary conductor executes, so "no policy configured" must not
// mean "any repo on the internet is fine" — but requiring ceremony to install an
// official plugin would defeat the whole app-extension model. So: official is
// always allowed, a third-party repo needs an explicit `plugin_trust.allow`
// entry (or `--allow-unlisted`), and a local path is the operator's own disk.
func (t *PackTrustConfig) PluginSourceAllowed(source string) bool {
	s := strings.TrimSpace(source)
	if s == "" {
		return true // local binary: the operator's own disk
	}
	// The official plugin and pack repos are trusted by default: naming an
	// official component needs no ceremony, a third-party source still does.
	for _, official := range []string{OfficialSource, OfficialPacksSource} {
		if s == official || strings.HasPrefix(s, official+"/") {
			return true
		}
	}
	if t == nil {
		return false
	}
	for _, pat := range t.Allow {
		if globMatch(strings.TrimSpace(pat), s) {
			return true
		}
	}
	return false
}

// globMatch reports whether a pack/plugin SOURCE matches a trust pattern.
//
// `*` IS SEGMENT-BOUNDED: it does not match across a `/`, the same rule Go's
// path.Match uses. That is the whole security property of this function, and
// the wildcard branch did not have it — `*` was any run of characters,
// including separators, so
//
//	pack_trust: { allow: ["github.com/trusted-org*"] }
//
// matched `github.com/trusted-org-evil/malicious-pack`: a DIFFERENT,
// attacker-registered org, whose name merely continues the trusted one. The
// daemon then fetched and executed it. The exact-match branch was anchored at
// a delimiter for the same reason one commit earlier; this is its sibling.
//
// The two rules:
//
//	a `*` inside a segment matches within THAT segment only, so
//	  `github.com/trusted-org*` can name an org and never a repo under a
//	  different one;
//	a BARE `*` as the final segment matches the remaining path, so
//	  `github.com/acme/*` still means "any repo under acme" — including a
//	  deeper host layout like a gitlab subgroup. It cannot escape the org,
//	  because everything before it is literal.
//
// A source's `//subdir` and `@ref` are stripped before matching: a subdir or
// ref OF a trusted repo is trusted, which is what the exact branch's
// delimiter anchoring says too.
//
// RESIDUAL, deliberately: a mid-segment `*` still matches same-SEGMENT
// continuations — `github.com/acme/conductor-packs*` admits
// `github.com/acme/conductor-packs2`. That is inherent to asking for a
// mid-segment wildcard, and registering that name needs write access under
// `acme` already. The cross-`/`, cross-org escalation is the one that had to
// close. The docs no longer recommend the mid-segment form.
func globMatch(pattern, s string) bool {
	// Fast paths.
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	// No wildcard: exact match OR a prefix anchored at a source delimiter, so
	// `github.com/acme/repo` matches `github.com/acme/repo//sub@ref` (a subdir
	// or ref of the SAME repo) but NOT `github.com/acme/repo-evil-fork` (a
	// different, attacker-registered repo whose name merely continues the
	// trusted one — a typosquat/name-continuation supply-chain bypass).
	if len(parts) == 1 {
		if s == pattern {
			return true
		}
		if strings.HasPrefix(s, pattern) {
			rest := s[len(pattern):]
			return strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "@")
		}
		return false
	}
	// A trust pattern names a REPO or an ORG, so the source's subdir/ref
	// continuation is stripped and the repo path is what gets matched.
	return segmentMatch(pattern, sourceRepoPath(s))
}

// sourceRepoPath drops a source's `//subdir` and `@ref` continuation, leaving
// the `host/org/repo` path a trust pattern names.
func sourceRepoPath(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i]
	}
	return s
}

// segmentMatch matches a `*` pattern against a path, segment by segment, so a
// `*` never crosses a `/`. A BARE `*` in the final pattern segment matches one
// or more remaining path segments (the `org/*` form); every other `*` is
// confined to its own segment.
func segmentMatch(pattern, path string) bool {
	pseg := strings.Split(pattern, "/")
	sseg := strings.Split(path, "/")
	for i, p := range pseg {
		last := i == len(pseg)-1
		if last && p == "*" {
			// "any repo under here" — one or more segments, all of them
			// below the literal prefix that precedes this `*`.
			return len(sseg) > i
		}
		if i >= len(sseg) {
			return false
		}
		if !segmentGlob(p, sseg[i]) {
			return false
		}
	}
	return len(pseg) == len(sseg)
}

// segmentGlob matches ONE path segment, where `*` is any run of characters
// within that segment (never a `/`, since a segment contains none).
func segmentGlob(pattern, seg string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == seg
	}
	if !strings.HasPrefix(seg, parts[0]) {
		return false
	}
	seg = seg[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(seg, mid)
		if i < 0 {
			return false
		}
		seg = seg[i+len(mid):]
	}
	return strings.HasSuffix(seg, parts[len(parts)-1])
}
