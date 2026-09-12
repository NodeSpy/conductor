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

// META-TEST, ENUMERATED BY DISCOVERY (round-7 #2). The previous version of
// this test listed two files by hand — membind.go and connector/memory.go —
// and asserted each calls CheckOp. There was a THIRD face: ipc.go, the
// MCP/CLI memory tool an agent drives directly, which reached the store with
// only the reserved-bucket check. A hand-written list of faces cannot fail
// for a face nobody put on it.
//
// So the list is DISCOVERED: every file in the tree that reaches the store's
// agent-reachable methods must also call CheckOp. A fourth face added
// tomorrow is enumerated the moment it touches Remember/Recall/List/Forget.
func TestEveryAgentFacingMemoryFaceCallsTheSharedGate(t *testing.T) {
	root := repoRoot(t)
	// The store methods an agent-facing face has to go through to read or
	// write memories. A face is anything that calls one of these.
	touches := regexp.MustCompile(`\bm\.(Remember|Recall|List|Forget)\(`)
	// Where a face can live. internal/memory's own internals (the manager,
	// the harvester, the prompt injector) are not agent-facing: they are the
	// implementation the faces call, and the harvest path has its own guard.
	faceDirs := []string{"internal/code", "internal/connector", "internal/memory"}
	notAFace := map[string]bool{
		"internal/memory/memory.go":     true, // the manager itself
		"internal/memory/harvest.go":    true, // output-contract harvest (CheckAgentScope + write guard)
		"internal/memory/prompt.go":     true, // prompt injection, no caller input
		"internal/memory/peer.go":       true,
		"internal/memory/scopeguard.go": true,
	}
	found := 0
	for _, dir := range faceDirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			rel := filepath.Join(dir, e.Name())
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			if notAFace[filepath.ToSlash(rel)] {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			src := string(b)
			if !touches.MatchString(src) {
				continue // not a face
			}
			found++
			if !strings.Contains(src, ".CheckOp(") {
				t.Errorf("%s reaches the memory store (%s) but never calls CheckOp — every "+
					"agent-facing face must authorize the scope it touches, or allow_memory_scopes "+
					"is inert on it (which is exactly how the MCP/CLI face shipped)",
					rel, touches.FindString(src))
			}
		}
	}
	if found < 3 {
		t.Errorf("discovered only %d memory face(s); the run:code binding, the memory.* verbs "+
			"and the MCP/CLI tool are all expected — if a face moved, this test has stopped "+
			"looking where the faces are", found)
	}
}

// …and the gate must come BEFORE the op switch on each face, not inside one
// case: a per-case call is how `remember` ended up guarded alone.
func TestTheSharedGateComesBeforeTheOpSwitch(t *testing.T) {
	root := repoRoot(t)
	for rel, what := range map[string]string{
		"internal/code/membind.go":     "the run: code binding (js/go-embed/risor/lua)",
		"internal/connector/memory.go": "the memory.* verbs (skill grants / plan steps)",
		"internal/memory/ipc.go":       "the MCP/CLI memory tool",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		src := string(b)
		gate := strings.Index(src, ".CheckOp(")
		if gate < 0 {
			t.Errorf("%s (%s) no longer calls CheckOp", rel, what)
			continue
		}
		sw := -1
		for _, marker := range []string{"switch verb {", "switch op {", "switch req.Op {"} {
			if i := strings.Index(src, marker); i >= 0 && (sw < 0 || i < sw) {
				sw = i
			}
		}
		if sw >= 0 && gate > sw {
			t.Errorf("%s calls CheckOp INSIDE the op switch — gate before it, or the next op "+
				"added gets no guard", rel)
		}
	}
}

// agentCaller is the caller shape these tests mean: an agent-facing op with
// no dispatch repo of its own, so only the allowlist can admit it.
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
