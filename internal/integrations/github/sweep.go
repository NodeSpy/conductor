package github

import (
	"context"
	"log"
	"path"
	"strings"
	"time"

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
// the App installation can access. For PRs you authored it re-checks merge
// state (conflict/behind) and emits triggers.
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

// sweepRepo checks one repo's open PRs (authored by you) for conflict/behind.
func (g *Integration) sweepRepo(ctx context.Context, emit core.EmitFunc, instID int64, owner, name, repo string) {
	prs, err := g.rest.listOpenPRs(ctx, instID, owner, name)
	if err != nil {
		log.Printf("github[%s]: sweep %s: %v", g.name, repo, err)
		return
	}
	for _, pr := range prs {
		if !g.self[strings.ToLower(pr.User.Login)] {
			continue // autopilot is for PRs you authored
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
