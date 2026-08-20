package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	Head           struct {
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
