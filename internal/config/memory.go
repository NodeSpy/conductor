package config

import (
	"fmt"
	"strings"

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

// MemorySelector is an agent profile's `memory:` opt-in. `memory: true`
// injects the defaults (global + the target repo + the agent's own scope,
// newest first, capped); a map narrows it:
//
//	memory: true
//	memory: { scopes: [global, repo], tags: [ci], limit: 10 }
//
// Absent (nil) or false → no injection, no token cost.
type MemorySelector struct {
	Enabled bool
	Scopes  []string
	Tags    []string
	Limit   int
}

// UnmarshalYAML accepts a bool or the filter map.
func (s *MemorySelector) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if err := n.Decode(&b); err == nil {
		*s = MemorySelector{Enabled: b}
		return nil
	}
	var body struct {
		Scope  string   `yaml:"scope"`
		Scopes []string `yaml:"scopes"`
		Tags   []string `yaml:"tags"`
		Limit  int      `yaml:"limit"`
	}
	if err := n.Decode(&body); err != nil {
		return fmt.Errorf("memory: want true/false or { scopes, tags, limit }: %w", err)
	}
	scopes := body.Scopes
	if body.Scope != "" {
		scopes = append(scopes, body.Scope)
	}
	for _, sc := range scopes {
		if !validMemoryScope(sc) {
			return fmt.Errorf("memory: bad scope %q (global, repo, agent, repo:<owner/repo>, agent:<name>)", sc)
		}
	}
	*s = MemorySelector{Enabled: true, Scopes: scopes, Tags: body.Tags, Limit: body.Limit}
	return nil
}

// validMemoryScope mirrors memory.ResolveScope's accepted forms (relative
// forms resolve against the dispatch at runtime).
func validMemoryScope(s string) bool {
	switch s {
	case "global", "repo", "agent":
		return true
	}
	return (strings.HasPrefix(s, "repo:") && len(s) > len("repo:")) ||
		(strings.HasPrefix(s, "agent:") && len(s) > len("agent:"))
}
