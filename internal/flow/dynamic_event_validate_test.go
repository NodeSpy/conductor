package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// A connector type with a DYNAMIC event whose impl cannot enumerate its
// configured names — the shape of an external SOURCE plugin (fswatch/logwatch as
// out-of-process plugins): the per-instance names live in the plugin's own
// config, so DeclaredEvents() returns nil.
var dynSrcDecl = &connector.TypeDecl{
	Type: "dyntestsrc",
	Desc: "test: an external-like source with a dynamic event it cannot enumerate here",
	Events: []connector.EventDecl{
		{Name: "<watch>", Dynamic: true, Context: connector.Schema{"path": {Type: connector.TString}}},
	},
}

type dynSrcImpl struct{}

func (dynSrcImpl) Validate() error          { return nil }
func (dynSrcImpl) DeclaredEvents() []string { return nil } // can't enumerate — external
func (dynSrcImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (dynSrcImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("dyntestsrc: no verbs")
}

func init() {
	connector.RegisterType(dynSrcDecl, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
		return dynSrcImpl{}, nil
	})
}

// A dynamic-event source that cannot enumerate its configured names (an external
// plugin) must NOT have its trigger rejected for naming an "undeclared" event —
// the Dynamic template already matched, and the plugin validates the name at
// StartSource. (Builtin sources that CAN enumerate stay strict — cron's own
// tests cover that.)
func TestExternalDynamicEventTriggerValidates(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  src: { use: dyntestsrc }
triggers:
  - on: src.some_configured_watch
    steps:
      - id: note
        log: "fired {{.path}}"
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("an external dynamic-event source must accept any configured name, got: %v", err)
	}
}

// Sanity: the enumeration check still fires for a connector that DOES declare its
// names (guards against the relax over-reaching). Uses the same decl but an impl
// that enumerates a fixed set via a second registered type.
var dynStrictDecl = &connector.TypeDecl{
	Type: "dynteststrict",
	Desc: "test: a source that enumerates its dynamic names (builtin-like)",
	Events: []connector.EventDecl{
		{Name: "<watch>", Dynamic: true, Context: connector.Schema{"path": {Type: connector.TString}}},
	},
}

type dynStrictImpl struct{}

func (dynStrictImpl) Validate() error          { return nil }
func (dynStrictImpl) DeclaredEvents() []string { return []string{"known"} }
func (dynStrictImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (dynStrictImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("dynteststrict: no verbs")
}

func init() {
	connector.RegisterType(dynStrictDecl, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
		return dynStrictImpl{}, nil
	})
}

func TestEnumerableDynamicEventStaysStrict(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  src: { use: dynteststrict }
triggers:
  - on: src.bogus
    steps:
      - id: note
        log: "x"
`)
	reg := buildRegistry(t, cfg)
	err := Validate(cfg, reg)
	if err == nil || !strings.Contains(err.Error(), "declares no") {
		t.Fatalf("an enumerable source must still reject an undeclared name, got: %v", err)
	}
}
