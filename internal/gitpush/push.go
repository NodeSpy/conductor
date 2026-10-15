package gitpush

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Request is one push of an agent's workspace.
type Request struct {
	// Workspace is the agent's checkout.
	Workspace string
	// GitDir is the trusted git dir conductor's git runs in.
	GitDir string
	// HeadDir is a linked worktree's own git dir (its HEAD), as conductor
	// recorded it; "" for a per-dispatch clone (HEAD and objects are read
	// from Workspace/.git, by snapshot).
	HeadDir string
	// Branch is the dispatch branch: the only ref updated.
	Branch string
	// Remote is the URL pushed to (from trusted config).
	Remote string
	// Signer re-signs every new commit; nil pushes them as they are.
	Signer *Signer
	// MaxCommits bounds the range (default 500).
	MaxCommits int
}

// Result reports a push.
type Result struct {
	Branch   string // the dispatch branch
	Old      string // the remote branch's tip before ("" = it did not exist)
	New      string // what it is now
	Commits  int    // commits pushed
	Signed   bool   // they were re-signed
	UpToDate bool   // nothing to push
	// WorkspaceUpdated: the workspace's branch now points at the signed
	// commits (its own unsigned ones are the same trees).
	WorkspaceUpdated bool
}

// ErrDiverged is a push refused because the remote branch has commits the
// workspace does not: conductor never forces.
var ErrDiverged = errors.New("gitpush: the remote branch has diverged from the workspace — conductor pushes fast-forward only, never forced")

// Push pushes r.Workspace's HEAD to refs/heads/<r.Branch> at r.Remote,
// fast-forward only, re-signing the new commits.
func Push(ctx context.Context, r Request) (Result, error) {
	res := Result{Branch: r.Branch}
	if err := CheckBranch(r.Branch); err != nil {
		return res, err
	}
	if r.GitDir == "" || r.Remote == "" || r.Workspace == "" {
		return res, fmt.Errorf("gitpush: no trusted git dir or remote for %s", r.Workspace)
	}
	if strings.HasPrefix(r.Remote, "-") || strings.HasPrefix(strings.ToLower(r.Remote), "ext::") {
		return res, fmt.Errorf("gitpush: refusing remote %q", r.Remote)
	}
	max := r.MaxCommits
	if max <= 0 {
		max = 500
	}
	tmp, err := os.MkdirTemp("", "conductor-push-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	objDir := filepath.Join(tmp, "objects")
	if err := os.MkdirAll(objDir, 0o700); err != nil {
		return res, err
	}
	clone := r.HeadDir == ""
	alts := []string{filepath.Join(r.GitDir, "objects")}
	var head, onBranch string
	if clone {
		snap := filepath.Join(tmp, "snap")
		if err := snapshotObjects(r.Workspace, snap); err != nil {
			return res, err
		}
		alts = append(alts, snap)
		if head, onBranch, err = readHead(r.Workspace, true); err != nil {
			return res, err
		}
	} else {
		if head, onBranch, err = readHead(r.HeadDir, false); err != nil {
			return res, err
		}
	}
	env := []string{"GIT_OBJECT_DIRECTORY=" + objDir, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.Join(alts, string(os.PathListSeparator))}
	git := func(stdin []byte, args ...string) (string, error) { return runGit(ctx, r.GitDir, env, stdin, args...) }
	if head == "" {
		out, err := git(nil, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+onBranch+"^{commit}")
		if err != nil {
			return res, fmt.Errorf("gitpush: resolve the workspace's branch %s: %w", onBranch, err)
		}
		head = strings.TrimSpace(out)
	}
	if !isSHA(head) {
		return res, fmt.Errorf("gitpush: the workspace HEAD is not a commit")
	}
	if _, err := git(nil, "cat-file", "-e", head+"^{commit}"); err != nil {
		return res, fmt.Errorf("gitpush: the workspace HEAD %s is not a commit conductor can read: %w", head, err)
	}

	ref := "refs/heads/" + r.Branch
	out, err := git(nil, "ls-remote", "--end-of-options", r.Remote, ref)
	if err != nil {
		return res, fmt.Errorf("gitpush: read %s at the remote: %w", r.Branch, err)
	}
	old := ""
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == ref && isSHA(f[0]) {
			old = f[0]
		}
	}
	res.Old = old
	if old == head {
		res.UpToDate, res.New = true, head
		return res, nil
	}
	if old != "" {
		if _, err := git(nil, "cat-file", "-e", old+"^{commit}"); err != nil {
			if _, err := git(nil, "fetch", "--no-write-fetch-head", "--no-tags", "--end-of-options", r.Remote, ref); err != nil {
				return res, fmt.Errorf("gitpush: fetch %s: %w", r.Branch, err)
			}
		}
		if _, err := git(nil, "merge-base", "--is-ancestor", old, head); err != nil {
			return res, ErrDiverged
		}
	}
	revArgs := []string{"rev-list", "--reverse", "--topo-order", head}
	if old != "" {
		revArgs = append(revArgs, "^"+old)
	} else {
		revArgs = append(revArgs, "--not", "--remotes")
	}
	out, err = git(nil, revArgs...)
	if err != nil {
		return res, fmt.Errorf("gitpush: list the commits to push: %w", err)
	}
	commits := strings.Fields(out)
	if len(commits) > max {
		return res, fmt.Errorf("gitpush: %d new commits — more than the %d conductor pushes at once", len(commits), max)
	}
	res.Commits = len(commits)

	newHead := head
	if r.Signer != nil && len(commits) > 0 {
		mapped := map[string]string{}
		for _, c := range commits {
			raw, err := git(nil, "cat-file", "commit", c)
			if err != nil {
				return res, err
			}
			signed, err := rewriteCommit(ctx, []byte(raw), mapped, r.Signer)
			if err != nil {
				return res, fmt.Errorf("gitpush: sign %s: %w", c[:12], err)
			}
			id, err := git(signed, "hash-object", "-t", "commit", "-w", "--stdin")
			if err != nil {
				return res, err
			}
			mapped[c] = strings.TrimSpace(id)
		}
		newHead = mapped[head]
		res.Signed = true
	}
	// The ONLY push: the workspace's HEAD (signed) to the dispatch branch.
	// No '+', no --force: a remote that moved since is a rejection.
	if _, err := git(nil, "push", "--porcelain", "--no-verify", "--end-of-options", r.Remote, newHead+":"+ref); err != nil {
		return res, fmt.Errorf("gitpush: push %s: %w", r.Branch, err)
	}
	res.New = newHead
	res.WorkspaceUpdated = updateWorkspace(ctx, r, clone, objDir, head, newHead, onBranch) == nil
	return res, nil
}

// updateWorkspace moves the workspace onto what was pushed — the signed
// commits (same trees) and the remote-tracking ref — so the agent's next
// commit builds on them and its work reads as pushed. Only when its HEAD
// still names the commit pushed (an agent that moved on keeps its work).
func updateWorkspace(ctx context.Context, r Request, clone bool, objDir, head, newHead, onBranch string) error {
	if clone {
		if newHead != head {
			if err := copyLooseObjects(objDir, r.Workspace, true); err != nil {
				return err
			}
		}
		cur, br, err := readHead(r.Workspace, true)
		if err != nil || cur != head || br != onBranch {
			return fmt.Errorf("the workspace moved on")
		}
		if newHead != head {
			rel := ".git/HEAD"
			if br != "" {
				rel = filepath.Join(".git", "refs", "heads", br)
				if err := mkdirsUnder(r.Workspace, filepath.Dir(rel)); err != nil {
					return err
				}
			}
			if err := writeUnder(r.Workspace, rel, []byte(newHead+"\n"), 0o644); err != nil {
				return err
			}
		}
		rt := filepath.Join(".git", "refs", "remotes", "origin", r.Branch)
		if err := mkdirsUnder(r.Workspace, filepath.Dir(rt)); err != nil {
			return err
		}
		return writeUnder(r.Workspace, rt, []byte(newHead+"\n"), 0o644)
	}
	// A linked worktree: its objects and refs are the trusted git dir's.
	if newHead != head {
		if err := copyLooseObjects(objDir, filepath.Join(r.GitDir, "objects"), false); err != nil {
			return err
		}
		if onBranch != "" {
			if _, err := runGit(ctx, r.GitDir, nil, nil, "update-ref", "refs/heads/"+onBranch, newHead, head); err != nil {
				return err
			}
		} else if err := writeUnder(r.HeadDir, "HEAD", []byte(newHead+"\n"), 0o644); err != nil {
			return err
		}
	}
	_, err := runGit(ctx, r.GitDir, nil, nil, "update-ref", "refs/remotes/origin/"+r.Branch, newHead)
	return err
}

// copyLooseObjects copies the loose objects conductor wrote (the signed
// commits) into a repository: an agent's workspace (dst is the workspace,
// written under .git/objects with no symlink followed), or a trusted object
// store (dst is the objects dir).
func copyLooseObjects(objDir, dst string, workspace bool) error {
	ents, err := os.ReadDir(objDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.IsDir() || len(e.Name()) != 2 || !isHex(e.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(objDir, e.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(objDir, e.Name(), f.Name()))
			if err != nil {
				return err
			}
			if workspace {
				if err := mkdirUnder(dst, ".git/objects", e.Name()); err != nil {
					return err
				}
				if err := writeUnder(dst, filepath.Join(".git", "objects", e.Name(), f.Name()), b, 0o444); err != nil {
					return err
				}
				continue
			}
			d := filepath.Join(dst, e.Name())
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(d, f.Name()), b, 0o444); err != nil && !os.IsExist(err) {
				return err
			}
		}
	}
	return nil
}

// mkdirsUnder makes each component of rel under root, refusing symlinks.
func mkdirsUnder(root, rel string) error {
	parent := ""
	for _, c := range splitRel(rel) {
		if err := mkdirUnder(root, parent, c); err != nil {
			return err
		}
		parent = filepath.Join(parent, c)
	}
	return nil
}
