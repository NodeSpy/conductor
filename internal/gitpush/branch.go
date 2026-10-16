// Package gitpush is how a sandboxed agent's work reaches its remote:
// conductor pushes it, the agent never does (docs/design/agent-sandbox.md
// §6). An agent holds no push credential; it commits in its workspace, and
// conductor — outside the sandbox, as the operator — pushes the workspace's
// HEAD to exactly the dispatch's own branch, fast-forward only, after
// re-signing every new commit with the operator's SSH signing key.
//
// Nothing the agent can write steers conductor's git: the push runs in the
// workspace's TRUSTED git dir (the base clone behind a per-dispatch clone, a
// linked worktree's common dir — whose config the sandbox shows read-only),
// with hooks, fsmonitor, external diff and the ext transport off and the
// ssh command from the operator's own config. A per-dispatch clone's HEAD
// and objects are read by path with O_NOFOLLOW at every component and copied
// into a conductor-owned directory; git never opens the clone itself.
package gitpush

import (
	"fmt"
	"strings"
)

// CheckBranch refuses a branch name conductor will not push to: empty, a
// full ref (refs/…), HEAD, an option (-…), anything git's check-ref-format
// rejects, and the characters a refspec or shell would read as syntax
// (space, ~ ^ : * ? [ \). The agent never names the ref — the dispatch's
// branch is conductor's record — so this is defense in depth against a
// branch name that came off event data.
func CheckBranch(b string) error {
	bad := func(why string) error { return fmt.Errorf("gitpush: refusing branch %q: %s", b, why) }
	switch {
	case b == "":
		return bad("empty")
	case b == "HEAD" || b == "@":
		return bad("not a branch")
	case strings.HasPrefix(b, "refs/"):
		return bad("a full ref, not a branch name")
	case strings.HasPrefix(b, "-"):
		return bad("starts with '-'")
	case strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.Contains(b, "//"):
		return bad("empty path component")
	case strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock"):
		return bad("ends with '.' or '.lock'")
	case strings.Contains(b, "..") || strings.Contains(b, "@{"):
		return bad("contains '..' or '@{'")
	case len(b) > 200:
		return bad("too long")
	}
	if strings.ContainsAny(b, " \t~^:*?[\\") {
		return bad("contains a character git or a refspec reads as syntax")
	}
	for _, r := range b {
		if r < 0x20 || r == 0x7f {
			return bad("contains a control character")
		}
	}
	for _, comp := range strings.Split(b, "/") {
		if strings.HasPrefix(comp, ".") {
			return bad("a component starts with '.'")
		}
	}
	return nil
}
