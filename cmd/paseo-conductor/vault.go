package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/secrets"
)

// cmdVault manages the built-in encrypted vault (vault: secret references).
//
//	conductor vault init          create the vault + seed a generated key file
//	conductor vault add <name>    read a value from stdin and store it
//	conductor vault show <name>   print one entry (for verification)
//	conductor vault ls            list entry names
//	conductor vault rm <name>     delete an entry
func cmdVault(args []string) error {
	rest := positional(args)
	if len(rest) == 0 {
		return fmt.Errorf("usage: conductor vault init|add|show|ls|rm")
	}
	path := secrets.DefaultVaultPath()
	switch rest[0] {
	case "init":
		material, err := secrets.GenerateKey()
		if err != nil {
			return err
		}
		kf, err := secrets.SeedKeyFile(path, material)
		if err != nil {
			return err
		}
		v, err := secrets.OpenVault(path, func() ([]byte, error) { return []byte(material), nil })
		if err != nil {
			return err
		}
		if err := v.Save(); err != nil {
			return err
		}
		fmt.Printf("vault initialized at %s\nkey file seeded at %s (chmod 600)\n", path, kf)
		fmt.Printf("for machine-bound storage on Linux, move the key into a systemd credential instead:\n")
		fmt.Printf("  systemd-creds encrypt --name=conductor-vault-key %s /etc/credstore.encrypted/conductor-vault-key\n", kf)
		return nil
	case "add":
		if len(rest) != 2 {
			return fmt.Errorf("usage: conductor vault add <name> (value on stdin)")
		}
		v, err := secrets.OpenVault(path, nil)
		if err != nil {
			return err
		}
		value, err := readSecretStdin()
		if err != nil {
			return err
		}
		if err := v.Set(rest[1], value); err != nil {
			return err
		}
		if err := v.Save(); err != nil {
			return err
		}
		fmt.Printf("stored vault:%s\n", rest[1])
		return nil
	case "show":
		if len(rest) != 2 {
			return fmt.Errorf("usage: conductor vault show <name>")
		}
		v, err := secrets.OpenVault(path, nil)
		if err != nil {
			return err
		}
		val, err := v.Get(rest[1])
		if err != nil {
			return err
		}
		fmt.Println(val)
		return nil
	case "ls":
		v, err := secrets.OpenVault(path, nil)
		if err != nil {
			return err
		}
		for _, n := range v.Names() {
			fmt.Println(n)
		}
		return nil
	case "rm":
		if len(rest) != 2 {
			return fmt.Errorf("usage: conductor vault rm <name>")
		}
		v, err := secrets.OpenVault(path, nil)
		if err != nil {
			return err
		}
		if !v.Delete(rest[1]) {
			return fmt.Errorf("no entry %q", rest[1])
		}
		return v.Save()
	}
	return fmt.Errorf("unknown vault subcommand %q (init|add|show|ls|rm)", rest[0])
}

// cmdUnlock seeds the vault key file from interactive input — one-time
// seeding for setups whose key isn't in the environment or a systemd
// credential. The daemon restarts itself (auto-update), so steady-state
// unlocking must be non-interactive: this writes the chmod-600 key file the
// KeyChain falls back to.
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
	r := bufio.NewReader(os.Stdin)
	fi, _ := os.Stdin.Stat()
	if fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		// Piped: take everything, trim one trailing newline.
		b, err := os.ReadFile("/dev/stdin")
		if err == nil {
			return strings.TrimRight(string(b), "\r\n"), nil
		}
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
