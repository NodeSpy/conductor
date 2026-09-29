// Package gitdiff reads an agent's PROPOSED change out of its worktree
// (#36 §17): what would land if the work were applied — the uncommitted
// delta against HEAD plus anything committed locally but not pushed. It
// shells out to the system git, hardened through internal/gitsafe (the
// worktrees it reads may have been provisioned against an untrusted remote —
// a fork, a PR branch — so their local git config is not trusted input
// either). When no `git` binary is present it falls back to a go-git
// implementation (fallback.go) with the same two-section shape, on a
// best-effort basis — see that file's doc comment for what differs.
package gitdiff

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/NodeSpy/conductor/internal/gitsafe"
)

// LookPath resolves the `git` binary. A package var so a test can force the
// no-git fallback path without touching the real PATH.
var LookPath = exec.LookPath

func hasGit() bool {
	_, err := LookPath("git")
	return err == nil
}

// MaxBytes is the default clip for a captured diff: big enough for review,
// small enough for scope/checkpoint/hand-off bodies.
const MaxBytes = 64 << 10

// Proposed returns the proposed change in dir, clipped to maxBytes
// (<=0 → MaxBytes): a "working tree" section (`git diff HEAD` — staged +
// unstaged) and, when commits aren't on any remote yet, an "unpushed commits"
// section (`git diff <base>...HEAD`, base per upstreamBase). "" (nil error) means no proposed change.
// A dir that isn't a git worktree is an error.
func Proposed(ctx context.Context, dir string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}
	if !hasGit() {
		return proposedFallback(dir, maxBytes)
	}
	if _, err := git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return "", fmt.Errorf("gitdiff: %s is not a git worktree: %w", dir, err)
	}

	var b strings.Builder
	if wt, err := git(ctx, dir, "diff", "HEAD"); err == nil && strings.TrimSpace(wt) != "" {
		b.WriteString("# uncommitted (vs HEAD)\n")
		b.WriteString(wt)
	}
	// Committed-but-unpushed: measured against the upstream, or — for a
	// branch without one — the last commit already on a remote (see
	// upstreamBase). Nothing unpushed, nothing reported.
	if base := upstreamBase(ctx, dir); base != "" {
		if ahead, err := git(ctx, dir, "diff", base+"...HEAD"); err == nil && strings.TrimSpace(ahead) != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "# committed, not pushed (vs %s)\n", base)
			b.WriteString(ahead)
		}
	}
	out := b.String()
	if len(out) > maxBytes {
		out = out[:maxBytes] + "\n… (diff clipped)"
	}
	return out, nil
}

// upstreamBase resolves what unpushed work is measured against: the branch's
// own upstream when set; else the last pushed commit on HEAD's first-parent
// line (so a worktree whose branch has no upstream — a PR checked out as a
// local `pr-N` branch and pushed with an explicit refspec — reports only what
// is genuinely not on any remote, and nothing once it's pushed); else the
// remote default head (origin/HEAD); else "".
func upstreamBase(ctx context.Context, dir string) string {
	if _, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		return "@{upstream}"
	}
	if refs, err := git(ctx, dir, "for-each-ref", "--count=1", "refs/remotes"); err != nil || strings.TrimSpace(refs) == "" {
		return "" // no remote refs: nothing to call "pushed"
	}
	if out, err := git(ctx, dir, "rev-list", "--first-parent", "HEAD", "--not", "--remotes"); err == nil {
		unpushed := strings.Fields(out)
		if len(unpushed) == 0 {
			return "" // HEAD is on a remote: nothing unpushed
		}
		if parent, err := git(ctx, dir, "rev-parse", "--verify", "-q", unpushed[len(unpushed)-1]+"^"); err == nil {
			base := strings.TrimSpace(parent)
			// Name it after a remote ref for the label ("origin/feat~1"), not a bare sha.
			if name, err := git(ctx, dir, "name-rev", "--name-only", "--no-undefined", "--refs=refs/remotes/*", base); err == nil {
				if n := strings.TrimSpace(name); n != "" {
					return strings.TrimPrefix(n, "remotes/")
				}
			}
			return base
		}
	}
	if ref, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		return strings.TrimSpace(ref)
	}
	return ""
}

// git runs one HARDENED git command in dir and returns its stdout — see
// internal/gitsafe for what "hardened" neutralizes.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitsafe.Command(ctx, dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
