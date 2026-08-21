// Package sentry turns Sentry issue/error alerts into triggers: a production
// error, regression, or spike dispatches an agent that root-causes it against the
// affected repo. It consumes Sentry's Integration-Platform webhooks (resources
// `issue`, `error`, `event_alert`) over a direct listener or a smee channel, and
// verifies the `Sentry-Hook-Signature` HMAC. Unlike the generic webhook receiver
// it needs no field mapping — it knows Sentry's payload shape.
package sentry

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

func init() { core.Register("sentry", newIntegration) }

const maxBody = 8 << 20

// Config is one sentry instance.
type Config struct {
	Listen       string `yaml:"listen"`        // direct HTTP listener addr
	SmeeURL      string `yaml:"smee_url"`      // smee.io channel (no public ingress)
	Path         string `yaml:"path"`          // listener path (default /sentry)
	ClientSecret string `yaml:"client_secret"` // Sentry-Hook-Signature HMAC key (usually ${ENV})
	Rules        []Rule `yaml:"rules"`
}

// Rule routes a matched alert to a repo + action(s). First matching rule wins.
type Rule struct {
	Match   Match            `yaml:"match"`
	Repo    string           `yaml:"repo"` // github repo to investigate/check out; "" → scratch
	Actions config.ActionSet `yaml:"actions"`
}

// Match filters an alert (empty list = match-all, case-insensitive).
type Match struct {
	Projects     []string `yaml:"projects"`
	Levels       []string `yaml:"levels"`
	Environments []string `yaml:"environments"`
}

// Integration implements core.Integration for one sentry instance.
type Integration struct {
	name string
	cfg  Config
	seen *inbound.DeliveryDedup
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("sentry[%s]: decode config: %w", name, err)
	}
	if cfg.Path == "" {
		cfg.Path = "/sentry"
	}
	return &Integration{name: name, cfg: cfg, seen: inbound.NewDeliveryDedup(2048)}, nil
}

func (g *Integration) Name() string { return g.name }

func (g *Integration) Validate() error {
	if g.cfg.Listen == "" && g.cfg.SmeeURL == "" {
		return fmt.Errorf("sentry[%s]: set `listen` and/or `smee_url`", g.name)
	}
	if len(g.cfg.Rules) == 0 {
		return fmt.Errorf("sentry[%s]: no rules", g.name)
	}
	for i, r := range g.cfg.Rules {
		if len(r.Actions) == 0 {
			return fmt.Errorf("sentry[%s]: rules[%d]: no actions", g.name, i)
		}
		for _, a := range r.Actions {
			if a.Type == "" {
				return fmt.Errorf("sentry[%s]: rules[%d]: action.type is required", g.name, i)
			}
		}
	}
	return nil
}

func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	if g.cfg.Listen != "" {
		inbound.Register(ctx, g.cfg.Listen, g.cfg.Path, g.handler(ctx, emit), log.Printf)
		log.Printf("sentry[%s]: listener on %s%s", g.name, g.cfg.Listen, g.cfg.Path)
	}
	if g.cfg.SmeeURL != "" {
		go func() {
			_ = inbound.Smee(ctx, g.cfg.SmeeURL, log.Printf, func(f inbound.Frame) {
				g.deliver(ctx, emit, f.Header("Sentry-Hook-Resource"), f.Header("Sentry-Hook-Signature"), f.Body)
			})
		}()
		log.Printf("sentry[%s]: via smee", g.name)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (g *Integration) handler(ctx context.Context, emit core.EmitFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if g.cfg.ClientSecret != "" &&
			!inbound.VerifyHMAC(g.cfg.ClientSecret, body, r.Header.Get("Sentry-Hook-Signature"), "hex") {
			log.Printf("sentry[%s]: signature mismatch (dropped)", g.name)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		g.deliver(ctx, emit, r.Header.Get("Sentry-Hook-Resource"), "", body)
		w.WriteHeader(http.StatusAccepted)
	}
}

// deliver parses a Sentry webhook, matches it to a rule, and emits a trigger per
// enabled action variant. The extra sig arg (set only over smee) is verified inline.
func (g *Integration) deliver(ctx context.Context, emit core.EmitFunc, resource, smeeSig string, body []byte) {
	if smeeSig != "" && g.cfg.ClientSecret != "" &&
		!inbound.VerifyHMAC(g.cfg.ClientSecret, body, smeeSig, "hex") {
		return
	}
	f := parse(resource, body)
	if f.ShortID == "" && f.Title == "" {
		return // nothing actionable
	}
	if !g.seen.Add(f.ShortID + "\x00" + f.Action) {
		return // duplicate delivery
	}

	rule, ok := g.match(f)
	if !ok {
		return
	}
	dedup := f.ShortID
	if dedup == "" {
		dedup = f.Title
	}
	title := fmt.Sprintf("sentry %s: %s", firstNonEmpty(f.Level, "error"), f.Title)

	var target core.Target
	synthetic := rule.Repo == ""
	if synthetic {
		target = inbound.SyntheticTarget("sentry:"+firstNonEmpty(f.Project, g.name), dedup)
	} else {
		owner, name, _ := strings.Cut(rule.Repo, "/")
		target = core.Target{Repo: rule.Repo, Owner: owner, Name: name,
			Number: inbound.SyntheticTarget("", dedup).Number, HTMLURL: f.URL}
	}

	sctx := map[string]any{
		"resource": f.Resource, "action": f.Action, "title": f.Title, "level": f.Level,
		"environment": f.Environment, "culprit": f.Culprit, "short_id": f.ShortID,
		"project": f.Project, "url": f.URL,
	}
	for _, act := range rule.Actions {
		if !act.IsEnabled() {
			continue
		}
		if synthetic {
			act = inbound.ForceNoCheckout(act)
		}
		emit(ctx, core.Trigger{
			Source:   "sentry",
			Instance: g.name,
			Kind:     "sentry_alert",
			Variant:  act.Name,
			Target:   target,
			Title:    title,
			Dedup:    dedup,
			Context:  map[string]any{"sentry": sctx, "url": f.URL},
			Action:   act,
		})
	}
}

func (g *Integration) match(f facts) (Rule, bool) {
	for _, r := range g.cfg.Rules {
		if containsFold(r.Match.Projects, f.Project) &&
			containsFold(r.Match.Levels, f.Level) &&
			containsFold(r.Match.Environments, f.Environment) {
			return r, true
		}
	}
	return Rule{}, false
}

// containsFold reports whether want is in list case-insensitively; an empty list
// matches anything (an unset filter).
func containsFold(list []string, want string) bool {
	if len(list) == 0 {
		return true
	}
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
