package config

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// SessionSpec is an agent profile's `session:` block — session affinity.
// By default each dispatch gets a fresh agent; with a session spec, a live
// agent session binds to the rendered `key`, and every event resolving to
// the same value reaches the SAME agent as a follow-up with full prior
// context. Because it's declared on the agent, every trigger dispatching to
// that agent shares one session pool:
//
//	agents:
//	  reviewer:
//	    runtime: paseo
//	    session:
//	      key: "{{.repo}}#{{.pr}}"            # same value → same live session
//	      idle_ttl: 12h                        # reap after this long idle
//	      max_lifetime: 7d                     # hard cap on session age
//	      end_on: [ gh.pr_closed, gh.merged ]  # evict when the work is done
type SessionSpec struct {
	// Key is a template rendered against each dispatch's trigger context;
	// equal values share one live session.
	Key string `yaml:"key"`
	// IdleTTL evicts a session idle this long (default 24h).
	IdleTTL Duration `yaml:"idle_ttl"`
	// MaxLifetime evicts a session this old regardless of activity
	// (default 7d).
	MaxLifetime Duration `yaml:"max_lifetime"`
	// EndOn lists lifecycle events (`<connector>.<kind>`) that evict the
	// session immediately — the event's own trigger context renders the key,
	// so `gh.pr_closed` on repo X PR N ends that PR's session. The event
	// must be one conductor receives (some trigger listens on it).
	EndOn []string `yaml:"end_on"`
}

// Session TTL defaults: bounded by default so an abandoned session never
// lives forever even when the profile doesn't say so.
const (
	DefaultSessionIdleTTL     = 24 * time.Hour
	DefaultSessionMaxLifetime = 7 * 24 * time.Hour
)

// IdleTTLOrDefault returns the idle eviction window.
func (s *SessionSpec) IdleTTLOrDefault() time.Duration {
	if d := s.IdleTTL.D(); d > 0 {
		return d
	}
	return DefaultSessionIdleTTL
}

// MaxLifetimeOrDefault returns the hard age cap.
func (s *SessionSpec) MaxLifetimeOrDefault() time.Duration {
	if d := s.MaxLifetime.D(); d > 0 {
		return d
	}
	return DefaultSessionMaxLifetime
}

// EndsOn reports whether a lifecycle event (by its connector instance or
// source type + kind) is one of the spec's end_on entries.
func (s *SessionSpec) EndsOn(instance, source, kind string) bool {
	for _, e := range s.EndOn {
		if e == instance+"."+kind || e == source+"."+kind || e == kind {
			return true
		}
	}
	return false
}

// validateSessions checks each profile's session: block at load time.
func (c *Config) validateSessions() error {
	for name, p := range c.Agents {
		s := p.Session
		if s == nil {
			continue
		}
		if strings.TrimSpace(s.Key) == "" {
			return fmt.Errorf("config: agent %q: session.key is required (e.g. \"{{.repo}}#{{.pr}}\")", name)
		}
		if _, err := template.New("k").Parse(s.Key); err != nil {
			return fmt.Errorf("config: agent %q: session.key: %v", name, err)
		}
		for _, e := range s.EndOn {
			if strings.TrimSpace(e) == "" {
				return fmt.Errorf("config: agent %q: session.end_on: empty event", name)
			}
		}
	}
	return nil
}
