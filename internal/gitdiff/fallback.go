package gitdiff

// This file is the go-git fallback for Proposed, used only when the `git`
// binary is not on PATH (see hasGit). It reproduces the same two-section
// shape ("# uncommitted (vs HEAD)" / "# committed, not pushed (vs <base>)")
// using go-git's own diff machinery instead of shelling out.
//
// Known differences from the git path, documented rather than hidden:
//   - The uncommitted section is a per-file line diff built from go-git's own
//     diff primitives (plumbing/format/diff + the same line-diff algorithm
//     go-git's own object.Patch uses); it does not go through git's rename
//     detection, so a renamed file shows as a delete + an add.
//   - Binary files are reported as "Binary files differ", never inlined.
//   - The "committed, not pushed" base is resolved from: the branch's
//     recorded upstream (branch.<name>.merge/remote in config), else the
//     nearest ancestor not reachable from any refs/remotes/* ref (mirrors
//     `git rev-list HEAD --not --remotes`), else refs/remotes/origin/HEAD.
//     A repo with no remote-tracking refs at all reports nothing pushed, same
//     as the git path.

import (
	"fmt"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	linediff "github.com/go-git/go-git/v5/utils/diff"
)

// proposedFallback is the go-git implementation of Proposed.
func proposedFallback(dir string, maxBytes int) (string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("gitdiff: %s is not a git worktree: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("gitdiff: %s has no HEAD: %w", dir, err)
	}
	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", fmt.Errorf("gitdiff: resolve HEAD commit: %w", err)
	}

	var b strings.Builder
	if wtDiff, err := workingTreeDiff(repo, headCommit); err == nil && strings.TrimSpace(wtDiff) != "" {
		b.WriteString("# uncommitted (vs HEAD)\n")
		b.WriteString(wtDiff)
	}
	if base, label := unpushedBaseFallback(repo, head, headCommit); base != nil {
		if patch, err := base.Patch(headCommit); err == nil {
			text := patch.String()
			if strings.TrimSpace(text) != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				fmt.Fprintf(&b, "# committed, not pushed (vs %s)\n", label)
				b.WriteString(text)
			}
		}
	}
	out := b.String()
	if len(out) > maxBytes {
		out = out[:maxBytes] + "\n… (diff clipped)"
	}
	return out, nil
}

// workingTreeDiff renders the uncommitted delta (staged + unstaged) against
// headCommit as a unified diff, one file patch per changed path reported by
// the worktree's Status.
func workingTreeDiff(repo *gogit.Repository, headCommit *object.Commit) (string, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	status, err := wt.Status()
	if err != nil {
		return "", err
	}
	if status.IsClean() {
		return "", nil
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return "", err
	}

	var fps []fdiff.FilePatch
	// Deterministic order: Status is a map, and its ordering shouldn't leak
	// into the reported diff.
	paths := make([]string, 0, len(status))
	for p := range status {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		st := status[path]
		if st.Staging == gogit.Unmodified && st.Worktree == gogit.Unmodified {
			continue
		}
		oldContent, hadOld := blobContent(headTree, path)
		newContent, hasNew := workingFileContent(wt, path)
		if !hadOld && !hasNew {
			continue // e.g. an ignored/untracked entry that vanished between calls
		}
		fps = append(fps, buildFilePatch(path, oldContent, hadOld, newContent, hasNew))
	}
	if len(fps) == 0 {
		return "", nil
	}
	return encodePatch(fps)
}

// unpushedBaseFallback resolves what committed-but-unpushed work is measured
// against, mirroring upstreamBase's precedence in the git path as closely as
// go-git's plumbing allows.
func unpushedBaseFallback(repo *gogit.Repository, head *plumbing.Reference, headCommit *object.Commit) (*object.Commit, string) {
	if head.Name().IsBranch() {
		if cfg, err := repo.Config(); err == nil {
			if br, ok := cfg.Branches[head.Name().Short()]; ok && br.Remote != "" && br.Merge != "" {
				upstream := plumbing.NewRemoteReferenceName(br.Remote, br.Merge.Short())
				if ref, err := repo.Reference(upstream, true); err == nil {
					if c, err := repo.CommitObject(ref.Hash()); err == nil {
						return c, "@{upstream}"
					}
				}
			}
		}
	}

	remoteHeads, err := reachableFromRemotes(repo)
	if err != nil {
		return nil, ""
	}
	if len(remoteHeads) == 0 {
		return nil, "" // no remote refs: nothing to call "pushed"
	}
	if remoteHeads[headCommit.Hash] {
		return nil, "" // HEAD is on a remote: nothing unpushed
	}
	// Walk HEAD's first-parent chain until a commit already reachable from a
	// remote is found; the base is that boundary commit's own parent.
	cur := headCommit
	var lastUnpushed *object.Commit
	for {
		if remoteHeads[cur.Hash] {
			break
		}
		lastUnpushed = cur
		parent, err := cur.Parent(0)
		if err != nil {
			break // reached a root commit with nothing unpushed before it
		}
		cur = parent
	}
	if lastUnpushed == nil {
		return nil, ""
	}
	base, err := lastUnpushed.Parent(0)
	if err != nil {
		return nil, "" // the whole history is unpushed; no base to diff from
	}
	if name, ok := nameFromRemotes(repo, base.Hash); ok {
		return base, name
	}
	return base, base.Hash.String()[:12]
}

// reachableFromRemotes returns the set of commit hashes reachable from any
// refs/remotes/* ref — the go-git equivalent of `--not --remotes`.
func reachableFromRemotes(repo *gogit.Repository) (map[plumbing.Hash]bool, error) {
	seen := map[plumbing.Hash]bool{}
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var starts []plumbing.Hash
	err = refs.ForEach(func(r *plumbing.Reference) error {
		if r.Name().IsRemote() && r.Type() == plumbing.HashReference {
			starts = append(starts, r.Hash())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, h := range starts {
		markReachable(repo, h, seen)
	}
	return seen, nil
}

// markReachable walks every ancestor of start (BFS over parent hashes) into
// seen. Unreadable commits are marked seen and otherwise ignored, rather than
// failing the whole walk over one bad ref.
func markReachable(repo *gogit.Repository, start plumbing.Hash, seen map[plumbing.Hash]bool) {
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] {
			continue
		}
		seen[h] = true
		c, err := repo.CommitObject(h)
		if err != nil {
			continue
		}
		for _, p := range c.ParentHashes {
			if !seen[p] {
				stack = append(stack, p)
			}
		}
	}
}

// nameFromRemotes labels a hash after a remote-tracking ref that points at it
// directly, e.g. "origin/main", for a friendlier section header than a bare
// sha (git's own name-rev does the same job on the git path).
func nameFromRemotes(repo *gogit.Repository, h plumbing.Hash) (string, bool) {
	refs, err := repo.References()
	if err != nil {
		return "", false
	}
	defer refs.Close()
	name, found := "", false
	_ = refs.ForEach(func(r *plumbing.Reference) error {
		if found || !r.Name().IsRemote() || r.Type() != plumbing.HashReference {
			return nil
		}
		if r.Hash() == h {
			name, found = strings.TrimPrefix(r.Name().String(), "refs/remotes/"), true
		}
		return nil
	})
	return name, found
}

// blobContent reads path's content from tree ("" , false when the path does
// not exist in it).
func blobContent(tree *object.Tree, path string) (string, bool) {
	f, err := tree.File(path)
	if err != nil {
		return "", false
	}
	if isBin, err := f.IsBinary(); err == nil && isBin {
		return "", true // present, but callers must not inline it
	}
	content, err := f.Contents()
	if err != nil {
		return "", true
	}
	return content, true
}

// workingFileContent reads path's current content off the worktree
// filesystem ("", false when it does not exist — a deleted file).
func workingFileContent(wt *gogit.Worktree, path string) (string, bool) {
	fi, err := wt.Filesystem.Stat(path)
	if err != nil || fi.IsDir() {
		return "", false
	}
	f, err := wt.Filesystem.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	const maxRead = 8 << 20 // 8MiB: generous for a text file, bounds a huge blob
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for len(buf) < maxRead {
		n, rerr := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if rerr != nil {
			break
		}
	}
	return string(buf), true
}

// buildFilePatch constructs one fdiff.FilePatch between oldContent and
// newContent using the same line-diff algorithm go-git's own object.Patch
// uses, so encodePatch renders a standard unified diff.
func buildFilePatch(path, oldContent string, hadOld bool, newContent string, hasNew bool) fdiff.FilePatch {
	binary := looksBinary(oldContent) || looksBinary(newContent)
	var from, to fdiff.File
	if hadOld {
		from = simpleFile{path: path, mode: filemode.Regular}
	}
	if hasNew {
		to = simpleFile{path: path, mode: filemode.Regular}
	}
	if binary {
		return simpleFilePatch{from: from, to: to, binary: true}
	}
	var chunks []fdiff.Chunk
	for _, d := range linediff.Do(oldContent, newContent) {
		var op fdiff.Operation
		switch {
		case d.Type < 0:
			op = fdiff.Delete
		case d.Type > 0:
			op = fdiff.Add
		default:
			op = fdiff.Equal
		}
		chunks = append(chunks, simpleChunk{content: d.Text, op: op})
	}
	return simpleFilePatch{from: from, to: to, chunks: chunks}
}

// looksBinary is the same NUL-byte heuristic git itself uses.
func looksBinary(s string) bool {
	return strings.IndexByte(s, 0) >= 0
}

// encodePatch renders file patches as a standard unified diff via go-git's
// own UnifiedEncoder — the same encoder object.Patch.String() uses, so the
// output shape matches a real `git diff` as closely as this package's
// hand-built FilePatch/File/Chunk implementations allow.
func encodePatch(fps []fdiff.FilePatch) (string, error) {
	p := simplePatch{fps: fps}
	var b strings.Builder
	enc := fdiff.NewUnifiedEncoder(&b, fdiff.DefaultContextLines)
	if err := enc.Encode(&p); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ---- minimal fdiff.Patch / FilePatch / File / Chunk implementations ------

type simpleFile struct {
	path string
	mode filemode.FileMode
}

func (f simpleFile) Hash() plumbing.Hash     { return plumbing.ZeroHash }
func (f simpleFile) Mode() filemode.FileMode { return f.mode }
func (f simpleFile) Path() string            { return f.path }

type simpleChunk struct {
	content string
	op      fdiff.Operation
}

func (c simpleChunk) Content() string       { return c.content }
func (c simpleChunk) Type() fdiff.Operation { return c.op }

type simpleFilePatch struct {
	from, to fdiff.File
	chunks   []fdiff.Chunk
	binary   bool
}

func (p simpleFilePatch) Files() (fdiff.File, fdiff.File) { return p.from, p.to }
func (p simpleFilePatch) Chunks() []fdiff.Chunk           { return p.chunks }
func (p simpleFilePatch) IsBinary() bool                  { return p.binary }

type simplePatch struct {
	fps []fdiff.FilePatch
}

func (p *simplePatch) FilePatches() []fdiff.FilePatch { return p.fps }
func (p *simplePatch) Message() string                { return "" }
