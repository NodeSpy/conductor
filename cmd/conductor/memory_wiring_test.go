package main

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestConfigureMemoryGuardsTrackedSecrets (#57 T2) exercises the PRODUCTION
// wiring — configureMemory building the real memory.Manager from a real
// *secrets.Resolver — rather than a hand-rolled guard closure. It proves that
// the manager published via memory.Active() refuses to harvest a tracked secret
// on the write side and redacts one on the read side, so the vault-not-memory
// contract holds through exactly the path the daemon uses.
func TestConfigureMemoryGuardsTrackedSecrets(t *testing.T) {
	memory.Reset()
	t.Cleanup(memory.Reset)

	const secret = "wiring-s3cr3t-XYZZY"
	sec := secrets.New()
	sec.Track(secret)

	cfg := &config.Config{Memory: &config.MemoryConfig{Type: "memory"}}
	if err := configureMemory(cfg, sec); err != nil {
		t.Fatal(err)
	}
	m := memory.Active()
	if m == nil {
		t.Fatal("configureMemory must publish an active manager")
	}

	// Write side: harvesting an agent output that carries the tracked secret is
	// refused, and nothing (not even the innocent sibling note) persists.
	out := "done.\n```remember\n- a plain fact\n- deploy token is " + secret + "\n```"
	if _, err := m.HarvestOutput(out, memory.Source{Step: "a"}); err == nil || !strings.Contains(err.Error(), "refusing to persist") {
		t.Fatalf("harvest of a tracked secret must be refused, got %v", err)
	}
	if all, _ := m.List(); len(all) != 0 {
		t.Fatalf("a refused harvest must persist nothing: %v", all)
	}

	// A clean harvest still lands.
	if _, err := m.HarvestOutput("```remember\n- squash merges only\n```", memory.Source{Step: "a"}); err != nil {
		t.Fatalf("clean harvest must pass: %v", err)
	}

	// Read side: an entry written directly (e.g. before the guard, or via a
	// trusted path) that carries the secret is redacted on the way out.
	if _, err := m.Remember("legacy note: key "+secret, nil, "global", memory.Source{Step: "old"}); err != nil {
		t.Fatal(err)
	}
	section := m.PromptSection(memory.Filter{}, memory.ContextKeys("acme/w", "", "fixer"))
	if strings.Contains(section, secret) {
		t.Fatalf("recalled memory leaked the tracked secret: %s", section)
	}
}
