package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// fakeDynImpl is a Go-native (non-contract) connector.Impl whose
// DeclaredEvents() returns a non-nil, obviously-fake set of names. Nothing in
// the real codebase has an Impl like this wired through cmd/conductor's
// connectors ls / schema commands anymore (finding 4: externalImpl — a
// spawned plugin OR any in-process builtin, cron/rss/webhook/rest/graphql —
// always answers nil), but this fixture proves those commands no longer
// consult Impl.DeclaredEvents() at all: if they did, this fake set would leak
// into their output.
type fakeDynImpl struct{}

func (fakeDynImpl) Validate() error { return nil }
func (fakeDynImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("fakedyn: no verbs")
}
func (fakeDynImpl) Source([]connector.CompiledTrigger) (core.Integration, error) { return nil, nil }
func (fakeDynImpl) DeclaredEvents() []string                                     { return []string{"zzz-fake-should-never-show"} }

var fakeDynDecl = &connector.TypeDecl{
	Type: "fakedyn",
	Desc: "test: a connector whose Impl implements DeclaredEvents meaningfully, to prove cmd/conductor no longer reads it",
	Events: []connector.EventDecl{
		{Name: "<fake>", Dynamic: true, Desc: "a fake dynamic event"},
	},
}

var registerFakeDynOnce sync.Once

func registerFakeDyn(t *testing.T) {
	t.Helper()
	registerFakeDynOnce.Do(func() {
		connector.RegisterType(fakeDynDecl, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
			return fakeDynImpl{}, nil
		})
	})
}

// TestCmdConnectorsLsNeverConsultsDeclaredEvents is the finding-4 regression:
// before the fix, `connectors ls` swapped in whatever Impl.DeclaredEvents()
// returned, non-nil, in place of the declared Decl's own event names — dead
// in every real case because externalImpl (every contract connector) always
// answers nil, but still reachable code. With the fix, ls always shows the
// Decl's own event names and never reads DeclaredEvents().
func TestCmdConnectorsLsNeverConsultsDeclaredEvents(t *testing.T) {
	registerFakeDyn(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  fd:\n    use: fakedyn\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdConnectors([]string{"--config", path, "ls"}) })
	if err != nil {
		t.Fatalf("connectors ls: %v\n%s", err, out)
	}
	if strings.Contains(out, "zzz-fake-should-never-show") {
		t.Fatalf("connectors ls must never surface Impl.DeclaredEvents() — dead for every contract connector:\n%s", out)
	}
	if !strings.Contains(out, "<fake>") {
		t.Errorf("expected the type-level Dynamic placeholder's own name, got:\n%s", out)
	}
}

// TestCmdSchemaNeverConsultsDeclaredEvents is cmdSchema's half of the same
// regression: it must print the generic "<declared in connection>" stand-in
// for a Dynamic event, never a fake Impl.DeclaredEvents() name.
func TestCmdSchemaNeverConsultsDeclaredEvents(t *testing.T) {
	registerFakeDyn(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  fd:\n    use: fakedyn\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "fd"}) })
	if err != nil {
		t.Fatalf("schema fd: %v\n%s", err, out)
	}
	if strings.Contains(out, "zzz-fake-should-never-show") {
		t.Fatalf("schema must never surface Impl.DeclaredEvents() — dead for every contract connector:\n%s", out)
	}
	if !strings.Contains(out, "<declared in connection>") {
		t.Errorf("expected the generic Dynamic placeholder stand-in, got:\n%s", out)
	}
}
