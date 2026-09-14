package core

import (
	"context"
	"sync"
	"testing"
)

type stubIntegration struct{ name string }

func (s stubIntegration) Name() string                          { return s.name }
func (s stubIntegration) Validate() error                       { return nil }
func (s stubIntegration) Start(context.Context, EmitFunc) error { return nil }

// registerStubOnce keeps the test count-safe: the registry is process-global
// and Register panics on duplicates, so `go test -count=2` would re-register.
var registerStubOnce sync.Once

func TestRegistryBuild(t *testing.T) {
	registerStubOnce.Do(func() {
		Register("stub-test", func(name string, decode func(any) error) (Integration, error) {
			var cfg struct {
				Extra string `yaml:"extra"`
			}
			if err := decode(&cfg); err != nil {
				return nil, err
			}
			return stubIntegration{name: name + ":" + cfg.Extra}, nil
		})
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
	tr := Trigger{Source: "github", TargetTrusted: true, Target: Target{Repo: "acme/w", Number: 42}}
	if tr.Key() != "acme/w#42" {
		t.Fatalf("key=%q", tr.Key())
	}
	// A target the event's SENDER chose keys in its own namespace, so it can
	// never equal the key a real dispatch for that repo produces — which is
	// the key the session broker, the dedup store and the PR labels all use.
	forged := Trigger{Source: "webhook", Instance: "hooks", Target: Target{Repo: "acme/w", Number: 42}}
	if forged.Key() == tr.Key() {
		t.Fatalf("a forged target produced the real PR's key %q — it would land on that "+
			"PR's live review session and share its dedup entry", forged.Key())
	}
	if forged.Key() == (Trigger{Source: "webhook", Instance: "hooks",
		Target: Target{Repo: "acme/w", Number: 43}}).Key() {
		t.Fatal("untrusted keys must still discriminate per target")
	}
	// No repo → falls back to source:instance.
	tr2 := Trigger{Source: "slack", Instance: "team"}
	if tr2.Key() != "slack:team" {
		t.Fatalf("fallback key=%q", tr2.Key())
	}
}
