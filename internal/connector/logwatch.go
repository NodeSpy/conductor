package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	logwatchint "github.com/NodeSpy/conductor/internal/integrations/logwatch"
)

// ---------------------------------------------------------------------------
// logwatch — log-line source. Streams a file (via tail -F) or a command's
// stdout and fires when a line matches a regexp. The log sibling of fswatch.
// ---------------------------------------------------------------------------

var logwatchDecl = &TypeDecl{
	Type: "logwatch",
	Desc: "Log watch: fires when a line matching a regexp appears in a followed file or a streaming command's stdout; no verbs.",
	Connection: Schema{
		"watches": {Type: TMap, Required: true, Desc: "name -> { path | command, pattern, debounce }"},
	},
	Events: []EventDecl{
		{
			Name: "<watch>", Dynamic: true, Desc: "a line matched the watch's pattern",
			Context: Schema{
				"watch":  {Type: TString},
				"line":   {Type: TString, Desc: "the full matched line"},
				"source": {Type: TString, Desc: "the file path, or \"command\""},
				"groups": {Type: TMap, Desc: "named regexp capture groups from the match"},
			},
		},
	},
}

func init() { RegisterType(logwatchDecl, newLogwatchImpl) }

type logwatchWatch struct {
	Path     string          `yaml:"path"`
	Command  []string        `yaml:"command"`
	Pattern  string          `yaml:"pattern"`
	Debounce config.Duration `yaml:"debounce"`
}

type logwatchConn struct {
	Watches map[string]logwatchWatch `yaml:"watches"`
}

type logwatchImpl struct {
	name string
	conn logwatchConn
	deps Deps
}

func newLogwatchImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn logwatchConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode logwatch connection: %w", name, err)
	}
	return &logwatchImpl{name: name, conn: conn, deps: deps}, nil
}

func (c *logwatchImpl) Validate() error { return nil }

func (c *logwatchImpl) DeclaredEvents() []string {
	out := make([]string, 0, len(c.conn.Watches))
	for k := range c.conn.Watches {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Source lowers each trigger into its watch's logwatchint.Watch. Exactly one
// trigger may target a given watch name — a Watch carries a single
// config.Action, so a second trigger on the same watch has nowhere to go.
func (c *logwatchImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	watches := make([]logwatchint.Watch, 0, len(triggers))
	for _, t := range triggers {
		name := t.Spec.Event()
		wc, ok := c.conn.Watches[name]
		if !ok {
			return nil, fmt.Errorf("trigger on %s: unknown logwatch watch %q (declared: %s)", t.Spec.On, name, strings.Join(c.DeclaredEvents(), ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf("trigger on %s: one trigger per logwatch watch — define a second watch", t.Spec.On)
		}
		seen[name] = true
		watches = append(watches, logwatchint.Watch{
			Name:     name,
			Path:     wc.Path,
			Command:  wc.Command,
			Pattern:  wc.Pattern,
			Debounce: wc.Debounce,
			Action:   lowerAction(t),
		})
	}
	return buildIntegration("logwatch", c.name, logwatchint.Config{Watches: watches})
}

func (c *logwatchImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("logwatch: no verbs")
}
