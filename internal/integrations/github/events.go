package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
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
	} `json:"issue"`
	Review *struct {
		State string `json:"state"`
		ID    int64  `json:"id"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	Comment *struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
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
		trs = g.issueTriggers(repo, p)
	case "projects_v2_item":
		trs = g.projectTriggers(ctx, p)
	}

	// Inject the App installation token so dispatch can use it for reads.
	if len(trs) > 0 && p.Installation.ID > 0 && g.app != nil {
		if tok, err := g.app.installationToken(ctx, p.Installation.ID); err == nil {
			for i := range trs {
				if trs[i].Context == nil {
					trs[i].Context = map[string]any{}
				}
				trs[i].Context["app_token"] = tok
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
	if p.Review.State == "changes_requested" {
		t := g.prTarget(repo, p.PullRequest)
		trs = append(trs, g.single(repo, "changes_requested", t,
			fmt.Sprintf("changes requested on %s#%d", repo, p.PullRequest.Number),
			fmt.Sprintf("review:%d@%s", p.Review.ID, p.PullRequest.Head.SHA), nil)...)
	}
	// Any submitted review may have made the PR merge-ready.
	trs = append(trs, g.mergeReadyTriggers(ctx, repo, p.PullRequest.Number, p)...)
	return trs
}

func (g *Integration) commentTriggers(repo, eventType string, p ghPayload) []core.Trigger {
	if p.Action != "created" || p.Comment == nil {
		return nil
	}
	var num int
	var head, base, url string
	switch {
	case p.PullRequest != nil:
		num = p.PullRequest.Number
		head, base, url = p.PullRequest.Head.SHA, p.PullRequest.Base.Ref, p.PullRequest.HTMLURL
	case eventType == "issue_comment" && p.Issue != nil && p.Issue.PullRequest != nil:
		num = p.Issue.Number
		url = p.Issue.HTMLURL
	default:
		return nil // comment on a plain issue, not a PR
	}
	author := strings.ToLower(p.Comment.User.Login)
	if g.self[author] {
		return nil // ignore our own comments
	}
	t := g.target(repo, num, head, base, url)
	extra := map[string]any{"author": p.Comment.User.Login, "comment_body": p.Comment.Body}
	trs := g.single(repo, "new_comment", t,
		fmt.Sprintf("new comment by %s on %s#%d", p.Comment.User.Login, repo, num),
		fmt.Sprintf("comment:%d", p.Comment.ID), extra)
	// Apply the action's from_users filter, if any.
	return g.filterCommentByUsers(trs, author)
}

func (g *Integration) filterCommentByUsers(trs []core.Trigger, author string) []core.Trigger {
	out := trs[:0]
	for _, t := range trs {
		if act, ok := g.actionFor(t.Target.Repo, "new_comment"); ok && len(act.FromUsers) > 0 {
			match := false
			for _, u := range act.FromUsers {
				if strings.EqualFold(u, author) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, t)
	}
	return out
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
	if failureConclusions[c.Conclusion] {
		t := g.target(repo, num, c.HeadSHA, "", "")
		extra := map[string]any{"failing_check": c.Name, "run_id": c.ID}
		trs = append(trs, g.single(repo, "failing_checks", t,
			fmt.Sprintf("failing checks on %s#%d", repo, num), "fail@"+c.HeadSHA, extra)...)
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
		if !g.reviewerMatches(repo, p) {
			return nil
		}
		t := g.prTarget(repo, pr)
		return g.single(repo, "review_requested", t,
			fmt.Sprintf("review requested on %s#%d", repo, pr.Number),
			"reviewreq@"+pr.Head.SHA, nil)
	case "opened", "reopened", "synchronize", "ready_for_review":
		trs := g.mergeStateTriggers(ctx, repo, p, pr)
		trs = append(trs, g.selfReviewTriggers(repo, pr)...)
		trs = append(trs, g.mergeReadyTriggers(ctx, repo, pr.Number, p)...)
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
	act, ok := g.actionFor(repo, "merge_ready")
	if !ok || !act.IsEnabled() {
		return nil
	}
	owner, name := splitRepo(repo)
	gate, err := g.rest.prGate(ctx, p.Installation.ID, owner, name, number)
	if err != nil {
		return nil
	}
	if act.RequireLabel != "" && !containsFold(gate.Labels, act.RequireLabel) {
		return nil
	}
	if !mergeGatePasses(gate, act.Gates) {
		return nil
	}
	t := g.target(repo, number, gate.HeadSHA, "", "")
	return g.single(repo, "merge_ready", t,
		fmt.Sprintf("merge-ready %s#%d", repo, number), "mergeready@"+gate.HeadSHA, nil)
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

// projectTriggers handles Projects v2 item moves (e.g. Status → "Ready"). The
// webhook is thin, so it resolves the issue + field value via GraphQL.
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
	act, ok := g.actionFor(repo, "issue_project_moved")
	if !ok || !act.IsEnabled() {
		return nil
	}
	field, want := "Status", "Ready"
	if act.Project != nil {
		if f, ok := act.Project["field"].(string); ok && f != "" {
			field = f
		}
		if to, ok := act.Project["to"].(string); ok && to != "" {
			want = to
		}
	}
	value := item.Value
	if field != "Status" { // re-query with the configured field
		if it2, err := g.rest.projectItem(ctx, p.Installation.ID, p.ProjectsV2Item.NodeID, field); err == nil {
			value = it2.Value
		}
	}
	if !strings.EqualFold(value, want) {
		return nil
	}
	t := g.target(repo, item.Number, "", "", "")
	t.PR = 0
	t.Issue = item.Number
	return g.single(repo, "issue_project_moved", t,
		fmt.Sprintf("issue %s#%d moved to %s", repo, item.Number, want),
		fmt.Sprintf("project:%s=%s@%d", field, want, item.Number), nil)
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

func (g *Integration) issueTriggers(repo string, p ghPayload) []core.Trigger {
	if p.Issue == nil {
		return nil
	}
	t := g.target(repo, p.Issue.Number, "", p.Repository.DefaultBranch, p.Issue.HTMLURL)
	t.PR = 0
	t.Issue = p.Issue.Number
	switch p.Action {
	case "assigned":
		if p.Assignee == nil || !g.assigneeMatches(repo, p.Assignee.Login) {
			return nil
		}
		return g.single(repo, "issue_assigned", t,
			fmt.Sprintf("issue %s#%d assigned to you", repo, p.Issue.Number),
			"assigned:"+p.Assignee.Login+"@"+itoa(p.Issue.Number), nil)
	case "labeled":
		if p.Label == nil {
			return nil
		}
		act, ok := g.actionFor(repo, "issue_ready")
		if !ok || !containsFold(act.LabelsAny, p.Label.Name) {
			return nil
		}
		return g.single(repo, "issue_ready", t,
			fmt.Sprintf("issue %s#%d labeled %s", repo, p.Issue.Number, p.Label.Name),
			"label:"+p.Label.Name+"@"+itoa(p.Issue.Number), nil)
	}
	return nil
}

// single builds a one-element trigger slice iff a rule matches the repo and has
// an enabled action for the kind. The resolved action is attached for the engine.
func (g *Integration) single(repo, kind string, t core.Target, title, dedup string, extra map[string]any) []core.Trigger {
	r, ok := g.resolve(repo)
	if !ok {
		return nil
	}
	act, ok := r.Actions[kind]
	if !ok || !act.IsEnabled() {
		return nil
	}
	ctxMap := map[string]any{}
	for k, v := range extra {
		ctxMap[k] = v
	}
	return []core.Trigger{{
		Source: "github", Instance: g.name, Kind: kind, Target: t,
		Title: title, Dedup: dedup, Context: ctxMap, Action: act,
	}}
}

func (g *Integration) prTarget(repo string, pr *prPayload) core.Target {
	t := g.target(repo, pr.Number, pr.Head.SHA, pr.Base.Ref, pr.HTMLURL)
	return t
}

func (g *Integration) target(repo string, num int, head, base, url string) core.Target {
	owner, name := splitRepo(repo)
	return core.Target{
		Repo: repo, Owner: owner, Name: name,
		PR: num, Number: num, HeadSHA: head, BaseRef: base, HTMLURL: url,
	}
}

func (g *Integration) reviewerMatches(repo string, p ghPayload) bool {
	r, ok := g.resolve(repo)
	if !ok {
		return false
	}
	rev := actorsOr(r.Actions["review_requested"].Reviewer, r.Reviewer) // action-level, rule fallback
	if p.RequestedReviewer != nil && rev.HasLogin(p.RequestedReviewer.Login) {
		return true
	}
	if p.RequestedTeam != nil {
		for _, tm := range rev.Teams {
			if strings.EqualFold(tm, p.RequestedTeam.Slug) {
				return true
			}
		}
	}
	return false
}

func (g *Integration) assigneeMatches(repo, login string) bool {
	r, ok := g.resolve(repo)
	if !ok {
		return false
	}
	asg := actorsOr(r.Actions["issue_assigned"].Assignee, r.Assignee) // action-level, rule fallback
	return asg.HasLogin(login)
}

// actorsOr returns a if it has any logins/teams, else the fallback.
func actorsOr(a, fallback config.Actors) config.Actors {
	if len(a.Logins) > 0 || len(a.Teams) > 0 {
		return a
	}
	return fallback
}

// actionFor returns the resolved action for a repo+kind.
func (g *Integration) actionFor(repo, kind string) (config.Action, bool) {
	r, ok := g.resolve(repo)
	if !ok {
		return config.Action{}, false
	}
	a, ok := r.Actions[kind]
	return a, ok
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

func itoa(n int) string { return fmt.Sprintf("%d", n) }
