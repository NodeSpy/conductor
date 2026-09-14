package config

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// SessionSpec is a `session:` block — session affinity. By default each
// dispatch gets a fresh agent; with a session spec a live agent session binds
// to the rendered `key`, and every event resolving to the same value reaches
// the SAME agent as a follow-up with full prior context.
//
// A binding is `(runtime, resolvedModel, key)` — see
// docs/design/agents-removal.md §3. Runtime and model are STRUCTURAL, not
// identities: a running agent is one model on one runtime, and you can
// resume neither a paseo session on codex nor an opus step into a haiku
// session. Because a pack assigns a fleet per step, its model assignments
// partition affinity for free.
//
// It is declarable at two scopes:
//
//	runtimes:                       # OVERALL affinity: one agent per key,
//	  paseo:                        # shared across every step that has no
//	    session:                    # session: of its own
//	      key: "{{.repo}}#{{.pr}}"
//	      idle_ttl: 12h
//
//	triggers:
//	  github.pull_request:
//	    steps:
//	      - id: review              # STEP affinity: its own pool, namespaced
//	        type: agent             # to the step identity, so an identical
//	        session:                # key string is still a distinct session
//	          key: "{{.repo}}#{{.pr}}"
//	          end_on: [gh.pr_closed, gh.merged]
//
// Resolution per dispatch: the step's session: wins, else the runtime's, else
// a fresh agent.
type SessionSpec struct {
	// Key is a template rendered against each dispatch's trigger context;
	// equal values share one live session (within one runtime+model).
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
// lives forever even when the spec doesn't say so.
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

// validateSessions checks every `session:` block at load time — on runtimes
// (the overall pool) and on steps (their own).
func (c *Config) validateSessions() error {
	for _, name := range sortedNames(c.Runtimes) {
		if err := validateStepSession("runtime "+name, c.Runtimes[name].Session); err != nil {
			return err
		}
	}
	// Step sessions are checked by validateSteps, which walks every step.
	return nil
}

// validateStepSession checks one session: block's shape.
func validateStepSession(where string, s *SessionSpec) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(s.Key) == "" {
		return fmt.Errorf("config: %s: session.key is required (e.g. \"{{.repo}}#{{.pr}}\")", where)
	}
	if _, err := template.New("k").Parse(s.Key); err != nil {
		return fmt.Errorf("config: %s: session.key: %v", where, err)
	}
	for _, e := range s.EndOn {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("config: %s: session.end_on: empty event", where)
		}
	}
	return nil
}
