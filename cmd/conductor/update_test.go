package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeConductor writes a shell script standing in for the downloaded
// release binary's `validate` subcommand, so preflightValidate can be tested
// without a real network fetch.
func writeFakeConductor(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conductor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPreflightValidatePassesThroughSuccess: a downloaded binary whose
// `validate` exits 0 against the daemon's config preflights clean.
func TestPreflightValidatePassesThroughSuccess(t *testing.T) {
	bin := writeFakeConductor(t, "exit 0")
	if err := preflightValidate(bin, "/any/config.yaml", true); err != nil {
		t.Fatalf("preflightValidate: %v", err)
	}
}

// TestPreflightValidateReportsFailureWithOutputTail: the self-update
// preflight (5a) — a release that cannot load this box's config must be
// refused, with enough of its own explanation to act on, not just "exit
// status 1".
func TestPreflightValidateReportsFailureWithOutputTail(t *testing.T) {
	bin := writeFakeConductor(t, `echo "config: the legacy integrations: block is no longer supported" >&2; exit 1`)
	err := preflightValidate(bin, "/any/config.yaml", true)
	if err == nil {
		t.Fatal("want an error when the downloaded binary's validate fails")
	}
	if !strings.Contains(err.Error(), "legacy integrations") {
		t.Fatalf("want the validate failure's own output in the error, got: %v", err)
	}
}

// TestPreflightValidateTimesOut: a hung validate (a stuck network check in a
// plugin-fetchability probe, say) must not hang the whole update.
func TestPreflightValidateTimesOut(t *testing.T) {
	old := selfUpdatePreflightTimeout
	selfUpdatePreflightTimeout = 150 * time.Millisecond
	t.Cleanup(func() { selfUpdatePreflightTimeout = old })

	// `exec` replaces the shell process in place (no forked grandchild) so
	// killing the single pid on timeout closes its stdout/stderr pipes
	// immediately — exactly how a real (single-process) downloaded binary
	// behaves. A plain "sleep 5" would fork sleep as a child of the shell:
	// killing only the shell leaves the orphaned sleep holding the output
	// pipe open, and CombinedOutput would hang for the full sleep regardless
	// of the timeout.
	bin := writeFakeConductor(t, "exec sleep 5")
	start := time.Now()
	err := preflightValidate(bin, "/any/config.yaml", true)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("preflightValidate took %s — selfUpdatePreflightTimeout did not bound it", elapsed)
	}
}

// TestSaveRollbackCopyWritesPrevBinary: 5b — a manual rollback must be
// possible with no re-fetch. saveRollbackCopy must leave the PREVIOUS
// binary's exact bytes at <exe>.prev.
func TestSaveRollbackCopyWritesPrevBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "conductor")
	content := []byte("#!/bin/sh\necho v1\n")
	if err := os.WriteFile(exe, content, 0o755); err != nil {
		t.Fatal(err)
	}
	saveRollbackCopy(exe, "v1.0.0")
	got, err := os.ReadFile(exe + prevBinarySuffix)
	if err != nil {
		t.Fatalf("expected a rollback copy at %s: %v", exe+prevBinarySuffix, err)
	}
	if string(got) != string(content) {
		t.Fatal("rollback copy content does not match the previous binary")
	}
}

// TestSaveRollbackCopyBestEffort: a backup failure (unreadable source) must
// not panic or block — it is best-effort, never allowed to stop an update
// that already passed preflight.
func TestSaveRollbackCopyBestEffort(t *testing.T) {
	saveRollbackCopy(filepath.Join(t.TempDir(), "does-not-exist"), "v1.0.0")
}

// The release check reports changed only when the newest published tag
// moves, and surfaces a lookup failure.
func TestReleaseCheckerReportsMovement(t *testing.T) {
	tags := []string{"v1.0.0", "v1.0.0", "v1.1.0"}
	i := 0
	rc := &releaseChecker{latest: func() (string, error) { i++; return tags[i-1], nil }}
	for n, want := range []bool{true, false, true} {
		tag, changed, err := rc.check()
		if err != nil || changed != want || tag != tags[n] {
			t.Fatalf("check %d: tag=%q changed=%v err=%v, want changed=%v", n, tag, changed, err, want)
		}
	}
	bad := &releaseChecker{latest: func() (string, error) { return "", errors.New("unreachable") }}
	if _, _, err := bad.check(); err == nil {
		t.Fatal("a failed lookup must be reported")
	}
}

func TestNewerRelease(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		changed bool
		running string
		want    bool
	}{
		{"nothing new", "", false, "v0.5.23", false},
		{"changed but same tag", "v0.5.23", true, "v0.5.23", false},
		{"changed and newer", "v0.5.24", true, "v0.5.23", true},
		{"changed but empty tag", "", true, "v0.5.23", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newerRelease(c.tag, c.changed, c.running); got != c.want {
				t.Fatalf("newerRelease(%q,%v,%q) = %v, want %v", c.tag, c.changed, c.running, got, c.want)
			}
		})
	}
}

// The self-update preflight requires the release's plugins: a box never
// auto-updates into a release whose plugins it cannot find or fetch.
func TestPreflightValidateRequiresPlugins(t *testing.T) {
	bin := writeFakeConductor(t, `case "$*" in *--require-plugins*) exit 0;; esac; echo "preflight must pass --require-plugins" >&2; exit 1`)
	if err := preflightValidate(bin, "/any/config.yaml", true); err != nil {
		t.Fatalf("preflight did not pass --require-plugins: %v", err)
	}
}

// A forced update does not require the plugins (the operator's escape hatch
// for an unreachable source), but still proves the config loads.
func TestPreflightValidateForcedSkipsThePluginRequirement(t *testing.T) {
	bin := writeFakeConductor(t, `case "$*" in *--require-plugins*) echo "plugins required" >&2; exit 1;; esac; exit 0`)
	if err := preflightValidate(bin, "/any/config.yaml", false); err != nil {
		t.Fatalf("a forced preflight still required the plugins: %v", err)
	}
	if err := preflightValidate(bin, "/any/config.yaml", true); err == nil {
		t.Fatal("an unforced preflight must require them")
	}
}
