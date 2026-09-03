package handoff

import (
	"context"
	"errors"
	"fmt"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// ErrNotWired is returned by a chat (Slack/Discord) hand-off channel's Present
// until that channel's implementation lands: the `handoffs:` config schema for
// slack/discord is stable now (decodes, validates, resolves), but only the web
// channel actually presents a draft in this build.
var ErrNotWired = errors.New("handoff channel not wired in this build (slack/discord land in a later increment)")

// WebEntry pairs a configured web hand-off channel with the inbound listen
// address it should be mounted on, so the caller (main.go) can register one
// inbound HTTP handler per web entry without re-deriving the config default.
type WebEntry struct {
	Name   string // the handoffs: entry name
	Listen string // resolved listen address (":8099" default applied)
	Chan   *WebChannel
}

// Registry holds the configured hand-off channels (from `handoffs:`) and
// resolves which one an interactive review step presents its draft on. Mirrors
// controller.Registry's shape and resolution order.
type Registry struct {
	channels    map[string]Channel // user-configured, by name
	defaultName string             // the entry flagged default:true, or ""
	webEntries  []WebEntry
}

// NewRegistry builds the hand-off channel set from config. Config is assumed
// already validated (see config.Config.Validate): each entry sets exactly one
// channel sub-block, and at most one is flagged default:true. log may be nil.
func NewRegistry(cfgs map[string]config.HandoffConfig, defaultName string, log func(string, ...any)) *Registry {
	if log == nil {
		log = func(string, ...any) {}
	}
	r := &Registry{
		channels:    make(map[string]Channel, len(cfgs)),
		defaultName: defaultName,
	}
	for name, hc := range cfgs {
		ch := buildChannel(name, hc, log)
		r.channels[name] = ch
		if hc.Web != nil {
			if w, ok := ch.(*WebChannel); ok {
				r.webEntries = append(r.webEntries, WebEntry{Name: name, Listen: webListen(hc.Web), Chan: w})
			}
		}
	}
	return r
}

// webListen returns the entry's configured listen address, defaulting to
// :8099 (mirrors the default previously applied in cmd/paseo-conductor/main.go).
func webListen(w *config.HandoffWeb) string {
	if w.Listen != "" {
		return w.Listen
	}
	return ":8099"
}

// buildChannel constructs one hand-off channel from its config: a Web entry
// builds a real *WebChannel wired to its tunnel (see NewTunnel); a Slack/Discord
// entry (schema only in this build) builds a stub whose Present always fails
// with ErrNotWired, so the config resolves and the failure is loud and specific
// rather than a nil dereference.
func buildChannel(name string, hc config.HandoffConfig, log func(string, ...any)) Channel {
	switch {
	case hc.Web != nil:
		w := NewWebChannel(hc.Web.BaseURL, hc.Web.TTL.D(), log)
		t, err := NewTunnel(hc.Web.Tunnel, hc.Web.BaseURL, log)
		if err != nil {
			// config.Validate already guards the provider/mode/ssh_host/url_pattern
			// shape, so this only fires when a caller builds a Registry from
			// unvalidated config; fall back to base_url rather than leaving the
			// channel unusable.
			log("handoff %s: tunnel config invalid (%v); falling back to base_url", name, err)
		} else {
			w.SetTunnel(t, webListen(hc.Web))
		}
		return w
	default:
		// hc.Slack != nil or hc.Discord != nil (config.Validate already rejected
		// anything else, i.e. zero or more than one channel sub-block set).
		return notWiredChannel{name: name}
	}
}

// WebEntries returns the configured web hand-off channels paired with their
// resolved listen address, so the caller mounts one inbound HTTP handler per
// entry (see cmd/paseo-conductor/main.go).
func (r *Registry) WebEntries() []WebEntry { return r.webEntries }

// Resolve returns the hand-off channel a step should present its draft on,
// applying the resolution order: an explicit per-step `handoff:` name → the
// entry flagged default:true → the sole configured entry → nil (no error) when
// none of those apply, meaning the review hand-off keeps paseo-native behavior.
func (r *Registry) Resolve(name string) (Channel, error) {
	if name != "" {
		ch, ok := r.channels[name]
		if !ok {
			return nil, fmt.Errorf("unknown handoff %q", name)
		}
		return ch, nil
	}
	if r.defaultName != "" {
		ch, ok := r.channels[r.defaultName]
		if !ok {
			return nil, fmt.Errorf("default handoff %q not found", r.defaultName)
		}
		return ch, nil
	}
	if len(r.channels) == 1 {
		for _, ch := range r.channels {
			return ch, nil
		}
	}
	return nil, nil
}

// notWiredChannel is a placeholder for a configured Slack/Discord hand-off whose
// implementation hasn't landed yet. It satisfies Channel so the registry and its
// resolution order are fully exercisable today; only Present is refused.
type notWiredChannel struct{ name string }

func (c notWiredChannel) Present(context.Context, Draft) (Presentation, error) {
	return nil, fmt.Errorf("handoff %q: %w", c.name, ErrNotWired)
}
