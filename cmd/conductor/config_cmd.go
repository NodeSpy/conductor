package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/NodeSpy/conductor/internal/migrate"
)

// cmdConfig handles `conductor config migrate [--dry-run]`.
func cmdConfig(args []string) error {
	rest := positional(args)
	if len(rest) == 0 || rest[0] != "migrate" {
		return fmt.Errorf("usage: conductor config migrate [--dry-run]")
	}
	return cmdConfigMigrate(args)
}

// cmdConfigMigrate transforms a legacy config to the connectors schema.
// --dry-run prints the transformed YAML and the mapping summary without
// touching anything; otherwise each legacy file is backed up, swapped, and
// the whole config re-validated (restoring the file if validation fails).
func cmdConfigMigrate(args []string) error {
	path, _ := configPath(args)
	dry := slices.Contains(args, "--dry-run")
	if dry {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		res, err := migrate.Transform(raw)
		if err != nil {
			return err
		}
		if !res.Changed {
			fmt.Println("nothing to migrate: no legacy constructs found")
			return nil
		}
		os.Stdout.Write(res.Output)
		fmt.Fprintln(os.Stderr, "\n# mapping summary:")
		for _, s := range res.Summary {
			fmt.Fprintln(os.Stderr, "#  - "+s)
		}
		fmt.Fprintln(os.Stderr, "# dry-run: nothing written")
		return nil
	}
	n, summary, err := migrate.AutoMigrate(path, func() error { return validateAt(args) }, logf)
	for _, s := range summary {
		fmt.Println("  - " + s)
	}
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("nothing to migrate: no legacy constructs found")
		return nil
	}
	fmt.Printf("migrated %d file(s); originals backed up with the %s suffix\n", n, migrate.BackupSuffix)
	return nil
}

// validateAt runs the full load+validate pipeline (legacy integrations,
// agent refs, and the connectors-model semantic pass) against the current
// on-disk config.
func validateAt(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	if err := validateAll(cfg, igs); err != nil {
		return err
	}
	_, err = buildFlowStack(cfg, nil, nil, true)
	return err
}

// autoMigrateOnBoot runs the automatic in-place migration when the daemon
// starts on a legacy config (deployed boxes auto-update; a schema change that
// required a manual edit would crash-loop them). Fail-safe: on any error the
// original config stays in place and the daemon keeps running on it — the
// returned warning is surfaced through notify once the notifier exists.
func autoMigrateOnBoot(args []string) (warning string) {
	path, _ := configPath(args)
	if _, err := os.Stat(path); err != nil {
		return "" // no config; normal load error handling reports it
	}
	n, _, err := migrate.AutoMigrate(path, func() error { return validateAt(args) }, logf)
	if err != nil {
		logf("config migrate: %v — staying on the legacy config", err)
		return fmt.Sprintf("config needs manual migration: %v", err)
	}
	if n > 0 {
		logf("config migrate: %d file(s) now on the connectors schema", n)
	}
	return ""
}
