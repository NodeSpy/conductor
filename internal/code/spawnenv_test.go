package code

import (
	"context"
	"strings"
	"testing"
)

// REGRESSION: spawned host interpreters inherited the daemon's FULL
// environment — webhook secrets, tokens passed to conductor via env, all of
// it. Children now get an allowlisted base (PATH/HOME/locale/GO*) plus the
// step's own env: only.
func TestSpawnedInterpreterEnvAllowlisted(t *testing.T) {
	t.Setenv("CONDUCTOR_SUPER_SECRET", "hunter2")
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run:  "sh",
		Code: `printf '%s|%s|%s' "$CONDUCTOR_SUPER_SECRET" "$STEP_VAR" "$HOME"`,
		Env:  map[string]string{"STEP_VAR": "explicit"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := out["text"].(string)
	parts := strings.SplitN(text, "|", 3)
	if len(parts) != 3 {
		t.Fatalf("output: %v", out)
	}
	if parts[0] != "" {
		t.Fatalf("daemon secret leaked into the spawned interpreter: %q", parts[0])
	}
	if parts[1] != "explicit" {
		t.Fatalf("step env: did not reach the child: %q", parts[1])
	}
	if parts[2] == "" {
		t.Fatal("allowlisted HOME missing from the child env")
	}
}
