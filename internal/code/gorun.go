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

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// execGoToolchain runs `run: go` by writing Code out as a real main.go and
// invoking the host's own `go run` on it — the "I need the full language and
// I'm fine requiring the Go toolchain be installed" engine. It is a HOST
// INTERPRETER in everything but name (the toolchain is a program on the box,
// like python3 or node), which is why it only ever runs locally here; a
// remote `run: go` step goes through execRemote/hostinterp.go instead,
// which treats "go" as just another interpreter name on the target host.
//
// The program is a complete, ordinary Go program: it must read the step's
// ctx as JSON from stdin and print its result as JSON on stdout (there is no
// injected `ctx` variable — `go run` runs an arbitrary compiled binary,
// which has no hook into conductor's process to inject anything through
// besides stdin/env/args). Non-JSON or blank stdout still produces outputs
// via the shared ParseOutputs contract; a non-zero exit is an error that
// includes the program's stderr.
func (e *Executor) execGoToolchain(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	goBin, err := e.lookPath()("go")
	if err != nil {
		return nil, fmt.Errorf("code: go not found on PATH — run: go needs the Go toolchain installed on this box")
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

	fullArgv := append([]string{goBin, "run", mainPath}, spec.Args...)
	env := append(spawnBaseEnv(), envSlice(spec.Env)...)

	// isolation: is OPT-IN and per-step, exactly like execCLILocal/
	// execHostLocal: a spec with no Isolation runs this exact argv bare,
	// precisely as it always has. See those two for the fuller account of
	// the wrap and its best-effort (IsolationDefaulted) vs. fail-closed
	// (explicit) split.
	cleanup := func() {}
	if spec.Isolation != nil {
		deps := e.Sandbox
		deps.Confine = true
		// go run needs to read main.go (and args), and the toolchain needs
		// somewhere of its own to build and cache modules: the operator's
		// real GOCACHE/GOMODCACHE are hidden by the jail's absence-based
		// confinement like everything else, so a jailed `run: go` step gets
		// a private, conductor-owned cache instead (shared across confined
		// go steps for the cache's benefit; never the operator's own).
		deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: tmpDir, RO: true})
		buildCache, modCache, cacheErr := e.goCacheDirs()
		var werr error
		var wrapped, wrappedEnv []string
		var c func()
		if cacheErr != nil {
			werr = cacheErr
		} else {
			deps.ExtraBinds = append(deps.ExtraBinds,
				sandbox.BindMount{Path: buildCache}, sandbox.BindMount{Path: modCache})
			env = append(env, "GOCACHE="+buildCache, "GOMODCACHE="+modCache)
			wrapped, wrappedEnv, c, werr = sandbox.WrapLocalCommand(sandbox.FromConfig(spec.Isolation), fullArgv, spec.WorkDir, env, deps)
		}
		switch {
		case werr == nil:
			fullArgv, env, cleanup = wrapped, wrappedEnv, c
		case spec.IsolationDefaulted:
			// A default-confined `run: go` step that cannot be jailed on this
			// box degrades to a bare run + warning (best-effort), rather than
			// breaking the step. An EXPLICIT isolation: fails closed below.
			e.warnf("code: go: default sandbox unavailable (%v) — running WITHOUT isolation; declare isolation: explicitly to require it", werr)
		default:
			return nil, fmt.Errorf("code: go: isolation: %w", werr)
		}
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, fullArgv[0], fullArgv[1:]...)
	cmd.Dir = spec.WorkDir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(dataJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("code: go: run: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseOutputs(stdout.String()), nil
}

// goCacheDirs resolves (and creates) the private build/module cache a
// confined `run: go` step binds read-write — e.GoCacheDir when set, else
// os.UserCacheDir()/conductor/code-go. Two subdirectories (build, mod) so
// GOCACHE and GOMODCACHE never alias.
func (e *Executor) goCacheDirs() (buildCache, modCache string, err error) {
	root := e.GoCacheDir
	if root == "" {
		base, uerr := os.UserCacheDir()
		if uerr != nil || base == "" {
			base = os.TempDir()
		}
		root = filepath.Join(base, "conductor", "code-go")
	}
	buildCache = filepath.Join(root, "build")
	modCache = filepath.Join(root, "mod")
	if err := os.MkdirAll(buildCache, 0o700); err != nil {
		return "", "", fmt.Errorf("go module/build cache: %w", err)
	}
	if err := os.MkdirAll(modCache, 0o700); err != nil {
		return "", "", fmt.Errorf("go module/build cache: %w", err)
	}
	return buildCache, modCache, nil
}

// envSlice renders extra env vars in os/exec's "K=V" form, appended after
// the allowlisted base environment (later entries win when os/exec builds
// the child's actual environment) so a step's `env:` overrides it.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// spawnBaseEnv is the ambient environment a spawned interpreter gets:
// operational basics plus the Go toolchain's own variables — and nothing
// else. The daemon's full environment routinely carries credentials
// (webhook secrets, tokens passed to conductor itself via env), and code
// steps run event-derived programs; forwarding os.Environ() wholesale hands
// every one of those to the child. A step that genuinely needs an ambient
// variable passes it explicitly via env:.
func spawnBaseEnv() []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "TERM": true, "TZ": true, "LANG": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// GO* covers the toolchain (GOPATH/GOCACHE/GOMODCACHE/GOTOOLCHAIN/…)
		// run: go needs; LC_* covers locale.
		if allow[k] || strings.HasPrefix(k, "GO") || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}
