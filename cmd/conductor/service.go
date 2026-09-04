package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// This file owns the per-user service unit (systemd on Linux, launchd on
// macOS): rendering it, installing/starting it, and — crucially — keeping the
// installed unit in sync when the template changes across releases. Both the
// installer (scripts/service.sh) and `update`/auto-update generate the unit
// through here, so there's a single source of truth.

func serviceKind() string {
	switch runtime.GOOS {
	case "linux":
		return "systemd"
	case "darwin":
		return "launchd"
	default:
		return ""
	}
}

// launchedByServiceManager reports whether THIS process was started by the
// per-user service manager (vs. a manual `conductor run` in a shell). When
// true, an auto-update applies by exiting cleanly so the manager relaunches the
// new binary with the unit's fresh environment — a re-exec would instead inherit
// our stale env and never pick up a regenerated unit.
func launchedByServiceManager() bool {
	switch serviceKind() {
	case "systemd":
		// systemd sets INVOCATION_ID in the environment of every unit it starts.
		return os.Getenv("INVOCATION_ID") != ""
	case "launchd":
		// launchd exports the job label as XPC_SERVICE_NAME for managed agents,
		// and is PID 1 (the parent of its LaunchAgents).
		if n := os.Getenv("XPC_SERVICE_NAME"); n != "" && n != "0" {
			return true
		}
		return os.Getppid() == 1
	}
	return false
}

func selfExe() string {
	p, err := os.Executable()
	if err != nil {
		return "conductor"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func launchdLog() string { return filepath.Join(home(), "Library/Logs/conductor.log") }

// serviceName is the per-user service unit / launchd label name to use for
// every systemctl/launchctl operation.
func serviceName() string {
	return "conductor"
}

// configDir returns the config directory to use: ~/.config/conductor.
func configDir() string {
	return filepath.Join(home(), ".config/conductor")
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// servicePATH builds the PATH the service should run with. A --user service
// otherwise inherits systemd/launchd's minimal PATH, so `paseo`, `gh`, `go`, and
// `claude` in ~/.local/bin aren't found. Guarantee ~/.local/bin + the standard
// dirs, then append the install-time PATH (deduped) so wherever the user keeps
// those tools comes along too.
func servicePATH() string {
	seen := map[string]bool{}
	var parts []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	add(filepath.Join(home(), ".local/bin"))
	for _, p := range []string{
		"/opt/homebrew/bin", "/opt/homebrew/sbin", // macOS (Apple silicon)
		"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
		"/usr/local/go/bin", "/usr/lib/go/bin", filepath.Join(home(), "go/bin"),
	} {
		add(p)
	}
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		add(p)
	}
	return strings.Join(parts, ":")
}

// unitPathAndContent returns the install path and rendered content of the
// service unit for the current OS, for THIS install: exe=selfExe(),
// cfg=configDir(), name=serviceName(). Empty path => unsupported OS.
func unitPathAndContent() (path, content string) {
	return renderUnit(selfExe(), configDir(), serviceName())
}

// renderUnit renders the service unit for the current OS for an explicit
// exe/cfg/name. Empty path => unsupported OS.
func renderUnit(exe, cfg, name string) (path, content string) {
	switch serviceKind() {
	case "systemd":
		path = filepath.Join(home(), ".config/systemd/user", name+".service")
		content = fmt.Sprintf(`[Unit]
Description=conductor — event-driven agent orchestration for your Paseo daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run
Restart=always
RestartSec=5
Environment=PATH=%s
EnvironmentFile=-%s/conductor.env

[Install]
WantedBy=default.target
`, exe, servicePATH(), cfg)
	case "launchd":
		path = filepath.Join(home(), "Library/LaunchAgents", "sh."+name+".plist")
		log := launchdLog()
		content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>sh.%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict><key>PATH</key><string>%s</string></dict>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, name, exe, servicePATH(), log, log)
	}
	return path, content
}

// writeUnitIfChanged writes the unit for the current OS, returning whether the
// on-disk content changed.
func writeUnitIfChanged() (path string, changed bool, err error) {
	path, content := unitPathAndContent()
	if path == "" {
		return "", false, fmt.Errorf("unsupported OS for a service unit")
	}
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// syncServiceUnit refreshes an *already-installed* unit if its content changed
// (called by update/auto-update). It never creates a service that wasn't there.
// restart=true applies the change to the running service immediately.
func syncServiceUnit(restart bool) (bool, error) {
	path, _ := unitPathAndContent()
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		return false, nil // no unit installed → nothing to sync
	}
	_, changed, err := writeUnitIfChanged()
	if err != nil {
		return false, err
	}
	if changed {
		reloadService(restart)
		logf("service unit refreshed (%s)", path)
	}
	return changed, nil
}

func reloadService(restart bool) {
	switch serviceKind() {
	case "systemd":
		_ = sh("systemctl", "--user", "daemon-reload")
		if restart {
			_ = sh("systemctl", "--user", "try-restart", serviceName())
		}
	case "launchd":
		if restart {
			p, _ := unitPathAndContent()
			_ = exec.Command("launchctl", "unload", p).Run()
			_ = sh("launchctl", "load", "-w", p)
		}
	}
}

// restartInstalledService restarts an already-installed & currently-running unit
// so a freshly-downloaded binary takes over now. It never starts a service that
// isn't installed or isn't already running. Returns whether it restarted one —
// used by the manual `update` CLI (the daemon's own auto-update restarts itself
// by exiting for the manager to relaunch; see applyUpdate).
func restartInstalledService() bool {
	path, _ := unitPathAndContent()
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false // no unit installed → nothing to restart
	}
	switch serviceKind() {
	case "systemd":
		// try-restart is a no-op on a stopped unit, so gate on is-active to only
		// claim a restart when one actually happened.
		if exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName()).Run() != nil {
			return false
		}
		_ = sh("systemctl", "--user", "daemon-reload")
		return sh("systemctl", "--user", "restart", serviceName()) == nil
	case "launchd":
		// kickstart -k restarts the running agent (error ⇒ not loaded ⇒ false).
		return sh("launchctl", "kickstart", "-k",
			fmt.Sprintf("gui/%d/sh.%s", os.Getuid(), serviceName())) == nil
	}
	return false
}

func startHint() string {
	switch serviceKind() {
	case "systemd":
		return "systemctl --user enable --now " + serviceName()
	case "launchd":
		p, _ := unitPathAndContent()
		return "launchctl load -w " + p
	}
	return ""
}

// cmdService: install | sync | uninstall.
func cmdService(args []string) error {
	sub := "install"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "install":
		return serviceInstall()
	case "sync":
		_, err := syncServiceUnit(true)
		return err
	case "uninstall":
		return serviceUninstall()
	default:
		return fmt.Errorf("unknown service subcommand %q (install|sync|uninstall)", sub)
	}
}

// serviceInstall writes the unit and, if the config validates, enables+starts
// it. On an invalid config it installs the unit but does not start it.
func serviceInstall() error {
	if serviceKind() == "" {
		return fmt.Errorf("no supported service manager on %s", runtime.GOOS)
	}
	path, _, err := writeUnitIfChanged()
	if err != nil {
		return err
	}
	if err := serviceConfigValid(); err != nil {
		reloadService(false) // load the (new) unit, but don't start with a bad config
		logf("service unit installed at %s but NOT started — config isn't ready: %v", path, err)
		logf("start it after fixing config: %s", startHint())
		return nil
	}
	return startService()
}

func startService() error {
	switch serviceKind() {
	case "systemd":
		if err := sh("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := sh("systemctl", "--user", "enable", "--now", serviceName()); err != nil {
			return err
		}
		_ = sh("loginctl", "enable-linger", os.Getenv("USER"))
		logf("==> systemd --user service installed and started (logs: journalctl --user -u %s -f)", serviceName())
	case "launchd":
		p, _ := unitPathAndContent()
		_ = exec.Command("launchctl", "unload", p).Run()
		if err := sh("launchctl", "load", "-w", p); err != nil {
			return err
		}
		logf("==> launchd agent installed and started (logs: tail -f %s)", launchdLog())
	}
	return nil
}

func serviceUninstall() error {
	p, _ := unitPathAndContent()
	switch serviceKind() {
	case "systemd":
		_ = sh("systemctl", "--user", "disable", "--now", serviceName())
	case "launchd":
		_ = exec.Command("launchctl", "unload", p).Run()
	}
	if p != "" {
		_ = os.Remove(p)
	}
	logf("service uninstalled")
	return nil
}

// serviceConfigValid loads + validates the config (no output on success).
func serviceConfigValid() error {
	cfg, _, err := loadConfig(nil)
	if err != nil {
		return err
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	for _, ig := range igs {
		if err := ig.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func sh(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	return c.Run()
}
