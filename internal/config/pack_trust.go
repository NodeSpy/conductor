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
	for _, pat := range t.Allow {
		if globMatch(strings.TrimSpace(pat), s) {
			return true
		}
	}
	return false
}

// globMatch reports whether s matches pattern, where `*` matches any run of
// characters (including `/`). Anchored at both ends.
func globMatch(pattern, s string) bool {
	// Fast paths.
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	// No wildcard: exact match OR prefix (so `github.com/acme/repo` matches
	// `github.com/acme/repo//sub@ref`).
	if len(parts) == 1 {
		return s == pattern || strings.HasPrefix(s, pattern)
	}
	// Anchor the first segment at the start.
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	// Middle segments match in order.
	for _, seg := range parts[1 : len(parts)-1] {
		i := strings.Index(s, seg)
		if i < 0 {
			return false
		}
		s = s[i+len(seg):]
	}
	// Anchor the last segment at the end (empty last => trailing `*` matches all).
	last := parts[len(parts)-1]
	return strings.HasSuffix(s, last)
}
