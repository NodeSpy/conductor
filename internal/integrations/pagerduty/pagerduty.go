// Package pagerduty turns PagerDuty incidents into triggers: a page (incident
// triggered/escalated/…) dispatches an agent to start triage against the affected
// repo, or runs a command. It consumes PagerDuty V3 webhook subscriptions over a
// direct listener or a smee channel, and verifies the X-PagerDuty-Signature HMAC.
// Like the sentry integration it knows the payload shape — no field mapping.
package pagerduty

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/inbound"
)

func init() { core.Register("pagerduty", newIntegration) }

const maxBody = 4 << 20

// Config is one pagerduty instance.
type Config struct {
	Listen        string `yaml:"listen"`         // direct HTTP listener addr
	SmeeURL       string `yaml:"smee_url"`       // smee.io channel (no public ingress)
	Path          string `yaml:"path"`           // listener path (default /pagerduty)
	SigningSecret string `yaml:"signing_secret"` // webhook subscription signing secret (usually ${ENV})
	Rules         []Rule `yaml:"rules"`
}

// Rule routes a matched incident to a repo + action(s). First matching rule wins.
type Rule struct {
	Match   Match            `yaml:"match"`
	Repo    string           `yaml:"repo"` // github repo to triage/check out; "" → scratch
	Actions config.ActionSet `yaml:"actions"`
}

// Match filters an incident (empty list = match-all, case-insensitive).
type Match struct {
	EventTypes []string `yaml:"event_types"` // e.g. incident.triggered, incident.escalated
	Services   []string `yaml:"services"`    // service summary or id
	Urgencies  []string `yaml:"urgencies"`   // high | low
	Priorities []string `yaml:"priorities"`  // P1, P2, …
}

// Integration implements core.Integration for one pagerduty instance.
type Integration struct {
	name string
	cfg  Config
	seen *inbound.DeliveryDedup
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("pagerduty[%s]: decode config: %w", name, err)
	}
	if cfg.Path == "" {
		cfg.Path = "/pagerduty"
	}
	return &Integration{name: name, cfg: cfg, seen: inbound.NewDeliveryDedup(2048)}, nil
}

func (g *Integration) Name() string { return g.name }

func (g *Integration) Validate() error {
	if g.cfg.Listen == "" && g.cfg.SmeeURL == "" {
		return fmt.Errorf("pagerduty[%s]: set `listen` and/or `smee_url`", g.name)
	}
	if len(g.cfg.Rules) == 0 {
		return fmt.Errorf("pagerduty[%s]: no rules", g.name)
	}
	for i, r := range g.cfg.Rules {
		if len(r.Actions) == 0 {
			return fmt.Errorf("pagerduty[%s]: rules[%d]: no actions", g.name, i)
		}
		for _, a := range r.Actions {
			if a.Type == "" && a.FlowRef == "" { // FlowRef: lowered connectors-model action
				return fmt.Errorf("pagerduty[%s]: rules[%d]: action.type is required", g.name, i)
			}
		}
	}
	return nil
}

// Actions enumerates every rule's actions with their location, for the CLI's
// cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	var refs []config.ActionRef
	for i, r := range g.cfg.Rules {
		refs = append(refs, r.Actions.Refs(fmt.Sprintf("pagerduty[%s] rules[%d]", g.name, i))...)
	}
	return refs
}

func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	if g.cfg.Listen != "" {
		inbound.Register(ctx, g.cfg.Listen, g.cfg.Path, g.handler(ctx, emit), log.Printf)
		log.Printf("pagerduty[%s]: listener on %s%s", g.name, g.cfg.Listen, g.cfg.Path)
	}
	if g.cfg.SmeeURL != "" {
		go func() {
			_ = inbound.Smee(ctx, g.cfg.SmeeURL, log.Printf, func(f inbound.Frame) {
				g.deliver(ctx, emit, f.Header("X-PagerDuty-Signature"), f.Body)
			})
		}()
		log.Printf("pagerduty[%s]: via smee", g.name)
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
		if !g.verify(r.Header.Get("X-PagerDuty-Signature"), body) {
			log.Printf("pagerduty[%s]: signature mismatch (dropped)", g.name)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		g.deliver(ctx, emit, "", body)
		w.WriteHeader(http.StatusAccepted)
	}
}

// deliver parses a webhook, matches it to a rule, and emits a trigger per enabled
// action variant. sig is only set over smee (verified inline).
func (g *Integration) deliver(ctx context.Context, emit core.EmitFunc, smeeSig string, body []byte) {
	if smeeSig != "" && !g.verify(smeeSig, body) {
		return
	}
	f := parse(body)
	if f.ID == "" && f.Title == "" {
		return // nothing actionable
	}
	if !g.seen.Add(f.ID + "\x00" + f.EventType) {
		return // duplicate delivery
	}
	rule, ok := g.match(f)
	if !ok {
		return
	}
	dedup := f.ID + ":" + f.EventType
	title := fmt.Sprintf("pagerduty %s: %s", strings.TrimPrefix(f.EventType, "incident."), f.Title)

	pctx := map[string]any{
		"event_type": f.EventType, "status": f.Status, "title": f.Title,
		"urgency": f.Urgency, "priority": f.Priority, "service": f.Service,
		"service_id": f.ServiceID, "number": f.Number, "id": f.ID, "url": f.URL,
	}
	for _, act := range rule.Actions {
		if !act.IsEnabled() {
			continue
		}
		// The checkout repo: the rule's, or a per-variant override (lowered
		// connectors-model triggers each carry their own repo:).
		repo := rule.Repo
		if act.TargetRepo != "" {
			repo = act.TargetRepo
		}
		var target core.Target
		synthetic := repo == ""
		if synthetic {
			target = inbound.SyntheticTarget("pagerduty:"+firstNonEmpty(f.Service, g.name), dedup)
			target.HTMLURL = f.URL
		} else {
			owner, name, _ := strings.Cut(repo, "/")
			target = core.Target{Repo: repo, Owner: owner, Name: name,
				Number: inbound.SyntheticTarget("", dedup).Number, HTMLURL: f.URL}
		}
		if synthetic {
			act = inbound.ForceNoCheckout(act)
		}
		emit(ctx, core.Trigger{
			Source:   "pagerduty",
			Instance: g.name,
			Kind:     "pagerduty_incident",
			Variant:  act.Name,
			Target:   target,
			Title:    title,
			Dedup:    dedup,
			Context:  map[string]any{"pagerduty": pctx, "url": f.URL},
			Action:   act,
		})
	}
}

func (g *Integration) match(f facts) (Rule, bool) {
	for _, r := range g.cfg.Rules {
		if containsFold(r.Match.EventTypes, f.EventType) &&
			containsFold(r.Match.Urgencies, f.Urgency) &&
			containsFold(r.Match.Priorities, f.Priority) &&
			serviceMatch(r.Match.Services, f) {
			return r, true
		}
	}
	return Rule{}, false
}

// verify checks the X-PagerDuty-Signature header, which is a comma-separated list
// of `v1=<hex hmac-sha256>` values (more than one during signing-secret rotation).
// Any match passes. An unset secret means no verification.
func (g *Integration) verify(header string, body []byte) bool {
	if g.cfg.SigningSecret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(g.cfg.SigningSecret))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "v1=")
		if got, err := hex.DecodeString(part); err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// serviceMatch reports whether the incident's service matches the filter by summary
// or id (empty filter = any).
func serviceMatch(filter []string, f facts) bool {
	if len(filter) == 0 {
		return true
	}
	for _, s := range filter {
		if strings.EqualFold(s, f.Service) || strings.EqualFold(s, f.ServiceID) {
			return true
		}
	}
	return false
}

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
