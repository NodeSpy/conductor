package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// H4: a `use: cli` runtime dropped the resolved model entirely — an exact
// `model:` pin launched whatever the tool defaults to, silently.
func TestCLILaunchCarriesTheResolvedModel(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			c := newCLIController("r", config.ControllerConfig{Tool: tool}, nil)
			var got []string
			c.launch = func(_ context.Context, _ string, _ []string, argv []string) (cliProc, error) {
				got = argv
				return &fakeProc{}, nil
			}
			_, err := c.NewSession(context.Background(), Spec{
				Request: dispatch.Request{Model: "claude-opus-5"},
			}, nil)
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			joined := strings.Join(got, " ")
			if !strings.Contains(joined, "--model claude-opus-5") {
				t.Fatalf("the resolved model must reach the argv, got %q", joined)
			}
		})
	}
}

// A bare launch stays bare: nothing is injected when no model resolved.
func TestCLIBareLaunchPassesNoModelFlag(t *testing.T) {
	c := newCLIController("r", config.ControllerConfig{Tool: "claude"}, nil)
	var got []string
	c.launch = func(_ context.Context, _ string, _ []string, argv []string) (cliProc, error) {
		got = argv
		return &fakeProc{}, nil
	}
	if _, err := c.NewSession(context.Background(), Spec{}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got, " "), "--model") {
		t.Fatalf("a bare launch must pass no model flag, got %v", got)
	}
}

// An operator-written `command:` owns its argv — conductor must not guess
// at a flag the binary may not accept.
func TestCLICustomCommandGetsNoInventedFlag(t *testing.T) {
	c := newCLIController("r", config.ControllerConfig{Command: []string{"/bin/mytool", "run"}}, nil)
	var got []string
	c.launch = func(_ context.Context, _ string, _ []string, argv []string) (cliProc, error) {
		got = argv
		return &fakeProc{}, nil
	}
	if _, err := c.NewSession(context.Background(), Spec{
		Request: dispatch.Request{Model: "m1"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got, " "), "--model") {
		t.Fatalf("a hand-written command: must not gain an invented flag, got %v", got)
	}
}
