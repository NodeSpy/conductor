package config

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/memory"
	"gopkg.in/yaml.v3"
)

// StepCache is a step's (or, as the default for its agent steps, a
// workflow's) `cache:` block (docs/wiki/Step-Caches.md):
//
//	cache:
//	  key: "pr-{{.repo}}-{{.pr}}"      # this step's cache: restored from, and published to
//	  restore_from: ["{{.repo}}"]      # read-only fallbacks, in order
//	  teach: true                      # a default-branch run teaches the repo's git deps
//
// Each entry is a template over the step's facts, rendered the way a memory
// scope key is (memory.RenderScopeKey). A step publishes only to its own
// rendered key; its restore_from keys are read-only to it. A key's identity
// is its namespace (the pack instance that declared it; the operator's own
// config has none), its template text and what that renders to — so a
// template that renders facts an event's sender controls can never land on
// another template's key.
type StepCache struct {
	Key         string   `yaml:"key"`
	RestoreFrom []string `yaml:"restore_from,omitempty"`
	// Teach: a run of this step in conductor's own fresh checkout of a
	// trusted target's default branch head learns the repository's git
	// fetch dependencies (docs/wiki/Step-Caches.md). The operator's (or a
	// pack's) to declare: an agent-authored step can never set cache: at
	// all. Teaching gives the launch a fetch credential for any repository
	// on the forge host, so declare it only on a step whose prompt no
	// event's sender writes.
	Teach bool `yaml:"teach,omitempty"`
}

// maxRestoreFrom bounds a step's fallbacks.
const maxRestoreFrom = 8

// UnmarshalYAML reads the block and checks every template, so a malformed
// key fails at load rather than by silently caching nothing.
func (c *StepCache) UnmarshalYAML(n *yaml.Node) error {
	type raw StepCache
	var r raw
	// Strict: KnownFields does not reach into a custom unmarshaler, so a
	// typo'd or invented key (`paths:`) would otherwise drop silently.
	if err := strictNodeDecode(n, &r); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := (*StepCache)(&r).validate(); err != nil {
		return err
	}
	*c = StepCache(r)
	return nil
}

func (c *StepCache) validate() error {
	if strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("cache: key is required (a template naming this step's cache)")
	}
	if len(c.RestoreFrom) > maxRestoreFrom {
		return fmt.Errorf("cache: restore_from has %d entries (at most %d)", len(c.RestoreFrom), maxRestoreFrom)
	}
	for i, k := range append([]string{c.Key}, c.RestoreFrom...) {
		where := "key"
		if i > 0 {
			where = fmt.Sprintf("restore_from[%d]", i-1)
		}
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("cache: %s is blank", where)
		}
		if err := memory.CheckScopeKey(k); err != nil {
			return fmt.Errorf("cache: %s: %w", where, err)
		}
	}
	return nil
}

// CacheTemplate is a cache key template as its identity: trimmed.
func CacheTemplate(t string) string { return strings.TrimSpace(t) }
