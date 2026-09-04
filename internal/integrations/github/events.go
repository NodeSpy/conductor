package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// failureConclusions are check conclusions we treat as "failing".
var failureConclusions = map[string]bool{
	"failure": true, "timed_out": true, "cancelled": true,
	"startup_failure": true, "stale": true, "action_required": true,
}

type ghPayload struct {
	Action       string `json:"action"`
	Ref          string `json:"ref"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName      string `json:"full_name"`
		Name          string `json:"name"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	PullRequest *prPayload `json:"pull_request"`
	Issue       *struct {
		Number      int         `json:"number"`
		Title       string      `json:"title"`
		HTMLURL     string      `json:"html_url"`
		PullRequest interface{} `json:"pull_request"` // non-nil => the issue is a PR
		User        struct {
			Login string `json:"login"`
		} `json:"user"` // the PR/issue author
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"` // issue assignees (for issue_labeled's assignee gate)
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"` // the issue's CURRENT label set (for state-based matching)
	} `json:"issue"`
	Review *struct {
		State string `json:"state"`
		ID    int64  `json:"id"`
		User  struct {
			Login string `json:"login"`
			Type  string `json:"type"` // "Bot" for app-authored reviews
		} `json:"user"`
	} `json:"review"`
	Comment *struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"` // "Bot" for app-authored comments
		} `json:"user"`
		Body string `json:"body"`
	} `json:"comment"`
	Assignee *struct {
		Login string `json:"login"`
	} `json:"assignee"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
	RequestedReviewer *struct {
		Login string `json:"login"`
	} `json:"requested_reviewer"`
	RequestedTeam *struct {
		Slug string `json:"slug"`
	} `json:"requested_team"`
	CheckRun       *checkPayload `json:"check_run"`
	CheckSuite     *checkPayload `json:"check_suite"`
	WorkflowRun    *checkPayload `json:"workflow_run"`
	ProjectsV2Item *struct {
		NodeID        string `json:"node_id"`
		ContentNodeID string `json:"content_node_id"`
		ContentType   string `json:"content_type"`
	} `json:"projects_v2_item"`
	Release *struct {
		TagName         string `json:"tag_name"`
		Name            string `json:"name"`
		HTMLURL         string `json:"html_url"`
		TargetCommitish string `json:"target_commitish"` // branch/SHA the tag points at
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
		Author          struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"release"`
	DeploymentStatus *struct {
		State       string `json:"state"`
		Environment string `json:"environment"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	} `json:"deployment_status"`
	Deployment *struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"deployment"`
	// Alert serves both dependabot_alert and secret_scanning_alert (different
	// subfields populated per event).
	Alert *struct {
		Number         int    `json:"number"`
		State          string `json:"state"`
		HTMLURL        string `json:"html_url"`
		SecretType     string `json:"secret_type"`
		SecretTypeName string `json:"secret_type_display_name"`
		Dependency     *struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
		} `json:"dependency"`
		SecurityAdvisory *struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
		} `json:"security_advisory"`
	} `json:"alert"`
}

type prPayload struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
	RequestedTeams []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type checkPayload struct {
	Conclusion   string `json:"conclusion"`
	Name         string `json:"name"`
	HeadSHA      string `json:"head_sha"`
	ID           int64  `json:"id"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

// triggersFor translates one webhook delivery into zero or more Triggers.
func (g *Integration) triggersFor(ctx context.Context, eventType string, body []byte) []core.Trigger {
	var p ghPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	repo := p.Repository.FullName
	if repo == "" {
		return nil
	}

	var trs []core.Trigger
	switch eventType {
	case "pull_request_review":
		trs = g.reviewTriggers(ctx, repo, p)
	case "pull_request_review_thread":
		trs = g.mergeReadyTriggers(ctx, repo, threadPR(p), p) // resolved threads may unblock merge
	case "issue_comment", "pull_request_review_comment":
		trs = g.commentTriggers(repo, eventType, p)
	case "check_run", "check_suite", "workflow_run":
		trs = g.checkTriggers(ctx, repo, p)
	case "pull_request":
		trs = g.pullRequestTriggers(ctx, repo, p)
	case "issues":
		trs = g.issueTriggers(ctx, repo, p)
	case "projects_v2_item":
		trs = g.projectTriggers(ctx, p)
	case "release":
		trs = g.releaseTriggers(repo, p)
	case "deployment_status":
		trs = g.deploymentStatusTriggers(repo, p)
	case "dependabot_alert":
		trs = g.dependabotAlertTriggers(repo, p)
	case "secret_scanning_alert":
		trs = g.secretScanningAlertTriggers(repo, p)
	}

	// Stamp the object's current labels so the engine can honor control.pause_label
	// (a per-PR/issue opt-out). Available on pull_request / issues events; a comment
	// or review event carries none, which is fine — pause_label just won't catch it.
	if labels := payloadLabels(p); len(labels) > 0 {
		for i := range trs {
			if trs[i].Context == nil {
				trs[i].Context = map[string]any{}
			}
			trs[i].Context["labels"] = labels
		}
	}

	// App-less (static-token) mode: there is no installation, but the fixed
	// token serves the same read role. Inject it so dispatch behaves the same.
	if len(trs) > 0 && g.app != nil && g.app.static != "" {
		for i := range trs {
			if trs[i].Context == nil {
				trs[i].Context = map[string]any{}
			}
			trs[i].Context["app_token"] = g.app.static
		}
	}

	// Inject the App installation token so dispatch can use it for reads, plus the
	// installation id so a persisted workflow can re-mint the (short-lived) token
	// on resume.
	if len(trs) > 0 && p.Installation.ID > 0 && g.app != nil {
		tok, err := g.app.installationToken(ctx, p.Installation.ID)
		var headRef string
		var prLabels []string
		fetched := false // resolve head_ref + labels once, lazily, if a feedback trigger needs it
		for i := range trs {
			if trs[i].Context == nil {
				trs[i].Context = map[string]any{}
			}
			trs[i].Context["installation_id"] = p.Installation.ID
			if err == nil {
				trs[i].Context["app_token"] = tok
			}
			// Feedback kinds (comment / review events) carry neither the PR head branch
			// nor its labels in the payload. Fetch both once: head_ref lets dispatch
			// adopt an open workspace already on it, and labels let the engine honor
			// control.pause_label on comment/review triggers (otherwise a `conductor:off`
			// label can't park a PR's comment autopilot — it isn't in the payload).
			if feedbackKind(trs[i].Kind) && g.rest != nil && trs[i].Target.Number > 0 {
				if !fetched {
					owner, name := splitRepo(repo)
					if hr, lbls, herr := g.rest.pullHeadRefAndLabels(ctx, p.Installation.ID, owner, name, trs[i].Target.Number); herr == nil {
						headRef, prLabels, fetched = hr, lbls, true
					}
				}
				if headRef != "" && emptyStr(trs[i].Context["head_ref"]) {
					trs[i].Context["head_ref"] = headRef
				}
				if len(prLabels) > 0 {
					if _, ok := trs[i].Context["labels"]; !ok {
						trs[i].Context["labels"] = prLabels
					}
				}
			}
		}
	}
	return trs
}

func (g *Integration) reviewTriggers(ctx context.Context, repo string, p ghPayload) []core.Trigger {
	if p.Action != "submitted" || p.Review == nil || p.PullRequest == nil {
		return nil
	}
	var trs []core.Trigger
	if p.Review.State == "changes_requested" && g.ownPR(p.PullRequest.User.Login) {
		t := g.prTarget(repo, p.PullRequest)
		reviewerIsBot := isBotActor(p.Review.User.Type, p.Review.User.Login)
		trs = append(trs, g.emit(repo, "changes_requested", t,
			fmt.Sprintf("changes requested on %s#%d", repo, p.PullRequest.Number),
			fmt.Sprintf("review:%d@%s", p.Review.ID, p.PullRequest.Head.SHA),
			map[string]any{"head_ref": p.PullRequest.Head.Ref,
				"author": p.Review.User.Login, "author_is_bot": reviewerIsBot},
			func(act config.Action) bool {
				return authorBotMatch(act.AuthorBot, reviewerIsBot)
			})...)
	}
	// Any submitted review may have made the PR merge-ready.
	trs = append(trs, g.mergeReadyTriggers(ctx, repo, p.PullRequest.Number, p)...)
	return trs
}

// ownPR reports whether login is one of the `me` identities (case-insensitive).
// Autopilot kinds (new_comment, changes_requested, failing_checks, merge_conflict,
// pr_behind) push fixes to the PR branch, so they must only fire on PRs you authored.
func (g *Integration) ownPR(login string) bool {
	if login == "" {
		return false
	}
	return g.self[strings.ToLower(login)]
}

// checkOwnPR resolves the PR author for a check event (whose payload omits it) via a
// single REST read on App creds, and reports whether you authored it. Fails closed:
// if the author can't be determined, don't act.
func (g *Integration) checkOwnPR(ctx context.Context, p ghPayload, num int) bool {
	if g.rest == nil || p.Installation.ID == 0 {
		return false
	}
	info, err := g.rest.pull(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, num)
	if err != nil {
		return false
	}
	return g.ownPR(info.User.Login)
}

func (g *Integration) commentTriggers(repo, eventType string, p ghPayload) []core.Trigger {
	if p.Action != "created" || p.Comment == nil {
		return nil
	}
	var num int
	var head, base, url, prAuthor, headRef string
	switch {
	case p.PullRequest != nil:
		num = p.PullRequest.Number
		head, base, url = p.PullRequest.Head.SHA, p.PullRequest.Base.Ref, p.PullRequest.HTMLURL
		headRef = p.PullRequest.Head.Ref
		prAuthor = p.PullRequest.User.Login
	case eventType == "issue_comment" && p.Issue != nil && p.Issue.PullRequest != nil:
		num = p.Issue.Number
		url = p.Issue.HTMLURL
		prAuthor = p.Issue.User.Login
	default:
		return nil // comment on a plain issue, not a PR
	}
	if !g.ownPR(prAuthor) {
		return nil // new_comment autopilot is for PRs you authored (we push fixes)
	}
	author := strings.ToLower(p.Comment.User.Login)
	if g.self[author] {
		return nil // ignore our own comments
	}
	t := g.target(repo, num, head, base, url)
	// comment_kind picks the engine's per-kind high-water mark: inline review
	// comments and conversation comments are separate GitHub id sequences.
	kind := store.CommentKindIssue
	if eventType == "pull_request_review_comment" {
		kind = store.CommentKindReview
	}
	authorIsBot := isBotActor(p.Comment.User.Type, p.Comment.User.Login)
	extra := map[string]any{"author": p.Comment.User.Login, "author_is_bot": authorIsBot,
		"comment_body": p.Comment.Body, "head_ref": headRef,
		"comment_id": p.Comment.ID, "comment_kind": kind}
	// Each variant may set its own from_users / author_bot filters.
	return g.emit(repo, "new_comment", t,
		fmt.Sprintf("new comment by %s on %s#%d", p.Comment.User.Login, repo, num),
		fmt.Sprintf("comment:%d", p.Comment.ID), extra, func(act config.Action) bool {
			return commentAuthorAllowed(act, author) && authorBotMatch(act.AuthorBot, authorIsBot)
		})
}

// commentAuthorAllowed reports whether a comment by author should trigger this
// new_comment variant: allowed by from_users (empty = any) AND not on ignore_users
// (e.g. CI report bots like github-actions[bot]). ignore wins over allow.
func commentAuthorAllowed(act config.Action, author string) bool {
	return fromUsersMatch(act.FromUsers, author) && !loginMatch(act.IgnoreUsers, author)
}

// isBotLogin reports GitHub's app-account login convention: a "[bot]" suffix
// (case-insensitive), e.g. dependabot[bot], cursor[bot].
func isBotLogin(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}

// isBotActor reports whether an event's actor is an automated bot: the
// webhook marks the account type "Bot", or the login carries the "[bot]"
// suffix (belt and suspenders — some payloads omit the type).
func isBotActor(userType, login string) bool {
	return strings.EqualFold(userType, "Bot") || isBotLogin(login)
}

// authorBotMatch evaluates an author_bot filter: nil = either, else the
// resolved bot-ness must match.
func authorBotMatch(want *bool, isBot bool) bool {
	return want == nil || *want == isBot
}

// fromUsersMatch reports whether the commenter is allowed by a from_users filter
// (an empty filter allows any author). Case-insensitive.
func fromUsersMatch(fromUsers []string, author string) bool {
	if len(fromUsers) == 0 {
		return true
	}
	return loginMatch(fromUsers, author)
}

// loginMatch reports whether author is in logins (case-insensitive exact match).
func loginMatch(logins []string, author string) bool {
	for _, u := range logins {
		if strings.EqualFold(u, author) {
			return true
		}
	}
	return false
}

func (g *Integration) checkTriggers(ctx context.Context, repo string, p ghPayload) []core.Trigger {
	if p.Action != "completed" {
		return nil
	}
	c := p.CheckRun
	if c == nil {
		c = p.CheckSuite
	}
	if c == nil {
		c = p.WorkflowRun
	}
	if c == nil || len(c.PullRequests) == 0 {
		return nil
	}
	num := c.PullRequests[0].Number
	var trs []core.Trigger
	if failureConclusions[c.Conclusion] && g.checkOwnPR(ctx, p, num) {
		// Suppress a fixer for a check the user opted to ignore (ignore_checks) — e.g.
		// a PR-title convention gate they don't follow. Logged so ignores are visible.
		if act, ok := g.actionFor(repo, "failing_checks"); ok && containsFold(act.IgnoreChecks, c.Name) {
			log.Printf("github[%s]: %s#%d failing check %q ignored (ignore_checks)", g.name, repo, num, c.Name)
		} else {
			t := g.target(repo, num, c.HeadSHA, "", "")
			extra := map[string]any{"failing_check": c.Name, "run_id": c.ID}
			trs = append(trs, g.single(repo, "failing_checks", t,
				fmt.Sprintf("failing checks on %s#%d", repo, num), "fail@"+c.HeadSHA, extra)...)
		}
	}
	// A completed check (pass or fail) may have made the PR merge-ready.
	trs = append(trs, g.mergeReadyTriggers(ctx, repo, num, p)...)
	return trs
}

func (g *Integration) pullRequestTriggers(ctx context.Context, repo string, p ghPayload) []core.Trigger {
	if p.PullRequest == nil {
		return nil
	}
	pr := p.PullRequest
	switch p.Action {
	case "closed":
		// Signal the engine to drop dedup state; no dispatch.
		return []core.Trigger{{Source: "github", Instance: g.name, Kind: core.KindClosed,
			Target: g.prTarget(repo, pr)}}
	case "review_requested":
		t := g.prTarget(repo, pr)
		labels := prLabelNames(pr)
		return g.emit(repo, "review_requested", t,
			fmt.Sprintf("review requested on %s#%d", repo, pr.Number),
			"reviewreq@"+pr.Head.SHA, nil, func(act config.Action) bool {
				return g.reviewerRequestedMatches(repo, act, p) &&
					!draftGate(act, pr.Draft) &&
					!act.Exclude.Matches(pr.Head.Ref, pr.Title, labels)
			})
	case "opened", "reopened", "synchronize", "ready_for_review":
		trs := g.mergeStateTriggers(ctx, repo, p, pr)
		trs = append(trs, g.selfReviewTriggers(repo, pr)...)
		trs = append(trs, g.mergeReadyTriggers(ctx, repo, pr.Number, p)...)
		if p.Action == "ready_for_review" {
			// A review requested while draft (and skipped by not_draft) fires now.
			trs = append(trs, g.readyReviewTriggers(ctx, repo, p)...)
		}
		return trs
	}
	return nil
}

// selfReviewTriggers fires when you open/update your own PR (critique it).
func (g *Integration) selfReviewTriggers(repo string, pr *prPayload) []core.Trigger {
	if !g.self[strings.ToLower(pr.User.Login)] {
		return nil // only your own PRs
	}
	t := g.prTarget(repo, pr)
	return g.single(repo, "self_review", t,
		fmt.Sprintf("self-review %s#%d", repo, pr.Number), "selfreview@"+pr.Head.SHA, nil)
}

// mergeReadyTriggers evaluates the composite merge gate (one GraphQL query, on
// App creds) and fires `merge_ready` when everything's green and the PR opts in.
// It short-circuits before any fetch when the action is absent/disabled.
func (g *Integration) mergeReadyTriggers(ctx context.Context, repo string, number int, p ghPayload) []core.Trigger {
	if number == 0 || g.rest == nil || p.Installation.ID == 0 {
		return nil
	}
	set := g.actionsFor(repo, "merge_ready")
	if len(set) == 0 {
		return nil // not configured — no GraphQL cost
	}
	owner, name := splitRepo(repo)
	gate, err := g.rest.prGate(ctx, p.Installation.ID, owner, name, number)
	if err != nil {
		return nil
	}
	if !g.ownPR(gate.Author) {
		return nil // auto-merge only merges PRs you authored
	}
	t := g.target(repo, number, gate.HeadSHA, "", "")
	return g.emit(repo, "merge_ready", t,
		fmt.Sprintf("merge-ready %s#%d", repo, number), "mergeready@"+gate.HeadSHA, nil,
		func(act config.Action) bool {
			if act.RequireLabel != "" && !containsFold(gate.Labels, act.RequireLabel) {
				return false
			}
			return mergeGatePasses(gate, act.Gates)
		})
}

// mergeGatePasses applies the standard gate, honoring explicit `false` toggles
// in the action's `gates` map to relax individual checks (default: all on).
func mergeGatePasses(g *mergeGate, gates map[string]any) bool {
	on := func(key string) bool {
		v, ok := gates[key]
		if !ok {
			return true
		}
		switch x := v.(type) {
		case bool:
			return x
		case string:
			return x != "" && x != "false" && x != "no"
		default:
			return true
		}
	}
	if on("not_draft") && g.IsDraft {
		return false
	}
	if on("merge_state") && g.MergeStateStatus != "CLEAN" {
		return false
	}
	if on("review_decision") && g.ReviewDecision != "APPROVED" {
		return false
	}
	if on("non_author_approval") && !g.NonAuthorApprove {
		return false
	}
	if on("threads_resolved") && !g.ThreadsResolved {
		return false
	}
	return true
}

// threadPR extracts the PR number from a pull_request_review_thread event.
func threadPR(p ghPayload) int {
	if p.PullRequest != nil {
		return p.PullRequest.Number
	}
	return 0
}

// projectTriggers re-evaluates issue_matched when a Projects v2 field changes.
// The webhook only carries the item node id, so it resolves the issue's repo +
// number, then fetches the issue's full state (labels/assignees/author/title +
// gate facts) so the same matcher used for `issues` events applies here too —
// this is what lets an issue that becomes matching via a board move fire.
func (g *Integration) projectTriggers(ctx context.Context, p ghPayload) []core.Trigger {
	if p.Action != "edited" || p.ProjectsV2Item == nil || p.Installation.ID == 0 || g.rest == nil {
		return nil
	}
	if !strings.EqualFold(p.ProjectsV2Item.ContentType, "Issue") {
		return nil
	}
	item, err := g.rest.projectItem(ctx, p.Installation.ID, p.ProjectsV2Item.NodeID, "Status")
	if err != nil {
		return nil
	}
	repo := item.Repo
	if len(g.actionsFor(repo, "issue_matched")) == 0 {
		return nil
	}
	owner, name := splitRepo(repo)
	facts, err := g.rest.issueEnrich(ctx, p.Installation.ID, owner, name, item.Number)
	if err != nil {
		return nil // fail closed — no issue state to match on
	}
	st := issueMatchState{title: facts.Title, author: facts.Author, labels: facts.Labels, assignees: facts.Assignees}
	t := g.target(repo, item.Number, "", "", "")
	t.PR = 0
	t.Issue = item.Number
	return g.emit(repo, "issue_matched", t,
		fmt.Sprintf("issue %s#%d matched your criteria", repo, item.Number),
		fmt.Sprintf("issuematch@%d", item.Number), nil, func(act config.Action) bool {
			return g.cheapMatch(repo, act, st) && (len(act.Gates) == 0 || issueGatePasses(facts, act.Gates))
		})
}

// mergeStateTriggers enriches a PR via REST to detect conflict/behind state.
func (g *Integration) mergeStateTriggers(ctx context.Context, repo string, p ghPayload, pr *prPayload) []core.Trigger {
	if g.rest == nil || p.Installation.ID == 0 {
		return nil
	}
	info, err := g.rest.pull(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, pr.Number)
	if err != nil {
		return nil
	}
	if !g.ownPR(info.User.Login) {
		return nil // conflict/behind autopilot is for PRs you authored
	}
	t := g.target(repo, pr.Number, info.Head.SHA, info.Base.Ref, info.HTMLURL)
	switch info.MergeableState {
	case "dirty":
		return g.single(repo, "merge_conflict", t,
			fmt.Sprintf("merge conflict on %s#%d", repo, pr.Number),
			"conflict:"+info.Base.Ref+"/"+info.Head.SHA, nil)
	case "behind":
		return g.single(repo, "pr_behind", t,
			fmt.Sprintf("%s#%d is behind %s", repo, pr.Number, info.Base.Ref),
			"behind:"+info.Base.Ref+"/"+info.Head.SHA, nil)
	}
	return nil
}

// releaseTriggers fires the `release` kind when a release is published. By
// default prereleases are skipped (they're usually RCs, not ship-worthy); a
// variant opts in with `include_prereleases: true`. Draft publishes are ignored.
// The tag drives dedup (releases aren't tied to a head SHA the way PR checks are);
// tag_name/prerelease/draft are exposed to templates via Context.
func (g *Integration) releaseTriggers(repo string, p ghPayload) []core.Trigger {
	if p.Release == nil || p.Action != "published" || p.Release.Draft {
		return nil
	}
	t := g.target(repo, 0, "", p.Release.TargetCommitish, p.Release.HTMLURL)
	t.PR, t.Number = 0, 0
	extra := map[string]any{
		"tag_name":   p.Release.TagName,
		"prerelease": p.Release.Prerelease,
		"draft":      p.Release.Draft,
	}
	return g.emit(repo, "release", t,
		fmt.Sprintf("release %s published on %s", p.Release.TagName, repo),
		"release:"+p.Release.TagName, extra, func(act config.Action) bool {
			return act.IncludePrereleases || !p.Release.Prerelease
		})
}

// deploymentStatusTriggers fires when a deployment reports failure/error (the
// actionable states) — e.g. to have an agent start triage. success/pending/etc.
// are ignored. {{.state}} / {{.environment}} / {{.url}} are exposed.
func (g *Integration) deploymentStatusTriggers(repo string, p ghPayload) []core.Trigger {
	if p.DeploymentStatus == nil {
		return nil
	}
	st := strings.ToLower(p.DeploymentStatus.State)
	if st != "failure" && st != "error" {
		return nil
	}
	sha, ref := "", p.Repository.DefaultBranch
	if p.Deployment != nil {
		sha = p.Deployment.SHA
		if p.Deployment.Ref != "" {
			ref = p.Deployment.Ref
		}
	}
	t := g.target(repo, 0, sha, ref, p.DeploymentStatus.TargetURL)
	t.PR, t.Number = 0, 0
	extra := map[string]any{"state": st, "environment": p.DeploymentStatus.Environment, "description": p.DeploymentStatus.Description}
	return g.single(repo, "deployment_status", t,
		fmt.Sprintf("deployment %s on %s (%s)", st, repo, p.DeploymentStatus.Environment),
		fmt.Sprintf("deploy:%s:%s:%s", p.DeploymentStatus.Environment, sha, st), extra)
}

// dependabotAlertTriggers fires when a Dependabot alert is created. {{.severity}} /
// {{.package}} / {{.summary}} / {{.url}} are exposed; dedup is per alert number.
func (g *Integration) dependabotAlertTriggers(repo string, p ghPayload) []core.Trigger {
	if p.Alert == nil || p.Action != "created" {
		return nil
	}
	pkg, sev, summary := "", "", ""
	if p.Alert.Dependency != nil {
		pkg = p.Alert.Dependency.Package.Name
	}
	if p.Alert.SecurityAdvisory != nil {
		sev, summary = p.Alert.SecurityAdvisory.Severity, p.Alert.SecurityAdvisory.Summary
	}
	t := g.target(repo, 0, "", p.Repository.DefaultBranch, p.Alert.HTMLURL)
	t.PR, t.Number = 0, 0
	extra := map[string]any{"severity": sev, "package": pkg, "summary": summary, "number": p.Alert.Number}
	return g.single(repo, "dependabot_alert", t,
		fmt.Sprintf("dependabot %s alert on %s: %s", sev, repo, pkg),
		fmt.Sprintf("dependabot:%d", p.Alert.Number), extra)
}

// secretScanningAlertTriggers fires when a secret-scanning alert is created.
// {{.secret_type}} / {{.url}} are exposed; dedup is per alert number.
func (g *Integration) secretScanningAlertTriggers(repo string, p ghPayload) []core.Trigger {
	if p.Alert == nil || p.Action != "created" {
		return nil
	}
	stype := p.Alert.SecretTypeName
	if stype == "" {
		stype = p.Alert.SecretType
	}
	t := g.target(repo, 0, "", p.Repository.DefaultBranch, p.Alert.HTMLURL)
	t.PR, t.Number = 0, 0
	extra := map[string]any{"secret_type": stype, "number": p.Alert.Number}
	return g.single(repo, "secret_scanning_alert", t,
		fmt.Sprintf("secret scanning alert on %s: %s", repo, stype),
		fmt.Sprintf("secretscan:%d", p.Alert.Number), extra)
}

func (g *Integration) issueTriggers(ctx context.Context, repo string, p ghPayload) []core.Trigger {
	if p.Issue == nil {
		return nil
	}
	t := g.target(repo, p.Issue.Number, "", p.Repository.DefaultBranch, p.Issue.HTMLURL)
	t.PR = 0
	t.Issue = p.Issue.Number

	// issue_matched: state-based match, re-evaluated on any change that could flip it.
	switch p.Action {
	case "opened", "edited", "labeled", "unlabeled", "assigned", "unassigned", "reopened":
		labels := make([]string, 0, len(p.Issue.Labels))
		for _, l := range p.Issue.Labels {
			labels = append(labels, l.Name)
		}
		assignees := make([]string, 0, len(p.Issue.Assignees))
		for _, a := range p.Issue.Assignees {
			assignees = append(assignees, a.Login)
		}
		st := issueMatchState{title: p.Issue.Title, author: p.Issue.User.Login, labels: labels, assignees: assignees}
		return g.emit(repo, "issue_matched", t,
			fmt.Sprintf("issue %s#%d matched your criteria", repo, p.Issue.Number),
			fmt.Sprintf("issuematch@%d", p.Issue.Number), nil, func(act config.Action) bool {
				// Cheap payload match first; enrich for gates only if those pass.
				if !g.cheapMatch(repo, act, st) {
					return false
				}
				return g.gatesPass(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.Issue.Number, act)
			})
	}
	return nil
}

// issueMatchState is an issue's current state for matching — sourced from a webhook
// payload (issues events) or from issueEnrich (projects_v2_item events).
type issueMatchState struct {
	title, author     string
	labels, assignees []string
}

// cheapMatch evaluates one issue_matched variant's payload filters (no API):
// assignee (default: assigned to you), sole-assignee, labels any/all, none-of +
// title exclude, and author allowlist.
func (g *Integration) cheapMatch(repo string, act config.Action, st issueMatchState) bool {
	if !g.issueAssigneeMatch(repo, act, st.assignees) {
		return false
	}
	if act.SoleAssignee && !g.soleSelf(st.assignees) {
		return false
	}
	if len(act.LabelsAny) > 0 && !anyFold(st.labels, act.LabelsAny) {
		return false
	}
	if len(act.LabelsAll) > 0 && !allFold(st.labels, act.LabelsAll) {
		return false
	}
	if act.Exclude.Matches("", st.title, st.labels) { // none-of + title exclude
		return false
	}
	if len(act.Authors) > 0 && !containsFold(act.Authors, st.author) {
		return false
	}
	return true
}

// gatesPass runs the GraphQL-backed gates (no_branch, project) if any are set;
// fail-closed on a missing client or fetch error. No gates → true (no API call).
func (g *Integration) gatesPass(ctx context.Context, instID int64, owner, name string, num int, act config.Action) bool {
	if len(act.Gates) == 0 {
		return true
	}
	if g.rest == nil || instID == 0 {
		return false
	}
	facts, err := g.rest.issueEnrich(ctx, instID, owner, name, num)
	return err == nil && issueGatePasses(facts, act.Gates)
}

// issueAssigneeMatch reports whether the issue's assignees satisfy the variant's
// `assignee` filter — defaulting to "assigned to the me identity" when unset.
func (g *Integration) issueAssigneeMatch(repo string, act config.Action, assignees []string) bool {
	asg := act.Assignee
	if len(asg.Logins) == 0 && len(asg.Teams) == 0 {
		return g.anySelf(assignees)
	}
	for _, l := range assignees {
		if asg.HasLogin(l) {
			return true
		}
	}
	return false
}

// anySelf reports whether any login is a `me` identity.
func (g *Integration) anySelf(logins []string) bool {
	for _, l := range logins {
		if g.self[strings.ToLower(l)] {
			return true
		}
	}
	return false
}

// soleSelf reports whether you are the ONLY assignee (every assignee is a `me`
// identity, and there is at least one).
func (g *Integration) soleSelf(logins []string) bool {
	if len(logins) == 0 {
		return false
	}
	for _, l := range logins {
		if !g.self[strings.ToLower(l)] {
			return false
		}
	}
	return true
}

// anyFold reports whether have contains ANY of want (case-insensitive).
func anyFold(have, want []string) bool {
	for _, w := range want {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}

// allFold reports whether have contains ALL of want (case-insensitive).
func allFold(have, want []string) bool {
	for _, w := range want {
		if !containsFold(have, w) {
			return false
		}
	}
	return true
}

// emit builds one trigger per ENABLED variant of the kind for which keep(act)
// returns true (keep==nil ⇒ every enabled variant). Each trigger carries that
// variant's action + name; the engine keys dedup/attempts on kind#variant so
// variants never collide. The resolved action is attached for the engine.
func (g *Integration) emit(repo, kind string, t core.Target, title, dedup string, extra map[string]any, keep func(config.Action) bool) []core.Trigger {
	var out []core.Trigger
	for _, act := range g.actionsFor(repo, kind) {
		if !act.IsEnabled() {
			continue
		}
		// Per-variant repo gates, used by the connectors-model lowering where
		// every trigger on a kind is a variant carrying its own `repos:` filter
		// (legacy configs never set these fields, so behavior is unchanged).
		if len(act.Repos) > 0 && !matchRepo(act.Repos, repo) {
			continue
		}
		if len(act.ExcludeRepos) > 0 && matchRepo(act.ExcludeRepos, repo) {
			continue
		}
		if keep != nil && !keep(act) {
			continue
		}
		ctxMap := map[string]any{}
		for k, v := range extra {
			ctxMap[k] = v
		}
		out = append(out, core.Trigger{
			Source: "github", Instance: g.name, Kind: kind, Variant: act.Name, Target: t,
			Title: title, Dedup: dedup, Context: ctxMap, Action: act,
		})
	}
	return out
}

// single emits every enabled variant of the kind (no per-variant applicability
// filter) — for kinds that fire on the event/state, not on per-variant criteria.
func (g *Integration) single(repo, kind string, t core.Target, title, dedup string, extra map[string]any) []core.Trigger {
	return g.emit(repo, kind, t, title, dedup, extra, nil)
}

func (g *Integration) prTarget(repo string, pr *prPayload) core.Target {
	t := g.target(repo, pr.Number, pr.Head.SHA, pr.Base.Ref, pr.HTMLURL)
	return t
}

// payloadLabels returns the current label names on the PR or issue in the payload
// (for control.pause_label). Empty when the event carries no object labels.
func payloadLabels(p ghPayload) []string {
	var out []string
	switch {
	case p.PullRequest != nil:
		for _, l := range p.PullRequest.Labels {
			out = append(out, l.Name)
		}
	case p.Issue != nil:
		for _, l := range p.Issue.Labels {
			out = append(out, l.Name)
		}
	}
	return out
}

// feedbackKind reports whether a kind is PR feedback that dispatch may route to an
// agent already checked out on the PR's head branch (see AdoptOpenWorkspaces).
func feedbackKind(k string) bool { return k == "new_comment" || k == "changes_requested" }

// emptyStr reports whether a Context value is absent or an empty string.
func emptyStr(v any) bool { s, _ := v.(string); return s == "" }

func (g *Integration) target(repo string, num int, head, base, url string) core.Target {
	owner, name := splitRepo(repo)
	return core.Target{
		Repo: repo, Owner: owner, Name: name, Project: g.mapProject(repo),
		PR: num, Number: num, HeadSHA: head, BaseRef: base, HTMLURL: url,
	}
}

// mapProject remaps repo (owner/name) to a configured paseo project name so an
// existing workspace is reused instead of cloning. An explicit project_map entry
// wins (case-insensitive); otherwise the org-wide project_rewrite applies. Returns
// "" when nothing changes the name (Target.Repo is then used for checkout).
func (g *Integration) mapProject(repo string) string {
	if p, ok := g.projectMap[strings.ToLower(repo)]; ok {
		return p
	}
	rw := g.cfg.ProjectRewrite
	if !rw.active() {
		return ""
	}
	owner, name := splitRepo(repo)
	if rw.Org != "" {
		owner = rw.Org
	}
	// paseo project names are lowercased, so normalize case-insensitively.
	project := strings.ToLower(owner + "/" + name)
	if strings.EqualFold(project, repo) {
		return "" // rewrite is a no-op for this repo (casing aside)
	}
	return project
}

// reviewerFor is a variant's effective reviewer: its own `reviewer`, else the
// rule-level `reviewer` (an empty result means "default to the me identity").
func (g *Integration) reviewerFor(repo string, act config.Action) config.Actors {
	r, _ := g.resolve(repo)
	return actorsOr(act.Reviewer, r.Reviewer)
}

// reviewerRequestedMatches reports whether this variant's reviewer is the one the
// webhook just requested (single reviewer/team), via reviewerInList.
func (g *Integration) reviewerRequestedMatches(repo string, act config.Action, p ghPayload) bool {
	var logins, slugs []string
	if p.RequestedReviewer != nil {
		logins = []string{p.RequestedReviewer.Login}
	}
	if p.RequestedTeam != nil {
		slugs = []string{p.RequestedTeam.Slug}
	}
	return g.reviewerInList(g.reviewerFor(repo, act), logins, slugs)
}

// draftGate reports whether a variant's opt-in `not_draft` gate should suppress it
// because the PR is still a draft (off unless configured).
func draftGate(act config.Action, isDraft bool) bool {
	return isDraft && gateEnabled(act.Gates, "not_draft")
}

// reviewerInList reports whether the configured reviewer (defaulting to the `me`
// identity when unset) is among the given requested-reviewer logins / team slugs.
func (g *Integration) reviewerInList(rev config.Actors, logins, teamSlugs []string) bool {
	byDefault := len(rev.Logins) == 0 && len(rev.Teams) == 0
	for _, l := range logins {
		if byDefault {
			if g.self[strings.ToLower(l)] {
				return true
			}
		} else if rev.HasLogin(l) {
			return true
		}
	}
	for _, s := range teamSlugs {
		for _, want := range rev.Teams {
			if strings.EqualFold(want, s) {
				return true
			}
		}
	}
	return false
}

// readyReviewTriggers fires review_requested when a PR is marked ready-for-review
// and your review is still pending. This is what makes the opt-in not_draft guard
// coherent: a review requested while the PR was a draft is skipped, then picked up
// promptly here once it's ready (rather than only on the next sweep).
func (g *Integration) readyReviewTriggers(ctx context.Context, repo string, p ghPayload) []core.Trigger {
	pr := p.PullRequest
	logins := make([]string, 0, len(pr.RequestedReviewers))
	for _, rr := range pr.RequestedReviewers {
		logins = append(logins, rr.Login)
	}
	slugs := make([]string, 0, len(pr.RequestedTeams))
	for _, tm := range pr.RequestedTeams {
		slugs = append(slugs, tm.Slug)
	}
	// The ready_for_review payload can omit reviewers that were requested while the
	// PR was a draft. If none are present, fetch the authoritative pending set via
	// REST so a draft→ready transition still kicks off your review now (not on the
	// hourly sweep).
	if len(logins) == 0 && len(slugs) == 0 && g.rest != nil && g.app != nil && p.Installation.ID > 0 {
		owner, name := splitRepo(repo)
		if rr, tt, err := g.rest.requestedReviewers(ctx, p.Installation.ID, owner, name, pr.Number); err == nil {
			logins, slugs = rr, tt
		}
	}
	labels := prLabelNames(pr)
	t := g.prTarget(repo, pr)
	return g.emit(repo, "review_requested", t,
		fmt.Sprintf("ready for review on %s#%d", repo, pr.Number), "reviewreq@"+pr.Head.SHA, nil,
		func(act config.Action) bool {
			return g.reviewerInList(g.reviewerFor(repo, act), logins, slugs) &&
				!act.Exclude.Matches(pr.Head.Ref, pr.Title, labels)
		})
}

// labelNames flattens a PR payload's labels to names.
func prLabelNames(pr *prPayload) []string {
	out := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		out = append(out, l.Name)
	}
	return out
}

// gateEnabled reads an opt-in boolean gate (absent → false, i.e. not enforced).
func gateEnabled(gates map[string]any, key string) bool {
	switch x := gates[key].(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "no"
	default:
		return false
	}
}

// actorsOr returns a if it has any logins/teams, else the fallback.
func actorsOr(a, fallback config.Actors) config.Actors {
	if len(a.Logins) > 0 || len(a.Teams) > 0 {
		return a
	}
	return fallback
}

// actionFor returns the resolved action for a repo+kind.
// actionFor returns the FIRST configured variant for a kind — for callers that
// only read config (gate/label checks, existence) rather than emit per variant.
func (g *Integration) actionFor(repo, kind string) (config.Action, bool) {
	set := g.actionsFor(repo, kind)
	if len(set) == 0 {
		return config.Action{}, false
	}
	return set[0], true
}

// actionsFor returns all configured variants for a kind (nil if the repo/kind has none).
func (g *Integration) actionsFor(repo, kind string) config.ActionSet {
	r, ok := g.resolve(repo)
	if !ok {
		return nil
	}
	return r.Actions[kind]
}

func splitRepo(full string) (owner, name string) {
	if i := strings.IndexByte(full, '/'); i > 0 {
		return full[:i], full[i+1:]
	}
	return full, ""
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
