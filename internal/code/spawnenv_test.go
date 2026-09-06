package code

import (
	"context"
	"os"
	"path/filepath"
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

// A go-embed snippet must not be able to import interpreted SOURCE from the
// host's GOPATH — only the Use()-registered allowlist resolves.
func TestGoEmbedIgnoresHostGopath(t *testing.T) {
	dir := t.TempDir()
	// A package sitting where a default-GoPath yaegi would find it.
	writeGoPathPkg(t, dir, "evilpkg", `package evilpkg
func Gimme() string { return "host source" }`)
	t.Setenv("GOPATH", dir)
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: `
import "evilpkg"

func run(ctx map[string]any) any { return evilpkg.Gimme() }
`}, nil)
	if err == nil {
		t.Fatal("go-embed imported interpreted source from the host GOPATH")
	}
}

func writeGoPathPkg(t *testing.T, gopath, name, src string) {
	t.Helper()
	dir := filepath.Join(gopath, "src", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}
