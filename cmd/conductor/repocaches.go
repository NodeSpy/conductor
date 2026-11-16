package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/gitwt"
	"github.com/NodeSpy/conductor/internal/repocache"
)

// repoCacheManager builds the repository cache manager the config asks for
// (nil: caches off). gitProv reads the default branch (nil: no repo file,
// no warming).
func repoCacheManager(cfg *config.Config, gitProv *gitwt.Provisioner, logf func(string, ...any)) *repocache.Manager {
	if !cfg.CachesOn() {
		return nil
	}
	var c *config.SandboxCaches
	if cfg.Sandbox != nil {
		c = cfg.Sandbox.Caches
	}
	m := &repocache.Manager{
		Store:       &repocache.Store{Dir: cfg.CachesDir()},
		Autodetect:  c.AutodetectOn(),
		RepoFileOn:  c.RepoFileOn(),
		Protected:   agentsandbox.ToolStatePaths(),
		MaxSize:     c.MaxSizeBytes(),
		WarmOnMiss:  c.WarmOnMissOn(),
		WarmEvery:   c.WarmEveryDur(),
		WarmTimeout: c.WarmTimeoutDur(),
		HomeRoots:   cacheHomeRoots(cfg),
		Logf:        logf,
	}
	if c != nil {
		m.Repos, m.Defaults = c.Repos, c.Defaults
		m.Recipes = repocache.Recipes(c.Recipes)
	} else {
		m.Recipes = repocache.Recipes(nil)
	}
	if gitProv != nil {
		m.DefaultFile = gitProv.DefaultBranchFile
		m.Checkout = gitProv.DefaultBranchCheckout
		m.Warm = controller.RunCacheWarm
	}
	return m
}

// cacheHomeRoots are the directories whose children are agent homes: the
// per-dispatch homes and the identity homes.
func cacheHomeRoots(cfg *config.Config) []string {
	root := cfg.SandboxStateDir()
	return []string{filepath.Join(root, ".dispatch-homes"), root}
}

// wireRepoCaches turns repository caches on for the agent sandbox's
// launches: the resolver restores them, and the manager evicts and (when
// configured) re-warms on a schedule until ctx ends.
func wireRepoCaches(ctx context.Context, cfg *config.Config, gitProv *gitwt.Provisioner, logf func(string, ...any)) {
	m := repoCacheManager(cfg, gitProv, logf)
	if sandboxResolver.Load() == nil || m == nil {
		repoCaches.Store(nil)
		return
	}
	repoCaches.Store(&repoCacheWiring{m: m, ctx: ctx, logf: logf})
	logf("repo caches: on (store %s, cap %s, autodetect %v, repo file %v)", m.Store.Dir, capString(m.MaxSize), m.Autodetect, m.RepoFileOn)
	go m.Run(ctx)
}

// repoCaches is the daemon's cache manager (nil: off), read by the
// resolver's Caches hook at each launch.
var repoCaches atomic.Pointer[repoCacheWiring]

type repoCacheWiring struct {
	m    *repocache.Manager
	ctx  context.Context
	logf func(string, ...any)
}

// restoreRepoCaches is the resolver's Caches hook.
func restoreRepoCaches(req repocache.Request) repocache.Grant {
	w := repoCaches.Load()
	if w == nil {
		return repocache.Grant{}
	}
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
	defer cancel()
	g, err := w.m.Restore(ctx, req)
	if err != nil {
		w.logf("repo cache %s: not restored for %s: %v", req.Repo, req.Run, err)
	}
	return g
}

func capString(n int64) string {
	if n <= 0 {
		return "none"
	}
	return repocache.HumanSize(n)
}
