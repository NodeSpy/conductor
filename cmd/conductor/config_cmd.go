package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// cmdConfig handles `conductor config`. Its one subcommand, `migrate`, went
// with the legacy schema (plugin-contract.md Q4): a legacy config is migrated
// by the release before the plugin contract, and this binary's `validate`
// names any legacy block still present.
func cmdConfig(args []string) error {
	rest := positional(args)
	if len(rest) > 0 && rest[0] == "migrate" {
		return fmt.Errorf("`conductor config migrate` was removed with the legacy config schema: run it with the release before the plugin contract, then upgrade")
	}
	return fmt.Errorf("usage: conductor config <subcommand> (none remain; `migrate` was removed with the legacy config schema)")
}

// validateAt runs the full load+validate pipeline (the connectors-model
// semantic pass) against the current on-disk config.
func validateAt(args []string) error {
	path, _ := configPath(args)
	return validateConfigFile(path)
}

// validateConfigFile is validateAt against an explicit file (the dry-run
// writes the transform to a scratch file beside the config so relative
// imports and the sibling conductor.env resolve identically).
func validateConfigFile(path string) error {
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	stack.Close()
	return err
}

// degradedRetryInterval paces the degraded-boot hold (a test shortens it).
var degradedRetryInterval = time.Minute

// holdDegradedUntilLoadable keeps the daemon process ALIVE when the config
// does not load. Exiting would just have the service manager restart us into
// the same wall forever (a silent crash-loop). It logs the blocker loudly,
// retries the load on a ticker, and returns the config the moment a retry
// succeeds (an operator edit is picked up without intervention). SIGINT/SIGTERM
// end the hold.
//
// warning may be ""; the escalate message is then synthesized from the load
// error so the returned warning is always non-empty for the caller's escalate
// notify.
func holdDegradedUntilLoadable(args []string, warning string, loadErr error) (*config.Config, string, error) {
	if warning == "" {
		warning = fmt.Sprintf("config does not load at boot: %v", loadErr)
	}
	logf("BOOT DEGRADED: config does not load: %v", loadErr)
	logf("BOOT DEGRADED: %s", warning)
	logf("BOOT DEGRADED: holding (nothing dispatches) and retrying every %s — the next successful load resumes a normal boot", degradedRetryInterval)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	t := time.NewTicker(degradedRetryInterval)
	defer t.Stop()
	for {
		select {
		case <-sig:
			return nil, warning, fmt.Errorf("shut down while boot-degraded — the config never loaded: %w", loadErr)
		case <-t.C:
			cfg, _, err := loadConfig(args)
			if err == nil {
				logf("BOOT DEGRADED: config loads now — resuming normal boot")
				return cfg, warning, nil
			}
			loadErr = err
			logf("BOOT DEGRADED: still not loadable: %v", err)
		}
	}
}
