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

func selfExe() string {
	p, err := os.Executable()
	if err != nil {
		return "paseo-conductor"
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

func launchdLog() string { return filepath.Join(home(), "Library/Logs/paseo-conductor.log") }

// unitPathAndContent returns the install path and rendered content of the
// service unit for the current OS. Empty path => unsupported OS.
func unitPathAndContent() (path, content string) {
	exe := selfExe()
	cfg := expandHome("~/.config/paseo-conductor")
	switch serviceKind() {
	case "systemd":
		path = filepath.Join(home(), ".config/systemd/user/paseo-conductor.service")
		content = fmt.Sprintf(`[Unit]
Description=paseo-conductor — event-driven agent orchestration for your Paseo daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run
Restart=always
RestartSec=5
EnvironmentFile=-%s/conductor.env

[Install]
WantedBy=default.target
`, exe, cfg)
	case "launchd":
		path = filepath.Join(home(), "Library/LaunchAgents/sh.paseo-conductor.plist")
		log := launchdLog()
		content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>sh.paseo-conductor</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, exe, log, log)
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
			_ = sh("systemctl", "--user", "try-restart", "paseo-conductor")
		}
	case "launchd":
		if restart {
			p, _ := unitPathAndContent()
			_ = exec.Command("launchctl", "unload", p).Run()
			_ = sh("launchctl", "load", "-w", p)
		}
	}
}

func startHint() string {
	switch serviceKind() {
	case "systemd":
		return "systemctl --user enable --now paseo-conductor"
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
		if err := sh("systemctl", "--user", "enable", "--now", "paseo-conductor"); err != nil {
			return err
		}
		_ = sh("loginctl", "enable-linger", os.Getenv("USER"))
		logf("==> systemd --user service installed and started (logs: journalctl --user -u paseo-conductor -f)")
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
		_ = sh("systemctl", "--user", "disable", "--now", "paseo-conductor")
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
