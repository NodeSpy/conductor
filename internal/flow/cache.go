package flow

import (
	"context"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/keycache"
	"github.com/NodeSpy/conductor/internal/memory"
)

type workflowCacheKey struct{}

// workflowCache is a workflow's `cache:` default and the pack instance
// that declared it.
type workflowCache struct {
	c    *config.StepCache
	pack string
}

// withWorkflowCache sets the cache default for a workflow's own agent steps
// (a step's own wins). A called workflow does not inherit its caller's: a
// key is about the steps that declare it.
func withWorkflowCache(ctx context.Context, c *config.StepCache, pack string) context.Context {
	return context.WithValue(ctx, workflowCacheKey{}, workflowCache{c: c, pack: pack})
}

// stepCacheKeys renders an agent dispatch's cache keys (keycache) over the
// step's facts: its own `cache:`, else its workflow's. None (nil) when
// caches are off, the step names no cache, the step is agent-authored (a
// cache key is a write grant into a shared store: the operator's to give),
// or the target was chosen by the event's sender. A key that does not
// render — a fact the step has no value for, or a value outside the key
// charset — means no cache for the step; a restore_from entry that does
// not render is dropped.
func (r *Runner) stepCacheKeys(ctx context.Context, t core.Trigger, step config.Step, facts map[string]any, id string) *dispatch.CacheKeys {
	if r.Cfg == nil || !r.Cfg.CachesOn() || agentAuthored(ctx) || !t.TargetTrusted {
		return nil
	}
	sc, ns := step.Cache, step.PackInstance
	if sc == nil {
		if wc, ok := ctx.Value(workflowCacheKey{}).(workflowCache); ok && wc.c != nil {
			sc, ns = wc.c, wc.pack
		}
	}
	if sc == nil {
		return nil
	}
	render := func(tmpl string) (keycache.Key, error) {
		k := keycache.Key{Namespace: ns, Template: config.CacheTemplate(tmpl)}
		v, err := memory.RenderScopeKey(k.Template, facts)
		if err != nil {
			return k, err
		}
		k.Value = v
		return k, k.Valid()
	}
	key, err := render(sc.Key)
	if err != nil {
		r.Log("%s step %s: no cache: %v", flowTag(t), id, err)
		return nil
	}
	out := &dispatch.CacheKeys{Key: key, Teach: sc.Teach}
	for _, tmpl := range sc.RestoreFrom {
		k, err := render(tmpl)
		if err != nil {
			r.Log("%s step %s: cache restore_from %q dropped: %v", flowTag(t), id, tmpl, err)
			continue
		}
		if k.ID() != key.ID() {
			out.RestoreFrom = append(out.RestoreFrom, k)
		}
	}
	return out
}
