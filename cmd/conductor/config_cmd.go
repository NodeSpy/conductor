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
// by v0.60.0 — the last release before the plugin contract, and the last one
// with `config migrate` at all — and this binary's `validate` names any
// legacy block still present.
func cmdConfig(args []string) error {
	rest := positional(args)
	if len(rest) > 0 && rest[0] == "migrate" {
		return fmt.Errorf("`conductor config migrate` was removed with the legacy config schema: %s", migrateHint())
	}
	return fmt.Errorf("usage: conductor config <subcommand> (none remain; `migrate` was removed with the legacy config schema): %s", migrateHint())
}

// migrateHint is the actionable half of every "run `conductor config
// migrate` with v0.60.0 (the last release before the plugin contract)"
// message in this codebase (internal/config, internal/secrets, this file):
// the ONE place
// that knows whether a usable previous-release binary actually exists on
// this box. Before 5b, that advice pointed at a binary the operator usually
// no longer had — an unattended auto-update replaces the executable in
// place with nothing kept behind. doUpdate now saves the replaced binary
// next to the new one as <exe>.prev (best effort), so when that file is
// present and executable, THAT is almost always the needed release; this
// tells the operator to run it directly instead of re-fetching anything.
func migrateHint() string {
	exe, err := os.Executable()
	if err != nil {
		return "run it with v0.60.0 (the last release before the plugin contract), then upgrade"
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return migrateHintForExe(exe)
}

// migrateHintForExe is migrateHint's testable half: given the daemon's own
// executable path, decide whether <exe>.prev — the rollback copy doUpdate
// saves (5b) — is there to run the old migrate command against. v0.60.0 is
// named explicitly (finding 8, LOW) — a release an operator can go find and
// install, rather than a description ("the release before the plugin
// contract") they'd first have to resolve into one.
func migrateHintForExe(exe string) string {
	prev := exe + prevBinarySuffix
	if info, serr := os.Stat(prev); serr == nil && !info.IsDir() {
		return fmt.Sprintf("run `%s config migrate` (the release this box auto-updated from, saved alongside this binary — v0.60.0 is the last release that still has `config migrate`), then upgrade", prev)
	}
	return "run it with v0.60.0 (the last release before the plugin contract), then upgrade (no " + prev + " was found on this box — fetch that release if you no longer have it)"
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
