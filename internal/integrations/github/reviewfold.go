package github

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// ONE REVIEW SUBMISSION IS ONE EVENT; A STANDALONE COMMENT IS ITS OWN EVENT.
//
// A submitted review reaches us as one pull_request_review event plus one
// pull_request_review_comment event per inline comment it carries, delivered
// together and in no fixed order (and the sweep may later find the same
// comments again). Every one of those deliveries resolves to the same review
// id, and the review becomes exactly one trigger:
//
//   - a CHANGES_REQUESTED review a changes_requested trigger takes is ONE
//     changes_requested run; its inline comments ride in its context
//     (review_comments).
//   - any other review with inline comments (COMMENTED, APPROVED with
//     comments, or a changes-request no changes_requested trigger takes) is
//     ONE new_comment carrying the review body and all its inline comments.
//
// Whichever delivery for the review is handled first emits that trigger and
// claims the review id; every later delivery for it emits nothing. So the
// order the events land in doesn't matter, and neither does losing the review
// event itself.
//
// A comment with no review (a conversation comment, or a review comment that
// carries no review id) is its own new_comment, as it always was. So is a
// review comment whose review can't be read: a duplicate fixer is
// recoverable, a dropped comment is not.

const (
	// reviewCacheTTL bounds how long a review's facts and its claim are
	// remembered. A review's deliveries land within seconds of each other;
	// an hour also covers a sweep recovering them after a short outage
	// (past it, the engine's comment high-water mark still drops a review
	// already dispatched — its comment_id is the review's highest).
	reviewCacheTTL = time.Hour
	// reviewCacheMax caps the cache; past it, expired entries are dropped.
	reviewCacheMax = 512
	// maxReviewComments / maxReviewCommentBody / maxReviewCommentsBytes cap
	// what a review's run carries, so one enormous review can't push the
	// rendered prompt past what a runtime accepts (paseo's single prompt
	// argument tops out near 120KB) and fail the dispatch. Past the budget the
	// rest are counted in review_comments_omitted; the agent reads them on
	// the PR.
	maxReviewComments      = 100
	maxReviewCommentBody   = 2000
	maxReviewCommentsBytes = 48 << 10
	// maxReviewSummaryBytes caps a folded review's comment_body (the review
	// body plus every inline comment, as one text for filters and prompts
	// that read comment_body).
	maxReviewSummaryBytes = 8 << 10
)

// reviewInfo is what a review's event (or one REST read) says about it.
type reviewInfo struct {
	State       string // lowercased: "changes_requested", "commented", "approved", …
	Body        string
	Author      string
	AuthorIsBot bool
}

// reviewCache remembers submitted reviews' facts by id, and which reviews have
// already been turned into their one new_comment (claims).
type reviewCache struct {
	mu     sync.Mutex
	info   map[int64]reviewEntry
	claims map[int64]time.Time
}

type reviewEntry struct {
	reviewInfo
	at time.Time
}

func (c *reviewCache) get(id int64, now time.Time) (reviewInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.info[id]
	if !ok || now.Sub(e.at) > reviewCacheTTL {
		return reviewInfo{}, false
	}
	return e.reviewInfo, true
}

func (c *reviewCache) put(id int64, ri reviewInfo, now time.Time) {
	if id == 0 || ri.State == "" {
		return
	}
	ri.State = strings.ToLower(ri.State)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info == nil {
		c.info = map[int64]reviewEntry{}
	}
	if len(c.info) >= reviewCacheMax {
		for k, e := range c.info {
			if now.Sub(e.at) > reviewCacheTTL {
				delete(c.info, k)
			}
		}
		if len(c.info) >= reviewCacheMax {
			c.info = map[int64]reviewEntry{} // all fresh: a burst this big is rare; start over
		}
	}
	c.info[id] = reviewEntry{reviewInfo: ri, at: now}
}

// claimed reports whether the review was already turned into its event.
func (c *reviewCache) claimed(id int64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.claims[id]
	return ok && now.Sub(at) <= reviewCacheTTL
}

// claim marks the review as turned into its event; false if another delivery
// got there first (the caller then emits nothing).
func (c *reviewCache) claim(id int64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at, ok := c.claims[id]; ok && now.Sub(at) <= reviewCacheTTL {
		return false
	}
	if c.claims == nil {
		c.claims = map[int64]time.Time{}
	}
	if len(c.claims) >= reviewCacheMax {
		for k, at := range c.claims {
			if now.Sub(at) > reviewCacheTTL {
				delete(c.claims, k)
			}
		}
	}
	c.claims[id] = now
	return true
}

// reviewFacts resolves a submitted review: from the review's own event if this
// daemon saw it, else one REST read, cached so a review's N deliveries cost at
// most one. false when it can't be determined.
func (g *Integration) reviewFacts(ctx context.Context, instID int64, repo string, pr int, reviewID int64) (reviewInfo, bool) {
	if reviewID == 0 {
		return reviewInfo{}, false
	}
	if ri, ok := g.reviews.get(reviewID, time.Now()); ok {
		return ri, true
	}
	if g.rest == nil {
		return reviewInfo{}, false
	}
	owner, name := splitRepo(repo)
	ri, err := g.rest.review(ctx, instID, owner, name, pr, reviewID)
	if err != nil || ri.State == "" {
		log.Printf("github[%s]: %s#%d review %d: %v", g.name, repo, pr, reviewID, err)
		return reviewInfo{}, false
	}
	g.reviews.put(reviewID, ri, time.Now())
	return ri, true
}

// listReviewComments reads a review's inline comments (an error without a
// REST client).
func (g *Integration) listReviewComments(ctx context.Context, instID int64, repo string, pr int, reviewID int64) ([]reviewComment, error) {
	if g.rest == nil {
		return nil, fmt.Errorf("no REST client")
	}
	owner, name := splitRepo(repo)
	return g.rest.reviewComments(ctx, instID, owner, name, pr, reviewID)
}

// changesRequestedKeep is changes_requested's per-variant keep-condition for a
// review on pr by reviewer — shared by the review event itself and by the
// fold check on its inline comments, so a comment is folded only into a review
// the changes_requested trigger actually takes.
func (g *Integration) changesRequestedKeep(repo string, pr *prPayload, reviewer string, reviewerIsBot bool) func(config.Action) bool {
	return func(act config.Action) bool {
		facts := prFilterFacts(pr.Head.Ref, pr.Base.Ref, pr.Title, pr.User.Login,
			prLabelNames(pr), pr.Draft)
		facts["reviewer"] = reviewer
		facts["author_is_bot"] = reviewerIsBot
		return g.filterPasses(act, "changes_requested", repo, facts, lowerChangesRequested(act))
	}
}

// takenAsChangesRequested reports whether the review is a changes-request a
// changes_requested trigger takes — its run, not a new_comment, is the event.
func (g *Integration) takenAsChangesRequested(repo string, pr *prPayload, ri reviewInfo) bool {
	return ri.State == "changes_requested" &&
		g.wouldEmit(repo, "changes_requested", g.changesRequestedKeep(repo, pr, ri.Author, ri.AuthorIsBot))
}

// reviewEvent turns a submitted review into its ONE trigger (see the package
// note), unless a delivery for it already did. handled=false means its inline
// comments can't be read (or it has none): the caller decides what that means
// — a comment then stands alone, a review event emits nothing.
func (g *Integration) reviewEvent(ctx context.Context, repo string, instID int64, pr *prPayload, reviewID int64, ri reviewInfo) (trs []core.Trigger, handled bool) {
	if g.reviews.claimed(reviewID, time.Now()) {
		return nil, true
	}
	t := g.prTarget(repo, pr)
	if g.takenAsChangesRequested(repo, pr, ri) {
		if !g.reviews.claim(reviewID, time.Now()) {
			return nil, true
		}
		cr := g.emit(repo, "changes_requested", t,
			fmt.Sprintf("changes requested on %s#%d", repo, pr.Number),
			fmt.Sprintf("review:%d@%s", reviewID, pr.Head.SHA),
			map[string]any{"head_ref": pr.Head.Ref,
				"author": ri.Author, "author_is_bot": ri.AuthorIsBot,
				"review_id": reviewID, "review_body": ri.Body},
			g.changesRequestedKeep(repo, pr, ri.Author, ri.AuthorIsBot))
		// The review's inline comments ride this run, so it must carry them.
		g.attachReviewComments(ctx, cr, repo, instID, pr.Number, reviewID)
		return cr, true
	}
	cs, err := g.listReviewComments(ctx, instID, repo, pr.Number, reviewID)
	if err != nil || len(cs) == 0 {
		if err != nil {
			log.Printf("github[%s]: %s#%d review %d comments: %v", g.name, repo, pr.Number, reviewID, err)
		}
		return nil, false
	}
	return g.reviewNewComment(repo, t, pr.Head.Ref, reviewID, ri, cs), true
}

// reviewCommentDelivery handles a webhook inline comment that names its
// review: it is part of that review's one event. handled=false means "treat it
// as a standalone comment" — the review or its comments can't be read, so
// per-comment is the safe fallback.
func (g *Integration) reviewCommentDelivery(ctx context.Context, repo string, p ghPayload) (trs []core.Trigger, handled bool) {
	c, pr := p.Comment, p.PullRequest
	ri, ok := g.reviewFacts(ctx, p.Installation.ID, repo, pr.Number, c.PullRequestReviewID)
	if !ok {
		return nil, false
	}
	if ri.Author == "" {
		ri.Author, ri.AuthorIsBot = c.User.Login, isBotActor(c.User.Type, c.User.Login)
	}
	trs, handled = g.reviewEvent(ctx, repo, p.Installation.ID, pr, c.PullRequestReviewID, ri)
	if !handled {
		log.Printf("github[%s]: %s#%d review %d unreadable — comment %d stands alone",
			g.name, repo, pr.Number, c.PullRequestReviewID, c.ID)
	}
	return trs, handled
}

// reviewNewComment is the ONE new_comment a non-changes-requested review with
// inline comments becomes. It claims the review first, so whichever delivery
// gets here first is the only one that emits. Its fields are a comment's, so
// a trigger written for single comments still reads sensibly — the reviewer
// is author/comment_author, comment_body is the review body plus every inline
// comment, comment_id is the review's highest (the engine's high-water mark
// then drops any later recovery of it) — plus the review_* fields
// changes_requested carries.
func (g *Integration) reviewNewComment(repo string, t core.Target, headRef string, reviewID int64, ri reviewInfo, cs []reviewComment) []core.Trigger {
	if !g.reviews.claim(reviewID, time.Now()) {
		return nil
	}
	var maxID int64
	for _, c := range cs {
		if c.ID > maxID {
			maxID = c.ID
		}
	}
	body := reviewSummary(ri.Body, cs)
	extra := map[string]any{"author": ri.Author, "author_is_bot": ri.AuthorIsBot,
		"comment_body": body, "head_ref": headRef,
		"comment_id": maxID, "comment_kind": store.CommentKindReview,
		"review_id": reviewID, "review_body": ri.Body, "review_state": ri.State}
	addReviewComments(extra, cs)
	return g.emit(repo, "new_comment", t,
		fmt.Sprintf("review by %s on %s#%d (%d inline comment(s))", ri.Author, repo, t.Number, len(cs)),
		fmt.Sprintf("review:%d", reviewID), extra, func(act config.Action) bool {
			return g.filterPasses(act, "new_comment", repo,
				commentFilterFacts(ri.Author, body, ri.AuthorIsBot), lowerComment(act, true))
		})
}

// reviewSummary is a folded review as one comment_body: the review's own body,
// then each inline comment as "path:line: body", capped.
func reviewSummary(body string, cs []reviewComment) string {
	var b strings.Builder
	if strings.TrimSpace(body) != "" {
		b.WriteString(body)
	}
	for _, c := range cs {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(c.Path)
		if c.Line > 0 {
			fmt.Fprintf(&b, ":%d", c.Line)
		}
		b.WriteString(": ")
		b.WriteString(c.Body)
	}
	return clip(b.String(), maxReviewSummaryBytes)
}

// attachReviewComments stamps a changes_requested run with the review's inline
// comments (review_comments), one REST read per review. A failed read leaves
// the run without them — it still names the review (review_id) and the agent
// can read the PR itself — rather than dropping the run.
func (g *Integration) attachReviewComments(ctx context.Context, trs []core.Trigger, repo string, instID int64, pr int, reviewID int64) {
	if len(trs) == 0 {
		return
	}
	cs, err := g.listReviewComments(ctx, instID, repo, pr, reviewID)
	if err != nil {
		log.Printf("github[%s]: %s#%d review %d comments: %v — dispatching without them",
			g.name, repo, pr, reviewID, err)
		return
	}
	if len(cs) == 0 {
		return
	}
	for i := range trs {
		addReviewComments(trs[i].Context, cs)
	}
}

// addReviewComments stamps review_comments — one {author, path, line, body,
// url} object per comment, within the size caps — and, when the caps cut any,
// review_comments_omitted with how many.
func addReviewComments(ctx map[string]any, cs []reviewComment) {
	out := make([]any, 0, len(cs))
	budget := maxReviewCommentsBytes
	for _, c := range cs {
		body := clip(c.Body, maxReviewCommentBody)
		if len(out) == maxReviewComments || len(body) > budget {
			break
		}
		budget -= len(body)
		m := map[string]any{"author": c.Author, "path": c.Path, "body": body}
		if c.Line > 0 {
			m["line"] = c.Line
		}
		if c.URL != "" {
			m["url"] = c.URL
		}
		out = append(out, m)
	}
	ctx["review_comments"] = out
	if n := len(cs) - len(out); n > 0 {
		ctx["review_comments_omitted"] = n
	}
}

// clip caps s at n bytes, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
