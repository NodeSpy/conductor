package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	fswatchint "github.com/NodeSpy/conductor/internal/integrations/fswatch"
)

// ---------------------------------------------------------------------------
// fswatch — filesystem-watch source (the low-latency counterpart to a cron
// sweep). Fires a trigger when a matching file settles under a watched dir.
// ---------------------------------------------------------------------------

var fswatchDecl = &TypeDecl{
	Type: "fswatch",
	Desc: "Filesystem watch: fires when a matching file settles in a watched directory; no verbs.",
	Connection: Schema{
		"watches": {Type: TMap, Required: true, Desc: "name -> { path, match, debounce, recursive, events }"},
	},
	Events: []EventDecl{
		{
			Name: "<watch>", Dynamic: true, Desc: "a matching file settled under a watch",
			Context: Schema{
				"watch": {Type: TString},
				"path":  {Type: TString},
				"file":  {Type: TString, Desc: "basename of the changed file (not `name` — that reserved key is the repo name)"},
				"op":    {Type: TString},
			},
		},
	},
}

func init() { RegisterType(fswatchDecl, newFswatchImpl) }

type fswatchWatch struct {
	Path      string          `yaml:"path"`
	Match     string          `yaml:"match"`
	Debounce  config.Duration `yaml:"debounce"`
	Recursive *bool           `yaml:"recursive"`
	Events    []string        `yaml:"events"`
}

type fswatchConn struct {
	Watches map[string]fswatchWatch `yaml:"watches"`
}

type fswatchImpl struct {
	name string
	conn fswatchConn
	deps Deps
}

func newFswatchImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn fswatchConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode fswatch connection: %w", name, err)
	}
	return &fswatchImpl{name: name, conn: conn, deps: deps}, nil
}

func (c *fswatchImpl) Validate() error { return nil }

func (c *fswatchImpl) DeclaredEvents() []string {
	out := make([]string, 0, len(c.conn.Watches))
	for k := range c.conn.Watches {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Source lowers each trigger into its watch's fswatchint.Watch. Exactly one
// trigger may target a given watch name — a Watch carries a single
// config.Action, so a second trigger on the same watch has nowhere to go.
func (c *fswatchImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	watches := make([]fswatchint.Watch, 0, len(triggers))
	for _, t := range triggers {
		name := t.Spec.Event()
		wc, ok := c.conn.Watches[name]
		if !ok {
			return nil, fmt.Errorf("trigger on %s: unknown fswatch watch %q (declared: %s)", t.Spec.On, name, strings.Join(c.DeclaredEvents(), ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf("trigger on %s: one trigger per fswatch watch — define a second watch", t.Spec.On)
		}
		seen[name] = true
		watches = append(watches, fswatchint.Watch{
			Name:      name,
			Path:      wc.Path,
			Match:     wc.Match,
			Debounce:  wc.Debounce,
			Recursive: wc.Recursive,
			Events:    wc.Events,
			Action:    lowerAction(t),
		})
	}
	return buildIntegration("fswatch", c.name, fswatchint.Config{Watches: watches})
}

func (c *fswatchImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("fswatch: no verbs")
}
