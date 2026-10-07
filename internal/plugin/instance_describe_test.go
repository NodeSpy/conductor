package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// instDescriber is a fake rest/graphql-shaped plugin: its verbs depend on the
// instance's own config (Q6), over the real in-process wire.
type instDescriber struct{}

func (instDescriber) Describe() sdk.Decl {
	return sdk.Decl{Type: "rest", Connection: sdk.Schema{"verbs": {Type: "map"}}}
}
func (instDescriber) Invoke(sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{}, sdk.Errorf(sdk.CodeMethodNotFound, "unused")
}
func (instDescriber) DescribeInstance(_ context.Context, instance string, config map[string]any) (sdk.Decl, error) {
	verbs, _ := config["verbs"].(map[string]any)
	d := sdk.Decl{Type: "rest", Desc: "instance " + instance}
	for name := range verbs {
		d.Verbs = append(d.Verbs, sdk.Verb{Name: name})
	}
	return d, nil
}

// TestClientDescribeInstance drives Q6 over the real client/transport (the
// same path a spawned plugin or an in-process builtin takes): two instances
// of the same type get two different declarations, driven by their own
// config.
func TestClientDescribeInstance(t *testing.T) {
	c := NewClient(Spec{Name: "rest", Kind: KindConnector, Provides: "rest", InProcess: instDescriber{}}, Deps{})
	defer c.Close()
	ctx := context.Background()

	d, supported, err := c.DescribeInstance(ctx, "a", map[string]any{"verbs": map[string]any{"create_issue": map[string]any{}}})
	if err != nil {
		t.Fatalf("describe instance a: %v", err)
	}
	if !supported {
		t.Fatal("instDescriber implements InstanceDescriber — must report supported")
	}
	if len(d.Verbs) != 1 || d.Verbs[0].Name != "create_issue" {
		t.Fatalf("instance a verbs = %+v", d.Verbs)
	}

	d2, _, err := c.DescribeInstance(ctx, "b", map[string]any{"verbs": map[string]any{"list_repos": map[string]any{}, "get_repo": map[string]any{}}})
	if err != nil {
		t.Fatalf("describe instance b: %v", err)
	}
	if len(d2.Verbs) != 2 {
		t.Fatalf("instance b verbs = %+v, want 2", d2.Verbs)
	}

	// The type-level Describe() is unaffected by instance calls.
	td, err := c.Describe(ctx)
	if err != nil || len(td.Verbs) != 0 {
		t.Fatalf("type-level describe changed: %+v %v", td, err)
	}
}

// plainConnector has no InstanceDescriber: a per-instance describe must come
// back "not supported" (CodeMethodNotFound), not a silently-reused Decl.
type plainConnector struct{}

func (plainConnector) Describe() sdk.Decl {
	return sdk.Decl{Type: "acme", Verbs: []sdk.Verb{{Name: "echo"}}}
}
func (plainConnector) Invoke(sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{}, nil
}

func TestClientDescribeInstanceUnsupported(t *testing.T) {
	c := NewClient(Spec{Name: "acme", Kind: KindConnector, Provides: "acme", InProcess: plainConnector{}}, Deps{})
	defer c.Close()
	ctx := context.Background()
	d, supported, err := c.DescribeInstance(ctx, "a", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if supported {
		t.Fatal("plainConnector has no InstanceDescriber — must report unsupported")
	}
	if d != nil {
		t.Fatalf("unsupported must return a nil decl, got %+v", d)
	}
}

// identityForgingDescriber claims a different type on its per-instance
// describe than it was configured to provide — the same anti-forgery check
// Describe() applies must apply here too.
type identityForgingDescriber struct{}

func (identityForgingDescriber) Describe() sdk.Decl { return sdk.Decl{Type: "rest"} }
func (identityForgingDescriber) Invoke(sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{}, nil
}
func (identityForgingDescriber) DescribeInstance(context.Context, string, map[string]any) (sdk.Decl, error) {
	return sdk.Decl{Type: "github"}, nil
}

func TestClientDescribeInstanceRefusesIdentityForgery(t *testing.T) {
	c := NewClient(Spec{Name: "rest", Kind: KindConnector, Provides: "rest", InProcess: identityForgingDescriber{}}, Deps{})
	defer c.Close()
	_, _, err := c.DescribeInstance(context.Background(), "a", nil)
	if err == nil {
		t.Fatal("expected a refusal when the instance describe claims a different type")
	}
}

// Declarations are must-understand on the PER-INSTANCE describe path too
// (plugin-contract.md §1.4, §3.9 G13): the returned Decl is checked exactly
// like the type-level one — an unknown semantic, or one that does not hang
// together, refuses the instance. This kills a dropped/short-circuited
// CheckSemantics/ValidateSemantics call on DescribeInstance specifically
// (client.go's type-level Describe has its own, separately-tested check;
// these two paths are easy to accidentally diverge).
//
// fakeConn.rawDescribe round-trips the describe result VERBATIM (bytes a
// Decl struct cannot model survive) — the only way to put a semantic key
// this SDK does not implement on the wire at all, since a typed
// InstanceDescriber return value can never carry a field its own Decl
// struct lacks.
func TestClientDescribeInstanceRefusesUnknownSemantic(t *testing.T) {
	fc := newFakeConn()
	raw := `{"protocol_version":1,"type":"jira","events":[{"name":"e","semantics":{"teleport":true}}]}`
	var d Decl
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	fc.describe = &d
	fc.rawDescribe = raw
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	defer c.Close()

	_, _, err := c.DescribeInstance(context.Background(), "a", nil)
	if err == nil || !strings.Contains(err.Error(), "events[e].semantics.teleport") {
		t.Fatalf("an unknown semantic on the per-instance describe must refuse it, naming it: %v", err)
	}
}

// Same path, an INCONSISTENT declaration: a mints_credential verb that is
// not host_only does not hang together (ValidateSemantics), on the
// per-instance describe exactly as on the type-level one.
func TestClientDescribeInstanceRefusesInconsistentSemantics(t *testing.T) {
	fc := newFakeConn()
	raw := `{"protocol_version":1,"type":"jira","verbs":[{"name":"mint","semantics":{"mints_credential":{"credential":"w"}}}]}`
	var d Decl
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	fc.describe = &d
	fc.rawDescribe = raw
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	defer c.Close()

	_, _, err := c.DescribeInstance(context.Background(), "a", nil)
	if err == nil || !strings.Contains(err.Error(), "host_only") {
		t.Fatalf("an inconsistent per-instance declaration must be refused: %v", err)
	}
}
