package config

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/memory"
	"gopkg.in/yaml.v3"
)

// MemoryConfig is the `memory:` section — the durable memory agents share
// across runs. Exactly one backend is picked, no default:
//
//	memory:
//	  store: state                           # (a) a durable stores: KV entry
//	  # or  dir: ~/.config/conductor/memory  # (b) one Markdown file per memory
//	  # or  type: memory                     # (c) ephemeral in-process
type MemoryConfig struct {
	// Store names a `stores:` KV entry (boltdb/redis/http) to keep entries in.
	Store string `yaml:"store"`
	// Dir is a directory holding one Markdown-with-frontmatter file per
	// memory (implies type: file).
	Dir string `yaml:"dir"`
	// Type selects a backend that needs no location: "memory" (ephemeral
	// in-process). "file" is accepted alongside dir: for explicitness.
	Type string `yaml:"type"`
}

// validateMemory checks the memory: section's shape at load time.
func (c *Config) validateMemory() error {
	m := c.Memory
	if m == nil {
		return nil
	}
	set := 0
	if m.Store != "" {
		set++
	}
	if m.Dir != "" {
		set++
	}
	if m.Type == "memory" {
		set++
	}
	if set != 1 {
		return fmt.Errorf("config: memory: pick exactly one backend — store: <stores: entry>, dir: <path>, or type: memory")
	}
	switch m.Type {
	case "", "memory":
	case "file":
		if m.Dir == "" {
			return fmt.Errorf("config: memory: type: file needs dir: <path>")
		}
	default:
		return fmt.Errorf("config: memory: unknown type %q (file, memory)", m.Type)
	}
	if m.Store != "" {
		if _, ok := c.Stores[m.Store]; !ok {
			return fmt.Errorf("config: memory: unknown store %q (defined stores: %s)", m.Store, c.StoreNames())
		}
	}
	return nil
}

// MemorySelector is a step's `memory:` opt-in. `memory: true` injects the
// defaults — the shared (no-key) set plus the context keys the engine
// supplies for the run: the repo string, the workflow name, and the step
// identity. A map narrows it to specific scope keys:
//
//	memory: true
//	memory: { scopes: ["", "acme/api"], tags: [ci], limit: 10 }
//	memory: { scopes: ["{{.step}}", "{{.workflow}}"], tags: [ci] }
//	memory: { extra_scopes: ["team:{{.inputs.team}}", "standard:{{.owner}}"] }
//
// `scopes:` (or `scope:`) REPLACES the defaults; `extra_scopes:` is appended
// to whichever applies — the defaults, or the explicit list — so a step can
// add keys without restating the shared set and the run's context keys.
//
// SCOPE KEYS ARE OPAQUE (docs/design/agents-removal.md §2): the memory core
// never interprets them, and there are no privileged `repo`/`agent` types.
// A key is a TEMPLATE over the step's facts (memory.RenderScopeKey: inputs,
// the event's facts, workflow, step, …), rendered per run by the engine;
// `${repo}` / `${workflow}` / `${step}` are aliases of `{{.repo}}` /
// `{{.workflow}}` / `{{.step}}`. A key that reads a fact the run has no
// value for is dropped for that run.
//
// Absent (nil) or false → no injection, no token cost.
type MemorySelector struct {
	Enabled     bool
	Scopes      []string
	ExtraScopes []string
	Tags        []string
	Limit       int
}

// UnmarshalYAML accepts a bool or the filter map. The map is on unless it
// says `enabled: false`.
func (s *MemorySelector) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if err := n.Decode(&b); err == nil {
		*s = MemorySelector{Enabled: b}
		return nil
	}
	var body struct {
		// Enabled is the round-trip key (see MarshalYAML): absent means on,
		// as a map always has; `enabled: false` keeps a disabled selector
		// disabled through a marshal → decode.
		Enabled     *bool    `yaml:"enabled"`
		Scope       string   `yaml:"scope"`
		Scopes      []string `yaml:"scopes"`
		ExtraScopes []string `yaml:"extra_scopes"`
		Tags        []string `yaml:"tags"`
		Limit       int      `yaml:"limit"`
	}
	if err := n.Decode(&body); err != nil {
		return fmt.Errorf("memory: want true/false or { scopes, extra_scopes, tags, limit }: %w", err)
	}
	scopes := body.Scopes
	if body.Scope != "" {
		scopes = append(scopes, body.Scope)
	}
	// Scope keys are opaque — there is deliberately nothing to validate. The
	// one thing worth catching is a key that is only whitespace, which would
	// silently read as the shared set instead of what the author meant.
	for i, sc := range scopes {
		if sc != "" && strings.TrimSpace(sc) == "" {
			return fmt.Errorf("memory: scopes[%d] is blank — omit it for the shared set, or name a scope key", i)
		}
	}
	for i, sc := range body.ExtraScopes {
		if strings.TrimSpace(sc) == "" {
			return fmt.Errorf("memory: extra_scopes[%d] is blank — name a scope key (the shared set is already in the defaults)", i)
		}
	}
	// Keys are templates over the step's facts (memory.RenderScopeKey): a
	// malformed one fails here, not by silently recalling nothing.
	for _, sc := range append(append([]string(nil), scopes...), body.ExtraScopes...) {
		if err := memory.CheckScopeKey(sc); err != nil {
			return fmt.Errorf("memory: %w", err)
		}
	}
	enabled := body.Enabled == nil || *body.Enabled
	*s = MemorySelector{Enabled: enabled, Scopes: scopes, ExtraScopes: body.ExtraScopes, Tags: body.Tags, Limit: body.Limit}
	return nil
}

// MarshalYAML writes the selector back in a shape UnmarshalYAML reads the
// same way, so a step survives a marshal → decode round trip — which every
// pack step override takes (applyStepOverride). Before it existed the struct
// marshaled as a map of its Go fields, and decoding ANY map turned the
// selector on: a pack step shipped with `memory: false` came back enabled
// after an unrelated override.
//
// Disabled is the bool `false`. That is also what lets an override that
// turns memory ON work: the consumer's `memory: { extra_scopes: […] }`
// replaces a scalar outright, where a map carrying `enabled: false` would
// have been deep-merged into and stayed off.
//
// Enabled keeps the field map it always marshaled as (`enabled`, `scopes`,
// `tags`, `limit`, byte for byte), now decoded with `enabled:` honoured.
// Step.Fingerprint hashes a step's marshaled YAML, and an unnamed step's
// identity — its session binding and its `${step}` memory key — is that
// hash; a new shape would move every such identity on upgrade.
// `extra_scopes` is written only when set, so a selector without it marshals
// exactly as before.
func (s MemorySelector) MarshalYAML() (any, error) {
	if !s.Enabled {
		return false, nil
	}
	type wire struct {
		Enabled     bool     `yaml:"enabled"`
		Scopes      []string `yaml:"scopes"`
		Tags        []string `yaml:"tags"`
		Limit       int      `yaml:"limit"`
		ExtraScopes []string `yaml:"extra_scopes,omitempty"`
	}
	return wire{Enabled: s.Enabled, Scopes: s.Scopes, Tags: s.Tags, Limit: s.Limit, ExtraScopes: s.ExtraScopes}, nil
}

// Memory scope placeholders the ENGINE expands to a run's context keys. They
// are sugar: `${repo}` is the repo string, `${workflow}` the workflow name,
// `${step}` the step identity. The memory core sees only the expansions.
const (
	MemoryScopeRepo     = "${repo}"
	MemoryScopeWorkflow = "${workflow}"
	MemoryScopeStep     = "${step}"
)
