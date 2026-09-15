package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// decodeWorkspace runs one `workspace:` value through the real decoder.
func decodeWorkspace(t *testing.T, src string) (Workspace, error) {
	t.Helper()
	var holder struct {
		W Workspace `yaml:"w"`
	}
	err := yaml.Unmarshal([]byte("w: "+src+"\n"), &holder)
	return holder.W, err
}

// The bare-string form is the whole of the pre-existing surface, and every
// deployed config uses it. It must keep parsing to exactly the isolation mode
// it always did, with no pin.
func TestWorkspaceStringFormUnchanged(t *testing.T) {
	for _, mode := range []string{"local", "worktree"} {
		got, err := decodeWorkspace(t, mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if got != (Workspace{Isolation: mode}) {
			t.Fatalf("%s decoded to %+v", mode, got)
		}
		if err := got.Validate("step"); err != nil {
			t.Fatalf("%s should validate: %v", mode, err)
		}
	}
	// An absent workspace is the zero value, which every consumer reads as
	// "the runtime's default" — and which `extends:` treats as unset.
	var zero Workspace
	if !zero.IsZero() || zero.Validate("step") != nil {
		t.Fatal("the zero workspace must be unset and valid")
	}
	// A valueless `workspace:` means the same as omitting it, rather than being
	// an error: yaml.v3 decodes a null straight to the zero value without
	// consulting the unmarshaler, and "unset" is the honest reading.
	for _, src := range []string{"", "null", `""`} {
		got, err := decodeWorkspace(t, src)
		if err != nil || !got.IsZero() {
			t.Fatalf("workspace: %q should read as unset, got %+v %v", src, got, err)
		}
	}
}

func TestWorkspaceObjectForm(t *testing.T) {
	cases := []struct {
		src  string
		want Workspace
	}{
		{"{ isolation: local, pin: triage }", Workspace{Isolation: "local", Pin: "triage"}},
		{"{ pin: triage }", Workspace{Pin: "triage"}},
		{"{ isolation: worktree }", Workspace{Isolation: "worktree"}},
		// A pin is trimmed, so a stray space can't produce two different names
		// for one workspace.
		{`{ pin: "  triage  " }`, Workspace{Pin: "triage"}},
	}
	for _, c := range cases {
		got, err := decodeWorkspace(t, c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got != c.want {
			t.Fatalf("%s decoded to %+v, want %+v", c.src, got, c.want)
		}
	}
}

func TestWorkspaceRejectsBadShapes(t *testing.T) {
	cases := []struct{ src, wantIn string }{
		// A typo'd key must NOT drop silently — the step would sit on the
		// defaults it was trying to override.
		{"{ isolation: local, pinned: triage }", "pinned"},
		{"{ isolate: local }", "isolate"},
		// Present-but-blank reads as "pin me" and behaves as "no pin".
		{"{ pin: }", "blank"},
		{`{ pin: "   " }`, "blank"},
		{"{}", "empty map"},
		{"[local]", "got a list"},
	}
	for _, c := range cases {
		_, err := decodeWorkspace(t, c.src)
		if err == nil {
			t.Fatalf("%s should be rejected", c.src)
		}
		if !strings.Contains(err.Error(), c.wantIn) {
			t.Fatalf("%s: error %q should mention %q", c.src, err, c.wantIn)
		}
	}
	// An unknown isolation mode is caught at validation, where the step can be
	// named in the message.
	w, err := decodeWorkspace(t, "{ isolation: sandbox }")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := w.Validate("triggers[0].steps[0]"); err == nil ||
		!strings.Contains(err.Error(), "local|worktree") {
		t.Fatalf("bad isolation should name the valid modes, got %v", err)
	}
}

// Marshalling re-emits the shape that was written, so a config the writer
// round-trips is not silently rewritten into the object form.
func TestWorkspaceMarshalRoundTrip(t *testing.T) {
	for _, src := range []string{"worktree", "{ isolation: local, pin: triage }", "{ pin: triage }"} {
		w, err := decodeWorkspace(t, src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		b, err := yaml.Marshal(w)
		if err != nil {
			t.Fatalf("%s: marshal: %v", src, err)
		}
		back, err := decodeWorkspace(t, "\n  "+strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\n  "))
		if err != nil {
			t.Fatalf("%s: re-decode %q: %v", src, b, err)
		}
		if back != w {
			t.Fatalf("%s: round-trip %+v -> %q -> %+v", src, w, b, back)
		}
	}
	// A pinless workspace marshals back to the bare string it came from.
	b, _ := yaml.Marshal(Workspace{Isolation: "worktree"})
	if strings.TrimSpace(string(b)) != "worktree" {
		t.Fatalf("a pinless workspace should stay a bare string, got %q", b)
	}
	// And `omitempty` still drops an unset one.
	b, _ = yaml.Marshal(struct {
		W Workspace `yaml:"workspace,omitempty"`
	}{})
	if strings.Contains(string(b), "workspace") {
		t.Fatalf("an unset workspace should be omitted, got %q", b)
	}
}

// The pin flows onto the step, and the step surface rejects the one
// combination that cannot be honoured.
func TestStepWorkspacePinValidation(t *testing.T) {
	if err := validateWorkspacePin("s", Step{Workspace: Workspace{Pin: "triage"}}); err != nil {
		t.Fatalf("a pin with no explicit checkout is fine: %v", err)
	}
	if err := validateWorkspacePin("s", Step{Checkout: "none", Workspace: Workspace{Pin: "triage"}}); err != nil {
		t.Fatalf("a pin on checkout:none is the intended use: %v", err)
	}
	for _, co := range []string{"checkout-pr", "branch-off"} {
		err := validateWorkspacePin("s", Step{Checkout: co, Workspace: Workspace{Pin: "triage"}})
		if err == nil || !strings.Contains(err.Error(), co) {
			t.Fatalf("a pin on checkout %s must be refused, got %v", co, err)
		}
	}
	// No pin, no opinion.
	if err := validateWorkspacePin("s", Step{Checkout: "checkout-pr",
		Workspace: Workspace{Isolation: "worktree"}}); err != nil {
		t.Fatalf("isolation alone is unaffected: %v", err)
	}
}

// The `extends:` merge treats a workspace like any other scalar: a child that
// sets one inherits nothing, a child that leaves it unset inherits the base's.
func TestWorkspaceMergePolicy(t *testing.T) {
	base := Step{Workspace: Workspace{Isolation: "worktree"}}

	unset := Step{}
	MergeStepInto(&unset, base)
	if unset.Workspace != (Workspace{Isolation: "worktree"}) {
		t.Fatalf("an unset workspace should inherit, got %+v", unset.Workspace)
	}

	own := Step{Workspace: Workspace{Isolation: "local", Pin: "triage"}}
	MergeStepInto(&own, base)
	if own.Workspace != (Workspace{Isolation: "local", Pin: "triage"}) {
		t.Fatalf("a step's own workspace must win whole, got %+v", own.Workspace)
	}
}
