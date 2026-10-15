package agentsandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Engine runs a container engine's CLI (docker or podman) for the
// container backend. The CLI engine is the real one; tests use a fake.
type Engine interface {
	// Bin is the engine binary the launches exec (`<bin> exec …`).
	Bin() string
	// Kind is "docker" or "podman".
	Kind() string
	// Output runs `<bin> args…` and returns its stdout.
	Output(ctx context.Context, args ...string) ([]byte, error)
	// Start runs `<bin> args…` with its stdin and stdout piped (the mux).
	Start(args ...string) (stdout io.Reader, stdin io.WriteCloser, wait func() error, err error)
}

// CLIEngine is the engine's own CLI.
type CLIEngine struct {
	bin, kind string
	// Env is the client's environment: what finds the engine (DOCKER_HOST,
	// a Docker Desktop context in HOME's ~/.docker), never the agent's.
	Env []string
}

// Bin implements Engine.
func (e *CLIEngine) Bin() string { return e.bin }

// Kind implements Engine.
func (e *CLIEngine) Kind() string { return e.kind }

// Output implements Engine.
func (e *CLIEngine) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Env = e.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return out, fmt.Errorf("%s %s: %w: %s", e.kind, firstArg(args), err, msg)
	}
	return out, nil
}

// Start implements Engine.
func (e *CLIEngine) Start(args ...string) (io.Reader, io.WriteCloser, func() error, error) {
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.Env
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return out, in, cmd.Wait, nil
}

// runQuiet runs bin args with env, discarding its output.
func runQuiet(ctx context.Context, bin string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	return cmd.Run()
}

func firstArg(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

// ResolveEngine finds the container engine: pref (docker | podman | a
// path) when set, else the platform's preference — rootless podman on
// Linux, Docker Desktop on macOS — falling back to the other. No engine is
// an error: the container backend fails closed.
func ResolveEngine(pref, goos string, lookPath func(string) (string, error), env []string) (*CLIEngine, error) {
	order := []string{"podman", "docker"}
	if goos == "darwin" {
		order = []string{"docker", "podman"}
	}
	if pref != "" {
		order = []string{pref}
	}
	var tried []string
	for _, name := range order {
		bin, err := lookPath(name)
		if err != nil {
			tried = append(tried, name)
			continue
		}
		kind := filepath.Base(name)
		if kind != "docker" && kind != "podman" {
			return nil, fmt.Errorf("agentsandbox: container engine %q is neither docker nor podman", name)
		}
		return &CLIEngine{bin: bin, kind: kind, Env: EngineClientEnv(env)}, nil
	}
	return nil, fmt.Errorf("agentsandbox: no container engine (%s) on PATH — an agent configured for a container is not launched without one", strings.Join(tried, ", "))
}

// engineClientKeys are the variables an engine CLI reads to find its
// engine and its own config.
var engineClientKeys = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "XDG_RUNTIME_DIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "DBUS_SESSION_BUS_ADDRESS": true,
}

// EngineClientEnv is the part of env an engine CLI needs: where its engine
// is (DOCKER_HOST, DOCKER_CONTEXT, CONTAINER_HOST, a rootless podman's
// XDG_RUNTIME_DIR) and its config. It is the client's: none of it is passed
// into a container.
func EngineClientEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if engineClientKeys[k] || strings.HasPrefix(k, "DOCKER_") || strings.HasPrefix(k, "CONTAINER_") || strings.HasPrefix(k, "CONTAINERS_") || strings.HasPrefix(k, "PODMAN_") {
			out = append(out, kv)
		}
	}
	return out
}
