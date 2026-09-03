// Command mockgithub is a hermetic stand-in for the GitHub REST/GraphQL API used
// by the e2e harness (test/e2e/). Conductor is pointed at it via the
// PC_GITHUB_API_BASE testability hook. It serves the reads conductor needs
// (installation-token mint, installation-id lookup, PR state, comments, checks,
// GraphQL) from canned fixtures, and CAPTURES every write (the acts-as-the-user
// comment/label/etc. an agent posts) so delivery + identity can be asserted.
//
// Canned PR state is read from $CANNED_DIR/pull-<owner>-<repo>-<n>.json; a missing
// file yields a default clean PR authored by $ME. Captured writes are queryable at
// GET /_captured (JSON array) and reset at POST /_reset.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var (
	mu       sync.Mutex
	captured []capture
)

type capture struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Auth   string `json:"auth"`
	Body   string `json:"body"`
}

func me() string {
	if v := os.Getenv("ME"); v != "" {
		return v
	}
	return "conductor-user"
}

func cannedDir() string {
	if v := os.Getenv("CANNED_DIR"); v != "" {
		return v
	}
	return "/canned"
}

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	http.HandleFunc("/", route)
	log.Printf("mockgithub listening on %s (me=%s canned=%s)", addr, me(), cannedDir())
	log.Fatal(http.ListenAndServe(addr, nil))
}

var (
	reInstallToken = regexp.MustCompile(`^/app/installations/\d+/access_tokens$`)
	reRepoInstall  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/installation$`)
	reAcctInstall  = regexp.MustCompile(`^/(orgs|users)/([^/]+)/installation$`)
	rePull         = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)$`)
	reListPulls    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls$`)
	reReviewers    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/requested_reviewers$`)
	reIssueComment = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)/comments$`)
	rePullComment  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/comments$`)
	reRuns         = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/runs$`)
	reInstallRepos = regexp.MustCompile(`^/installation/repositories$`)
)

func route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// Harness control endpoints.
	switch p {
	case "/_health":
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	case "/_captured":
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, 200, captured)
		return
	case "/_reset":
		mu.Lock()
		captured = nil
		mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}

	// Reads.
	if r.Method == http.MethodGet || (r.Method == http.MethodPost && p == "/graphql") {
		switch {
		case reInstallToken.MatchString(p):
			writeJSON(w, 201, map[string]any{
				"token":      "fake-installation-token",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
			return
		case reRepoInstall.MatchString(p), reAcctInstall.MatchString(p):
			writeJSON(w, 200, map[string]any{"id": 42})
			return
		case rePull.MatchString(p):
			m := rePull.FindStringSubmatch(p)
			writeJSON(w, 200, pullJSON(m[1], m[2], m[3]))
			return
		case reReviewers.MatchString(p):
			writeJSON(w, 200, map[string]any{"users": []any{}, "teams": []any{}})
			return
		case reIssueComment.MatchString(p), rePullComment.MatchString(p):
			writeJSON(w, 200, []any{})
			return
		case reRuns.MatchString(p):
			writeJSON(w, 200, map[string]any{"workflow_runs": []any{}})
			return
		case reListPulls.MatchString(p):
			writeJSON(w, 200, []any{})
			return
		case reInstallRepos.MatchString(p):
			writeJSON(w, 200, map[string]any{"total_count": 0, "repositories": []any{}})
			return
		case p == "/graphql":
			writeJSON(w, 200, graphQL())
			return
		}
		writeJSON(w, 200, map[string]any{}) // benign default read
		return
	}

	// Writes: the installation-token mint is a POST but handled above; everything
	// else here is an acts-as-the-user write — capture it.
	if reInstallToken.MatchString(p) {
		writeJSON(w, 201, map[string]any{
			"token":      "fake-installation-token",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
		return
	}
	body, _ := io.ReadAll(r.Body)
	mu.Lock()
	captured = append(captured, capture{
		Method: r.Method, Path: p,
		Auth: r.Header.Get("Authorization"), Body: string(body),
	})
	mu.Unlock()
	log.Printf("captured write %s %s auth=%q", r.Method, p, r.Header.Get("Authorization"))
	writeJSON(w, 201, map[string]any{"id": 1, "ok": true})
}

// pullJSON returns the canned PR for owner/repo/num, or a default clean PR by $ME.
func pullJSON(owner, repo, num string) map[string]any {
	name := fmt.Sprintf("pull-%s-%s-%s.json", owner, repo, num)
	if b, err := os.ReadFile(filepath.Join(cannedDir(), name)); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			return m
		}
	}
	return map[string]any{
		"number": atoi(num), "state": "open", "draft": false,
		"mergeable_state": "clean", "mergeable": true,
		"title": "canned PR", "html_url": fmt.Sprintf("https://forge.test/%s/%s/pull/%s", owner, repo, num),
		"user":   map[string]any{"login": me()},
		"head":   map[string]any{"sha": "canned-head-sha", "ref": "pr-" + num},
		"base":   map[string]any{"ref": "main"},
		"labels": []any{},
	}
}

// graphQL returns a minimal, always-clean gate result. Conductor branches on the
// query text; a fixed shape satisfies the fields it reads.
func graphQL() map[string]any {
	return map[string]any{"data": map[string]any{
		"repository": map[string]any{
			"pullRequest": map[string]any{
				"headRefOid":       "canned-head-sha",
				"mergeStateStatus": "CLEAN",
				"reviewDecision":   nil,
				"isDraft":          false,
				"author":           map[string]any{"login": me()},
				"labels":           map[string]any{"nodes": []any{}},
				"reviewThreads":    map[string]any{"nodes": []any{}},
				"approvals":        map[string]any{"nodes": []any{}},
			},
		},
	}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
