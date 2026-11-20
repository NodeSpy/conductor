package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitwt"
	"github.com/NodeSpy/conductor/internal/keycache"
)

// stepCacheManager builds the step cache manager the config asks for (nil:
// caches off).
func stepCacheManager(cfg *config.Config, logf func(string, ...any)) *keycache.Manager {
	if !cfg.CachesOn() {
		return nil
	}
	c := cfg.Sandbox.Caches
	return &keycache.Manager{
		Store:     &keycache.Store{Dir: cfg.CachesDir()},
		Protected: agentsandbox.ToolStatePaths(),
		MaxSize:   c.MaxSizeBytes(),
		MinSize:   c.LearnMinSize(),
		MinRuns:   c.LearnMinRuns(),
		Logf:      logf,
	}
}

// wireStepCaches turns step caches on for the agent sandbox's launches: the
// resolver restores them, a dispatch's step end publishes them, and the
// manager evicts until ctx ends.
func wireStepCaches(ctx context.Context, cfg *config.Config, gitProv *gitwt.Provisioner, logf func(string, ...any)) (stop func()) {
	m := stepCacheManager(cfg, logf)
	if sandboxResolver.Load() == nil || m == nil {
		stepCaches.Store(nil)
		dispatch.CacheFinisher = nil
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := m.Start(ctx)
	stepCaches.Store(&stepCacheWiring{m: m, ctx: ctx, logf: logf})
	dispatch.CacheFinisher = finishStepCache
	logf("step caches: on (store %s, cap %s, learn %s in %d run(s))", m.Store.Dir, capString(m.MaxSize), keycache.HumanSize(m.MinSize), m.MinRuns)
	return func() {
		stepCaches.Store(nil)
		dispatch.CacheFinisher = nil
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			logf("step caches: still stopping 30s after shutdown began")
		}
	}
}

// stepCaches is the daemon's cache manager (nil: off), read by the
// resolver's Caches hook at each launch.
var stepCaches atomic.Pointer[stepCacheWiring]

type stepCacheWiring struct {
	m    *keycache.Manager
	ctx  context.Context
	logf func(string, ...any)
}

// restoreStepCache is the resolver's Caches hook.
func restoreStepCache(req keycache.Request) keycache.Grant {
	w := stepCaches.Load()
	if w == nil {
		return keycache.Grant{}
	}
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
	defer cancel()
	g, err := w.m.Restore(ctx, req)
	if err != nil {
		w.logf("cache %s: not restored for %s: %v", req.Key, req.Run, err)
	}
	return g
}

// finishStepCache is dispatch.CacheFinisher: a dispatch's step has ended.
func finishStepCache(ctx context.Context, dispatchID string, ok bool) {
	w := stepCaches.Load()
	if w == nil {
		return
	}
	if _, err := w.m.Finish(context.WithoutCancel(ctx), dispatchID, ok); err != nil {
		w.logf("cache: %s: not published: %v", dispatchID, err)
	}
}

func capString(n int64) string {
	if n <= 0 {
		return "none"
	}
	return keycache.HumanSize(n)
}
