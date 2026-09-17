package config

import (
	"strings"
	"testing"
	"time"
)

// The helper step family (helpers.go), via the real loader — the strict
// decode, the one-form-only check and the helper's own value check together.

// TestSleepStepForm: `sleep:` is its own step form, parses through the shared
// Duration type, and needs no other key.
func TestSleepStepForm(t *testing.T) {
	c, err := loadYAML(t, engineWF+`      - { id: nap, sleep: 2s }
      - { id: long, sleep: 1d12h }
`)
	if err != nil {
		t.Fatal(err)
	}
	steps := c.Workflows["w"].Steps
	if got := steps[0].Form(); got != "sleep" {
		t.Fatalf("form = %q, want sleep", got)
	}
	if !steps[0].IsHelper() || steps[0].HelperForm() != HelperSleep {
		t.Fatalf("helper form = %q, want %q", steps[0].HelperForm(), HelperSleep)
	}
	if got := steps[0].Sleep.D(); got != 2*time.Second {
		t.Fatalf("sleep = %s, want 2s", got)
	}
	// Duration's own spellings come along for free, day unit included.
	if got := steps[1].Sleep.D(); got != 36*time.Hour {
		t.Fatalf("sleep = %s, want 36h", got)
	}
	// A helper selects no engine, so the code-step machinery stays out of it.
	if _, class := steps[0].StepEngine(); class != EngineNone {
		t.Fatalf("engine class = %s, want %s", class, EngineNone)
	}
}

// TestSleepStepRejectsBadDuration: zero and negative are refused with a
// message that says what is wrong, rather than the step reading as formless.
func TestSleepStepRejectsBadDuration(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"zero", "0"},
		{"zero duration string", "0s"},
		{"negative", "-5s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAML(t, engineWF+`      - { id: nap, sleep: `+tc.value+` }
`)
			if err == nil {
				t.Fatalf("sleep: %s accepted", tc.value)
			}
			if !strings.Contains(err.Error(), "must be a POSITIVE duration") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestSleepStepExclusiveWithOtherForms: a helper is a form like any other, so
// combining it with one is the same error as combining any two.
func TestSleepStepExclusiveWithOtherForms(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{"verb", `uses: gh.comment`},
		{"code", `use: cli, command: [echo, hi]`},
		{"command", `type: command, command: [echo, hi]`},
		{"agent", `type: agent, prompt: p`},
		{"workflow call", `call: other`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAML(t, engineWF+`      - { id: nap, sleep: 2s, `+tc.extra+` }
  other:
    steps:
      - { id: x, uses: gh.comment }
`)
			if err == nil {
				t.Fatalf("sleep + %s accepted", tc.name)
			}
			if !strings.Contains(err.Error(), "step forms are mutually exclusive") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestSleepStepBetweenVerbs: the documented shape loads and validates.
func TestSleepStepBetweenVerbs(t *testing.T) {
	c, err := loadYAML(t, `
triggers:
  - on: gh.issue_comment
    name: rerun
    steps:
      - { id: cancel, uses: gh.comment, options: { body: "cancelling" } }
      - sleep: 5s
      - { id: rerun, uses: gh.comment, options: { body: "rerunning" } }
`)
	if err != nil {
		t.Fatal(err)
	}
	steps := c.Triggers[0].Steps
	if len(steps) != 3 || steps[1].Form() != "sleep" || steps[1].Sleep.D() != 5*time.Second {
		t.Fatalf("steps: %+v", steps)
	}
}

// TestSleepStepRejectsGate: a gate reviews an agent's proposed change, so it
// has nothing to say about a helper — and the error names the form so the
// operator can see why.
func TestSleepStepRejectsGate(t *testing.T) {
	_, err := loadYAML(t, `
checks:
  build: { type: command, command: [true] }
`+engineWF+`      - { id: nap, sleep: 2s, gate: { run: [build] } }
`)
	if err == nil || !strings.Contains(err.Error(), "this is a sleep step") {
		t.Fatalf("error = %v", err)
	}
}
