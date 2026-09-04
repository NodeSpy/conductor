package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StoreRef is one entry in the `stores:` map — a named data store a `kv.*`
// verb (or ctx.store in code) addresses with its `store:` selector. Nothing
// is implicit: only defined stores exist. The header carries the fields
// every type shares; the raw node is decoded by the store builder into the
// type's own connection struct (boltdb path:, redis url:, http base_url: +
// auth:, …).
type StoreRef struct {
	Type string
	raw  yaml.Node
}

// UnmarshalYAML captures the type and retains the raw node.
func (r *StoreRef) UnmarshalYAML(n *yaml.Node) error {
	var h struct {
		Type string `yaml:"type,omitempty"`
	}
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Type, r.raw = h.Type, *n
	return nil
}

// Decode unmarshals the full store body into a type-specific struct.
func (r StoreRef) Decode(v any) error {
	if r.raw.Kind == 0 {
		return nil
	}
	return r.raw.Decode(v)
}

// validateStores checks the section's shape; type-specific connection
// fields are checked by the store builder at boot (still load time).
func (c *Config) validateStores() error {
	for name, ref := range c.Stores {
		if name == "" {
			return fmt.Errorf("config: stores: empty store name")
		}
		if ref.Type == "" {
			return fmt.Errorf("config: store %q: missing type", name)
		}
	}
	return nil
}

// StoreNames returns the defined store names for error messages.
func (c *Config) StoreNames() string {
	if len(c.Stores) == 0 {
		return "none — add a stores: section"
	}
	return sortedKeys(c.Stores)
}
