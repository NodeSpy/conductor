package core

import (
	"context"
	"testing"
)

type stubIntegration struct{ name string }

func (s stubIntegration) Name() string                          { return s.name }
func (s stubIntegration) Validate() error                       { return nil }
func (s stubIntegration) Start(context.Context, EmitFunc) error { return nil }

func TestRegistryBuild(t *testing.T) {
	Register("stub-test", func(name string, decode func(any) error) (Integration, error) {
		var cfg struct {
			Extra string `yaml:"extra"`
		}
		if err := decode(&cfg); err != nil {
			return nil, err
		}
		return stubIntegration{name: name + ":" + cfg.Extra}, nil
	})

	ig, err := Build("stub-test", "acme", func(v any) error {
		*(v.(*struct {
			Extra string `yaml:"extra"`
		})) = struct {
			Extra string `yaml:"extra"`
		}{Extra: "x"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ig.Name() != "acme:x" {
		t.Fatalf("constructor/decode not wired: %q", ig.Name())
	}

	if _, err := Build("does-not-exist", "n", func(any) error { return nil }); err == nil {
		t.Fatal("unknown type should error")
	}

	found := false
	for _, ty := range Types() {
		if ty == "stub-test" {
			found = true
		}
	}
	if !found {
		t.Fatal("registered type not listed by Types()")
	}
}

func TestTriggerKey(t *testing.T) {
	tr := Trigger{Source: "github", Target: Target{Repo: "acme/w", Number: 42}}
	if tr.Key() != "acme/w#42" {
		t.Fatalf("key=%q", tr.Key())
	}
	// No repo → falls back to source:instance.
	tr2 := Trigger{Source: "slack", Instance: "team"}
	if tr2.Key() != "slack:team" {
		t.Fatalf("fallback key=%q", tr2.Key())
	}
}
