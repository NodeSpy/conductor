// Package gitdiff reads an agent's PROPOSED change out of its worktree
// (#36 §17): what would land if the work were applied — the uncommitted
// delta against HEAD plus anything committed locally but not pushed. It
// shells out to the system git (the worktrees it reads were created by git;
// no library re-implementation) and never mutates the repo.
package gitdiff

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/gitsafe"
)

// MaxBytes is the default clip for a captured diff: big enough for review,
// small enough for scope/checkpoint/hand-off bodies.
const MaxBytes = 64 << 10

// Proposed returns the proposed change in dir, clipped to maxBytes
// (<=0 → MaxBytes): a "working tree" section (`git diff HEAD` — staged +
// unstaged) and, when commits aren't on any remote yet, an "unpushed commits"
// section (`git diff <base>...HEAD`, base per upstreamBase). "" (nil error) means no proposed change.
// A dir that isn't a git worktree is an error.
func Proposed(ctx context.Context, dir string, maxBytes int) (string, error) {
	return proposed(ctx, dir, maxBytes, true)
}

// Uncommitted is Proposed without the "unpushed commits" section: for a
// workspace whose commits conductor pushed in another form (rebased onto a
// branch that moved), so they are on the remote though no remote ref names
// them.
func Uncommitted(ctx context.Context, dir string, maxBytes int) (string, error) {
	return proposed(ctx, dir, maxBytes, false)
}

func proposed(ctx context.Context, dir string, maxBytes int, unpushed bool) (string, error) {
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
	// Committed-but-unpushed: measured against the upstream, or — for a
	// branch without one — the last commit already on a remote (see
	// upstreamBase). Nothing unpushed, nothing reported.
	if base := upstreamBase(ctx, dir); unpushed && base != "" {
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

// Confine, when set (the agent sandbox is on), runs every git command for
// dir where dir's own content cannot steer it beyond dir: conductor's agent
// sandbox — a workspace's .git/config, .gitattributes and hooks may be an
// agent's, and a filter, textconv or fsmonitor command in them would
// otherwise run as conductor, on the host. It confines any directory, not
// only a registered one; ok=false (it could not) is a refusal, never a
// fall-back to an unconfined git.
var Confine func(ctx context.Context, dir string, args []string) (out string, ok bool, err error)

// git runs one git command in dir and returns its stdout: confined when the
// sandbox is on, else hardened (gitsafe) — never a plain git.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	if args[0] == "diff" {
		args = append([]string{"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all"}, args[1:]...)
	}
	if Confine != nil {
		out, ok, err := Confine(ctx, dir, args)
		if !ok {
			return "", fmt.Errorf("gitdiff: refusing an unconfined git read of %s", dir)
		}
		return out, err
	}
	return gitsafe.Output(ctx, dir, args...)
}

// Unpushed counts the commits in dir that are on no remote yet. since, when
// set, is the commit dir started at: only commits past it count (a checkout
// of a fork's PR holds commits no remote of the clone has — they are not
// the agent's), measured as `since..HEAD` minus everything on a remote, and
// base is since. Otherwise it is HEAD's commits past the base Proposed's
// "committed, not pushed" section reads (upstreamBase), and that base. 0
// when nothing is unpushed or there is no remote to measure against.
func Unpushed(ctx context.Context, dir, since string) (commits int, base string, err error) {
	if _, err := git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return 0, "", fmt.Errorf("gitdiff: %s is not a git worktree: %w", dir, err)
	}
	args := []string{"rev-list", "--count", "HEAD", "--not", "--remotes"}
	if since != "" {
		base = since
		args = append(args, since)
	} else {
		if base = upstreamBase(ctx, dir); base == "" {
			return 0, "", nil
		}
		args = []string{"rev-list", "--count", base + "..HEAD"}
	}
	out, err := git(ctx, dir, append(args, "--")...)
	if err != nil {
		return 0, base, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, base, fmt.Errorf("gitdiff: rev-list --count: %q", strings.TrimSpace(out))
	}
	return n, base, nil
}
