package github

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"path"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// sweepLoop runs the optional catch-up sweep on an ADAPTIVE cadence. It's off
// unless configured; it exists to reconcile anything missed while the daemon was
// offline or disconnected (webhooks aren't redelivered reliably).
//
// The cadence starts tight (MinInterval) after startup and backs off ×2 toward the
// ceiling (Interval) while nothing disrupts us — so a quiet, connected daemon
// settles at Interval and doesn't sweep for nothing. A signal on `renew` (a smee
// reconnect — a likely dropped-webhook window) resets it to the tight floor and
// sweeps promptly, catching the gap in a minute or two instead of up to a full
// Interval. `renew` may be nil (no smee transport); a nil channel simply never
// fires.
func (g *Integration) sweepLoop(ctx context.Context, emit core.EmitFunc, renew <-chan struct{}) {
	min, max := sweepBounds(g.cfg.Sweep)
	runSweep := func() {
		if err := g.sweep(ctx, emit); err != nil {
			log.Printf("github[%s]: sweep error: %v", g.name, err)
		}
	}
	if ctx.Err() != nil {
		return
	}
	runSweep() // once on start — reconcile anything missed while offline
	cur := min
	t := time.NewTimer(cur)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew:
			cur = min
			log.Printf("github[%s]: sweep cadence reset to %s (renew: reconnect or manual sweep)", g.name, cur)
			runSweep()
			resetTimer(t, cur)
		case <-t.C:
			runSweep()
			cur = backoffInterval(cur, max)
			resetTimer(t, cur)
		}
	}
}

// sweepBounds resolves the tight floor and the ceiling from config (defaults: 10m
// floor, 6h ceiling; the floor is clamped to never exceed the ceiling). Note the
// sweep runs immediately on startup and on a reconnect renewal regardless of the
// floor — the floor only sets the follow-up rhythm.
func sweepBounds(s SweepConfig) (min, max time.Duration) {
	max = s.Interval.D()
	if max <= 0 {
		max = 6 * time.Hour
	}
	min = s.MinInterval.D()
	if min <= 0 {
		min = 10 * time.Minute
	}
	if min > max {
		min = max
	}
	return min, max
}

// backoffInterval doubles cur, capped at max.
func backoffInterval(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		cur = max
	}
	return cur
}

// resetTimer safely rearms a timer to d (draining a pending fire if needed).
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// sweep reconciles the configured repos. Entries may be concrete (`owner/name`)
// or an owner glob (`owner/*`, `owner/svc-*`), which is expanded to the repos
// the App installation can access. It emits `review_requested` for PRs where
// your review is pending, and for PRs you authored re-checks merge state
// (conflict/behind) and outstanding review-comment threads (changes_requested) —
// recovering feedback that no live webhook picked up.
func (g *Integration) sweep(ctx context.Context, emit core.EmitFunc) error {
	st := &sweepStats{}
	log.Printf("github[%s]: sweep starting (%d repo entr%s)", g.name, len(g.cfg.Sweep.Repos), plural(len(g.cfg.Sweep.Repos)))
	for _, entry := range g.cfg.Sweep.Repos {
		owner, _ := splitRepo(entry)
		if owner == "" {
			continue
		}
		if strings.Contains(entry, "*") {
			instID, err := g.app.accountInstallationID(ctx, owner)
			if err != nil {
				log.Printf("github[%s]: sweep %s: %v", g.name, entry, err)
				continue
			}
			repos, err := g.rest.listInstallationRepos(ctx, instID)
			if err != nil {
				log.Printf("github[%s]: sweep %s: %v", g.name, entry, err)
				continue
			}
			for _, r := range repos {
				if ok, _ := path.Match(entry, r.FullName); ok {
					g.sweepRepo(ctx, emit, instID, r.Owner.Login, r.Name, r.FullName, st)
				}
			}
			continue
		}
		_, name := splitRepo(entry)
		if name == "" {
			continue
		}
		instID, err := g.app.repoInstallationID(ctx, owner, name)
		if err != nil {
			log.Printf("github[%s]: sweep %s: %v", g.name, entry, err)
			continue
		}
		g.sweepRepo(ctx, emit, instID, owner, name, entry, st)
	}
	log.Printf("github[%s]: sweep done — repos=%d prs=%d review_requested=%d (skipped draft=%d, excluded=%d) merge_conflict=%d pr_behind=%d changes_requested=%d new_comment=%d",
		g.name, st.repos, st.prs, st.review, st.reviewDraft, st.reviewExcluded, st.conflict, st.behind, st.comments, st.newComments)
	return nil
}

// sweepStats is a per-run tally so a sweep is never a black box: it says what it
// scanned and, crucially, WHY review candidates were skipped (draft/excluded).
type sweepStats struct {
	repos, prs                          int
	review, reviewDraft, reviewExcluded int
	conflict, behind, comments          int
	newComments                         int
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// sweepRepo reconciles one repo's open PRs: review_requested for PRs where your
// review is pending (recovers missed review-request webhooks), and conflict/behind
// for PRs you authored.
func (g *Integration) sweepRepo(ctx context.Context, emit core.EmitFunc, instID int64, owner, name, repo string, st *sweepStats) {
	prs, err := g.rest.listOpenPRs(ctx, instID, owner, name)
	if err != nil {
		log.Printf("github[%s]: sweep %s: %v", g.name, repo, err)
		return
	}
	st.repos++
	for _, pr := range prs {
		st.prs++
		// review_requested applies to *others'* PRs where your review is pending;
		// the list payload already carries requested reviewers (no extra fetch).
		for _, tr := range g.sweepReviewRequested(repo, pr, st) {
			tr.CatchUp = true
			emit(ctx, tr)
		}

		if !g.self[strings.ToLower(pr.User.Login)] {
			continue // conflict/behind autopilot is for PRs you authored
		}
		info, err := g.rest.pull(ctx, instID, owner, name, pr.Number)
		if err != nil {
			continue
		}
		t := g.target(repo, pr.Number, info.Head.SHA, info.Base.Ref, info.HTMLURL)
		var trs []core.Trigger
		switch info.MergeableState {
		case "dirty":
			trs = g.single(repo, "merge_conflict", t, "sweep: merge conflict",
				"conflict:"+info.Base.Ref+"/"+info.Head.SHA, nil)
			st.conflict += len(trs)
		case "behind":
			trs = g.single(repo, "pr_behind", t, "sweep: behind base",
				"behind:"+info.Base.Ref+"/"+info.Head.SHA, nil)
			st.behind += len(trs)
		}
		ct := g.sweepUnresolvedComments(ctx, instID, owner, name, repo, t)
		st.comments += len(ct)
		trs = append(trs, ct...)
		nc := g.sweepMissedComments(ctx, instID, owner, name, repo, t, info.Head.Ref)
		st.newComments += len(nc)
		trs = append(trs, nc...)
		for _, tr := range trs {
			tr.CatchUp = true
			emit(ctx, tr)
		}
	}
}

// sweepUnresolvedComments reconciles outstanding review-comment threads on your
// PR — recovering feedback no live webhook picked up — by emitting changes_requested
// for the fixer to address. The dedup signature includes the set of unresolved
// thread ids, so it re-fires when new threads appear and stops once they're
// resolved (acted per state). Only runs when changes_requested is configured, to
// avoid the extra GraphQL call otherwise.
func (g *Integration) sweepUnresolvedComments(ctx context.Context, instID int64, owner, name, repo string, t core.Target) []core.Trigger {
	act, ok := g.actionFor(repo, "changes_requested")
	if !ok || !act.IsEnabled() {
		return nil
	}
	ids, err := g.rest.unresolvedThreadIDs(ctx, instID, owner, name, t.Number)
	if err != nil || len(ids) == 0 {
		return nil
	}
	sig := "threads:" + t.HeadSHA + ":" + threadSig(ids)
	return g.single(repo, "changes_requested", t,
		fmt.Sprintf("sweep: %d unresolved comment thread(s) on %s#%d", len(ids), repo, t.Number), sig, nil)
}

// sweepMissedComments recovers PR comments (issue + review) whose live webhook the
// daemon missed while offline. It re-lists recent comments and emits new_comment for
// each non-self one; the engine's per-PR comment high-water mark (advanced on a
// successful new_comment dispatch) drops any already handled, so only genuinely-newer
// comments dispatch. Only runs when new_comment is configured (avoids the extra
// fetch), and only on your own PRs (new_comment autopilot pushes fixes to PRs you
// authored — matching the webhook gate in commentTriggers).
func (g *Integration) sweepMissedComments(ctx context.Context, instID int64, owner, name, repo string, t core.Target, headRef string) []core.Trigger {
	act, ok := g.actionFor(repo, "new_comment")
	if !ok || !act.IsEnabled() {
		return nil
	}
	comments, err := g.rest.recentComments(ctx, instID, owner, name, t.Number)
	if err != nil || len(comments) == 0 {
		return nil
	}
	var out []core.Trigger
	for _, c := range comments {
		author := strings.ToLower(c.User.Login)
		if g.self[author] {
			continue // our own comments never drive a fix
		}
		extra := map[string]any{"author": c.User.Login, "comment_body": c.Body, "head_ref": headRef, "comment_id": c.ID}
		trs := g.emit(repo, "new_comment", t,
			fmt.Sprintf("sweep: comment by %s on %s#%d", c.User.Login, repo, t.Number),
			fmt.Sprintf("comment:%d", c.ID), extra, func(act config.Action) bool {
				return commentAuthorAllowed(act, author)
			})
		out = append(out, trs...)
	}
	return out
}

// threadSig is a compact, stable signature for a set of unresolved thread ids.
func threadSig(ids []string) string {
	h := fnv.New64a()
	for _, id := range ids { // ids arrive sorted
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%d:%x", len(ids), h.Sum64())
}

// sweepReviewRequested emits a review_requested trigger when your review is a
// pending requested reviewer on pr. The dedup signature matches the webhook path
// ("reviewreq@<head>"), so a request already handled live isn't re-fired.
func (g *Integration) sweepReviewRequested(repo string, pr prListItem, st *sweepStats) []core.Trigger {
	labels := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, l.Name)
	}
	// Is your review pending on this PR for ANY configured review_requested variant?
	pending := false
	for _, act := range g.actionsFor(repo, "review_requested") {
		if act.IsEnabled() && g.prReviewerMatches(g.reviewerFor(repo, act), pr) {
			pending = true
			break
		}
	}
	if !pending {
		return nil // not a review pending on you — not a candidate
	}
	t := g.target(repo, pr.Number, pr.Head.SHA, pr.Base.Ref, pr.HTMLURL)
	trs := g.emit(repo, "review_requested", t,
		fmt.Sprintf("sweep: review requested on %s#%d", repo, pr.Number),
		"reviewreq@"+pr.Head.SHA, map[string]any{"labels": labels}, func(act config.Action) bool {
			return g.prReviewerMatches(g.reviewerFor(repo, act), pr) &&
				!draftGate(act, pr.Draft) && !act.Exclude.Matches(pr.Head.Ref, pr.Title, labels)
		})
	if len(trs) == 0 { // pending, but every matching variant is gated out
		if pr.Draft {
			st.reviewDraft++
			log.Printf("github[%s]: sweep %s#%d review pending but skipped (draft)", g.name, repo, pr.Number)
		} else {
			st.reviewExcluded++
			log.Printf("github[%s]: sweep %s#%d review pending but skipped (exclude)", g.name, repo, pr.Number)
		}
		return nil
	}
	st.review += len(trs)
	log.Printf("github[%s]: sweep %s#%d review pending -> emitting review_requested", g.name, repo, pr.Number)
	return trs
}

// prReviewerMatches reports whether the configured reviewer (defaulting to the
// `me` identity when unset) is among the PR's pending requested reviewers/teams.
func (g *Integration) prReviewerMatches(rev config.Actors, pr prListItem) bool {
	logins := make([]string, 0, len(pr.RequestedReviewers))
	for _, rr := range pr.RequestedReviewers {
		logins = append(logins, rr.Login)
	}
	slugs := make([]string, 0, len(pr.RequestedTeams))
	for _, tm := range pr.RequestedTeams {
		slugs = append(slugs, tm.Slug)
	}
	return g.reviewerInList(rev, logins, slugs)
}
