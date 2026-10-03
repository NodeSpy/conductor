package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// listeners.go implements the `listeners` connection semantic
// (docs/design/plugin-contract.md §2.4, ~line 441): a plugin that declares
// it runs an inbound listener on the address in its config field `listen`.
// When the operator sets `expose: <conn>` in the config field named by the
// listener's `expose` field, the engine opens an exposure on that connector
// for the listen address at instance start and passes the public URL to the
// plugin in the config field named by `url_to`, on start_source.
//
// Two gates, at two different times:
//   - load time (checkListenerExposures, called from Build): the expose
//     target names a connector that exists and declares an exposes verb, and
//     `listen` is set whenever `expose` is — the same config-mistake class
//     web's own `expose:` already catches (checkExposures), just read
//     straight off the instance's YAML rather than through an Impl method,
//     because at this point the registry (and so exposureVerb's lookup) may
//     not have built the target yet.
//   - instance start (pluginSourceIntegration.openListeners, called from
//     Start before building the StartSourceRequest): the exposure is
//     actually opened. A transient failure (the tunnel endpoint unreachable)
//     retries with capped backoff rather than taking the daemon down or
//     giving up after one try; Start blocks until it succeeds or ctx is
//     cancelled (stop/reload), at which point the lease is released.

// stringAtPath reads a dotted path ("webhook.listen") out of a connection
// config, walking nested maps (core.LookupFact's rule: an exact flat key
// wins first). "" when absent or not a string.
func stringAtPath(cfg map[string]any, path string) string {
	if path == "" || cfg == nil {
		return ""
	}
	v, ok := core.LookupFact(cfg, path)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// setAtPath returns a COPY of cfg with the dotted path set to val,
// shallow-copying every map along the path. cfg itself, and any map it
// shares with other callers (another verb call's options, the next
// Start's config), is left untouched — only the walked spine is new.
func setAtPath(cfg map[string]any, path string, val any) map[string]any {
	return setAtParts(cfg, strings.Split(path, "."), val)
}

func setAtParts(cfg map[string]any, parts []string, val any) map[string]any {
	out := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	if len(parts) == 1 {
		out[parts[0]] = val
		return out
	}
	nested, _ := out[parts[0]].(map[string]any)
	out[parts[0]] = setAtParts(nested, parts[1:], val)
	return out
}

// checkListenerExposures validates every connector instance whose type
// declares `listeners` semantics, once all instances are built (so a
// forward reference to an exposure connector declared later in the file
// resolves). A listener whose `expose` field is unset is a plain local
// listener — never exposed, nothing to validate. One that's set must name a
// connector that exists and declares an exposes verb, and `listen` must be
// set too. Failing either disables the connector, same as an `expose:`
// config mistake does for web (checkExposures).
func (r *Registry) checkListenerExposures(cfg *config.Config, log func(string, ...any)) {
	for _, name := range r.order {
		in := r.byName[name]
		if in.Impl == nil || in.DisabledReason != "" || in.Decl == nil || in.Decl.Semantics == nil || len(in.Decl.Semantics.Listeners) == 0 {
			continue
		}
		ref, ok := cfg.ConnectorsMap[name]
		if !ok {
			continue // a reserved built-in instance (kv, sql, …): never configured, never declares listeners
		}
		var raw map[string]any
		if ref.Decode(&raw) != nil {
			continue // Build already failed or disabled this instance over a bad connection block
		}
		for _, l := range in.Decl.Semantics.Listeners {
			expose := stringAtPath(raw, l.Expose)
			if expose == "" {
				continue
			}
			if stringAtPath(raw, l.Listen) == "" {
				in.DisabledReason = fmt.Sprintf("listeners: %s is set with no %s", l.Expose, l.Listen)
				break
			}
			if _, _, err := exposureVerb(r.Get, expose); err != nil {
				in.DisabledReason = err.Error()
				break
			}
		}
		if in.DisabledReason != "" && log != nil {
			log("connector %q disabled: %v", name, in.DisabledReason)
		}
	}
}

// exposureRetryInitial and exposureRetryMax bound openExposure's backoff —
// vars so a test can shrink them rather than wait out a real tunnel's
// timeout.
var (
	exposureRetryInitial = 2 * time.Second
	exposureRetryMax     = 2 * time.Minute
)

// openListeners resolves this source's declared listeners against its
// config: a listener with no `expose` set is untouched (the plugin's own
// `listen` serves locally, same as today). One with `expose` set gets its
// exposure opened (retried with backoff — see openExposure) and its public
// URL written into the config at `url_to`.
//
// It never mutates p.config: the returned config is a copy along the walked
// paths only (setAtPath), so the instance's stored config is unaffected by
// what one Start call fills in — important because the SAME config map
// backs every verb Invoke on this instance too.
//
// release stops every exposure this call opened (always non-nil, even when
// nothing was opened); the caller must call it once the source stops
// (ctx cancelled) so the lease is released on stop or reload, never before.
func (p *pluginSourceIntegration) openListeners(ctx context.Context) (map[string]any, func(), error) {
	cfg := p.config
	var closers []func() error
	release := func() {
		for _, c := range closers {
			if err := c(); err != nil {
				p.log("plugin source %s: release exposure: %v", p.instance, err)
			}
		}
	}
	for _, l := range p.listeners {
		expose := stringAtPath(cfg, l.Expose)
		if expose == "" {
			continue
		}
		listen := stringAtPath(cfg, l.Listen)
		if listen == "" {
			release()
			return nil, func() {}, fmt.Errorf("connector %q: listeners: %s is set with no %s", p.instance, l.Expose, l.Listen)
		}
		url, closeFn, err := p.openExposure(ctx, expose, listen)
		if err != nil {
			release()
			return nil, func() {}, fmt.Errorf("connector %q: %w", p.instance, err)
		}
		closers = append(closers, closeFn)
		if l.URLTo != "" {
			cfg = setAtPath(cfg, l.URLTo, url)
		}
	}
	return cfg, release, nil
}

// openExposure opens one exposure through its connector's exposes verb
// (exposureTunnel — the same adapter web's expose: uses), retrying with
// capped exponential backoff on failure: a tunnel endpoint that's down at
// boot (a DNS hiccup, a rate limit) must not take the whole source down
// permanently, but it must not hot-loop the daemon either. It returns only
// on success or when ctx is cancelled (daemon stop/reload).
func (p *pluginSourceIntegration) openExposure(ctx context.Context, name, listen string) (string, func() error, error) {
	tun := exposureTunnel{lookup: p.lookup, name: name, from: p.instance}
	delay := exposureRetryInitial
	for attempt := 1; ; attempt++ {
		url, closeFn, err := tun.Open(ctx, listen)
		if err == nil {
			return url, closeFn, nil
		}
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		p.log("plugin source %s: open exposure via %q (attempt %d): %v — retrying in %s", p.instance, name, attempt, err, delay)
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > exposureRetryMax {
			delay = exposureRetryMax
		}
	}
}

// listenersOf is a nil-safe read of a TypeDecl's declared listeners.
func listenersOf(d *TypeDecl) []sdk.Listener {
	if d == nil || d.Semantics == nil {
		return nil
	}
	return d.Semantics.Listeners
}
