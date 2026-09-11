package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// memoryOps is the complete agent-reachable op set. Both agent-facing faces —
// the `run: code` binding and the `memory.*` verbs — dispatch on exactly these.
var memoryOps = []string{"remember", "recall", "list", "forget"}

// META-TEST B. The H8 write guard was added to `remember` and stopped there,
// leaving recall/list/forget open: an agent could READ the shared bucket it
// was forbidden to write, list every tenant's entries, and forget an id in
// someone else's scope.
//
// This asserts the property per-op rather than per-file, so an op that stops
// being gated fails here no matter which face it was reached through.
func TestEveryAgentFacingMemoryOpIsGuarded(t *testing.T) {
	for _, op := range memoryOps {
		t.Run(op, func(t *testing.T) {
			m := testManager(t, NewMemBackend())

			// Deny-by-default: with a guard installed that allows only
			// "repo:acme/app", every op is refused for another scope.
			m.SetScopeGuard(func(_ Caller, _, scope string) error {
				if scope == "repo:acme/app" {
					return nil
				}
				return &testDenied{scope}
			})

			if err := m.CheckOp(agentCaller, op, "repo:other/tenant"); err == nil {
				t.Errorf("%s was permitted against a scope outside the allowlist — "+
					"a narrow grant reaches another tenant's memories", op)
			}
			if err := m.CheckOp(agentCaller, op, "repo:acme/app"); err != nil {
				t.Errorf("%s refused for an ALLOWED scope: %v — the gate is too broad", op, err)
			}
		})
	}

	// An unknown op is refused, not passed through.
	m := testManager(t, NewMemBackend())
	if err := m.CheckOp(agentCaller, "exfiltrate", "repo:acme/app"); err == nil {
		t.Error("an unrecognized memory op was permitted — the gate must deny by default")
	}
}

// The reserved bucket is refused on the op that NAMES its own scope, and no
// installed allowlist can grant it.
func TestTheSharedBucketCannotBeGrantedByPolicy(t *testing.T) {
	m := testManager(t, NewMemBackend())
	m.SetScopeGuard(func(Caller, string, string) error { return nil }) // allow everything

	for _, spelling := range []string{"global", "Global", "  global  "} {
		if err := m.CheckOp(agentCaller, "remember", spelling); err == nil {
			t.Errorf("remember into %q was permitted by an allow-all policy — the reserved "+
				"bucket is injected into every opted-in agent's prompt and is never grantable", spelling)
		}
	}
	// A named scope still writes under the same allow-all policy, so the
	// assertion above can't pass by refusing everything.
	if err := m.CheckOp(agentCaller, "remember", "repo:acme/app"); err != nil {
		t.Errorf("a named scope was refused: %v", err)
	}
}

// Redaction is a property of the READ, not of the caller. Every read face
// funnels through Recall, so asserting it there covers all of them — and is
// what stopped the code binding and the connector's recall/list from serving
// tracked secrets that the prompt and IPC paths were already scrubbing.
func TestEveryReadIsRedactedAtTheSource(t *testing.T) {
	m := testManager(t, NewMemBackend())
	const secret = "ghp_deadbeefdeadbeefdeadbeef"
	m.SetRedactor(func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") })

	if _, err := m.Remember("token is "+secret, nil, "repo:acme/app", Source{}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	for _, read := range []struct {
		name string
		fn   func() ([]Entry, error)
	}{
		{"Recall", func() ([]Entry, error) { return m.Recall(Query{}) }},
		{"Recall scoped", func() ([]Entry, error) { return m.Recall(Query{Scopes: []string{"repo:acme/app"}}) }},
		{"List", func() ([]Entry, error) { return m.List() }},
	} {
		entries, err := read.fn()
		if err != nil {
			t.Fatalf("%s: %v", read.name, err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s returned %d entries", read.name, len(entries))
		}
		if strings.Contains(entries[0].Text, secret) {
			t.Errorf("%s served a tracked secret: %q — redaction has to happen inside the "+
				"read, not at each call site, or the next read face will forget it", read.name, entries[0].Text)
		}
	}

	// Storage keeps the original: redaction is read-side only.
	raw, _ := m.backend.List()
	if len(raw) != 1 || !strings.Contains(raw[0].Text, secret) {
		t.Fatalf("storage must keep the original: %v", raw)
	}
}

// Both agent-facing faces must route their ops through CheckOp. A face that
// stops calling it silently reopens recall/list/forget, which is precisely the
// regression this chokepoint exists to prevent.
func TestBothAgentFacesCallTheSharedGate(t *testing.T) {
	root := repoRoot(t)
	faces := map[string]string{
		"internal/code/membind.go":     "the run: code binding (js/go-embed/risor/lua)",
		"internal/connector/memory.go": "the memory.* verbs (skill grants / MCP)",
	}
	for rel, what := range faces {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		src := string(b)
		if !regexp.MustCompile(`\.CheckOp\(`).MatchString(src) {
			t.Errorf("%s (%s) no longer calls memory.CheckOp — every op it dispatches "+
				"is ungated", rel, what)
			continue
		}
		// It must gate BEFORE the switch, not inside one case — a per-case
		// call is how `remember` ended up guarded alone.
		gate := strings.Index(src, ".CheckOp(")
		sw := strings.Index(src, "switch verb {")
		if sw < 0 {
			sw = strings.Index(src, "switch op {")
		}
		if sw >= 0 && gate > sw {
			t.Errorf("%s calls CheckOp INSIDE the op switch — gate before it, or the next "+
				"op added gets no guard (which is exactly how recall/list/forget stayed open)", rel)
		}
	}
}

// agentCaller is the caller shape every test here means: an agent-facing op
// with no dispatch repo of its own, so only the allowlist can admit it.
var agentCaller = Caller{AgentFacing: true}

// A CONFIG-AUTHORED caller (the zero Caller) is not gated by the operator's
// allowlist — that split is the point, and a guard that ignored it would gate
// the operator's own `uses: memory.recall` steps.
func TestConfigAuthoredCallersAreNotScopeGated(t *testing.T) {
	m := testManager(t, NewMemBackend())
	m.SetScopeGuard(func(Caller, string, string) error {
		return &testDenied{"everything"}
	})
	if err := m.CheckOp(Caller{}, "recall", "repo:any/where"); err != nil {
		t.Fatalf("a config-authored op must not be scope-gated: %v", err)
	}
	// …but the reserved bucket is still refused, for every caller.
	if err := m.CheckOp(Caller{}, "remember", "global"); err == nil {
		t.Error("the reserved bucket must be refused even for a config-authored caller")
	}
}

type testDenied struct{ scope string }

func (e *testDenied) Error() string { return "denied: " + e.scope }
