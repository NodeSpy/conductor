package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/configmigrate"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// cmdConfig handles `conductor config migrate [--dry-run]`.
func cmdConfig(args []string) error {
	rest := positional(args)
	if len(rest) > 0 && rest[0] == "migrate-check" {
		return cmdConfigMigrateCheck(args) // internal: checkConfigConnectors' child
	}
	if len(rest) == 0 || rest[0] != "migrate" {
		return fmt.Errorf("usage: conductor config migrate [--dry-run]")
	}
	return cmdConfigMigrate(args)
}

// cmdConfigMigrate rewrites retired config shapes in place
// (internal/configmigrate) — the same rewrite the daemon makes at boot, run
// by hand. --dry-run prints each rewritten file and the notes and writes
// nothing. A legacy-schema config (before the plugin contract) is not this
// command's: it loads with an error naming v0.60.0, whose `config migrate`
// handles it.
func cmdConfigMigrate(args []string) error {
	path, _ := configPath(args)
	if slices.Contains(args, "--dry-run") {
		plan, err := configmigrate.Compute(path, configmigrate.Options{})
		if err != nil {
			return err
		}
		for _, n := range plan.Notes {
			fmt.Fprintln(os.Stderr, "# "+n)
		}
		if !plan.Changed() {
			fmt.Println("nothing to migrate")
			return nil
		}
		for _, f := range plan.ChangedFiles() {
			fmt.Printf("# --- %s\n", f.Path)
			os.Stdout.Write(f.Output())
		}
		if len(plan.Uses) > 0 {
			fmt.Fprintf(os.Stderr, "# needs plugin(s): %s (installed before anything is written)\n", strings.Join(plan.Uses, ", "))
		}
		fmt.Fprintln(os.Stderr, "# dry run: nothing written")
		return nil
	}
	res, err := configmigrate.Apply(path, migrateOptions(path, true))
	if res != nil {
		for _, n := range res.Notes {
			fmt.Println("  - " + n)
		}
	}
	if err != nil {
		return err
	}
	if len(res.Files) == 0 {
		fmt.Println("nothing to migrate")
		return nil
	}
	for i, f := range res.Files {
		fmt.Printf("rewrote %s (backup: %s)\n", f, res.Backups[i])
	}
	return nil
}

// migrateOptions wires a migration run to the daemon: Check is the full
// load + connector build `validate` runs, Ensure installs the plugins the
// rewrite starts using (gaps only — nothing installed moves).
var migrateOptions = func(mainPath string, cli bool) configmigrate.Options {
	return configmigrate.Options{
		Check:  checkConfigConnectors,
		Ensure: func(uses []string) error { return ensureMigrationPlugins(mainPath, uses, cli) },
		Logf:   logf,
	}
}

// autoMigrateOnBoot rewrites retired config shapes before the daemon loads
// its config (deployed boxes auto-update; a release that needed a hand edit
// would leave them dark). Fail-safe: on any error nothing is left rewritten,
// the daemon runs on the config as it is, and the returned warning is
// surfaced once the notifier exists. The next boot tries again.
func autoMigrateOnBoot(args []string) (warning string) {
	path, _ := configPath(args)
	if _, err := os.Stat(path); err != nil {
		return "" // no config: the load reports it
	}
	res, err := configmigrate.Apply(path, migrateOptions(path, false))
	if res != nil {
		for _, n := range res.Notes {
			logf("config migrate: %s", n)
		}
	}
	if err != nil {
		logf("config migrate: %v — running on the config as it is", err)
		return fmt.Sprintf("config migrate did not complete: %v", err)
	}
	if len(res.Files) > 0 {
		logf("config migrate: %d file(s) rewritten", len(res.Files))
	}
	return ""
}

// checkConfigConnectors is a migration's validation: the full load +
// connector build `validate` runs, against the file at path, reporting the
// configured connectors that build disabled. It runs in a child process of
// this binary (`config migrate-check`): a build registers each plugin's
// connector types for the life of the process, so building here would make
// the daemon's own boot build that follows refuse them as duplicates.
func checkConfigConnectors(path string) (map[string]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "config", "migrate-check", "--config", path, "--state-dir", config.StateDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = strings.TrimPrefix(msg, "error: ")
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	disabled := map[string]string{}
	if err := json.Unmarshal(out, &disabled); err != nil {
		return nil, fmt.Errorf("config migrate-check: %w", err)
	}
	return disabled, nil
}

// cmdConfigMigrateCheck is checkConfigConnectors' child: it prints the
// disabled connectors as a JSON object (name → reason), or fails with the
// load/build error.
func cmdConfigMigrateCheck(args []string) error {
	path, _ := configPath(args)
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		return err
	}
	defer stack.Close()
	disabled := map[string]string{}
	if stack != nil {
		for name := range cfg.ConnectorsMap {
			if in, ok := stack.Registry.Get(name); ok && in.Enabled && in.DisabledReason != "" {
				disabled[name] = in.DisabledReason
			}
		}
	}
	return json.NewEncoder(os.Stdout).Encode(disabled)
}

// migrationPluginTimeout bounds the boot-time install of a plugin a
// migration needs.
var migrationPluginTimeout = 2 * time.Minute

// ensureMigrationPlugins installs every plugin uses names that is not
// installed yet (the boot gap-fill's own pass, with the migration's uses
// added to the config's references), and fails unless each ends installed.
func ensureMigrationPlugins(mainPath string, uses []string, cli bool) error {
	loadEnvFile(filepath.Join(filepath.Dir(mainPath), "conductor.env"))
	cfg, err := config.Load(mainPath)
	if err != nil {
		return fmt.Errorf("load the config to install plugins: %w", err)
	}
	cfg = withMigrationUses(cfg, uses)
	type out struct {
		res []plugin.Resolution
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := reconcilePlugins(cfg, plugin.Options{GapsOnly: true, Log: logf}, cli)
		done <- out{res, err}
	}()
	var o out
	select {
	case o = <-done:
	case <-time.After(migrationPluginTimeout):
		return fmt.Errorf("still fetching after %s", migrationPluginTimeout)
	}
	if o.err != nil {
		return o.err
	}
	want := map[string]bool{}
	for _, u := range uses {
		if pu, err := config.ParseUse(config.UseKindConnector, u); err == nil {
			want[pu.InstallKey()] = true
		}
	}
	for _, r := range o.res {
		if want[r.Key] && r.Err != nil {
			return fmt.Errorf("%s: %w", r.Name, r.Err)
		}
		delete(want, r.Key)
	}
	state := plugin.LoadInstallState(plugin.InstallDir())
	for key := range want {
		if len(state.AllVersions(key)) == 0 {
			return fmt.Errorf("%s did not install", key)
		}
	}
	return nil
}

// withMigrationUses returns cfg with a reference to each of uses added, so a
// plugin pass (install, fetchability) covers the plugins a pending migration
// will start using. cfg itself is not modified.
func withMigrationUses(cfg *config.Config, uses []string) *config.Config {
	cp := *cfg
	cp.ConnectorsMap = make(map[string]config.ConnectorRef, len(cfg.ConnectorsMap)+len(uses))
	for k, v := range cfg.ConnectorsMap {
		cp.ConnectorsMap[k] = v
	}
	for _, u := range uses {
		cp.ConnectorsMap["config-migrate:"+u] = config.ConnectorRef{Use: u}
	}
	return &cp
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
		return fmt.Sprintf("run `%s config migrate` (the release this box auto-updated from, saved alongside this binary — v0.60.0 is the last release whose `config migrate` handles the legacy schema), then upgrade", prev)
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

// reloadSignals subscribes to SIGHUP. cmdRun calls it before anything else:
// SIGHUP's default action ends the process without a word, and a reload can
// arrive at any point of a boot (two applies in a row; a SIGHUP to a daemon
// holding degraded). A SIGHUP that arrives before the reload handler runs
// waits in the channel and reloads once boot is done, since the config may
// have changed after it was read.
func reloadSignals() chan os.Signal {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	return hup
}

// degradedRetryInterval paces the degraded-boot hold (a test shortens it).
var degradedRetryInterval = time.Minute

// holdDegradedUntilLoadable keeps the daemon process ALIVE when the config
// does not load. Exiting would just have the service manager restart us into
// the same wall forever (a silent crash-loop). It logs the blocker loudly,
// retries the migration and the load on a ticker, and returns the config the moment a retry
// succeeds (an operator edit is picked up without intervention). SIGINT/SIGTERM
// end the hold.
//
// warning may be ""; the escalate message is then synthesized from the load
// error so the returned warning is always non-empty for the caller's escalate
// notify.
//
// hup carries SIGHUP (reloadSignals). A reload request retries the load at
// once and logs the outcome; it never ends the hold.
func holdDegradedUntilLoadable(args []string, warning string, loadErr error, hup <-chan os.Signal) (*config.Config, string, error) {
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
		case <-hup:
			logf("BOOT DEGRADED: reload requested (SIGHUP) — retrying the load now")
			cfg, _, err := loadConfig(args)
			if err == nil {
				logf("BOOT DEGRADED: config loads now — resuming normal boot")
				return cfg, warning, nil
			}
			loadErr = err
			logf("BOOT DEGRADED: still not loadable: %v", err)
		case <-t.C:
			_ = autoMigrateOnBoot(args)
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
