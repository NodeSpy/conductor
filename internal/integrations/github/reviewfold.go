package github

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// A submitted review reaches us as ONE pull_request_review event plus one
// pull_request_review_comment event per inline comment it carries, all
// delivered together and in no fixed order. When the review requests changes,
// the changes_requested trigger already addresses the whole review in one run
// (and re-requests the reviewer once it has pushed). A new_comment run per
// inline comment on top of that is N more fixers racing the same branch: one
// commit and push per comment, none of them re-requesting the reviewer.
//
// So an inline comment that belongs to a CHANGES_REQUESTED review a
// changes_requested trigger takes is FOLDED into that review: no new_comment
// is emitted for it, and the review's run carries every inline comment in its
// context (review_comments) so nothing it said is dropped. Comments on any
// other review (COMMENTED, APPROVED, or a lone reply) are new_comment events
// exactly as before.

const (
	// reviewStateTTL bounds how long a review's state is remembered. A
	// review's inline comments arrive within seconds of it; an hour also
	// covers a sweep recovering them after a short outage.
	reviewStateTTL = time.Hour
	// reviewStateMax caps the cache; past it, expired entries are dropped.
	reviewStateMax = 512
	// maxReviewComments / maxReviewCommentBody / maxReviewCommentsBytes cap
	// what a changes_requested run carries, so one enormous review can't push
	// the rendered prompt past what a runtime accepts (paseo's single prompt
	// argument tops out near 120KB) and fail the dispatch. Past the budget the
	// rest are counted in review_comments_omitted; the agent reads them on
	// the PR.
	maxReviewComments      = 100
	maxReviewCommentBody   = 2000
	maxReviewCommentsBytes = 48 << 10
)

// reviewStateCache remembers submitted reviews' states (lowercased, as the
// webhook spells them: "changes_requested", "commented", "approved").
type reviewStateCache struct {
	mu sync.Mutex
	m  map[int64]reviewStateEntry
}

type reviewStateEntry struct {
	state string
	at    time.Time
}

func (c *reviewStateCache) get(id int64, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || now.Sub(e.at) > reviewStateTTL {
		return "", false
	}
	return e.state, true
}

func (c *reviewStateCache) put(id int64, state string, now time.Time) {
	if id == 0 || state == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[int64]reviewStateEntry{}
	}
	if len(c.m) >= reviewStateMax {
		for k, e := range c.m {
			if now.Sub(e.at) > reviewStateTTL {
				delete(c.m, k)
			}
		}
		if len(c.m) >= reviewStateMax {
			c.m = map[int64]reviewStateEntry{} // all fresh: a burst this big is rare; start over
		}
	}
	c.m[id] = reviewStateEntry{state: strings.ToLower(state), at: now}
}

// reviewState resolves a submitted review's state: from the review's own event
// if this daemon saw it, else one REST read, cached so a review's N inline
// comments cost at most one lookup. "" when it can't be determined.
func (g *Integration) reviewState(ctx context.Context, instID int64, repo string, pr int, reviewID int64) string {
	if reviewID == 0 {
		return ""
	}
	if st, ok := g.reviewStates.get(reviewID, time.Now()); ok {
		return st
	}
	if g.rest == nil {
		return ""
	}
	owner, name := splitRepo(repo)
	st, err := g.rest.reviewState(ctx, instID, owner, name, pr, reviewID)
	if err != nil {
		log.Printf("github[%s]: %s#%d review %d state: %v", g.name, repo, pr, reviewID, err)
		return ""
	}
	g.reviewStates.put(reviewID, st, time.Now())
	return strings.ToLower(st)
}

// changesRequestedKeep is changes_requested's per-variant keep-condition for a
// review on pr by reviewer — shared by the review event itself and by the fold
// check on its inline comments, so a comment is folded only into a review the
// changes_requested trigger actually takes.
func (g *Integration) changesRequestedKeep(repo string, pr *prPayload, reviewer string, reviewerIsBot bool) func(config.Action) bool {
	return func(act config.Action) bool {
		facts := prFilterFacts(pr.Head.Ref, pr.Base.Ref, pr.Title, pr.User.Login,
			prLabelNames(pr), pr.Draft)
		facts["reviewer"] = reviewer
		facts["author_is_bot"] = reviewerIsBot
		return g.filterPasses(act, "changes_requested", repo, facts, lowerChangesRequested(act))
	}
}

// foldedIntoReview reports whether an inline review comment belongs to a
// review that requested changes AND that a changes_requested trigger takes —
// in which case that review's run addresses it, and it emits no new_comment.
// Unknown state (no REST client, a failed read) is NOT folded: a duplicate
// fixer is recoverable, a dropped comment is not.
func (g *Integration) foldedIntoReview(ctx context.Context, repo string, p ghPayload) bool {
	c, pr := p.Comment, p.PullRequest
	if c == nil || pr == nil || c.PullRequestReviewID == 0 {
		return false
	}
	// The review's author is the comment's author.
	keep := g.changesRequestedKeep(repo, pr, c.User.Login, isBotActor(c.User.Type, c.User.Login))
	if !g.wouldEmit(repo, "changes_requested", keep) {
		return false // nothing would address the review as a whole
	}
	return g.reviewState(ctx, p.Installation.ID, repo, pr.Number, c.PullRequestReviewID) == "changes_requested"
}

// attachReviewComments stamps a changes_requested run with the review's inline
// comments (review_comments), one REST read per review. A failed read leaves
// the run without them — it still names the review (review_id) and the agent
// can read the PR itself — rather than dropping the run.
func (g *Integration) attachReviewComments(ctx context.Context, trs []core.Trigger, p ghPayload, repo string) {
	if g.rest == nil || p.Review == nil || p.PullRequest == nil {
		return
	}
	owner, name := splitRepo(repo)
	cs, err := g.rest.reviewComments(ctx, p.Installation.ID, owner, name, p.PullRequest.Number, p.Review.ID)
	if err != nil {
		log.Printf("github[%s]: %s#%d review %d comments: %v — dispatching without them",
			g.name, repo, p.PullRequest.Number, p.Review.ID, err)
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
		body := clipBody(c.Body)
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

// clipBody caps a comment body at maxReviewCommentBody bytes, on a rune
// boundary.
func clipBody(s string) string {
	if len(s) <= maxReviewCommentBody {
		return s
	}
	cut := maxReviewCommentBody
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
