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

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// selfUpdatePreflightTimeout bounds the downloaded binary's `validate` dry
// run (below) — a hang in validate (a stuck network check, say) must not
// hang the whole update.
var selfUpdatePreflightTimeout = 60 * time.Second

// prevBinarySuffix names the rollback copy doUpdate leaves next to the
// executable (<exe>.prev) — best effort, so an operator can run the previous
// release by hand (`mv <exe>.prev <exe>`) without needing to re-fetch it,
// which matters most for `conductor config migrate`-style hints below: those
// ask the operator to run the PREVIOUS release's binary, and after an
// unattended auto-update there is otherwise no previous binary left on disk
// to run.
const prevBinarySuffix = ".prev"

// updateSource is the git repository conductor updates itself from. Releases
// are tags; each platform's binary is published on refs/dist/<tag>/<platform>
// (internal/plugin/gitdist.go), fetched over plain git — no forge CLI.
const updateSource = "https://github.com/NodeSpy/conductor"

func updateRemote() plugin.RemoteSource { return plugin.RemoteSource{URL: updateSource} }

// latestRelease is the newest stable release tag published for this
// platform.
func latestRelease() (string, error) {
	tags, err := plugin.GitDist{}.ListTags(updateRemote())
	if err != nil {
		return "", err
	}
	tag, ok := config.BestMatch(tags, "", "")
	if !ok {
		return "", fmt.Errorf("no release published for %s in %s", plugin.Platform(), updateSource)
	}
	return tag, nil
}

// cmdUpdate is the manual `update` subcommand.
func cmdUpdate(args []string) error {
	// `conductor update --packs` re-resolves the packs: block and diffs the
	// lockfile (the binary itself auto-updates; packs are opt-in, so this is
	// separate from the self-update below).
	for _, a := range args {
		if a == "--packs" {
			return cmdPackUpdate(args)
		}
		if a == "--plugins" {
			return cmdPluginUpdate(args)
		}
	}
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
	// The daemon's config path, resolved the same way it finds it at boot —
	// the preflight below validates THIS file against the downloaded binary.
	cfgFile, _ := configPath(args)
	updated, tag, err := doUpdate(force, pinTag, cfgFile, nil)
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
// current (and not forced). It fetches over git with the daemon user's own git
// credentials, and verifies the binary against the release's checksums.txt.
//
// cfgFile is the daemon's config path, resolved the same way the daemon
// itself finds it (configPath); "" skips the preflight below (nothing to
// validate against — a fresh install with no config yet must not be blocked
// from updating). notifier, if non-nil, is used to escalate a preflight
// failure through whatever attention route the operator already configured;
// nil is fine for a one-off manual `conductor update`.
func doUpdate(force bool, pinTag, cfgFile string, notifier *notify.Notifier) (updated bool, tag string, err error) {
	tag = pinTag
	if tag == "" {
		if tag, err = latestRelease(); err != nil {
			return false, "", fmt.Errorf("look up latest release: %w", err)
		}
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

	asset := fmt.Sprintf("conductor_%s_%s", runtime.GOOS, runtime.GOARCH)
	logf("update: fetching %s %s", asset, tag)
	dl, err := os.MkdirTemp(filepath.Dir(exe), ".conductor-update-*")
	if err != nil {
		return false, tag, fmt.Errorf("stage update next to %s (need write access to its directory): %w", exe, err)
	}
	defer os.RemoveAll(dl)
	g := plugin.GitDist{}
	bin, err := g.Download(updateRemote(), tag, asset, dl)
	if err != nil {
		return false, tag, fmt.Errorf("fetch conductor binary %s: %w", tag, err)
	}
	sums, err := g.Download(updateRemote(), tag, "checksums.txt", dl)
	if err != nil {
		return false, tag, fmt.Errorf("fetch checksums for %s: %w", tag, err)
	}
	if err := plugin.VerifyChecksum(bin, sums, asset); err != nil {
		return false, tag, err
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return false, tag, err
	}
	// PREFLIGHT: a release that cannot load THIS box's config (a removed
	// legacy block, a schema change) must never be swapped in — on an
	// unattended auto-updating box that is a silent crash-loop with no
	// operator watching. Run the DOWNLOADED binary's own `validate` against
	// the daemon's config, read-only, bounded — never the running binary's.
	if cfgFile != "" {
		if _, statErr := os.Stat(cfgFile); statErr == nil {
			if perr := preflightValidate(bin, cfgFile); perr != nil {
				msg := fmt.Sprintf("update: %s failed its own config preflight — NOT applying: %v", tag, perr)
				logf("%s", msg)
				if notifier != nil {
					notifier.Emit(context.Background(), notify.EventEscalate,
						core.Trigger{Source: "updater", Kind: "update_preflight_failed"}, msg)
				}
				return false, tag, fmt.Errorf("%s does not load this config (staying on %s): %w", tag, version, perr)
			}
		}
	}
	// Best-effort rollback copy, BEFORE the swap: an operator who hits a
	// problem the preflight above didn't catch (it only proves the config
	// loads, not that every workflow still behaves) can restore it by hand
	// with no re-fetch — `mv <exe>.prev <exe>`. A failure here (read-only
	// filesystem, out of space) must not block an update that already passed
	// preflight.
	saveRollbackCopy(exe, version)
	// Atomic replace: rename over the running executable (same dir/FS). The
	// running process keeps its old inode; the next launch is the new binary.
	if err := os.Rename(bin, exe); err != nil {
		return false, tag, fmt.Errorf("replace %s (need write access to its directory): %w", exe, err)
	}
	return true, tag, nil
}

// preflightValidate runs the DOWNLOADED (not yet installed) binary's
// `validate --config <cfgFile>` — read-only, bounded by
// selfUpdatePreflightTimeout — and returns a trimmed error combining the
// failure with the tail of its output when it refuses the config.
func preflightValidate(bin, cfgFile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdatePreflightTimeout)
	defer cancel()
	// --require-plugins: a release whose plugins this box can neither find
	// installed nor fetch is not applied (it would boot with them dark).
	cmd := exec.CommandContext(ctx, bin, "validate", "--config", cfgFile, "--require-plugins")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("validate timed out after %s", selfUpdatePreflightTimeout)
	}
	t := tail(out, 2000)
	if t == "" {
		return err
	}
	return fmt.Errorf("%w — %s", err, t)
}

// saveRollbackCopy best-effort copies exe to exe+prevBinarySuffix, so a
// manual `mv <exe>.prev <exe>` can restore the previous release with no
// re-fetch (5b). A failure (read-only filesystem, out of space) is logged,
// never returned: it must not block an update that already passed preflight.
func saveRollbackCopy(exe, fromVersion string) {
	prev := exe + prevBinarySuffix
	data, rerr := os.ReadFile(exe)
	if rerr != nil {
		logf("update: could not save a rollback copy of %s (best effort, continuing): %v", exe, rerr)
		return
	}
	if werr := os.WriteFile(prev, data, 0o755); werr != nil {
		logf("update: could not save a rollback copy at %s (best effort, continuing): %v", prev, werr)
		return
	}
	logf("update: previous binary (%s) saved to %s for manual rollback", fromVersion, prev)
}

// autoUpdateLoop watches the release repo for a newer version and installs it,
// then applies it by restarting into the new binary. `stop` cancels the daemon's
// context to trigger a graceful shutdown when the restart is handed to the service
// manager.
//
// Detection is decoupled from install: each tick is one `git ls-remote` of the
// release refs (see releaseChecker) — a small reply from any git host — so a
// tight interval is cheap and a newly-published release is picked up within one
// interval, with no webhook or per-operator setup.
// reloadFunc attempts to apply the moved plugins IN PLACE (no daemon restart)
// and returns true only if it handled ALL of them. nil disables in-place reload
// (always restart on a dep change). Built in cmdRun so it can reach the live
// plugin manager.
type reloadFunc func(moved []plugin.Resolution) bool

func autoUpdateLoop(ctx context.Context, u config.Update, cfgFile string, notifier *notify.Notifier, stop func(), reload reloadFunc) {
	iv := u.Interval.D()
	if iv <= 0 {
		iv = 10 * time.Minute
	}
	if u.DepsEnabled() {
		logf("auto-update: enabled, checking every %s (packs + plugins included)", iv)
	} else {
		logf("auto-update: enabled, checking every %s (binary only; deps: false)", iv)
	}
	checker := &releaseChecker{}
	t := time.NewTicker(iv)
	defer t.Stop()
	announced := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Dependency refresh first: a changed pack/plugin normally restarts to
			// load it, same as a new binary. Opt-in (update.deps) — off, no-op.
			if u.DepsEnabled() {
				packMoved, moved, act := refreshDeps(cfgFile)
				if act {
					// Try in-place plugin hot-reload when enabled and no pack moved
					// (packs aren't process-swappable). If it handles every moved
					// plugin, skip the restart; otherwise restart to apply.
					if u.ReloadEnabled() && !packMoved && reload != nil && reload(moved) {
						logf("auto-update: plugins hot-reloaded in place — no restart")
					} else {
						logf("auto-update: dependency change validated — restarting to apply")
						applyRelease(stop)
						return
					}
				}
			}
			tag, changed, err := checker.check()
			if err != nil {
				logf("auto-update: check failed: %v", err)
				continue
			}
			if !newerRelease(tag, changed, version) {
				continue // 304 (nothing new), or the latest is what we already run
			}
			var applied bool
			announced, applied = handleNewerRelease(ctx, u, tag, announced, notifier, stop, cfgFile)
			if applied {
				return // shutting down for a manager restart, or re-exec'd
			}
		}
	}
}

// refreshDeps re-resolves packs + remote plugins against their `version:`
// constraints, logging every change and every held dependency. It returns true
// only when something actually changed AND the resulting config still loads —
// the caller then restarts to apply it. A refresh that fails to validate is
// logged and reported as no-change: the running daemon stands, and the
// degraded-boot fail-safe covers the (now newer) on-disk lockfile on the next
// manual restart. Network errors are logged and swallowed (transient).
func refreshDeps(cfgFile string) (packMoved bool, pluginRes []plugin.Resolution, act bool) {
	dir := filepath.Dir(cfgFile)
	before, _ := config.ReadLockfile(dir)

	if _, err := config.ResolvePacks(cfgFile); err != nil {
		logf("auto-update: pack refresh failed (keeping current): %v", err)
		return false, nil, false
	}
	// Plugins stay current on the same cycle. Their install state is LOCAL, so
	// (unlike packs) nothing about this lands in the repo: the reconcile logs
	// each install/update with the sha it moved from, and a plugin whose fetch
	// fails keeps running the build already installed rather than failing the
	// refresh.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		logf("auto-update: config load failed (keeping current): %v", err)
		return false, nil, false
	}
	pluginsMoved, results, err := refreshPlugins(cfg)
	if err != nil {
		logf("auto-update: plugin refresh failed (keeping current): %v", err)
		return false, nil, false
	}

	after, _ := config.ReadLockfile(dir)
	packMoved = logDepChanges(before, after)
	if !packMoved && !pluginsMoved {
		return false, nil, false // nothing moved
	}
	// A dependency moved — only apply if the new graph still loads.
	if _, err := config.Load(cfgFile); err != nil {
		logf("auto-update: dependency update does NOT validate — NOT applying: %v", err)
		return false, nil, false
	}
	return packMoved, results, true
}

// logDepChanges logs each pack/plugin whose resolved revision changed between
// two lockfiles and returns whether anything changed. Held or unchanged
// dependencies are logged at most as "unchanged" only when something else moved.
func logDepChanges(before, after *config.Lockfile) bool {
	if after == nil {
		return false
	}
	moved := false
	oldPack := map[string]string{}
	if before != nil {
		for _, e := range before.Packs {
			oldPack[e.Instance] = e.Resolved
		}
	}
	for _, e := range after.Packs {
		if prev, ok := oldPack[e.Instance]; !ok || prev != e.Resolved {
			logf("auto-update: pack %s -> %s@%s (%s)", e.Instance, e.Name, orNone(e.Version), shortSha(e.Resolved))
			moved = true
		}
	}
	return moved
}

// refreshPlugins re-resolves every referenced plugin that is not pinned to an
// exact version — the stay-current half of the auto-update cycle. It reports
// whether any plugin actually moved. A per-plugin fetch failure is NOT an
// error: Reconcile keeps the installed build and records the failure, so one
// unreachable plugin never blocks the update or the daemon.
func refreshPlugins(cfg *config.Config) (bool, []plugin.Resolution, error) {
	results, err := reconcilePlugins(cfg, plugin.Options{Log: logf})
	if err != nil {
		return false, nil, err
	}
	moved := false
	for _, r := range results {
		if r.Changed() {
			moved = true
		}
		if r.Action == plugin.ActionFailed {
			logf("auto-update: plugin %s: %v", r.Name, r.Err)
		}
	}
	return moved, results, nil
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
func handleNewerRelease(ctx context.Context, u config.Update, tag, announced string, notifier *notify.Notifier, stop func(), cfgFile string) (newAnnounced string, applied bool) {
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
	updated, installed, err := installRelease(false, tag, cfgFile, notifier)
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

// releaseChecker polls the release refs, remembering the last tag it saw so
// an unchanged repository reports changed=false.
type releaseChecker struct {
	last   string
	latest func() (string, error)
}

// check lists the published releases once. changed=false means the newest
// tag is the one the previous check saw.
func (rc *releaseChecker) check() (tag string, changed bool, err error) {
	latest := rc.latest
	if latest == nil {
		latest = latestRelease
	}
	tag, err = latest()
	if err != nil {
		return "", false, fmt.Errorf("release check: %w", err)
	}
	if tag == rc.last {
		return tag, false, nil
	}
	rc.last = tag
	return tag, true, nil
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
