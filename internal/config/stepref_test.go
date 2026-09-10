package config

import (
	"strings"
	"testing"
)

// setTestStep parks a step in a one-step workflow — where steps live now
// that there is no top-level registry — so a validation test can still name
// one thing and assert on it.
func setTestStep(c *Config, name string, s Step) {
	if c.Workflows == nil {
		c.Workflows = map[string]WorkflowDef{}
	}
	s.ID = name
	// A workflow step must have a FORM; these rigs only care about the
	// behavior fields, so default to the agent form.
	if s.Type == "" && s.Run == "" && s.Uses == "" && s.Workflow == "" && len(s.Command) == 0 {
		s.Type = "agent"
	}
	if s.Type == "agent" && s.Prompt == "" {
		s.Prompt = "x"
	}
	wf := c.Workflows["w"]
	for i := range wf.Steps {
		if wf.Steps[i].ID == name {
			wf.Steps[i] = s
			c.Workflows["w"] = wf
			return
		}
	}
	wf.Steps = append(wf.Steps, s)
	c.Workflows["w"] = wf
}

// --- the reference grammar -------------------------------------------------

func TestParseStepRef(t *testing.T) {
	tests := []struct {
		in      string
		wf      string
		slot    string
		index   int
		wantErr string
	}{
		{in: "review/architect", wf: "review", slot: "architect", index: -1},
		{in: "review[2]", wf: "review", index: 2},
		{in: "review[0]", wf: "review", index: 0},
		// A pack namespaces its workflows, so the workflow half may itself
		// carry slashes; the LAST one separates the slot.
		{in: "kit/review-flow/post", wf: "kit/review-flow", slot: "post", index: -1},
		{in: "kit/review-flow[1]", wf: "kit/review-flow", index: 1},
		{in: "", wantErr: "empty step reference"},
		{in: "review", wantErr: "write <workflow>/<step-id>"},
		{in: "/architect", wantErr: "write <workflow>/<step-id>"},
		{in: "review/", wantErr: "write <workflow>/<step-id>"},
		{in: "review[x]", wantErr: "whole number"},
		{in: "review[-1]", wantErr: "whole number"},
		{in: "[0]", wantErr: "malformed index"},
	}
	for _, tc := range tests {
		got, err := ParseStepRef(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseStepRef(%q) = %v, want error containing %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseStepRef(%q): %v", tc.in, err)
			continue
		}
		if got.Workflow != tc.wf || got.Slot != tc.slot || (tc.slot == "" && got.Index != tc.index) {
			t.Errorf("ParseStepRef(%q) = %+v, want {%s %s %d}", tc.in, got, tc.wf, tc.slot, tc.index)
		}
		if rt := got.String(); rt != tc.in {
			t.Errorf("ParseStepRef(%q).String() = %q — a reference must round-trip", tc.in, rt)
		}
	}
}

// The slot a reference uses and the slot the identity ladder uses are the
// same rule. If they ever diverge, a team role would point at one step and
// accumulate history under another.
func TestStepSlotMatchesTheIdentityLadder(t *testing.T) {
	steps := []Step{
		{ID: "a"},
		{Name: "shared"},     // no id — its name is the slot
		{Prompt: "bare"},     // neither — its index is
		{ID: "d", Name: "n"}, // id wins over name
	}
	want := []string{"a", "shared", "2", "d"}
	for i, w := range want {
		if got := StepSlot(steps[i], i); got != w {
			t.Errorf("StepSlot(%d) = %q, want %q", i, got, w)
		}
	}
	// …and the structural identity of the id-less one uses the same "2".
	if got := steps[2].Identity(WorkflowScope("w"), 2); !strings.HasSuffix(got, "/2") {
		t.Errorf("structural identity should end in the index slot, got %q", got)
	}
}

func TestFindStepRef(t *testing.T) {
	c := &Config{Workflows: map[string]WorkflowDef{
		"review": {Steps: []Step{
			{ID: "plan", Prompt: "p"},
			{Name: "architect", Prompt: "a"},
			{Prompt: "anonymous"},
		}},
	}}
	for ref, wantPrompt := range map[string]string{
		"review/plan":      "p",
		"review/architect": "a",
		"review[2]":        "anonymous",
		"review[0]":        "p",
	} {
		got, err := c.FindStepRef(ref)
		if err != nil {
			t.Errorf("FindStepRef(%q): %v", ref, err)
			continue
		}
		if got.Prompt != wantPrompt {
			t.Errorf("FindStepRef(%q).Prompt = %q, want %q", ref, got.Prompt, wantPrompt)
		}
	}
	// The pointer is live: a caller reads the step as configured.
	s, _ := c.FindStepRef("review/plan")
	s.Workspace = "worktree"
	if c.Workflows["review"].Steps[0].Workspace != "worktree" {
		t.Fatal("FindStepRef must return a pointer into the config")
	}
}

func TestFindStepRefErrorsNameTheFix(t *testing.T) {
	c := &Config{Workflows: map[string]WorkflowDef{
		"review": {Steps: []Step{{ID: "plan"}, {ID: "post"}}},
	}}
	for ref, want := range map[string]string{
		"ghost/plan":  "no workflow or named trigger",
		"review/nope": "plan, post",
		"review[9]":   "out of range",
	} {
		_, err := c.FindStepRef(ref)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FindStepRef(%q) = %v, want an error containing %q", ref, err, want)
		}
	}
}

// There is no top-level steps: section any more — writing one is a plain
// unknown-key load error, like any other typo.
func TestTopLevelStepsSectionIsGone(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("steps:\n  fixer: { type: agent }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("a top-level steps: registry must be rejected, got %v", err)
	}
}

// …and so is the `step:` reference keyword it was reached by.
func TestStepKeywordIsGone(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("workflows:\n  w: { steps: [{ id: a, step: fixer }] }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("a step: reference keyword must be rejected, got %v", err)
	}
}

// packStep reads one step out of an instantiated pack's workflow — where a
// pack's members live now that there is no registry to namespace them into.
func packStep(t *testing.T, c *Config, ref string) Step {
	t.Helper()
	s, err := c.FindStepRef(ref)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return *s
}
