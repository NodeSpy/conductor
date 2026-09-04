package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// execGoToolchain runs `run: go` by writing Code out as a real main.go and
// invoking the host's own `go run` on it — the "I need the full language and
// I'm fine requiring the Go toolchain be installed" engine, as opposed to
// `run: go-embed` (yaegi, sandboxed, install-free, subset-of-Go) or `run:
// go-embed`'s remote unavailability. It only ever runs locally here; a
// remote `run: go` step goes through execRemote/hostinterp.go instead,
// which treats "go" as just another interpreter name on the target host.
//
// The program is a complete, ordinary Go program: it must read the step's
// ctx as JSON from stdin and print its result as JSON on stdout (there is no
// injected `ctx` variable the way js/go-embed provide one — `go run` runs an
// arbitrary compiled binary, which has no hook into conductor's process to
// inject anything through besides stdin/env/args). Non-JSON or blank stdout
// still produces outputs via the shared ParseOutputs contract; a non-zero
// exit is an error that includes the program's stderr.
func (e *Executor) execGoToolchain(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	if _, err := e.lookPath()("go"); err != nil {
		return nil, fmt.Errorf("code: go not found on PATH — run: go needs the Go toolchain; use run: go-embed for the install-free interpreter")
	}

	tmpDir, err := os.MkdirTemp("", "conductor-code-go-*")
	if err != nil {
		return nil, fmt.Errorf("code: go: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	mainPath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(mainPath, []byte(spec.Code), 0o600); err != nil {
		return nil, fmt.Errorf("code: go: write main.go: %w", err)
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: go: marshal ctx: %w", err)
	}

	argv := append([]string{"run", mainPath}, spec.Args...)
	cmd := exec.CommandContext(ctx, "go", argv...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
	cmd.Stdin = bytes.NewReader(dataJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("code: go: run: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseOutputs(stdout.String()), nil
}

// envSlice renders extra env vars in os/exec's "K=V" form, appended after
// os.Environ() so a step's `env:` overrides the ambient environment (later
// entries win when os/exec builds the child's actual environment) without
// ever replacing it outright — host interpreters still need PATH, HOME,
// etc. to function normally.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
