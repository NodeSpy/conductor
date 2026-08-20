package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

const updateRepo = "NodeSpy/paseo-conductor"

// cmdUpdate is the manual `update` subcommand.
func cmdUpdate(args []string) error {
	force := false
	var pinTag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force", "-f":
			force = true
		case "--tag":
			if i+1 < len(args) {
				pinTag = args[i+1]
				i++
			}
		}
	}
	updated, tag, err := doUpdate(force, pinTag)
	if err != nil {
		return err
	}
	if !updated {
		fmt.Printf("already up to date (%s)\n", version)
		return nil
	}
	fmt.Printf("updated %s → %s\n", version, tag)
	// Refresh the service unit if the new release changed its template.
	if changed, err := syncServiceUnit(true); err != nil {
		logf("update: service unit sync failed: %v", err)
	} else if changed {
		fmt.Println("service unit updated and reloaded")
	}
	return nil
}

// doUpdate installs the latest (or pinned) release binary for this OS/arch,
// replacing the running executable in place. Returns updated=false when already
// current (and not forced). The repo is private, so it uses the `gh` CLI.
func doUpdate(force bool, pinTag string) (updated bool, tag string, err error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return false, "", fmt.Errorf("update needs the GitHub CLI (gh), authenticated — this repo is private")
	}

	tag = pinTag
	if tag == "" {
		out, err := exec.Command("gh", "release", "view", "--repo", updateRepo,
			"--json", "tagName", "--jq", ".tagName").Output()
		if err != nil {
			return false, "", fmt.Errorf("look up latest release (any published yet?): %w", err)
		}
		tag = strings.TrimSpace(string(out))
	}
	if tag == "" {
		return false, "", fmt.Errorf("no release found in %s", updateRepo)
	}
	if tag == version && !force {
		return false, tag, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return false, tag, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	asset := fmt.Sprintf("paseo-conductor_%s_%s", runtime.GOOS, runtime.GOARCH)
	tmp := exe + ".new"
	_ = os.Remove(tmp)

	logf("update: downloading %s %s", asset, tag)
	dl := exec.Command("gh", "release", "download", tag,
		"--repo", updateRepo, "--pattern", asset, "--output", tmp, "--clobber")
	dl.Stderr = os.Stderr
	if err := dl.Run(); err != nil {
		return false, tag, fmt.Errorf("download %s from %s: %w", asset, tag, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return false, tag, err
	}
	// Atomic replace: rename over the running executable (same dir/FS). The
	// running process keeps its old inode; the next launch is the new binary.
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return false, tag, fmt.Errorf("replace %s (need write access to its directory): %w", exe, err)
	}
	return true, tag, nil
}

// autoUpdateLoop periodically checks for and installs updates, then applies
// them by re-execing into the new binary.
func autoUpdateLoop(ctx context.Context, u config.Update) {
	iv := u.Interval.D()
	if iv <= 0 {
		iv = 8 * time.Hour
	}
	logf("auto-update: enabled, checking every %s", iv)
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			updated, tag, err := doUpdate(false, "")
			if err != nil {
				logf("auto-update: check failed: %v", err)
				continue
			}
			if !updated {
				continue
			}
			logf("auto-update: installed %s (was %s)", tag, version)
			// Refresh the unit file (no restart here — the re-exec below applies
			// the new binary; daemon-reload just loads the new unit for later).
			if _, err := syncServiceUnit(false); err != nil {
				logf("auto-update: service unit sync failed: %v", err)
			}
			if u.ShouldApply() {
				applySelf()
			} else {
				logf("auto-update: restart to apply (apply: false)")
			}
		}
	}
}

// applySelf re-execs the (now-replaced) binary in place, so the new version
// takes over without needing a service-manager restart. If exec fails, exit so
// a Restart=always unit respawns the new binary.
func applySelf() {
	exe, err := os.Executable()
	if err == nil {
		if resolved, e := filepath.EvalSymlinks(exe); e == nil {
			exe = resolved
		}
	}
	logf("auto-update: re-exec %s", exe)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		logf("auto-update: re-exec failed (%v); exiting for the service manager to restart", err)
		os.Exit(0)
	}
}
