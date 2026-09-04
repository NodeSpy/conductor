package connector

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// Each vaults: entry surfaces as one connector-shaped instance named after
// the vault, publishing the data verbs `<name>.read` and (on writable
// backends) `<name>.write` — so steps, hooks, and validation address vaults
// through the same uses: grammar as everything else. A vault whose unlock
// fails registers BROKEN: the instance is disabled with the reason (the
// daemon boots; dependents fail with it; boot notifies), never a crash.

// vaultDecl builds the declaration for one vault instance. read is always
// present; write only when the backend can (so `uses: <ro-vault>.write`
// fails validation as "no verb", the load-time face of the capability).
func vaultDecl(typ string, writable bool) *TypeDecl {
	d := &TypeDecl{
		Type: typ,
		Desc: "Vault (" + typ + ") from the vaults: section — values read here are tainted sensitive and redacted from logs/audit.",
		Verbs: []VerbDecl{
			{
				Name: "read", Desc: "read one secret (the value is tainted sensitive)",
				Options: Schema{
					"key": {Type: TString, Required: true, Desc: "the entry name / item path"},
				},
				Outputs: Schema{"value": {Type: TString}},
			},
		},
	}
	if writable {
		d.Verbs = append(d.Verbs, VerbDecl{
			Name: "write", Desc: "store one secret",
			Options: Schema{
				"key":   {Type: TString, Required: true},
				"value": {Type: TString, Required: true},
			},
			Outputs: Schema{},
		})
	}
	return d
}

type vaultImpl struct {
	name string
}

func (vaultImpl) Validate() error          { return nil }
func (vaultImpl) DeclaredEvents() []string { return nil }
func (vaultImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("vaults have no source events")
}

func (v vaultImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	key, _ := opts["key"].(string)
	switch verb {
	case "read":
		val, err := vaults.Read(ctx, v.name, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": val}, nil
	case "write":
		val, ok := opts["value"].(string)
		if !ok {
			return nil, fmt.Errorf("%s.write: value must be a string, got %T", v.name, opts["value"])
		}
		if err := vaults.Write(ctx, v.name, key, val); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("vault %q: no verb %q", v.name, verb)
}

// buildVaults wires the vaults: section: build each backend, attempt its
// unlock, register it (healthy or broken), and return the connector-shaped
// instances. Structural config errors (unknown type, missing connection
// fields) are LOAD errors; unlock/availability failures disable — the boots-
// anyway rule for anything that can break at runtime. Wires the taint hook
// and the resolver's VaultRead while at it, so config credential fields can
// hold {{ vault … }} references when the connectors build after this.
func buildVaults(cfg *config.Config, deps Deps) ([]*Instance, error) {
	vaults.Reset()
	if deps.Secrets != nil {
		vaults.SetTaint(deps.Secrets.Track)
		deps.Secrets.VaultRead = vaults.Read
	}
	if cfg == nil {
		return nil, nil
	}
	boot := deps.VaultBoot
	if boot == nil {
		boot = vaults.NewBootstrap()
	}
	names := make([]string, 0, len(cfg.Vaults))
	for n := range cfg.Vaults {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []*Instance
	for _, name := range names {
		ref := cfg.Vaults[name]
		b, broken, err := buildVaultBackend(name, ref, boot, deps)
		if err != nil {
			return nil, err
		}
		writable := true
		if broken != "" {
			if rerr := vaults.RegisterBroken(name, ref.Type, broken); rerr != nil {
				return nil, rerr
			}
			if deps.Log != nil {
				deps.Log("vault %q disabled: %s", name, broken)
			}
		} else {
			if rerr := vaults.Register(name, ref.Type, b); rerr != nil {
				return nil, rerr
			}
			_, writable = b.(vaults.Writer)
		}
		in := &Instance{
			Name:           name,
			Decl:           vaultDecl(ref.Type, writable),
			Enabled:        true,
			Impl:           vaultImpl{name: name},
			DisabledReason: broken,
		}
		out = append(out, in)
	}
	return out, nil
}

// buildVaultBackend builds one typed backend. Returns (backend, "", nil) on
// success, (nil, reason, nil) for an unlock/availability failure (disable,
// don't crash), and (nil, "", err) for a structural config error.
func buildVaultBackend(name string, ref config.VaultRef, boot *vaults.Bootstrap, deps Deps) (vaults.Backend, string, error) {
	switch ref.Type {
	case "conductor":
		var conn struct {
			Path   string `yaml:"path"`
			Unlock struct {
				Key string `yaml:"key"`
			} `yaml:"unlock"`
		}
		if err := ref.Decode(&conn); err != nil {
			return nil, "", fmt.Errorf("vault %q: decode: %w", name, err)
		}
		c := vaults.NewConductorVault(conn.Path, conn.Unlock.Key, boot)
		if err := c.Unlock(); err != nil {
			return nil, err.Error(), nil
		}
		return c, "", nil
	case "onepassword":
		var conn struct {
			Account        string `yaml:"account"`
			ServiceAccount string `yaml:"service_account"`
		}
		if err := ref.Decode(&conn); err != nil {
			return nil, "", fmt.Errorf("vault %q: decode: %w", name, err)
		}
		sa := ""
		if conn.ServiceAccount != "" {
			v, err := boot.Resolve(conn.ServiceAccount)
			if err != nil {
				return nil, err.Error(), nil
			}
			sa = v
		}
		return &vaults.OnePasswordVault{Account: conn.Account, ServiceAccount: sa, Exec: deps.VaultExec}, "", nil
	case "pass":
		var conn struct {
			Prefix string `yaml:"prefix"`
		}
		if err := ref.Decode(&conn); err != nil {
			return nil, "", fmt.Errorf("vault %q: decode: %w", name, err)
		}
		return &vaults.PassVault{Prefix: conn.Prefix, Exec: deps.VaultExec}, "", nil
	case "file":
		var conn struct {
			Dir string `yaml:"dir"`
		}
		if err := ref.Decode(&conn); err != nil {
			return nil, "", fmt.Errorf("vault %q: decode: %w", name, err)
		}
		if conn.Dir == "" {
			return nil, "", fmt.Errorf("vault %q (file): dir: is required", name)
		}
		if fi, err := os.Stat(conn.Dir); err != nil || !fi.IsDir() {
			return nil, fmt.Sprintf("secret directory %s is unavailable", conn.Dir), nil
		}
		return &vaults.FileVault{Dir: conn.Dir}, "", nil
	case "hashicorp":
		var conn struct {
			Addr      string `yaml:"addr"`
			Mount     string `yaml:"mount"`
			Namespace string `yaml:"namespace"`
			Unlock    struct {
				Token string `yaml:"token"`
			} `yaml:"unlock"`
		}
		if err := ref.Decode(&conn); err != nil {
			return nil, "", fmt.Errorf("vault %q: decode: %w", name, err)
		}
		if conn.Addr == "" {
			return nil, "", fmt.Errorf("vault %q (hashicorp): addr: is required", name)
		}
		tok, err := boot.Resolve(conn.Unlock.Token)
		if err != nil {
			return nil, err.Error(), nil
		}
		if tok == "" {
			return nil, "unlock.token: is required (a VAULT_TOKEN bootstrap ref)", nil
		}
		return &vaults.HashicorpVault{Addr: conn.Addr, Mount: conn.Mount, Namespace: conn.Namespace, Token: tok}, "", nil
	}
	return nil, "", fmt.Errorf("vault %q: unknown type %q (known: conductor, file, hashicorp, onepassword, pass)", name, ref.Type)
}
