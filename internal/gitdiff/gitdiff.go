// Package gitdiff reads an agent's PROPOSED change out of its worktree
// (#36 §17): what would land if the work were applied — the uncommitted
// delta against HEAD plus anything committed locally but not pushed. It
// shells out to the system git (the worktrees it reads were created by git;
// no library re-implementation) and never mutates the repo.
package gitdiff

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// MaxBytes is the default clip for a captured diff: big enough for review,
// small enough for scope/checkpoint/hand-off bodies.
const MaxBytes = 64 << 10

// Proposed returns the proposed change in dir, clipped to maxBytes
// (<=0 → MaxBytes): a "working tree" section (`git diff HEAD` — staged +
// unstaged) and, when the branch has an upstream, an "unpushed commits"
// section (`git diff @{u}...HEAD`). "" (nil error) means no proposed change.
// A dir that isn't a git worktree is an error.
func Proposed(ctx context.Context, dir string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}
	if _, err := git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return "", fmt.Errorf("gitdiff: %s is not a git worktree: %w", dir, err)
	}

	var b strings.Builder
	if wt, err := git(ctx, dir, "diff", "HEAD"); err == nil && strings.TrimSpace(wt) != "" {
		b.WriteString("# uncommitted (vs HEAD)\n")
		b.WriteString(wt)
	}
	// Committed-but-unpushed: only meaningful when an upstream exists (a
	// fresh branch-off worktree may not have one — its commits are all
	// "proposed", but without a remote ref there is nothing to diff against;
	// fall back to the merge base with the default remote HEAD if resolvable).
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
// own upstream when set, else the remote default head (origin/HEAD), else "".
func upstreamBase(ctx context.Context, dir string) string {
	if _, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		return "@{upstream}"
	}
	if ref, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		return strings.TrimSpace(ref)
	}
	return ""
}

// git runs one git command in dir and returns its stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
