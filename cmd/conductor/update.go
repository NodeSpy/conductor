package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/notify"
)

const updateRepo = "NodeSpy/conductor"

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
	if changed, err := syncServiceUnit(false); err != nil {
		logf("update: service unit sync failed: %v", err)
	} else if changed {
		fmt.Println("service unit updated")
	}
	// Cycle a running service so the new binary takes over now — syncServiceUnit
	// only reloads on a template change, so a binary-only update otherwise leaves
	// the running daemon on the old version until the next restart.
	if restartInstalledService() {
		fmt.Println("restarted the running service into the new version")
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

	tmp := exe + ".new"
	_ = os.Remove(tmp)

	asset := fmt.Sprintf("conductor_%s_%s", runtime.GOOS, runtime.GOARCH)
	logf("update: downloading %s %s", asset, tag)
	dl := exec.Command("gh", "release", "download", tag,
		"--repo", updateRepo, "--pattern", asset, "--output", tmp, "--clobber")
	dl.Stderr = os.Stderr
	if err := dl.Run(); err != nil {
		_ = os.Remove(tmp)
		return false, tag, fmt.Errorf("download conductor binary from %s %s: %w", updateRepo, tag, err)
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

// autoUpdateLoop watches the release repo for a newer version and installs it,
// then applies it by restarting into the new binary. `stop` cancels the daemon's
// context to trigger a graceful shutdown when the restart is handed to the service
// manager.
//
// Detection is decoupled from install: each tick is a cheap CONDITIONAL request
// (see releaseChecker) that returns 304 Not Modified — a tiny reply GitHub does
// not bill against the rate limit — whenever nothing has been published since the
// last check. That makes a tight interval effectively free, so a newly-published
// release is picked up within one interval (minutes) rather than hours, for anyone
// running conductor, with no webhook or per-operator setup. GitHub exposes no
// release push a non-admin consumer can subscribe to, so a near-free conditional
// poll is the portable stand-in.
func autoUpdateLoop(ctx context.Context, u config.Update, notifier *notify.Notifier, stop func()) {
	iv := u.Interval.D()
	if iv <= 0 {
		iv = 10 * time.Minute
	}
	logf("auto-update: enabled, checking every %s", iv)
	checker := &releaseChecker{}
	t := time.NewTicker(iv)
	defer t.Stop()
	announced := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tag, changed, err := checker.check()
			if err != nil {
				logf("auto-update: check failed: %v", err)
				continue
			}
			if !newerRelease(tag, changed, version) {
				continue // 304 (nothing new), or the latest is what we already run
			}
			var applied bool
			announced, applied = handleNewerRelease(ctx, u, tag, announced, notifier, stop)
			if applied {
				return // shutting down for a manager restart, or re-exec'd
			}
		}
	}
}

// installRelease and applyRelease are the install/restart seams —
// package-level so tests exercise handleNewerRelease without touching the
// real binary or re-exec'ing the test process.
var (
	installRelease = doUpdate
	applyRelease   = applyUpdate
)

// handleNewerRelease acts on one detected newer release. DEFAULT
// (apply: true) is unchanged: install, sync the unit, restart into it —
// an unattended box self-updates. apply: false installs and stages.
// apply: workflow installs NOTHING — it emits conductor.update_available
// (once per tag) so a trigger drives the update as a workflow.
func handleNewerRelease(ctx context.Context, u config.Update, tag, announced string, notifier *notify.Notifier, stop func()) (newAnnounced string, applied bool) {
	if u.ApplyWorkflow() {
		if tag == announced {
			return announced, false // one announcement per release
		}
		logf("auto-update: %s available (apply: workflow) — emitting conductor.update_available", tag)
		if notifier != nil {
			notifier.Publish(ctx, notify.EventUpdateAvailable,
				core.Trigger{Source: "updater", Kind: "update_available"},
				fmt.Sprintf("release %s available (running %s)", tag, version),
				map[string]any{"version": tag})
		}
		return tag, false
	}
	updated, installed, err := installRelease(false, tag)
	if err != nil {
		logf("auto-update: install %s failed: %v", tag, err)
		return announced, false
	}
	if !updated {
		return announced, false
	}
	logf("auto-update: installed %s (was %s)", installed, version)
	// Refresh the unit so the restart below comes up with the new template
	// (daemon-reload on systemd). Never starts a service that wasn't there.
	if _, err := syncServiceUnit(false); err != nil {
		logf("auto-update: service unit sync failed: %v", err)
	}
	if !u.ShouldApply() {
		logf("auto-update: %s staged — restart to apply (apply: false)", installed)
		return announced, false
	}
	applyRelease(stop)
	return announced, true
}

// newerRelease reports whether a check result warrants an install: something
// changed (a 200, not a 304) and the latest tag differs from the running version.
func newerRelease(tag string, changed bool, running string) bool {
	return changed && tag != "" && tag != running
}

// releaseChecker performs cheap conditional polling of the release repo's latest
// release, remembering the last ETag so an unchanged repo answers 304 Not Modified.
// A 304 is tiny and un-billed against the rate limit, so a tight poll costs almost
// nothing — the whole point of decoupling detection from the (heavy) download.
type releaseChecker struct {
	etag string
}

// check does one conditional GET for the latest release tag via the operator's
// authenticated `gh` (the same credential the manual update path uses, so it works
// for the private repo). changed=false means a 304 — nothing new since last check.
func (rc *releaseChecker) check() (tag string, changed bool, err error) {
	args := []string{"api", "repos/" + updateRepo + "/releases/latest", "-i"}
	if rc.etag != "" {
		args = append(args, "-H", "If-None-Match: "+rc.etag)
	}
	// gh exits non-zero on a 304 (and other non-2xx), so ignore the exit code and
	// read the HTTP status line from the -i output instead.
	out, _ := exec.Command("gh", args...).CombinedOutput()
	status, etag, body := parseHTTPResponse(string(out))
	switch status {
	case 304:
		return "", false, nil
	case 200:
		if etag != "" {
			rc.etag = etag
		}
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal([]byte(body), &rel); err != nil {
			return "", false, fmt.Errorf("parse release json: %w", err)
		}
		if rel.TagName == "" {
			return "", false, fmt.Errorf("release lookup: empty tag (any published yet?)")
		}
		return rel.TagName, true, nil
	case 0:
		return "", false, fmt.Errorf("release check: no HTTP response from gh (auth/network?): %s", tail(out, 200))
	default:
		return "", false, fmt.Errorf("release check: gh api returned HTTP %d", status)
	}
}

// parseHTTPResponse splits `gh api -i` output into the HTTP status code, the ETag
// header value, and the JSON body (everything past the first blank line).
func parseHTTPResponse(out string) (status int, etag, body string) {
	lines := strings.Split(out, "\n")
	i := 0
	for ; i < len(lines); i++ {
		t := strings.TrimRight(lines[i], "\r")
		if t == "" { // blank line: headers end, body begins
			i++
			break
		}
		if strings.HasPrefix(t, "HTTP/") {
			if f := strings.Fields(t); len(f) >= 2 {
				if n, e := strconv.Atoi(f[1]); e == nil {
					status = n
				}
			}
		} else if strings.HasPrefix(strings.ToLower(t), "etag:") {
			etag = strings.TrimSpace(t[len("etag:"):])
		}
	}
	if i < len(lines) {
		body = strings.Join(lines[i:], "\n")
	}
	return status, etag, body
}

// tail returns the last n bytes of b, for compact error context.
func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

// applyUpdate puts the freshly-downloaded binary into service. When this process
// was started by the per-user service manager, it hands the restart to that
// manager: exit cleanly (via stop → graceful shutdown) and let systemd
// (Restart=always) / launchd (KeepAlive) relaunch, so the new version starts with
// the unit's fresh environment (PATH, conductor.env) rather than inheriting our
// stale one. Run manually (no manager), it re-execs in place so a foreground run
// still self-updates.
func applyUpdate(stop func()) {
	if launchedByServiceManager() {
		logf("auto-update: exiting for %s to relaunch the new binary", serviceKind())
		if stop != nil {
			stop() // cancel ctx → graceful shutdown → clean exit → manager restarts us
			return
		}
		os.Exit(0)
	}
	reExecInPlace()
}

// emitUpdatedOnBoot publishes conductor.updated on the first boot of a new
// release: the version at last run persists in a sibling of the state file,
// and a change means the self-update (or a manual `conductor update`)
// carried the daemon here — the reliable place to announce it, since the
// install path re-execs immediately. The short sleep lets the conductor.*
// source register before the event fires.
func emitUpdatedOnBoot(cfg *config.Config, notifier *notify.Notifier) {
	p := lastVersionPath(cfg)
	prev := ""
	if b, err := os.ReadFile(p); err == nil {
		prev = strings.TrimSpace(string(b))
	}
	if err := writeLastVersion(cfg, version); err != nil {
		logf("update: could not record running version: %v", err)
	}
	if prev == "" || prev == version || notifier == nil {
		return
	}
	time.Sleep(bootAnnounceDelay)
	notifier.Publish(context.Background(), notify.EventUpdated,
		core.Trigger{Source: "updater", Kind: "updated"},
		fmt.Sprintf("conductor self-updated %s → %s", prev, version),
		map[string]any{"version": version, "previous": prev})
}

// bootAnnounceDelay gives the conductor.* source time to register before the
// boot-time updated event fires (tests zero it).
var bootAnnounceDelay = 3 * time.Second

func lastVersionPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "last-version")
}

// writeLastVersion records the running release beside the state file.
func writeLastVersion(cfg *config.Config, v string) error {
	p := lastVersionPath(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// reExecInPlace replaces the running process image with the (already-replaced)
// binary. Used only for unsupervised/foreground runs — supervised runs restart
// via the service manager instead (see applyUpdate). If exec fails, exit so any
// wrapping supervisor respawns the new binary.
func reExecInPlace() {
	exe, err := os.Executable()
	if err == nil {
		if resolved, e := filepath.EvalSymlinks(exe); e == nil {
			exe = resolved
		}
	}
	logf("auto-update: re-exec %s", exe)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		logf("auto-update: re-exec failed (%v); exiting for a supervisor to restart", err)
		os.Exit(0)
	}
}
