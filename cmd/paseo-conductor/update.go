package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const updateRepo = "NodeSpy/paseo-conductor"

// cmdUpdate replaces the running binary with the latest release asset for this
// OS/arch. The repo is private, so it uses the authenticated `gh` CLI.
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

	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("update needs the GitHub CLI (gh), authenticated — this repo is private")
	}

	tag := pinTag
	if tag == "" {
		out, err := exec.Command("gh", "release", "view", "--repo", updateRepo, "--json", "tagName", "--jq", ".tagName").Output()
		if err != nil {
			return fmt.Errorf("look up latest release (any published yet?): %w", err)
		}
		tag = strings.TrimSpace(string(out))
	}
	if tag == "" {
		return fmt.Errorf("no release found in %s", updateRepo)
	}
	if tag == version && !force {
		fmt.Printf("already up to date (%s)\n", version)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	asset := fmt.Sprintf("paseo-conductor_%s_%s", runtime.GOOS, runtime.GOARCH)
	tmp := exe + ".new"
	_ = os.Remove(tmp)

	fmt.Printf("downloading %s %s…\n", asset, tag)
	dl := exec.Command("gh", "release", "download", tag,
		"--repo", updateRepo, "--pattern", asset, "--output", tmp, "--clobber")
	dl.Stderr = os.Stderr
	if err := dl.Run(); err != nil {
		return fmt.Errorf("download %s from %s: %w", asset, tag, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	// Atomic replace: rename over the running executable (same dir/FS). The
	// running process keeps its old inode; the next launch is the new binary.
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s (need write access to its directory): %w", exe, err)
	}
	fmt.Printf("updated %s → %s (%s)\n", version, tag, exe)
	return nil
}
