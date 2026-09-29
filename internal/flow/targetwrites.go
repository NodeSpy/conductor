package flow

import (
	"strings"

	"github.com/NodeSpy/conductor/internal/targets"
)

// checkGithubTargetWrite is RunSkillVerb's target-lifecycle gate for the
// github connector: it classifies verb into the vocabulary targets.Registry
// understands and checks it against the dispatch's own target (id.Repo/
// id.Number/its head branch) and id.Writes. Returns "" to allow the call, or
// the refusal reason to deny it. A read verb (githubWriteKind returns "")
// isn't checked at all — reads were never bound to the dispatch's own target.
func checkGithubTargetWrite(id SkillIdentity, verb string, options map[string]any) string {
	kind, number, branch := githubWriteKind(verb, options)
	if kind == "" {
		return ""
	}
	repo, _ := options["repo"].(string)
	target := targets.Target{
		Repo:       id.OwnRepo(),
		Number:     id.Number,
		IsPR:       id.Number > 0,
		HeadBranch: headRefOf(id),
	}
	if kind == "push" {
		// None of put_file/delete_file/create_branch (the only github verbs
		// this maps to "push") can force-push or delete a branch — those are a
		// real git ref-update concept a future git-level push broker will see
		// and pass through; the github content verbs here never are either.
		return targets.Default.CheckPush(target, id.Writes, repo, branch, false, false)
	}
	return targets.Default.CheckWrite(target, id.Writes, kind, repo, number)
}

// headRefOf reads the PR head branch off the dispatch's captured trigger
// context (see core.Trigger.Context / the github connector's "head_ref" fact)
// — "" when the originating trigger carried none (not every kind publishes
// it).
func headRefOf(id SkillIdentity) string {
	s, _ := id.Context["head_ref"].(string)
	return s
}

// githubWriteKind classifies a github verb call into the small vocabulary
// targets.Registry.CheckWrite/CheckPush understand: "" for a read verb that
// touches no lifecycle state and is never checked; "push" for the three
// content verbs that land a commit/branch on a branch (checked via CheckPush,
// branch-scoped rather than number-scoped); otherwise one of comment, review,
// reply, create_pr, create_issue, merge, close, reopen, edit, other. number is
// the PR/issue the write touches, read off whichever option name the verb
// declares (pr/number); branch is set only for a "push" verb.
func githubWriteKind(verb string, options map[string]any) (kind string, number int, branch string) {
	switch verb {
	// --- reads: never bound to the dispatch's own target ---
	case "pr_diff", "pr_get", "pr_files", "review_comments", "file", "get_issue",
		"get_ref", "list_runs", "get_run", "list_issues", "search_issues", "checks",
		"get_gist", "list_gists", "sweep":
		return "", 0, ""

	// --- comment-shaped writes ---
	case "comment":
		return "comment", githubOptNumber(options), ""
	case "reply":
		return "reply", githubOptNumber(options), ""
	case "submit_review":
		return "review", githubOptNumber(options), ""

	// --- PR/issue lifecycle ---
	case "merge_pr":
		return "merge", githubOptNumber(options), ""
	case "update_pr":
		return githubStateKind(options), githubOptNumber(options), ""
	case "update_issue":
		return githubStateKind(options), githubOptNumber(options), ""
	case "create_pr":
		return "create_pr", 0, ""
	case "create_issue":
		return "create_issue", 0, ""

	// --- push-shaped writes: branch-scoped, not number-scoped ---
	case "put_file", "delete_file", "create_branch":
		b, _ := options["branch"].(string)
		return "push", 0, b

	// --- everything else that mutates something: "edit" when it's clearly
	// scoped to a PR/issue number, "other" when it isn't (a repo-level or
	// account-level write: workflow runs, releases, gists) ---
	case "request_review", "rerequest_review", "remove_reviewer",
		"assign", "remove_label", "add_labels",
		"ready_for_review", "convert_to_draft":
		return "edit", githubOptNumber(options), ""
	case "dispatch_workflow", "rerun_run", "cancel_run",
		"create_release", "upload_asset",
		"create_gist", "update_gist", "delete_gist":
		return "other", 0, ""
	}
	// An unrecognized verb (a future addition to the connector's table this
	// classifier hasn't caught up with) is treated as a write of unknown shape
	// rather than silently let through unchecked.
	return "other", githubOptNumber(options), ""
}

// githubOptNumber reads the PR/issue number a verb's options name it by:
// "pr" (int), else "number" (int) — the two spellings the github verb table
// uses (some verbs alias one to the other; both are read here as ints since
// options arrive as agent-supplied JSON-ish values).
func githubOptNumber(options map[string]any) int {
	if n := githubOptInt(options, "pr"); n != 0 {
		return n
	}
	return githubOptInt(options, "number")
}

func githubOptInt(options map[string]any, key string) int {
	switch v := options[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// githubStateKind reads update_pr/update_issue's `state:` option into the
// close/reopen/edit vocabulary: "closed" closes the target, "open" reopens it,
// anything else (or absent — a plain title/body/base edit) is a bare edit.
func githubStateKind(options map[string]any) string {
	s, _ := options["state"].(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "closed":
		return "close"
	case "open":
		return "reopen"
	default:
		return "edit"
	}
}
