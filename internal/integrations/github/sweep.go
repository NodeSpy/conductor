package github

import (
	"context"
	"log"
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

// sweep lists open PRs in the configured repos, and for those you authored,
// re-checks merge state (conflict/behind), emitting triggers as needed.
func (g *Integration) sweep(ctx context.Context, emit core.EmitFunc) error {
	for _, repo := range g.cfg.Sweep.Repos {
		owner, name := splitRepo(repo)
		if owner == "" || name == "" {
			continue
		}
		instID, err := g.app.repoInstallationID(ctx, owner, name)
		if err != nil {
			log.Printf("github[%s]: sweep %s: %v", g.name, repo, err)
			continue
		}
		prs, err := g.rest.listOpenPRs(ctx, instID, owner, name)
		if err != nil {
			log.Printf("github[%s]: sweep %s: %v", g.name, repo, err)
			continue
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
	return nil
}
