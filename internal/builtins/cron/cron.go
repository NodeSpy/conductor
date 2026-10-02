// Package cron is the `cron` connector as a contract handler
// (docs/design/plugin-contract.md §1.10): a source that fires each configured
// schedule as its own event, served in-process over the same protocol a
// spawned plugin speaks.
package cron

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	robfig "github.com/robfig/cron/v3"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

// Cron is the cron handler.
type Cron struct{}

// Describe declares cron: one dynamic event per configured schedule; no
// semantics (a plain trigger with no target), no verbs.
func (Cron) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "cron", Kind: plugin.KindConnector,
		Desc: "Cron: fires on a schedule (cron spec or fixed interval); no verbs.",
		Connection: plugin.Schema{
			"schedules": {Type: "map", Required: true, Desc: "name -> { cron, every, run_on_start }"},
		},
		Events: []plugin.Event{{
			Name: "<schedule>", Dynamic: true, Desc: "a configured schedule fired",
			Context: plugin.Schema{"schedule": {Type: "string"}, "kind": {Type: "string"}, "title": {Type: "string"}},
		}},
	}
}

// Invoke: cron has no verbs.
func (Cron) Invoke(plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "cron: no verbs", nil)
}

type schedule struct {
	name, spec string
	runOnStart bool
}

var parser = robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor)

// schedules reads the connection's schedules, every problem named.
func schedules(cfg map[string]any) (map[string]schedule, []plugin.Problem) {
	raw, _ := cfg["schedules"].(map[string]any)
	out := map[string]schedule{}
	var problems []plugin.Problem
	if len(raw) == 0 {
		return out, []plugin.Problem{{Path: "schedules", Message: "no schedules"}}
	}
	for name, v := range raw {
		m, _ := v.(map[string]any)
		s := schedule{name: name}
		if c, _ := m["cron"].(string); c != "" {
			s.spec = c
		} else if every := m["every"]; every != nil {
			d, err := duration(every)
			if err != nil || d <= 0 {
				problems = append(problems, plugin.Problem{Path: "schedules." + name + ".every", Message: fmt.Sprintf("not a positive duration: %v", every)})
				continue
			}
			s.spec = "@every " + d.String()
		}
		if s.spec == "" {
			problems = append(problems, plugin.Problem{Path: "schedules." + name, Message: "set `cron` or `every`"})
			continue
		}
		if _, err := parser.Parse(s.spec); err != nil {
			problems = append(problems, plugin.Problem{Path: "schedules." + name, Message: fmt.Sprintf("bad spec %q: %v", s.spec, err)})
			continue
		}
		s.runOnStart, _ = m["run_on_start"].(bool)
		out[name] = s
	}
	return out, problems
}

func duration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case string:
		return time.ParseDuration(x)
	case int:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x) * time.Second, nil
	}
	return 0, fmt.Errorf("unsupported %T", v)
}

func names(m map[string]schedule) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// Validate checks the schedules and that each trigger names one, at most one
// trigger per schedule.
func (Cron) Validate(_ context.Context, req plugin.ValidateRequest) (plugin.ValidateResult, error) {
	sched, problems := schedules(req.Config)
	seen := map[string]bool{}
	for i, t := range req.Triggers {
		path := fmt.Sprintf("triggers[%d]", i)
		if _, ok := sched[t.Event]; !ok {
			problems = append(problems, plugin.Problem{Path: path, Message: fmt.Sprintf("unknown cron schedule %q (declared: %s)", t.Event, names(sched))})
			continue
		}
		if seen[t.Event] {
			problems = append(problems, plugin.Problem{Path: path, Message: "one trigger per cron schedule — define a second schedule"})
		}
		seen[t.Event] = true
	}
	return plugin.ValidateResult{Problems: problems}, nil
}

// StartSource fires each triggered schedule, routed to its trigger.
func (Cron) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	sched, problems := schedules(req.Config)
	if len(problems) > 0 {
		return fmt.Errorf("cron[%s]: %s", req.Instance, problems[0].Message)
	}
	c := robfig.New()
	for _, t := range req.Triggers {
		if t.Enabled != nil && !*t.Enabled {
			continue
		}
		s, ok := sched[t.Event]
		if !ok {
			continue
		}
		ev := plugin.SourceEvent{
			Event: s.name, Trigger: t.ID, Instance: req.Instance,
			Title:   "cron: " + req.Instance + "/" + s.name,
			Context: map[string]any{"schedule": s.name},
		}
		if _, err := c.AddFunc(s.spec, func() { _ = emit(ev) }); err != nil {
			return fmt.Errorf("cron[%s]: schedule %q: %w", req.Instance, s.name, err)
		}
		if s.runOnStart {
			_ = emit(ev)
		}
	}
	c.Start()
	<-ctx.Done()
	<-c.Stop().Done()
	return nil
}
