package targets

import (
	"fmt"
	"path"
	"strings"
)

// WritePolicy is what a dispatch's agent may write OUTSIDE plain reads. The
// zero value is the default FIXER policy: it may comment/reply/review and push
// to its own target and its own head branch, and nothing else — no new
// PRs/issues, no other target, and no merge/close/reopen of even its own
// target (that needs an explicit grant via Merge).
//
// TODO(jail): today every dispatch gets the zero-value default above; a later
// change populates this from per-step config (the "jail" work) so an operator
// can widen or narrow it per profile.
type WritePolicy struct {
	// ReadOnly refuses every write unconditionally — a review/critique step
	// that must never edit anything it's looking at.
	ReadOnly bool
	// CreatePR allows opening a new pull request.
	CreatePR bool
	// CreateIssue allows opening a new issue.
	CreateIssue bool
	// OtherTargets allows writing to a PR/issue (or branch) other than the
	// dispatch's own target.
	OtherTargets bool
	// Branches lists extra branch globs (path.Match) the dispatch may push to,
	// beyond its own head branch.
	Branches []string
	// Merge allows merging, closing, or reopening the dispatch's own target.
	Merge bool
}

// Target is the dispatch's own target: the PR/issue the agent was launched
// for, and — for a PR — the head branch it's expected to push to.
type Target struct {
	Repo       string
	Number     int
	IsPR       bool
	HeadBranch string
}

const readOnlyReason = "target: writes are refused — this step is read-only"

// CheckWrite decides one non-push write. kind is a small closed vocabulary:
// "comment", "review", "reply", "resolve_thread", "create_pr", "create_issue",
// "merge", "close", "reopen", "edit", "other". repo/number describe what the
// write touches (number 0 = not target-scoped — e.g. a repo-level verb with no
// PR/issue of its own). Returns "" to allow the write, or a reason to refuse
// it.
func (r *Registry) CheckWrite(t Target, p WritePolicy, kind, repo string, number int) string {
	if p.ReadOnly {
		return readOnlyReason
	}
	if reason := r.closedReason(t); reason != "" {
		return reason
	}
	switch kind {
	case "create_pr":
		if !p.CreatePR {
			return "target: opening a PR is refused (writes are bound to the dispatch's own target)"
		}
		return ""
	case "create_issue":
		if !p.CreateIssue {
			return "target: opening an issue is refused (writes are bound to the dispatch's own target)"
		}
		return ""
	}
	if number != 0 && number == t.Number && strings.EqualFold(repo, t.Repo) {
		// The dispatch's own target: merge/close/reopen need their own grant;
		// everything else (comment/review/reply/resolve_thread/edit/other) is
		// the baseline a fixer always has.
		if (kind == "merge" || kind == "close" || kind == "reopen") && !p.Merge {
			return fmt.Sprintf("target: %s is refused (policy does not allow this on the dispatch's own target)", kind)
		}
		return ""
	}
	if p.OtherTargets {
		return ""
	}
	return otherTargetReason(t, repo, number)
}

// CheckPush decides a push-shaped write: a commit, branch create, or ref
// update on repo/branch. The repo's default branch (branch == "" — every
// github verb that takes one spells "use the repo's default branch" that way),
// a force push, and a branch delete are always refused: WritePolicy carries no
// override for any of the three today. Exported so a future git-level push
// broker (which sees real force/delete ref updates, not just the github
// connector's content verbs) can reuse the same target-lifecycle + branch-scope
// decision.
func (r *Registry) CheckPush(t Target, p WritePolicy, repo, branch string, force, delete bool) string {
	if p.ReadOnly {
		return readOnlyReason
	}
	if reason := r.closedReason(t); reason != "" {
		return reason
	}
	if branch == "" {
		return "target: push to the repo's default branch is refused"
	}
	if force {
		return "target: force push is refused"
	}
	if delete {
		return "target: branch delete is refused"
	}
	sameRepo := strings.EqualFold(repo, t.Repo)
	if sameRepo && (branch == t.HeadBranch || matchAnyBranch(p.Branches, branch)) {
		return ""
	}
	if !sameRepo {
		if p.OtherTargets && matchAnyBranch(p.Branches, branch) {
			return ""
		}
		return otherTargetReason(t, repo, 0)
	}
	return fmt.Sprintf("target: push to %s is refused (not the dispatch's own branch)", branch)
}

// closedReason refuses EVERY write once the dispatch's own target is a known
// merged/closed PR — including a push to its own branch: once the target is
// dead, nothing the dispatch does to it (or in its name) should still land.
func (r *Registry) closedReason(t Target) string {
	outcome, ok := r.Closed(t.Repo, t.Number)
	if !ok {
		return ""
	}
	return fmt.Sprintf("target: %s#%d is %s — writes refused", t.Repo, t.Number, outcome)
}

// otherTargetReason formats the "not your target" refusal. It drops the repo
// prefix on the dispatch's own side when it names the same repo (the common
// case — a fixer reaching for a different PR/branch in the repo it's already
// working), matching how a same-repo mistake actually reads to an operator.
func otherTargetReason(t Target, repo string, number int) string {
	sameRepo := strings.EqualFold(repo, t.Repo)
	switch {
	case sameRepo && number != 0:
		return fmt.Sprintf("target: write to %s#%d but dispatch target is #%d", repo, number, t.Number)
	case sameRepo:
		return fmt.Sprintf("target: write to %s but dispatch target is #%d", repo, t.Number)
	case number != 0:
		return fmt.Sprintf("target: write to %s#%d but dispatch target is %s#%d", repo, number, t.Repo, t.Number)
	default:
		return fmt.Sprintf("target: write to %s but dispatch target is %s#%d", repo, t.Repo, t.Number)
	}
}

// matchAnyBranch reports whether branch matches any of the operator's extra
// allowed globs — the same path.Match matcher config.Exclude.Branches and the
// agent-authored policy's path globs use.
func matchAnyBranch(globs []string, branch string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, branch); err == nil && ok {
			return true
		}
	}
	return false
}
