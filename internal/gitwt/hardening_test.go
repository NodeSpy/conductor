package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/gitdiff"
	"github.com/NodeSpy/conductor/internal/gitsafe"
)

// TestHardenedGitIgnoresTamperedLocalConfig is the safety-critical test for
// internal/gitsafe's integration into gitwt and gitdiff: a base clone's local
// .git/config is not trusted input (it can carry settings from wherever the
// clone's initial state came from), and three of those settings turn an
// ordinary git invocation into arbitrary code execution as the conductor
// daemon's user — core.fsmonitor (a command run on every status-shaped
// query), core.hooksPath (a hook script run on checkout), and core.sshCommand
// (substitutes the transport for an ssh remote). It tampers a REAL base
// clone's config with all three, then drives conductor's own git surface
// (gitwt.ProvisionWorktree — fetch + worktree add — and gitdiff.Proposed) and
// asserts neither marker fires. A sanity check up front proves the fixture is
// actually exploitable via a plain, unhardened git invocation, so a pass here
// can't be a vacuous "nothing happened to happen" result.
func TestHardenedGitIgnoresTamperedLocalConfig(t *testing.T) {
	requireGit(t)
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()

	if _, _, err := p.ProvisionWorktree(ctx, prReq()); err != nil {
		t.Fatalf("provision (untampered): %v", err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")

	scratch := t.TempDir()
	fsMarker := filepath.Join(scratch, "fsmonitor-fired")
	hookMarker := filepath.Join(scratch, "hook-fired")
	fsScript := filepath.Join(scratch, "fsmonitor.sh")
	writeExecutable(t, fsScript, "#!/bin/sh\ntouch "+shq(fsMarker)+"\necho '{\"version\":1}'\n")
	hooksDir := filepath.Join(scratch, "hooks")
	writeExecutable(t, filepath.Join(hooksDir, "post-checkout"), "#!/bin/sh\ntouch "+shq(hookMarker)+"\n")

	run(t, base, "git", "config", "core.fsmonitor", fsScript)
	run(t, base, "git", "config", "core.hooksPath", hooksDir)
	run(t, base, "git", "config", "core.sshCommand", "/bin/false") // must never actually be invoked

	// Sanity check: an UNHARDENED git call against the same tampered config
	// must fire both markers, proving the fixture is real.
	run(t, base, "git", "status")
	if _, err := os.Stat(fsMarker); err != nil {
		t.Fatalf("fixture sanity check failed: unhardened `git status` did not trip the fsmonitor marker: %v", err)
	}
	if err := os.Remove(fsMarker); err != nil {
		t.Fatal(err)
	}
	run(t, base, "git", "checkout", "-b", "sanity-checkout-hook")
	if _, err := os.Stat(hookMarker); err != nil {
		t.Fatalf("fixture sanity check failed: unhardened `git checkout` did not trip the post-checkout hook marker: %v", err)
	}
	run(t, base, "git", "checkout", "main")
	run(t, base, "git", "branch", "-D", "sanity-checkout-hook")
	if err := os.Remove(hookMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fsMarker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Now the real thing: gitwt's own hardened chokepoint (fetch + worktree
	// add against the SAME tampered base) and gitdiff.Proposed (diff HEAD in
	// the resulting worktree, which shares the base's config).
	req2 := prReq()
	req2.DispatchID = "disp-2"
	_, cwd2, err := p.ProvisionWorktree(ctx, req2)
	if err != nil {
		t.Fatalf("provision (tampered, hardened): %v", err)
	}
	if _, err := gitdiff.Proposed(ctx, cwd2, 0); err != nil {
		t.Fatalf("gitdiff.Proposed against the tampered worktree: %v", err)
	}

	for _, m := range []string{fsMarker, hookMarker} {
		if _, err := os.Stat(m); err == nil {
			t.Fatalf("marker %s exists: conductor's hardened git path honored the tampered local config", m)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// TestGitsafeArgsIgnoreTrustedSSHFromRepoLocalConfig is the gitwt-level
// companion to gitsafe's own unit test: it points HOME/GIT_CONFIG_GLOBAL at a
// fixture carrying a marker sshCommand and proves the hardened args gitwt
// hands to every git call resolve THAT value, never a base clone's local
// override — see internal/gitsafe for the full unit coverage of this
// resolution; this only re-confirms it holds from gitwt's own call site.
func TestGitsafeArgsIgnoreTrustedSSHFromRepoLocalConfig(t *testing.T) {
	requireGit(t)
	origin := originRepo(t)
	p := newProv(t, origin)
	if _, _, err := p.ProvisionWorktree(context.Background(), prReq()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	run(t, base, "git", "config", "core.sshCommand", "/bin/false-local-marker")

	dir := t.TempDir()
	globalConfig := filepath.Join(dir, "gitconfig-global")
	if err := os.WriteFile(globalConfig, []byte("[core]\n\tsshCommand = /bin/false-global-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(dir, "does-not-exist"))
	gitsafe.ResetForTest()
	t.Cleanup(gitsafe.ResetForTest)

	args := gitsafe.Args("fetch")
	found := false
	for _, a := range args {
		if a == "core.sshCommand=/bin/false-global-marker" {
			found = true
		}
		if a == "core.sshCommand=/bin/false-local-marker" {
			t.Fatalf("hardened args leaked the base clone's local sshCommand: %v", args)
		}
	}
	if !found {
		t.Fatalf("hardened args did not resolve the trusted global sshCommand: %v", args)
	}
}

// writeExecutable writes body to path (creating parent dirs) and marks it
// executable.
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// shq single-quotes s for embedding in a generated shell script.
func shq(s string) string {
	return "'" + s + "'"
}
