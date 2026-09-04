package code

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// execHostLocal runs any `run:` value that isn't js/go-embed/go by shelling
// out to a real interpreter on this machine: spec.Run is itself the
// interpreter (sh, bash, ruby, node, python, perl, php, …) or, if it
// contains a '/', a path to one — used verbatim instead of a PATH lookup so
// a step can pin an exact interpreter (a venv's python, a non-PATH ruby,
// …) without conductor second-guessing it.
//
// Code is written to a private temp file rather than piped or passed as
// `-c` text: some interpreters need a real file (their own #!-style
// tooling, __file__/$0 introspection, line numbers in tracebacks), and a
// dedicated 0700 temp dir + 0600 file keeps a step's source off of a
// shared/world-readable tmp location even briefly. ctx reaches the program
// as JSON on stdin (the same contract `run: go` uses) since, like `go run`,
// there's no in-process hook to inject a variable through for an external
// process.
func (e *Executor) execHostLocal(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	interpPath := spec.Run
	if !strings.Contains(spec.Run, "/") {
		p, err := e.lookPath()(spec.Run)
		if err != nil {
			return nil, fmt.Errorf("code: %s not found on PATH: %w", spec.Run, err)
		}
		interpPath = p
	}

	tmpDir, err := os.MkdirTemp("", "conductor-code-*")
	if err != nil {
		return nil, fmt.Errorf("code: %s: temp dir: %w", spec.Run, err)
	}
	defer os.RemoveAll(tmpDir)

	codePath := filepath.Join(tmpDir, "code")
	if err := os.WriteFile(codePath, []byte(spec.Code), 0o600); err != nil {
		return nil, fmt.Errorf("code: %s: write code file: %w", spec.Run, err)
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: %s: marshal ctx: %w", spec.Run, err)
	}

	argv := append([]string{interpPath, codePath}, spec.Args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
	cmd.Stdin = bytes.NewReader(dataJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("code: %s: %w: %s", spec.Run, err, strings.TrimSpace(stderr.String()))
	}
	return ParseOutputs(stdout.String()), nil
}

// remoteNotFoundExit is the exit code the generated remote script uses when
// the target interpreter isn't on the remote box's PATH — chosen to match
// the shell convention for "command not found" so it reads naturally in
// logs, and (more importantly) so it's distinguishable from both a genuine
// script failure and ssh's own exit-255 connection-error convention
// (internal/hosts).
const remoteNotFoundExit = 127

// execRemote runs a host-interpreter spec on spec.Host over SSH.
// js/go-embed are rejected outright: they execute inside conductor's own
// process (see errRemoteInProcessEngine) and have no remote equivalent.
//
// The remote side is a single generated `sh` script (run through
// hosts.Client.Script, which already handles the target's env/cwd wrapping
// and connection-error/exit-code plumbing):
//
//	t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
//	printf '%s' '<base64 of Code>' | base64 -d > "$t/code"
//	command -v <interp> >/dev/null 2>&1 || { echo "conductor: <interp> not found on remote <name>" >&2; exit 127; }
//	exec <interp> "$t/code" <shell-quoted Args...>
//
// (for `run: go`, the code is moved to "$t/main.go" and the script execs
// `go run "$t/main.go"` instead — same idea, the name go run expects).
// Code travels base64-encoded rather than interpolated as a heredoc/literal
// because it can be arbitrary bytes (including embedded quotes or control
// characters); base64 sidesteps needing to quote *that* correctly on top of
// quoting the outer script. ctx is not embedded in the script at all — it
// is the stdin hosts.Client.Script attaches, exactly like the local host
// path, so it never touches argv or the generated script text.
func (e *Executor) execRemote(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	switch spec.Run {
	case "js", "go-embed":
		return nil, errRemoteInProcessEngine(spec.Run)
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: remote %s: marshal ctx: %w", spec.Run, err)
	}

	script := remoteScript(spec.Run, spec.Code, hostLabel(spec.Host), spec.Args)

	res, err := e.sshClient().Script(ctx, *spec.Host, script, dataJSON, spec.Env, spec.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("code: remote %s: %w", spec.Run, err)
	}
	switch {
	case res.ExitCode == remoteNotFoundExit:
		return nil, fmt.Errorf("code: remote %s: not found on host %s: %s", spec.Run, hostLabel(spec.Host), strings.TrimSpace(res.Stderr))
	case res.ExitCode != 0:
		return nil, fmt.Errorf("code: remote %s: exit %d: %s", spec.Run, res.ExitCode, tail(res.Stderr))
	}
	return ParseOutputs(res.Stdout), nil
}

// remoteScript builds the generated remote sh script described on
// execRemote. hostName is only used for the human-readable "not found"
// message the script itself prints to stderr on exit 127.
func remoteScript(interp, code, hostName string, args []string) string {
	var b strings.Builder
	b.WriteString("t=$(mktemp -d); trap 'rm -rf \"$t\"' EXIT\n")
	fmt.Fprintf(&b, "printf '%%s' %s | base64 -d > \"$t/code\"\n",
		shQuote(base64.StdEncoding.EncodeToString([]byte(code))))
	fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 || { echo %s >&2; exit %d; }\n",
		shQuote(interp),
		shQuote(fmt.Sprintf("conductor: %s not found on remote %s", interp, hostName)),
		remoteNotFoundExit)

	if interp == "go" {
		b.WriteString("mv \"$t/code\" \"$t/main.go\"\n")
		b.WriteString("exec go run \"$t/main.go\"")
	} else {
		fmt.Fprintf(&b, "exec %s \"$t/code\"", shQuote(interp))
	}
	for _, a := range args {
		b.WriteString(" " + shQuote(a))
	}
	return b.String()
}

// tail returns the last few lines of s (stderr), trimmed, for embedding in
// an error message without dumping an entire runaway script's output into
// the log — most stack traces/error messages put the useful part last.
func tail(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	const maxLines = 20
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
