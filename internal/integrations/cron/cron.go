// Package cron is a schedule-driven integration: it emits a Trigger on a cron
// spec (or a fixed interval), carrying an action to run — a `command` (e.g. a
// maintenance script or `gh` call) or an `agent`. It reuses the same dispatch
// backends as every other integration.
package cron

import (
	"context"
	"fmt"
	"log"

	"github.com/robfig/cron/v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func init() { core.Register("cron", newIntegration) }

// Config is a cron instance's configuration.
type Config struct {
	Schedules []Schedule `yaml:"schedules"`
}

// Schedule is one recurring job.
type Schedule struct {
	Name       string          `yaml:"name"`
	Cron       string          `yaml:"cron"`  // cron spec: "0 2 * * *", "@daily", "@every 6h"
	Every      config.Duration `yaml:"every"` // alternative to cron: a fixed interval
	RunOnStart bool            `yaml:"run_on_start"`
	Action     config.Action   `yaml:"action"`
}

// spec returns the cron spec, translating `every` into "@every <dur>".
func (s Schedule) spec() string {
	if s.Cron != "" {
		return s.Cron
	}
	if s.Every > 0 {
		return "@every " + s.Every.D().String()
	}
	return ""
}

// Integration implements core.Integration for one cron instance.
type Integration struct {
	name string
	cfg  Config
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("cron[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg}, nil
}

// Name returns the instance name.
func (g *Integration) Name() string { return g.name }

// Validate checks each schedule has a name, a valid spec, and an action.
func (g *Integration) Validate() error {
	if len(g.cfg.Schedules) == 0 {
		return fmt.Errorf("cron[%s]: no schedules", g.name)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	seen := map[string]bool{}
	for i, s := range g.cfg.Schedules {
		if s.Name == "" {
			return fmt.Errorf("cron[%s]: schedules[%d]: missing name", g.name, i)
		}
		if seen[s.Name] {
			return fmt.Errorf("cron[%s]: duplicate schedule name %q", g.name, s.Name)
		}
		seen[s.Name] = true
		if s.spec() == "" {
			return fmt.Errorf("cron[%s]: schedule %q: set `cron` or `every`", g.name, s.Name)
		}
		if _, err := parser.Parse(s.spec()); err != nil {
			return fmt.Errorf("cron[%s]: schedule %q: bad spec %q: %w", g.name, s.Name, s.spec(), err)
		}
		if s.Action.Type == "" && s.Action.FlowRef == "" { // FlowRef: a lowered connectors-model action, engine-routed
			return fmt.Errorf("cron[%s]: schedule %q: action.type is required", g.name, s.Name)
		}
	}
	return nil
}

// Actions enumerates each schedule's action with its location, for the CLI's
// cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	refs := make([]config.ActionRef, 0, len(g.cfg.Schedules))
	for _, s := range g.cfg.Schedules {
		refs = append(refs, config.ActionRef{
			Where: fmt.Sprintf("cron[%s] schedule %q", g.name, s.Name), Action: s.Action})
	}
	return refs
}

// trigger builds the Trigger a schedule emits (action attached for the engine).
func (g *Integration) trigger(s Schedule) core.Trigger {
	return core.Trigger{
		Source:   "cron",
		Instance: g.name,
		Kind:     s.Name,
		Title:    "cron: " + g.name + "/" + s.Name,
		Context:  map[string]any{"schedule": s.Name},
		Action:   s.Action,
	}
}

// Start registers the schedules and runs them until ctx is cancelled.
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	c := cron.New()
	for _, s := range g.cfg.Schedules {
		s := s
		if _, err := c.AddFunc(s.spec(), func() { emit(ctx, g.trigger(s)) }); err != nil {
			return fmt.Errorf("cron[%s]: schedule %q: %w", g.name, s.Name, err)
		}
		if s.RunOnStart {
			emit(ctx, g.trigger(s))
		}
	}
	log.Printf("cron[%s]: %d schedule(s) started", g.name, len(g.cfg.Schedules))
	c.Start()
	<-ctx.Done()
	<-c.Stop().Done()
	return ctx.Err()
}
