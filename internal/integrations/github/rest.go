package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// restClient is a small, rate-limit-aware GitHub REST client that authenticates
// with App installation tokens. It's used for the enrichment reads that
// webhook payloads don't carry (chiefly PR mergeable state).
type restClient struct {
	app   *appAuth
	httpc *http.Client
}

func newRESTClient(app *appAuth) *restClient {
	return &restClient{app: app, httpc: &http.Client{Timeout: 20 * time.Second}}
}

type pullInfo struct {
	MergeableState string `json:"mergeable_state"` // clean|dirty|behind|blocked|unstable|draft|unknown
	Mergeable      *bool  `json:"mergeable"`
	Draft          bool   `json:"draft"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"` // PR author
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	HTMLURL string `json:"html_url"`
}

// pull fetches a PR, retrying briefly while GitHub computes mergeable state.
func (c *restClient) pull(ctx context.Context, instID int64, owner, repo string, num int) (*pullInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.app.apiBase, owner, repo, num)
	for attempt := 0; attempt < 3; attempt++ {
		var pi pullInfo
		if err := c.get(ctx, instID, url, &pi); err != nil {
			return nil, err
		}
		if pi.MergeableState != "" && pi.MergeableState != "unknown" {
			return &pi, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 1500 * time.Millisecond):
		}
	}
	// Return whatever we have (state may be "unknown").
	var pi pullInfo
	if err := c.get(ctx, instID, url, &pi); err != nil {
		return nil, err
	}
	return &pi, nil
}

type prListItem struct {
	Number int  `json:"number"`
	Draft  bool `json:"draft"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	HTMLURL            string `json:"html_url"`
	Title              string `json:"title"`
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

// listOpenPRs lists open PRs for a repo (first page; sweep is a coarse catch-up).
func (c *restClient) listOpenPRs(ctx context.Context, instID int64, owner, repo string) ([]prListItem, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=100", c.app.apiBase, owner, repo)
	var out []prListItem
	if err := c.get(ctx, instID, url, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type repoRef struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// listInstallationRepos lists repositories the installation can access (first
// page; used to expand `owner/*` sweep globs).
func (c *restClient) listInstallationRepos(ctx context.Context, instID int64) ([]repoRef, error) {
	url := c.app.apiBase + "/installation/repositories?per_page=100"
	var data struct {
		Repositories []repoRef `json:"repositories"`
	}
	if err := c.get(ctx, instID, url, &data); err != nil {
		return nil, err
	}
	return data.Repositories, nil
}

// get performs a GET with the installation token, honoring rate-limit backoff.
func (c *restClient) get(ctx context.Context, instID int64, url string, out any) error {
	tok, err := c.app.installationToken(ctx, instID)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := c.httpc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			wait := rateLimitWait(resp)
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return fmt.Errorf("GET %s: rate-limited after retries", url)
}

// graphql runs a GraphQL query with the installation token and decodes the
// `data` field into out. GitHub's GraphQL is the only source for review-thread
// resolution and Projects v2.
func (c *restClient) graphql(ctx context.Context, instID int64, query string, vars map[string]any, out any) error {
	tok, err := c.app.installationToken(ctx, instID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	url := c.app.apiBase + "/graphql"
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			wait := rateLimitWait(resp)
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("graphql: HTTP %d", resp.StatusCode)
		}
		var env struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			return err
		}
		if len(env.Errors) > 0 {
			return fmt.Errorf("graphql: %s", env.Errors[0].Message)
		}
		return json.Unmarshal(env.Data, out)
	}
	return fmt.Errorf("graphql: rate-limited after retries")
}

// mergeGate is the composite merge-readiness of a PR (all from one GraphQL query).
type mergeGate struct {
	HeadSHA          string
	MergeStateStatus string // CLEAN, BLOCKED, BEHIND, DIRTY, DRAFT, UNSTABLE, …
	ReviewDecision   string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	IsDraft          bool
	Author           string
	ThreadsResolved  bool
	NonAuthorApprove bool
	Labels           []string
}

// unresolvedThreadIDs returns the node ids of the PR's unresolved review threads
// (on App creds), sorted. Used by the sweep to reconcile outstanding review
// comments that no live webhook recovered.
func (c *restClient) unresolvedThreadIDs(ctx context.Context, instID int64, owner, name string, number int) ([]string, error) {
	const q = `query($o:String!,$n:String!,$num:Int!){
	  repository(owner:$o,name:$n){ pullRequest(number:$num){
	    reviewThreads(first:100){nodes{id isResolved}}
	  }}}`
	var data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.graphql(ctx, instID, q, map[string]any{"o": owner, "n": name, "num": number}, &data); err != nil {
		return nil, err
	}
	var ids []string
	for _, t := range data.Repository.PullRequest.ReviewThreads.Nodes {
		if !t.IsResolved {
			ids = append(ids, t.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// prGate fetches the merge-readiness gate for a PR.
func (c *restClient) prGate(ctx context.Context, instID int64, owner, name string, number int) (*mergeGate, error) {
	const q = `query($o:String!,$n:String!,$num:Int!){
	  repository(owner:$o,name:$n){ pullRequest(number:$num){
	    headRefOid mergeStateStatus reviewDecision isDraft
	    author{login}
	    labels(first:50){nodes{name}}
	    reviewThreads(first:100){nodes{isResolved}}
	    approvals:reviews(first:50,states:[APPROVED]){nodes{author{login}}}
	  }}}`
	var data struct {
		Repository struct {
			PullRequest struct {
				HeadRefOid       string                 `json:"headRefOid"`
				MergeStateStatus string                 `json:"mergeStateStatus"`
				ReviewDecision   string                 `json:"reviewDecision"`
				IsDraft          bool                   `json:"isDraft"`
				Author           struct{ Login string } `json:"author"`
				Labels           struct {
					Nodes []struct{ Name string } `json:"nodes"`
				} `json:"labels"`
				ReviewThreads struct {
					Nodes []struct{ IsResolved bool } `json:"nodes"`
				} `json:"reviewThreads"`
				Approvals struct {
					Nodes []struct {
						Author struct{ Login string } `json:"author"`
					} `json:"nodes"`
				} `json:"approvals"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.graphql(ctx, instID, q, map[string]any{"o": owner, "n": name, "num": number}, &data); err != nil {
		return nil, err
	}
	pr := data.Repository.PullRequest
	g := &mergeGate{
		HeadSHA: pr.HeadRefOid, MergeStateStatus: pr.MergeStateStatus,
		ReviewDecision: pr.ReviewDecision, IsDraft: pr.IsDraft, Author: pr.Author.Login,
		ThreadsResolved: true,
	}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			g.ThreadsResolved = false
		}
	}
	for _, a := range pr.Approvals.Nodes {
		if a.Author.Login != "" && a.Author.Login != pr.Author.Login {
			g.NonAuthorApprove = true
		}
	}
	for _, l := range pr.Labels.Nodes {
		g.Labels = append(g.Labels, l.Name)
	}
	return g, nil
}

// projectItem is a Projects v2 item resolved to its issue + a field value.
type projectItem struct {
	Repo      string // owner/name
	Number    int
	Title     string
	Value     string   // the named single-select field's current option name
	Assignees []string // issue assignee logins (for the assignee gate)
}

// projectItem resolves a projects_v2_item node to its issue content and the
// current value of a single-select field (e.g. "Status").
func (c *restClient) projectItem(ctx context.Context, instID int64, itemNodeID, field string) (*projectItem, error) {
	const q = `query($id:ID!,$field:String!){ node(id:$id){ ... on ProjectV2Item {
	  content{ ... on Issue { number title repository{nameWithOwner} assignees(first:20){nodes{login}} } }
	  fieldValueByName(name:$field){ ... on ProjectV2ItemFieldSingleSelectValue { name } }
	}}}`
	var data struct {
		Node struct {
			Content struct {
				Number     int    `json:"number"`
				Title      string `json:"title"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
				Assignees struct {
					Nodes []struct {
						Login string `json:"login"`
					} `json:"nodes"`
				} `json:"assignees"`
			} `json:"content"`
			FieldValueByName struct {
				Name string `json:"name"`
			} `json:"fieldValueByName"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, instID, q, map[string]any{"id": itemNodeID, "field": field}, &data); err != nil {
		return nil, err
	}
	n := data.Node
	if n.Content.Repository.NameWithOwner == "" || n.Content.Number == 0 {
		return nil, fmt.Errorf("project item has no issue content")
	}
	assignees := make([]string, 0, len(n.Content.Assignees.Nodes))
	for _, a := range n.Content.Assignees.Nodes {
		assignees = append(assignees, a.Login)
	}
	return &projectItem{Repo: n.Content.Repository.NameWithOwner, Number: n.Content.Number,
		Title: n.Content.Title, Value: n.FieldValueByName.Name, Assignees: assignees}, nil
}

// rateLimitWait derives a backoff from Retry-After / X-RateLimit-Reset.
func rateLimitWait(resp *http.Response) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := time.ParseDuration(ra + "s"); err == nil {
			return secs
		}
	}
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		var epoch int64
		if _, err := fmt.Sscan(reset, &epoch); err == nil {
			d := time.Until(time.Unix(epoch, 0))
			if d > 0 && d < 5*time.Minute {
				return d
			}
		}
	}
	return 5 * time.Second
}
