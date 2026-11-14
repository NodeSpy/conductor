package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/NodeSpy/conductor/internal/connector"
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
		for _, w := range res.Warnings {
			fmt.Println("warning: " + w)
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
// rewrite starts using (gaps only — nothing installed moves), and Grant
// gets a plugin it wires what that wiring needs (ensureMigrationGrant).
var migrateOptions = func(mainPath string, cli bool) configmigrate.Options {
	return configmigrate.Options{
		Check: checkConfigConnectors,
		Ensure: func(uses []string, from map[string][]string) error {
			return ensureMigrationPlugins(mainPath, uses, from, cli)
		},
		Grant: func(g configmigrate.Grant) ([]string, error) { return ensureMigrationGrant(mainPath, g, cli) },
		Logf:  logf,
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
		for _, w := range res.Warnings {
			logf("config migrate: warning: %s", w)
		}
	}
	if err != nil {
		logf("config migrate: %v — running on the config as it is", err)
		return fmt.Sprintf("config migrate did not complete: %v", err)
	}
	if len(res.Files) > 0 {
		logf("config migrate: %d file(s) rewritten", len(res.Files))
	}
	if len(res.Warnings) > 0 {
		return "config migrate: " + strings.Join(res.Warnings, "; ")
	}
	return ""
}

// checkConfigConnectors is a migration's validation: the full load +
// connector build `validate` runs, against the file at path, reporting the
// configured connectors that would not work — built disabled, or an
// exposure whose plugin would not be granted the listener it is wired to. It runs in a child process of
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
	failing := map[string]string{}
	if err := json.Unmarshal(out, &failing); err != nil {
		return nil, fmt.Errorf("config migrate-check: %w", err)
	}
	return failing, nil
}

// cmdConfigMigrateCheck is checkConfigConnectors' child: it prints the
// connectors that would not work as a JSON object (name → reason), or fails
// with the load/build error.
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
	failing := map[string]string{}
	if stack != nil {
		for name := range cfg.ConnectorsMap {
			if in, ok := stack.Registry.Get(name); ok && in.Enabled && in.DisabledReason != "" {
				failing[name] = "it starts disabled: " + in.DisabledReason
			}
		}
		if stack.Plugins != nil {
			maps.Copy(failing, stack.Plugins.ExposeRefusals())
		}
	}
	maps.Copy(failing, connector.ExposeRefusals(cfg))
	return json.NewEncoder(os.Stdout).Encode(failing)
}

// migrationPluginTimeout bounds the boot-time install of a plugin a
// migration needs.
var migrationPluginTimeout = 2 * time.Minute

// ensureMigrationPlugins installs every plugin uses names that is not
// installed yet (the boot gap-fill's own pass, with the migration's uses
// added to the config's references), and fails unless each ends installed.
//
// A plugin taking over what was done before the plugin jail — by a plugin
// installed before it, or by conductor itself (a builtin connector) —
// inherits that pre-jail mark (Installed.PreJail), installed or not yet:
// on a host that cannot jail plugins it then runs unjailed as what it
// replaces did, rather than being refused as a plugin installed since. The
// migration upgrades a working setup; it does not start a new one.
func ensureMigrationPlugins(mainPath string, uses []string, from map[string][]string, cli bool) error {
	loadEnvFile(filepath.Join(filepath.Dir(mainPath), "conductor.env"))
	cfg, err := config.Load(mainPath)
	if err != nil {
		return fmt.Errorf("load the config to install plugins: %w", err)
	}
	cfg = withMigrationUses(cfg, uses)
	inherit, err := inheritPreJail(from, cli)
	if err != nil {
		return err
	}
	res, err := boundedReconcile(cfg, plugin.Options{GapsOnly: true, Log: logf, PreJail: inherit}, cli)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, u := range uses {
		if pu, err := config.ParseUse(config.UseKindConnector, u); err == nil {
			want[pu.InstallKey()] = true
		}
	}
	for _, r := range res {
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

// inheritPreJail marks each plugin from names (by `use:`) that takes over
// from a pre-jail plugin or a builtin as pre-jail: its installed builds now,
// and the install keys returned for a fresh install's record.
func inheritPreJail(from map[string][]string, cli bool) (map[string]bool, error) {
	state := plugin.LoadInstallState(plugin.InstallDir())
	inherit := map[string]bool{}
	for use, olds := range from {
		pu, err := config.ParseUse(config.UseKindConnector, use)
		if err != nil {
			continue
		}
		for _, old := range olds {
			po, err := config.ParseUse(config.UseKindConnector, old)
			if err != nil {
				continue
			}
			if rec, ok := state.Get(po.InstallKey()); po.IsBuiltin() || (ok && rec.PreJail) {
				inherit[pu.InstallKey()] = true
				if !plugin.HostJailStatus().OK {
					logf("config migrate: %s takes over from %s, which ran before the plugin jail: on this host, which cannot jail plugins, it runs unjailed as that did", pu.Name, old)
				}
				break
			}
		}
	}
	if len(inherit) == 0 {
		return nil, nil
	}
	var lock *plugin.FileLock
	var err error
	if cli {
		lock, err = plugin.LockInstallStateCLI(plugin.InstallDir(), pluginLockCLITimeout, lockWaitNotice)
	} else {
		lock, err = plugin.LockInstallState(plugin.InstallDir())
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	state = plugin.LoadInstallState(plugin.InstallDir())
	changed := false
	for key := range inherit {
		changed = state.MarkPreJail(key) || changed
	}
	if changed {
		if err := state.Save(); err != nil {
			return nil, err
		}
	}
	return inherit, nil
}

// boundedReconcile is a migration's plugin pass, bounded by
// migrationPluginTimeout (a boot must not wait out a dead network).
func boundedReconcile(cfg *config.Config, opts plugin.Options, cli bool) ([]plugin.Resolution, error) {
	type out struct {
		res []plugin.Resolution
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := reconcilePlugins(cfg, opts, cli)
		done <- out{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-time.After(migrationPluginTimeout):
		return nil, fmt.Errorf("still fetching after %s", migrationPluginTimeout)
	}
}

// ensureMigrationGrant gets the installed plugin g.Use what a rewrite wires
// it for (configmigrate.Grant): its install record declares it already, or
// the release an update moves it to does. An update that asks for more than
// the installed build may is held for the operator's approval (`plugin
// approve`); the migration is the change that wires it, so it records that
// approval itself when what the update asks for beyond the installed build
// is exactly what g names — returned as a note, every new permission named.
// An update asking for anything more stays held, and the error holds the
// rewrite back, as it does when no release declares what g needs.
func ensureMigrationGrant(mainPath string, g configmigrate.Grant, cli bool) ([]string, error) {
	pu, err := config.ParseUse(config.UseKindConnector, g.Use)
	if err != nil {
		return nil, err
	}
	key := pu.InstallKey()
	declares := func(m plugin.Manifest) bool { return !g.ExposeLocal || m.ExposeLocal }
	installed := func() (plugin.Installed, bool) { return plugin.LoadInstallState(plugin.InstallDir()).Get(key) }
	cur, ok := installed()
	if !ok {
		return nil, fmt.Errorf("%s is not installed", pu.Name)
	}
	if declares(cur.Manifest) || plugin.RunsAsBefore(cur) {
		// (Unjailed as before the jail, it reaches the listener as it did.)
		return nil, nil
	}
	loadEnvFile(filepath.Join(filepath.Dir(mainPath), "conductor.env"))
	cfg, err := config.Load(mainPath)
	if err != nil {
		return nil, fmt.Errorf("load the config to update %s: %w", pu.Name, err)
	}
	cfg = withMigrationUses(cfg, []string{g.Use})
	if notes, ok := recordFromInstalledBuild(cfg, key, pu.Name, cur, g, cli); ok {
		return notes, nil
	}
	update := func() error {
		_, err := boundedReconcile(cfg, plugin.Options{Only: pu.Name, Log: logf}, cli)
		return err
	}
	if err := update(); err != nil {
		return nil, fmt.Errorf("updating %s (installed: %s): %w", pu.Name, cur.Resolved, err)
	}
	if now, ok := installed(); ok && declares(now.Manifest) {
		return []string{fmt.Sprintf("updated %s %s → %s, a release declaring expose_local", pu.Name, cur.Resolved, now.Resolved)}, nil
	}
	notes, err := approveForMigration(key, pu.Name, g, cli)
	if err != nil {
		return nil, err
	}
	if now, ok := installed(); !ok || !declares(now.Manifest) {
		// Approved, but its binary was gone: the next pass installs it.
		if err := update(); err != nil {
			return notes, fmt.Errorf("installing the approved %s: %w", pu.Name, err)
		}
	}
	if now, ok := installed(); !ok || !declares(now.Manifest) {
		return notes, fmt.Errorf("%s did not install a release declaring expose_local", pu.Name)
	}
	return notes, nil
}

// recordFromInstalledBuild covers an installed build that declares what g
// needs but whose record lacks it: one installed by a conductor before
// expose_local (the plugin SDK withholds it from those, and they would not
// record it anyway), and on v0.88/v0.89 without its config: entries too.
// The build itself is asked (a describe, from an empty jail); when it
// declares everything g needs, exactly those items are recorded — what the
// migration would approve in an update — and returned as a note. ok is
// false when the build does not declare them, or cannot be asked.
func recordFromInstalledBuild(cfg *config.Config, key, name string, cur plugin.Installed, g configmigrate.Grant, cli bool) (notes []string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), migrationPluginTimeout)
	defer cancel()
	decl, err := describeForInstall(cfg)(ctx, plugin.Spec{
		Name: name, Kind: plugin.KindConnector, Provides: name,
		BinPath: cur.Path, Sha256: cur.Sha256, Resolved: cur.Resolved, PreJail: cur.PreJail,
		Commands: cfg.CommandPaths(),
	})
	if err != nil || decl == nil {
		return nil, false
	}
	declared := plugin.ManifestFromDecl(decl)
	if g.ExposeLocal && !declared.ExposeLocal {
		return nil, false
	}
	var lock *plugin.FileLock
	if cli {
		lock, err = plugin.LockInstallStateCLI(plugin.InstallDir(), pluginLockCLITimeout, lockWaitNotice)
	} else {
		lock, err = plugin.LockInstallState(plugin.InstallDir())
	}
	if err != nil {
		return nil, false
	}
	defer lock.Unlock()
	state := plugin.LoadInstallState(plugin.InstallDir())
	rec, found := state.Get(key)
	if !found || rec.Sha256 != cur.Sha256 {
		return nil, false // replaced meanwhile: the update path judges it
	}
	before := rec.Manifest
	rec.Manifest.ExposeLocal = rec.Manifest.ExposeLocal || g.ExposeLocal
	for _, e := range g.Egress {
		if slices.Contains(declared.Egress, e) && !slices.Contains(rec.Manifest.Egress, e) {
			rec.Manifest.Egress = append(slices.Clone(rec.Manifest.Egress), e)
		}
	}
	state.Put(rec)
	if err := state.Save(); err != nil {
		return nil, false
	}
	return []string{fmt.Sprintf("recorded for %s %s what the rewrite wires it for, which that build declares but the conductor that installed it could not record: %s", name, rec.Resolved, strings.Join(before.Widens(rec.Manifest), "; "))}, true
}

// approveForMigration approves the update held for key when it declares what
// g needs and asks for nothing beyond the installed build but what g names.
func approveForMigration(key, name string, g configmigrate.Grant, cli bool) ([]string, error) {
	var lock *plugin.FileLock
	var err error
	if cli {
		lock, err = plugin.LockInstallStateCLI(plugin.InstallDir(), pluginLockCLITimeout, lockWaitNotice)
	} else {
		lock, err = plugin.LockInstallState(plugin.InstallDir())
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	state := plugin.LoadInstallState(plugin.InstallDir())
	cur, _ := state.Get(key)
	var held *plugin.Pending
	for _, p := range state.PendingUpdates() {
		if p.Key == key {
			held = &p
		}
	}
	if held == nil || (g.ExposeLocal && !held.Manifest.ExposeLocal) {
		return nil, fmt.Errorf("no %s release declaring expose_local is available yet (installed: %s)", name, cur.Resolved)
	}
	wired := cur.Manifest
	wired.ExposeLocal = wired.ExposeLocal || g.ExposeLocal
	wired.Egress = append(slices.Clone(wired.Egress), g.Egress...)
	if more := wired.Widens(held.Manifest); len(more) > 0 {
		return nil, fmt.Errorf("%s %s declares expose_local, but its update (held for approval) also asks for %s, which this migration does not wire — review and approve it with `conductor plugin approve`", name, held.Resolved, strings.Join(more, "; "))
	}
	done, err := state.Approve(plugin.ApproveArg(held.Name, held.Installed))
	if err != nil {
		return nil, err
	}
	if err := state.Save(); err != nil {
		return nil, err
	}
	var notes []string
	for _, p := range done {
		notes = append(notes, fmt.Sprintf("approved %s %s (replacing %s), the update the rewrite needs, which also may: %s", p.Name, p.Resolved, p.Replaces, strings.Join(p.Widens, "; ")))
	}
	return notes, nil
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
