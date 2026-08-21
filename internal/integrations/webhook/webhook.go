// Package webhook is the force-multiplier integration: any service that can POST
// JSON (CloudWatch/SNS, Statuspage, a vendor, a form) becomes a Trigger via a
// small field-mapping DSL, with no bespoke Go per source. A source names a path
// (or is routed off a smee channel by a `match` predicate), optionally verifies an
// HMAC signature, maps body fields into the trigger's title/dedup/repo, and carries
// one or more actions. The parsed body is exposed to the action's own templates as
// {{.body.<...>}}.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"text/template"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

func init() { core.Register("webhook", newIntegration) }

const maxBody = 25 << 20

// Config is one webhook instance: a transport (direct listener and/or smee) and a
// set of named sources.
type Config struct {
	Listen  string   `yaml:"listen"`   // direct HTTP listener addr, e.g. ":8099"
	SmeeURL string   `yaml:"smee_url"` // smee.io channel (no public ingress needed)
	Sources []Source `yaml:"sources"`
}

// Source maps an inbound delivery to trigger(s).
type Source struct {
	Name    string           `yaml:"name"`
	Path    string           `yaml:"path"`  // HTTP path for the listener transport (e.g. /hooks/cloudwatch)
	Sign    Sign             `yaml:"sign"`  // optional HMAC verification
	Match   string           `yaml:"match"` // optional template predicate over {{.body}}; fire only if it renders "true"
	Title   string           `yaml:"title"` // template → Trigger.Title
	Dedup   string           `yaml:"dedup"` // template → Trigger.Dedup ("" = fire on every delivery)
	Repo    string           `yaml:"repo"`  // template → real "owner/name" (enables checkout); else synthetic
	Actions config.ActionSet `yaml:"actions"`
}

// Sign configures HMAC-SHA256 verification of the raw body.
type Sign struct {
	Header string `yaml:"header"` // request header carrying the signature
	Secret string `yaml:"secret"` // shared secret (usually ${ENV})
	Scheme string `yaml:"scheme"` // "" | "hex" | "sha256" | "base64"
}

// Integration implements core.Integration for one webhook instance.
type Integration struct {
	name string
	cfg  Config
	seen *inbound.DeliveryDedup
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("webhook[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg, seen: inbound.NewDeliveryDedup(2048)}, nil
}

func (g *Integration) Name() string { return g.name }

func (g *Integration) Validate() error {
	if g.cfg.Listen == "" && g.cfg.SmeeURL == "" {
		return fmt.Errorf("webhook[%s]: set `listen` and/or `smee_url`", g.name)
	}
	if len(g.cfg.Sources) == 0 {
		return fmt.Errorf("webhook[%s]: no sources", g.name)
	}
	seen := map[string]bool{}
	for i, s := range g.cfg.Sources {
		if s.Name == "" {
			return fmt.Errorf("webhook[%s]: sources[%d]: missing name", g.name, i)
		}
		if seen[s.Name] {
			return fmt.Errorf("webhook[%s]: duplicate source name %q", g.name, s.Name)
		}
		seen[s.Name] = true
		if g.cfg.Listen != "" && s.Path == "" {
			return fmt.Errorf("webhook[%s]: source %q: `path` is required with a listener", g.name, s.Name)
		}
		if g.cfg.SmeeURL != "" && s.Match == "" && len(g.cfg.Sources) > 1 {
			return fmt.Errorf("webhook[%s]: source %q: `match` is required to route a smee channel across multiple sources", g.name, s.Name)
		}
		if len(s.Actions) == 0 {
			return fmt.Errorf("webhook[%s]: source %q: no actions", g.name, s.Name)
		}
		for _, a := range s.Actions {
			if a.Type == "" {
				return fmt.Errorf("webhook[%s]: source %q: action.type is required", g.name, s.Name)
			}
		}
		for _, f := range []struct{ what, tmpl string }{
			{"match", s.Match}, {"title", s.Title}, {"dedup", s.Dedup}, {"repo", s.Repo},
		} {
			if _, err := template.New("t").Parse(f.tmpl); err != nil {
				return fmt.Errorf("webhook[%s]: source %q: bad %s template: %w", g.name, s.Name, f.what, err)
			}
		}
	}
	return nil
}

func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	if g.cfg.Listen != "" {
		for _, s := range g.cfg.Sources {
			s := s
			inbound.Register(ctx, g.cfg.Listen, s.Path, g.handler(ctx, emit, s), log.Printf)
		}
		log.Printf("webhook[%s]: %d source(s) on %s", g.name, len(g.cfg.Sources), g.cfg.Listen)
	}
	if g.cfg.SmeeURL != "" {
		go func() {
			_ = inbound.Smee(ctx, g.cfg.SmeeURL, log.Printf, func(f inbound.Frame) {
				// A smee channel carries every source's traffic; route by `match`.
				for _, s := range g.cfg.Sources {
					sig := ""
					if s.Sign.Header != "" {
						sig = f.Header(s.Sign.Header)
					}
					g.deliver(ctx, emit, s, sig, f.Body, true)
				}
			})
		}()
		log.Printf("webhook[%s]: %d source(s) via smee", g.name, len(g.cfg.Sources))
	}
	<-ctx.Done()
	return ctx.Err()
}

func (g *Integration) handler(ctx context.Context, emit core.EmitFunc, s Source) http.HandlerFunc {
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
		if s.Sign.Secret != "" && !inbound.VerifyHMAC(s.Sign.Secret, body, r.Header.Get(s.Sign.Header), s.Sign.Scheme) {
			log.Printf("webhook[%s]: source %q signature mismatch (dropped)", g.name, s.Name)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		g.deliver(ctx, emit, s, "", body, false)
		w.WriteHeader(http.StatusAccepted)
	}
}

// deliver maps one raw body through a source and emits a trigger per enabled action
// variant. smeeVerified reflects that the caller already verified (or skipped)
// signature — over smee the raw bytes are re-serialized, so verification there is
// best-effort and applied inline in Start.
func (g *Integration) deliver(ctx context.Context, emit core.EmitFunc, s Source, smeeSig string, body []byte, viaSmee bool) {
	if viaSmee && s.Sign.Secret != "" && !inbound.VerifyHMAC(s.Sign.Secret, body, smeeSig, s.Sign.Scheme) {
		return // best-effort over smee; drop on mismatch
	}
	parsed := parseBody(body)
	data := map[string]any{"body": parsed}

	if s.Match != "" && strings.TrimSpace(render(s.Match, data)) != "true" {
		return // predicate says this delivery isn't for this source
	}
	dedup := render(s.Dedup, data)
	if !g.seen.Add(s.Name + "\x00" + dedup) {
		return // duplicate delivery (smee redelivery / retried POST)
	}

	title := render(s.Title, data)
	if title == "" {
		title = fmt.Sprintf("%s: %s", g.name, s.Name)
	}
	repo := strings.TrimSpace(render(s.Repo, data))

	var target core.Target
	synthetic := repo == ""
	if synthetic {
		target = inbound.SyntheticTarget("webhook:"+s.Name, s.Name+dedup)
	} else {
		owner, name, _ := strings.Cut(repo, "/")
		target = core.Target{Repo: repo, Owner: owner, Name: name, Number: numID(s.Name + dedup)}
	}

	for _, act := range s.Actions {
		if !act.IsEnabled() {
			continue
		}
		if synthetic {
			act = inbound.ForceNoCheckout(act)
		}
		emit(ctx, core.Trigger{
			Source:   "webhook",
			Instance: g.name,
			Kind:     s.Name,
			Variant:  act.Name,
			Target:   target,
			Title:    title,
			Dedup:    dedup,
			Context:  map[string]any{"body": parsed},
			Action:   act,
		})
	}
}

// parseBody decodes a JSON body to a map; a non-JSON body is exposed as {raw: "…"}.
func parseBody(body []byte) any {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		return m
	}
	return map[string]any{"raw": string(body)}
}

// render evaluates a text/template against data, returning "" on any error so a
// mapping never blocks a delivery (missing keys render as zero values).
func render(tmpl string, data map[string]any) string {
	if tmpl == "" {
		return ""
	}
	t, err := template.New("t").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return ""
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return ""
	}
	return b.String()
}

// numID re-exports the synthetic-number hash via a real-repo Target too.
func numID(s string) int { return inbound.SyntheticTarget("", s).Number }
