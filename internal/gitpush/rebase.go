package gitpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DivergedError is a push refused because the remote branch moved since the
// workspace was checked out and the agent's commits could not be replayed
// onto its new head: rebasing is off, or the replay conflicts. It Is
// ErrDiverged.
type DivergedError struct {
	// Branch is the dispatch branch; Remote its head at the remote; Head
	// the workspace's.
	Branch, Remote, Head string
	// Attempted: a rebase was tried. Commit and Files name the agent commit
	// that conflicted and the paths it conflicted on; Cause is any other
	// reason the replay stopped (a merge commit, a git failure).
	Attempted bool
	Commit    string
	Files     []string
	Cause     error
}

func (e *DivergedError) Error() string {
	switch {
	case !e.Attempted:
		return ErrDiverged.Error() + " (sandbox.push.rebase is off)"
	case e.Cause != nil:
		return fmt.Sprintf("gitpush: the remote branch moved to %s and the agent's commits could not be rebased onto it: %v", short(e.Remote), e.Cause)
	default:
		return fmt.Sprintf("gitpush: the remote branch moved to %s and rebasing the agent's commit %s onto it conflicts in %s — origin/%s is the new head: rebase onto it, resolve, commit, and push again",
			short(e.Remote), short(e.Commit), strings.Join(e.Files, ", "), e.Branch)
	}
}

func (e *DivergedError) Is(target error) bool { return target == ErrDiverged }

// Conflicted reports a rebase that stopped on a conflict — work an agent can
// resolve against the remote's new head.
func (e *DivergedError) Conflicted() bool { return e.Attempted && e.Cause == nil }

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// conflictError is one commit's replay conflicting.
type conflictError struct {
	commit string
	files  []string
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("commit %s conflicts in %s", short(e.commit), strings.Join(e.files, ", "))
}

// rebaseOnto replays commits (the agent's own, oldest first, a linear
// chain) onto onto without a working tree: each commit's change against its
// parent is merged into the new base (`git merge-tree --merge-base`), and a
// commit with the same author, committer and message is written over the
// result. A commit whose change the new base already holds is dropped.
// Returns the new commits, oldest first. Objects are written to the push's
// scratch store, like the signed commits.
func rebaseOnto(git gitFn, commits []string, onto string) ([]string, error) {
	var out []string
	base := onto
	for _, c := range commits {
		raw, err := git(nil, "cat-file", "commit", c)
		if err != nil {
			return nil, err
		}
		parents := commitParents(raw)
		if len(parents) != 1 {
			return nil, fmt.Errorf("commit %s is a merge — conductor only rebases a linear run of commits", short(c))
		}
		mt, err := git(nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+parents[0], base, c)
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 1 {
				lines := strings.Split(strings.TrimSpace(mt), "\n")
				return nil, &conflictError{commit: c, files: lines[1:]}
			}
			return nil, err
		}
		tree := strings.TrimSpace(strings.SplitN(mt, "\n", 2)[0])
		baseTree, err := git(nil, "rev-parse", "--verify", base+"^{tree}")
		if err != nil {
			return nil, err
		}
		if tree == strings.TrimSpace(baseTree) {
			continue // already on the new base
		}
		id, err := git(replantCommit([]byte(raw), tree, base), "hash-object", "-t", "commit", "-w", "--stdin")
		if err != nil {
			return nil, err
		}
		base = strings.TrimSpace(id)
		out = append(out, base)
	}
	return out, nil
}

// commitParents lists a raw commit's parents.
func commitParents(raw string) []string {
	var ps []string
	head, _, _ := strings.Cut(raw, "\n\n")
	for _, line := range strings.Split(head, "\n") {
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			ps = append(ps, p)
		}
	}
	return ps
}

// replantCommit returns raw with its tree and (single) parent replaced and
// any signature dropped — the commit's author, committer and message kept.
func replantCommit(raw []byte, tree, parent string) []byte {
	head, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		head, body = bytes.TrimSuffix(raw, []byte("\n")), nil
	}
	var hdr bytes.Buffer
	skipping := false
	for _, line := range strings.Split(string(head), "\n") {
		if strings.HasPrefix(line, " ") {
			if !skipping {
				hdr.WriteString(line + "\n")
			}
			continue
		}
		skipping = false
		key, _, _ := strings.Cut(line, " ")
		switch key {
		case "gpgsig", "gpgsig-sha256":
			skipping = true
			continue
		case "tree":
			line = "tree " + tree
		case "parent":
			line = "parent " + parent
		}
		hdr.WriteString(line + "\n")
	}
	return append(append(hdr.Bytes(), '\n'), body...)
}

// UnpushedRef is where a failed push keeps the agent's commits in the
// trusted git dir: refs/conductor/unpushed/<branch>/<head>.
func UnpushedRef(branch, head string) string {
	return "refs/conductor/unpushed/" + branch + "/" + short(head)
}

// keepUnpushed keeps head (the agent's n commits a push failed to land)
// under UnpushedRef in the trusted git dir — a per-dispatch clone's objects
// copied in first — and names the ref in the returned error. Removing the
// workspace no longer loses the work: the operator, or the next dispatch on
// the branch (its worktree shares the trusted git dir), can pick it up.
func keepUnpushed(ctx context.Context, r Request, clone bool, env []string, head string, tips map[string]string, n int, perr error) error {
	ref := UnpushedRef(r.Branch, head)
	if err := importCommits(ctx, r, clone, env, head, tips); err != nil {
		return fmt.Errorf("%w (and keeping the agent's %d commit(s) failed: %v)", perr, n, err)
	}
	if _, err := runGit(ctx, r.GitDir, nil, nil, "update-ref", ref, head); err != nil {
		return fmt.Errorf("%w (and keeping the agent's %d commit(s) failed: %v)", perr, n, err)
	}
	return &KeptError{Err: perr, Ref: ref, Head: head, Commits: n}
}

// importCommits makes head's objects the trusted git dir's own. A linked
// worktree's commits are already there (its git dir is the trusted one); a
// per-dispatch clone's are packed out of the snapshot and indexed in.
func importCommits(ctx context.Context, r Request, clone bool, env []string, head string, tips map[string]string) error {
	if !clone {
		return nil
	}
	var in strings.Builder
	in.WriteString(head + "\n")
	for sha := range tips {
		if _, err := runGit(ctx, r.GitDir, nil, nil, "cat-file", "-e", sha+"^{commit}"); err == nil {
			in.WriteString("^" + sha + "\n")
		}
	}
	pack, err := runGit(ctx, r.GitDir, env, []byte(in.String()), "pack-objects", "--revs", "--stdout", "-q")
	if err != nil {
		return err
	}
	_, err = runGit(ctx, r.GitDir, nil, []byte(pack), "index-pack", "--stdin")
	return err
}

// KeptError is a failed push whose commits conductor kept (Ref, in the
// trusted git dir). It unwraps to the push's own error.
type KeptError struct {
	Err     error
	Ref     string
	Head    string
	Commits int
}

func (e *KeptError) Error() string {
	return fmt.Sprintf("%v — the agent's %d commit(s) are kept at %s (%s)", e.Err, e.Commits, e.Ref, short(e.Head))
}

func (e *KeptError) Unwrap() error { return e.Err }
