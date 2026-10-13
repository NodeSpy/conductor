package core

import (
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// DeclaredKey is the string a plugin's target_gone names a target by: the
// event's own target.key, else the declared key template over the facts,
// else the host's Key.
func TestDeclaredKey(t *testing.T) {
	sent := Trigger{Source: "chat", Instance: "c", TargetTrusted: true, Target: Target{Key: "chat:C1:1.5"}}
	if got := sent.DeclaredKey(); got != "chat:C1:1.5" {
		t.Fatalf("event key: got %q", got)
	}
	tmpl := Trigger{Source: "chat", Instance: "c", TargetTrusted: true,
		Context: map[string]any{"chat": map[string]any{"channel": "C2", "ts": "2.5"}},
		Sem:     &sdk.EventSemantics{Target: &sdk.TargetSemantics{Key: "chat:{{.chat.channel}}:{{.chat.ts}}"}}}
	if got := tmpl.DeclaredKey(); got != "chat:C2:2.5" {
		t.Fatalf("declared template: got %q", got)
	}
	legacy := Trigger{Source: "forge", Instance: "f", TargetTrusted: true, Target: Target{Repo: "o/r", Number: 7}}
	if got, want := legacy.DeclaredKey(), legacy.Key(); got != want {
		t.Fatalf("fallback: got %q, want Key %q", got, want)
	}

	// A sender-named (unassigned) target can't claim another target's key.
	forged := Trigger{Source: "relay", Instance: "r", Target: Target{Key: "chat:general"}}
	if got := forged.DeclaredKey(); got == "chat:general" || got != forged.Key() {
		t.Fatalf("untrusted target: got %q, want the namespaced Key %q", got, forged.Key())
	}
}
