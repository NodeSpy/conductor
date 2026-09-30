package hostcmd

import (
	"fmt"
	"strings"
)

// Write binding for the binaries that write to a forge as the operator
// (#154): gh and git. This is binary knowledge — what these tools do — and
// it is configured only through isolation.host.gh and isolation.host.git.
// conductor's own verbs (`conductor call github.create_pr`) are a separate
// surface with its own model (the verb grant, the connector's scopes); a
// refusal or an allowance on one never touches the other.
//
// The defaults:
//
//	gh   writes on the dispatch's own PR or issue — comment, review, reply,
//	     resolve a thread, edit, update its branch — are allowed; opening a
//	     PR or an issue, merging/closing/reopening, a repository-level write,
//	     a write to another PR/issue, and anything on another repository are
//	     refused
//	git  a push to the dispatch's own branch is allowed; any other branch is
//	     refused; a force push or a branch delete is always refused
//
// A review step writes nothing through either binary. A closed target takes
// no writes at all. The operator opens more with the binary's allow list,
// by naming the command: `host.gh.allow: ["*", "pr create *"]` keeps every
// gh command available ("*") and lets gh open PRs ("pr create *");
// `host.git.allow: ["*", "push release/*"]` lets pushes reach release
// branches. A pattern counts as naming the command only when its leading
// words are literal (two for gh — `pr create`, `api repos/*/pulls` — and
// `push` plus a branch pattern for git), so a blanket "*" or "pr *" never opens a write, and only
// the operator's own (global or runtime) blocks count: a step — a pack's
// included — can narrow, never widen.

// binder is a profile that binds its command's writes itself.
type binder interface {
	bind(p Parsed, rule Rule, ctx Context) string
}

// ownWriteKinds are the gh writes a fixer may make on its own PR/issue.
var ownWriteKinds = set("comment", "review", "reply", "resolve_thread", "edit", "push")

func (ghProfile) bind(p Parsed, rule Rule, ctx Context) string {
	for _, w := range p.Writes {
		if r := ghBind(w, p, rule, ctx); r != "" {
			return r
		}
	}
	return ""
}

func ghBind(w Write, p Parsed, rule Rule, ctx Context) string {
	if ctx.TargetClosed != nil {
		if r := ctx.TargetClosed(); r != "" {
			return r
		}
	}
	own := ctx.Repo != "" && strings.EqualFold(w.Repo, ctx.Repo) && w.Number != 0 && w.Number == ctx.Number
	if w.Kind == "push" && !(ctx.IsPR && ctx.HeadBranch != "" && w.Branch == ctx.HeadBranch) {
		own = false
	}
	if own && ownWriteKinds[w.Kind] && !ctx.ReadOnly {
		return ""
	}
	if namesCommand(rule, p.Words, p.Flags, 2, 2) {
		return ""
	}
	var why string
	switch {
	case ctx.Repo == "":
		why = "this launch has no dispatch target, so gh writes are refused"
	case ctx.ReadOnly:
		why = "this is a review step: its gh writes are refused"
	case w.Kind == "other_repo":
		why = fmt.Sprintf("gh on %s but the dispatch's repository is %s", w.Repo, ctx.Repo)
	case w.Kind == "create_pr":
		why = "opening a PR is refused (gh writes are bound to the dispatch's own PR)"
	case w.Kind == "create_issue":
		why = "opening an issue is refused (gh writes are bound to the dispatch's own PR)"
	case own:
		why = fmt.Sprintf("%s of the dispatch's own target is refused by default", w.Kind)
	case w.Number != 0:
		why = fmt.Sprintf("write to %s#%d but the dispatch's target is #%d", w.Repo, w.Number, ctx.Number)
	default:
		why = "a repository-level write is refused (gh writes are bound to the dispatch's own PR)"
	}
	return "gh: " + why + knob("gh", strings.Join(p.Words[:min(2, len(p.Words))], " ")+" *")
}

// GitPush binds one brokered push of the dispatch's clone: branch is the
// destination (refs/heads/ stripped), rule the binary's configured rule
// (Resolve("git", …)).
func GitPush(rule Rule, ctx Context, branch string, force, del bool) string {
	switch {
	case ctx.Repo == "":
		return "git: this launch has no dispatch target, so pushes are refused"
	case force:
		return "git: force push is refused"
	case del:
		return "git: branch delete is refused"
	}
	if ctx.TargetClosed != nil {
		if r := ctx.TargetClosed(); r != "" {
			return r
		}
	}
	words := []string{"push", branch}
	rule.Profiled = true
	if r := matchRules(rule, Parsed{Words: words}, nil); r != "" {
		return "git: " + r
	}
	if !ctx.ReadOnly && ctx.IsPR && ctx.HeadBranch != "" && branch == ctx.HeadBranch {
		return ""
	}
	if namesCommand(rule, words, nil, 1, 2) {
		return ""
	}
	why := fmt.Sprintf("push to %s is refused (not the dispatch's own branch)", branch)
	if ctx.ReadOnly {
		why = "this is a review step: its pushes are refused"
	}
	return "git: " + why + knob("git", "push "+branch)
}

// namesCommand reports whether one of the operator's own allow patterns
// names this command: it matches, it has at least `words` leading words
// (flags aside), and none of the first `literal` is a bare "*" (the first
// has no glob at all). gh: `pr create *` names `pr create`, `pr *` doesn't;
// git: `push release/*` and `push *` name a push destination.
func namesCommand(rule Rule, cmd []string, flags map[string][]string, literal, words int) bool {
	for _, pat := range rule.OperatorAllow {
		var lead []string
		for _, f := range strings.Fields(pat) {
			if strings.HasPrefix(f, "-") && f != "-" {
				continue
			}
			lead = append(lead, f)
		}
		if len(lead) < words || strings.ContainsAny(lead[0], "*?[") {
			continue
		}
		bare := false
		for _, w := range lead[:literal] {
			if w == "*" {
				bare = true
			}
		}
		if !bare && MatchRule(pat, cmd, flags) {
			return true
		}
	}
	return false
}

// knob names the allow entry that would permit the command.
func knob(tool, pattern string) string {
	return fmt.Sprintf(" — allow it with isolation.host.%s.allow: [\"*\", %q]", tool, pattern)
}
