package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// cmdVault manages a NAMED vault from the config's vaults: section — the
// CLI face of the same backends the daemon uses (type + unlock resolved from
// the entry):
//
//	conductor vault <name> init [--sensitive]   create a conductor-type vault
//	conductor vault <name> add <key>            store a value (stdin; writable backends)
//	conductor vault <name> get <key>            print one entry (for verification)
//	conductor vault <name> ls                   list entries (listable backends)
//	conductor vault <name> rm <key>             delete an entry (writable backends)
//
// Write ops error clearly on a read-only backend (onepassword, file);
// bootstrapping a conductor vault means declaring it in vaults: first, then
// `conductor vault <name> init`.
func cmdVault(args []string) error {
	rest := positional(args)
	if len(rest) < 2 {
		return fmt.Errorf("usage: conductor vault <name> init|add|get|ls|rm (the name is a vaults: entry)")
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	name, sub := rest[0], rest[1]
	ctx := context.Background()

	if sub == "init" {
		return vaultInit(cfg, name, args)
	}

	b, typ, err := connector.OpenVaultBackend(cfg, name, connector.Deps{Secrets: secrets.New(), Config: cfg})
	if err != nil {
		return err
	}
	readOnly := func(op string) error {
		return fmt.Errorf("vault %q (%s) is read-only — %s needs a writable type (conductor, pass, hashicorp)", name, typ, op)
	}
	switch sub {
	case "add":
		if len(rest) != 3 {
			return fmt.Errorf("usage: conductor vault %s add <key> (value on stdin)", name)
		}
		w, ok := b.(vaults.Writer)
		if !ok {
			return readOnly("add")
		}
		value, err := readSecretStdin()
		if err != nil {
			return err
		}
		if err := w.Write(ctx, rest[2], value); err != nil {
			return err
		}
		fmt.Printf("stored {{ vault %q %q }}\n", name, rest[2])
		return nil
	case "get":
		if len(rest) != 3 {
			return fmt.Errorf("usage: conductor vault %s get <key>", name)
		}
		v, err := b.Read(ctx, rest[2])
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "ls":
		l, ok := b.(vaults.Lister)
		if !ok {
			return fmt.Errorf("vault %q (%s) cannot enumerate entries — ls works on conductor/file vaults", name, typ)
		}
		names, err := l.List(ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "rm":
		if len(rest) != 3 {
			return fmt.Errorf("usage: conductor vault %s rm <key>", name)
		}
		d, ok := b.(vaults.Deleter)
		if !ok {
			return readOnly("rm")
		}
		return d.Delete(ctx, rest[2])
	}
	return fmt.Errorf("unknown vault subcommand %q (init|add|get|ls|rm)", sub)
}

// vaultInit creates a NEW conductor-type vault at the entry's configured
// path. --sensitive selects the hardened scrypt profile (N=2^20) for a vault
// that may end up somewhere public. When the entry's unlock: resolves to key
// material, that material is used; otherwise a key is generated and seeded
// into the sibling key file the default chain reads.
func vaultInit(cfg *config.Config, name string, args []string) error {
	ref, ok := cfg.Vaults[name]
	if !ok {
		return fmt.Errorf("no vault %q in vaults: (defined: %s) — declare it first, then init", name, cfg.VaultNames())
	}
	if ref.Type != "conductor" {
		return fmt.Errorf("vault %q is type %s — init creates conductor-type vaults (the other types own their own storage)", name, ref.Type)
	}
	var conn struct {
		Path   string `yaml:"path"`
		Unlock struct {
			Key string `yaml:"key"`
		} `yaml:"unlock"`
	}
	if err := ref.Decode(&conn); err != nil {
		return fmt.Errorf("vault %q: decode: %w", name, err)
	}
	path := conn.Path
	if path == "" {
		path = secrets.DefaultVaultPath()
	}
	profile := ""
	for _, a := range args {
		if a == "--sensitive" {
			profile = "sensitive"
		}
	}

	material := ""
	if conn.Unlock.Key != "" {
		m, err := vaults.NewBootstrap().Resolve(conn.Unlock.Key)
		if err != nil {
			return fmt.Errorf("vault %q: resolve unlock (%s) before init: %w", name, conn.Unlock.Key, err)
		}
		material = m
	}
	seeded := ""
	if material == "" {
		m, err := secrets.GenerateKey()
		if err != nil {
			return err
		}
		kf, err := secrets.SeedKeyFile(path, m)
		if err != nil {
			return err
		}
		material, seeded = m, kf
	}
	v, err := secrets.InitVault(path, func() ([]byte, error) { return []byte(material), nil }, profile)
	if err != nil {
		return err
	}
	if err := v.Save(); err != nil {
		return err
	}
	fmt.Printf("vault %q initialized at %s (scrypt N=%d)\n", name, path, v.KDF().N)
	if seeded != "" {
		fmt.Printf("key file seeded at %s (chmod 600)\n", seeded)
		fmt.Printf("for machine-bound storage on Linux, move the key into a systemd credential instead:\n")
		fmt.Printf("  systemd-creds encrypt --name=conductor-vault-key %s /etc/credstore.encrypted/conductor-vault-key\n", seeded)
	}
	return nil
}

// cmdUnlock seeds the default vault key file from interactive input —
// one-time seeding for setups whose key isn't in the environment or a
// systemd credential. The daemon restarts itself (auto-update), so
// steady-state unlocking must be non-interactive: this writes the chmod-600
// key file the KeyChain falls back to.
func cmdUnlock(args []string) error {
	fmt.Fprint(os.Stderr, "vault key material (base64 key or passphrase): ")
	material, err := readSecretStdin()
	if err != nil {
		return err
	}
	if strings.TrimSpace(material) == "" {
		return fmt.Errorf("empty key material")
	}
	kf, err := secrets.SeedKeyFile(secrets.DefaultVaultPath(), strings.TrimSpace(material))
	if err != nil {
		return err
	}
	// Verify it can open the vault (if one exists).
	if _, err := secrets.OpenVault(secrets.DefaultVaultPath(), nil); err != nil {
		return fmt.Errorf("key seeded at %s but the vault did not open: %w", kf, err)
	}
	fmt.Printf("vault key seeded at %s — restarts (including auto-update) unlock without a human\n", kf)
	return nil
}

// readSecretStdin reads one line (or all piped input) from stdin.
func readSecretStdin() (string, error) {
	fi, _ := os.Stdin.Stat()
	if fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		// Piped: take everything, trim trailing newlines.
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
