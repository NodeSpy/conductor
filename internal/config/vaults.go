package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// VaultRef is one entry in the `vaults:` map — a named, typed secret store
// addressed by `{{ vault "<name>" "<key>" }}` references, the per-vault
// read/write verbs (`uses: <name>.read`), and `conductor vault <name>`.
// Env is NOT a vault (`${VAR}` / `env:VAR` stays the implicit baseline);
// everything beyond env is declared here. The header carries the type; the
// raw node is decoded by the vault builder into the type's own connection
// struct (conductor path:+unlock:, onepassword account:+service_account:,
// file dir:, hashicorp addr:+unlock:, …).
type VaultRef struct {
	Type string
	raw  yaml.Node
}

// UnmarshalYAML captures the type and retains the raw node.
func (r *VaultRef) UnmarshalYAML(n *yaml.Node) error {
	var h struct {
		Type string `yaml:"type,omitempty"`
	}
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Type, r.raw = h.Type, *n
	return nil
}

// Decode unmarshals the full vault body into a type-specific struct.
func (r VaultRef) Decode(v any) error {
	if r.raw.Kind == 0 {
		return nil
	}
	return r.raw.Decode(v)
}

// validateVaults checks the section's shape; type-specific connection fields
// are checked by the vault builder at boot (still load time). Vault names
// share the `uses: <name>.<verb>` namespace with connectors, so collisions
// and the reserved built-ins are rejected here.
func (c *Config) validateVaults() error {
	for name, ref := range c.Vaults {
		if name == "" {
			return fmt.Errorf("config: vaults: empty vault name")
		}
		if ref.Type == "" {
			return fmt.Errorf("config: vault %q: missing type", name)
		}
		switch name {
		case "kv", "sql", "conductor", ManualSource:
			return fmt.Errorf("config: vault %q: the name is reserved (a built-in verb namespace)", name)
		}
		if _, dup := c.ConnectorsMap[name]; dup {
			return fmt.Errorf("config: vault %q collides with a connector of the same name — both serve `uses: %s.<verb>`", name, name)
		}
	}
	return nil
}

// VaultNames returns the defined vault names for error messages.
func (c *Config) VaultNames() string {
	if len(c.Vaults) == 0 {
		return "none — add a vaults: section"
	}
	return sortedKeys(c.Vaults)
}
