package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// This file owns `conductor migrate`: a one-time, fail-safe migration of an
// existing paseo-conductor install (binary, service unit, config dir, state
// dir) to the new conductor name. It runs on live, auto-updating production
// boxes, so the overriding rule is: any failure before the new service is
// CONFIRMED ACTIVE must leave the old paseo-conductor service exactly as it
// was. Data is always copied, never moved, so the legacy install stays intact
// as a rollback/backup regardless of how migrate ends.
//
// The service-manager calls (stop/write/start/verify/remove/rollback) go
// through the serviceOps interface so the stop→write→start→verify→[remove |
// rollback] ORDER — and specifically the rollback path — can be asserted in
// tests with a fake, without touching a real systemd/launchd.

// ---- path resolution (pure; HOME-driven like configDir()/serviceName(), so
// t.Setenv("HOME", tmp) covers it in tests) ----

func legacyBinPath() string { return filepath.Join(home(), ".local/bin/paseo-conductor") }
func newBinPath() string    { return filepath.Join(home(), ".local/bin/conductor") }

func legacyConfigDirAbs() string { return filepath.Join(home(), ".config/paseo-conductor") }
func newConfigDirAbs() string    { return filepath.Join(home(), ".config/conductor") }

func legacyStateDirAbs() string { return filepath.Join(home(), ".local/state/paseo-conductor") }
func newStateDirAbs() string    { return filepath.Join(home(), ".local/state/conductor") }

// newServiceUnitPath is the install path of the (new) conductor unit for this
// OS — independent of serviceName()'s legacy-detection fallback, since migrate
// must always target this exact path when writing/detecting the new unit,
// regardless of what's currently installed. Empty string => unsupported OS.
func newServiceUnitPath() string {
	switch serviceKind() {
	case "systemd":
		return filepath.Join(home(), ".config/systemd/user/conductor.service")
	case "launchd":
		return filepath.Join(home(), "Library/LaunchAgents/sh.conductor.plist")
	}
	return ""
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// ---- detection (pure) ----

// migrationStatus captures what's on disk, decided up front so migrate can
// make one clean fail-safe call: is there anything to migrate, and has this
// box already been migrated?
type migrationStatus struct {
	LegacyBin       bool
	LegacyUnit      bool
	LegacyConfigDir bool
	NewUnitExists   bool
}

func (s migrationStatus) legacyPresent() bool {
	return s.LegacyBin || s.LegacyUnit || s.LegacyConfigDir
}

// needsMigration is false when there's no legacy install at all, or when this
// box already has a conductor service unit (already migrated) — either way
// `migrate` is then a safe, clear no-op.
func (s migrationStatus) needsMigration() bool {
	return s.legacyPresent() && !s.NewUnitExists
}

func detectMigration() migrationStatus {
	return migrationStatus{
		LegacyBin:       fileExists(legacyBinPath()),
		LegacyUnit:      legacyServiceUnitPath() != "",
		LegacyConfigDir: isDir(legacyConfigDirAbs()),
		NewUnitExists:   fileExists(newServiceUnitPath()),
	}
}

// ---- recursive copy (filesystem only; no service calls — testable against
// temp dirs) ----

// copyTree copies src (a file or directory tree) to dst, creating directories
// as needed and preserving file mode. It only ever reads src (copy, never
// move) and is safe to re-run: it overwrites files at dst but never deletes
// anything at dst that isn't also present at src.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	return copyPath(src, dst, info)
}

func copyPath(src, dst string, info fs.FileInfo) error {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst) // best-effort: replace a stale link/file on re-run
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			ei, err := e.Info()
			if err != nil {
				return err
			}
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), ei); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyFile(src, dst, info.Mode())
	}
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyDirSkipExisting copies src -> dst, but only when dst does not already
// exist (migrate's "skip if the dest already exists" rule, so a re-run never
// clobbers data that's since diverged at the new location). copied=false,
// err=nil covers both "nothing to copy" (no src) and "already migrated"
// (dst exists).
func copyDirSkipExisting(src, dst string) (copied bool, err error) {
	if !isDir(src) {
		return false, nil
	}
	if fileExists(dst) {
		return false, nil
	}
	if err := copyTree(src, dst); err != nil {
		return false, err
	}
	return true, nil
}

// ---- service-manager seam ----

// serviceOps is the injectable seam over the handful of service-manager calls
// migrate makes. realServiceOps shells out to systemctl/launchctl; tests
// substitute a fake that records call order.
type serviceOps interface {
	// stopOld stops (but does not disable/remove) the currently-installed
	// paseo-conductor service, if any.
	stopOld() error
	// writeNewUnit renders + writes the conductor unit and reloads the
	// service manager so it's known, but does not start it.
	writeNewUnit() error
	// startNew enables+starts the just-written conductor unit.
	startNew() error
	// verifyActive polls whether the conductor unit is actually active, up
	// to timeout.
	verifyActive(timeout time.Duration) bool
	// removeOld disables and deletes the old paseo-conductor unit. Only
	// called after the new service is confirmed active.
	removeOld() error
	// rollback removes the half-written conductor unit and restarts the old
	// paseo-conductor service. The old unit/dirs were never touched by any
	// prior step, so this is just "start it back up".
	rollback() error
}

type realServiceOps struct{}

func (realServiceOps) stopOld() error {
	switch serviceKind() {
	case "systemd":
		return sh("systemctl", "--user", "stop", "paseo-conductor")
	case "launchd":
		return exec.Command("launchctl", "unload", legacyServiceUnitPath()).Run()
	}
	return nil
}

func (realServiceOps) writeNewUnit() error {
	path, content := renderUnit(newBinPath(), newConfigDirAbs(), "conductor")
	if path == "" {
		return fmt.Errorf("unsupported OS for a service unit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	if serviceKind() == "systemd" {
		return sh("systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (realServiceOps) startNew() error {
	switch serviceKind() {
	case "systemd":
		return sh("systemctl", "--user", "enable", "--now", "conductor")
	case "launchd":
		p := newServiceUnitPath()
		_ = exec.Command("launchctl", "unload", p).Run()
		return sh("launchctl", "load", "-w", p)
	}
	return nil
}

func (realServiceOps) verifyActive(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if isServiceActive("conductor") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func isServiceActive(name string) bool {
	switch serviceKind() {
	case "systemd":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", name).Run() == nil
	case "launchd":
		return exec.Command("launchctl", "kill", "-0", fmt.Sprintf("gui/%d/sh.%s", os.Getuid(), name)).Run() == nil
	}
	return false
}

func (realServiceOps) removeOld() error {
	switch serviceKind() {
	case "systemd":
		_ = sh("systemctl", "--user", "disable", "--now", "paseo-conductor")
	case "launchd":
		_ = exec.Command("launchctl", "unload", legacyServiceUnitPath()).Run()
	}
	if p := legacyServiceUnitPath(); p != "" {
		_ = os.Remove(p)
	}
	if serviceKind() == "systemd" {
		return sh("systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (realServiceOps) rollback() error {
	if p := newServiceUnitPath(); p != "" {
		_ = os.Remove(p)
	}
	switch serviceKind() {
	case "systemd":
		_ = sh("systemctl", "--user", "daemon-reload")
		return sh("systemctl", "--user", "start", "paseo-conductor")
	case "launchd":
		p := legacyServiceUnitPath()
		_ = exec.Command("launchctl", "unload", p).Run() // best-effort; may already be unloaded
		return sh("launchctl", "load", "-w", p)
	}
	return nil
}

// ---- the fail-safe orchestrator ----

// migrateService runs the fail-safe stop -> write-new -> start-new -> verify
// -> [remove-old | rollback] sequence. It never disables/removes the old unit
// until verifyActive succeeds; any failure before that rolls back to the old
// service instead (restarting paseo-conductor, which was never otherwise
// modified). A failure removing the OLD unit after a confirmed-active new
// service is logged but does not fail the migration — the new service is
// already up.
func migrateService(ops serviceOps, verifyTimeout time.Duration) error {
	if err := ops.stopOld(); err != nil {
		return fmt.Errorf("stop old service: %w (nothing else touched — old service state is unchanged)", err)
	}
	if err := ops.writeNewUnit(); err != nil {
		return rollbackAfter(ops, fmt.Errorf("write new unit: %w", err))
	}
	if err := ops.startNew(); err != nil {
		return rollbackAfter(ops, fmt.Errorf("start new service: %w", err))
	}
	if !ops.verifyActive(verifyTimeout) {
		return rollbackAfter(ops, fmt.Errorf("new conductor service did not become active"))
	}
	if err := ops.removeOld(); err != nil {
		logf("migrate: new service is active, but removing the old paseo-conductor unit failed: %v (safe to remove manually)", err)
	}
	return nil
}

// rollbackAfter restores the old service after a failed step and wraps cause
// with the outcome of that restore, so the printed error always tells the
// operator whether paseo-conductor is back up.
func rollbackAfter(ops serviceOps, cause error) error {
	if rerr := ops.rollback(); rerr != nil {
		return fmt.Errorf("%w (rollback ALSO failed: %v — restart paseo-conductor manually: %s)",
			cause, rerr, startHint())
	}
	return fmt.Errorf("%w (rolled back: old paseo-conductor service restarted, untouched)", cause)
}

// ---- CLI ----

// cmdMigrate is the `migrate [--dry-run]` subcommand.
func cmdMigrate(args []string) error {
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		default:
			return fmt.Errorf("unknown migrate flag %q (usage: conductor migrate [--dry-run])", a)
		}
	}

	st := detectMigration()
	if !st.needsMigration() {
		if st.NewUnitExists {
			fmt.Println("nothing to migrate: a conductor service is already installed")
		} else {
			fmt.Println("nothing to migrate: no legacy paseo-conductor install found")
		}
		return nil
	}

	if dryRun {
		printMigratePlan(st)
		return nil
	}

	fmt.Println("==> copying data (legacy install left untouched — copy, never move)")
	if err := doMigrateCopies(); err != nil {
		return fmt.Errorf("migrate: copy step failed, nothing else was touched: %w", err)
	}

	if serviceKind() == "" {
		fmt.Println("no supported service manager on this OS — files copied; set up the background service manually (see README)")
		return nil
	}
	if !st.LegacyUnit {
		fmt.Println("no legacy service unit found — files copied; nothing running to stop/migrate. Install the conductor service yourself: conductor service install")
		return nil
	}

	fmt.Println("==> migrating the service (brief downtime expected)")
	if err := migrateService(realServiceOps{}, 10*time.Second); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	fmt.Println("==> success: conductor service is active")
	fmt.Println("backup of the old install kept on disk (safe to remove once you're confident):")
	fmt.Printf("  %s\n  %s\n  %s\n", legacyBinPath(), legacyConfigDirAbs(), legacyStateDirAbs())
	return nil
}

// doMigrateCopies performs the copy-never-move data migration (binary, config
// dir, state dir). Any failure here leaves the legacy install completely
// unmodified — these are pure additive writes to the NEW paths.
func doMigrateCopies() error {
	if fileExists(newBinPath()) {
		fmt.Printf("  binary  already exists at %s — left as-is\n", newBinPath())
	} else {
		if err := copyRunningBinary(newBinPath()); err != nil {
			return fmt.Errorf("copy binary to %s: %w", newBinPath(), err)
		}
		fmt.Printf("  binary  -> %s\n", newBinPath())
	}

	copied, err := copyDirSkipExisting(legacyConfigDirAbs(), newConfigDirAbs())
	if err != nil {
		return fmt.Errorf("copy config to %s: %w", newConfigDirAbs(), err)
	}
	if copied {
		fmt.Printf("  config  -> %s\n", newConfigDirAbs())
	} else if isDir(newConfigDirAbs()) {
		fmt.Printf("  config  already exists at %s — left as-is\n", newConfigDirAbs())
	}

	copied, err = copyDirSkipExisting(legacyStateDirAbs(), newStateDirAbs())
	if err != nil {
		return fmt.Errorf("copy state to %s: %w", newStateDirAbs(), err)
	}
	if copied {
		fmt.Printf("  state   -> %s\n", newStateDirAbs())
	} else if isDir(newStateDirAbs()) {
		fmt.Printf("  state   already exists at %s — left as-is\n", newStateDirAbs())
	}
	return nil
}

// copyRunningBinary copies the currently-running executable — which, on an
// auto-updated legacy box, already contains this migrate command even though
// its on-disk filename is still paseo-conductor — to dst, marked executable.
// Falls back to the on-disk legacy binary path if selfExe() can't be read
// (e.g. invoked from a one-off downloaded copy rather than the installed
// path).
func copyRunningBinary(dst string) error {
	src := selfExe()
	if _, err := os.Stat(src); err != nil {
		src = legacyBinPath()
	}
	if err := copyFile(src, dst, 0o755); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

// printMigratePlan prints every step `migrate` would take, without touching
// anything — the operator-facing preflight for a live production box.
func printMigratePlan(st migrationStatus) {
	fmt.Println("dry run — no changes will be made")
	fmt.Println()
	fmt.Println("would copy:")
	fmt.Printf("  %s -> %s\n", selfExe(), newBinPath())
	if isDir(legacyConfigDirAbs()) {
		if isDir(newConfigDirAbs()) {
			fmt.Printf("  %s -> %s (skip: destination already exists)\n", legacyConfigDirAbs(), newConfigDirAbs())
		} else {
			fmt.Printf("  %s -> %s\n", legacyConfigDirAbs(), newConfigDirAbs())
		}
	}
	if isDir(legacyStateDirAbs()) {
		if isDir(newStateDirAbs()) {
			fmt.Printf("  %s -> %s (skip: destination already exists)\n", legacyStateDirAbs(), newStateDirAbs())
		} else {
			fmt.Printf("  %s -> %s\n", legacyStateDirAbs(), newStateDirAbs())
		}
	}

	if serviceKind() == "" {
		fmt.Println("\nno supported service manager on this OS — would stop after the file copies (set up the service manually)")
		return
	}
	if !st.LegacyUnit {
		fmt.Println("\nno legacy service unit found — would stop after the file copies")
		return
	}

	path, _ := renderUnit(newBinPath(), newConfigDirAbs(), "conductor")
	fmt.Println("\nwould migrate the service:")
	switch serviceKind() {
	case "systemd":
		fmt.Println("  systemctl --user stop paseo-conductor")
		fmt.Printf("  write %s\n", path)
		fmt.Println("  systemctl --user daemon-reload")
		fmt.Println("  systemctl --user enable --now conductor")
		fmt.Println("  poll: systemctl --user is-active conductor (up to 10s)")
		fmt.Println("  on success: systemctl --user disable --now paseo-conductor; rm " + legacyServiceUnitPath() + "; daemon-reload")
		fmt.Println("  on failure: rm " + path + "; daemon-reload; systemctl --user start paseo-conductor (rollback)")
	case "launchd":
		fmt.Println("  launchctl unload " + legacyServiceUnitPath())
		fmt.Printf("  write %s\n", path)
		fmt.Println("  launchctl load -w " + path)
		fmt.Printf("  poll: launchctl kill -0 gui/%d/sh.conductor (up to 10s)\n", os.Getuid())
		fmt.Println("  on success: launchctl unload " + legacyServiceUnitPath() + "; rm it")
		fmt.Println("  on failure: rm " + path + "; launchctl load -w " + legacyServiceUnitPath() + " (rollback)")
	}
}
