package github

import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// sweepLoop runs the optional catch-up sweep on an interval. It's off unless
// configured; it exists to reconcile anything missed while the daemon was
// offline (webhooks aren't redelivered reliably).
func (g *Integration) sweepLoop(ctx context.Context, emit core.EmitFunc) {
	iv := g.cfg.Sweep.Interval.D()
	if iv <= 0 {
		iv = time.Hour
	}
	// Sweep once on start to reconcile anything missed while offline, then every
	// iv thereafter (start at T0 → next at T0+iv → T0+2·iv → …).
	if ctx.Err() == nil {
		if err := g.sweep(ctx, emit); err != nil {
			log.Printf("github[%s]: sweep error: %v", g.name, err)
		}
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := g.sweep(ctx, emit); err != nil {
				log.Printf("github[%s]: sweep error: %v", g.name, err)
			}
		}
	}
}

// sweep reconciles the configured repos. Entries may be concrete (`owner/name`)
// or an owner glob (`owner/*`, `owner/svc-*`), which is expanded to the repos
// the App installation can access. It emits `review_requested` for PRs where
// your review is pending (recovering missed review-request webhooks) and
// re-checks merge state (conflict/behind) for PRs you authored.
func (g *Integration) sweep(ctx context.Context, emit core.EmitFunc) error {
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
					g.sweepRepo(ctx, emit, instID, r.Owner.Login, r.Name, r.FullName)
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
		g.sweepRepo(ctx, emit, instID, owner, name, entry)
	}
	return nil
}

// sweepRepo reconciles one repo's open PRs: review_requested for PRs where your
// review is pending (recovers missed review-request webhooks), and conflict/behind
// for PRs you authored.
func (g *Integration) sweepRepo(ctx context.Context, emit core.EmitFunc, instID int64, owner, name, repo string) {
	prs, err := g.rest.listOpenPRs(ctx, instID, owner, name)
	if err != nil {
		log.Printf("github[%s]: sweep %s: %v", g.name, repo, err)
		return
	}
	for _, pr := range prs {
		// review_requested applies to *others'* PRs where your review is pending;
		// the list payload already carries requested reviewers (no extra fetch).
		for _, tr := range g.sweepReviewRequested(repo, pr) {
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
		case "behind":
			trs = g.single(repo, "pr_behind", t, "sweep: behind base",
				"behind:"+info.Base.Ref+"/"+info.Head.SHA, nil)
		}
		for _, tr := range trs {
			emit(ctx, tr)
		}
	}
}

// sweepReviewRequested emits a review_requested trigger when your review is a
// pending requested reviewer on pr. The dedup signature matches the webhook path
// ("reviewreq@<head>"), so a request already handled live isn't re-fired.
func (g *Integration) sweepReviewRequested(repo string, pr prListItem) []core.Trigger {
	r, ok := g.resolve(repo)
	if !ok {
		return nil
	}
	rev := actorsOr(r.Actions["review_requested"].Reviewer, r.Reviewer)
	if !g.prReviewerMatches(rev, pr) {
		return nil
	}
	t := g.target(repo, pr.Number, pr.Head.SHA, pr.Base.Ref, pr.HTMLURL)
	return g.single(repo, "review_requested", t,
		fmt.Sprintf("sweep: review requested on %s#%d", repo, pr.Number),
		"reviewreq@"+pr.Head.SHA, nil)
}

// prReviewerMatches reports whether the configured reviewer (defaulting to the
// `me` identity when unset) is among the PR's pending requested reviewers/teams.
func (g *Integration) prReviewerMatches(rev config.Actors, pr prListItem) bool {
	byDefault := len(rev.Logins) == 0 && len(rev.Teams) == 0
	for _, rr := range pr.RequestedReviewers {
		if byDefault {
			if g.self[strings.ToLower(rr.Login)] {
				return true
			}
		} else if rev.HasLogin(rr.Login) {
			return true
		}
	}
	for _, tm := range pr.RequestedTeams {
		for _, want := range rev.Teams {
			if strings.EqualFold(want, tm.Slug) {
				return true
			}
		}
	}
	return false
}
