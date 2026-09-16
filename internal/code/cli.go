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

// The `cli` ENGINE: run an arbitrary argv as a subprocess and bridge it onto
// the code-step ABI.
//
//	- run: { id: build, use: cli, command: [make, -C, ./svc, release] }
//	- run: { id: shape, use: cli, command: [python3], code: "…" }
//
// It is the generalization of the host-interpreter path, not a new mechanism
// beside it: same allowlisted base environment, same ctx-JSON-on-stdin,
// same ParseOutputs over stdout, same base64-framed script over SSH when the
// step names a `host:`. What it adds is that the PROGRAM is the operator's to
// choose word for word, instead of being one interpreter name that conductor
// then decides how to invoke.
//
// The two halves of the ABI it bridges in this increment are INPUTS and
// OUTPUTS only:
//
//	inputs   the rendered ctx document, as JSON on the command's stdin
//	outputs  ParseOutputs over the command's stdout (see outputs.go)
//
// A `cli` step has no ctx.store/ctx.sql/ctx.memory: those are in-process
// bindings held by the in-process engines, and reaching them from a
// subprocess needs a data plane over the plugin socket that does not exist
// yet. Use the `kv.*`/`sql.*`/`memory.*` verbs in surrounding steps, exactly
// as a host-interpreter step does.
//
// How `command:` and `code:` compose — ONE rule, no special cases:
//
//	command: [make, test]              argv, run as written
//	command: [python3] + code:         code goes to a private temp file and
//	                                   the PATH is appended to the argv, so
//	                                   this is `python3 /tmp/…/code`
//
// which makes `use: cli, command: [bash]` + `code:` exactly today's
// `run: bash`, and `use: cli, command: [ruby]` exactly today's `run: ruby`.
// The rule is the file, never the text: a `code:` body is arbitrary bytes,
// and an engine that sometimes passed it as an argv word would differ from
// the host-interpreter path precisely where the body got interesting
// (quotes, newlines, a NUL). A step that wants a `-c`-style inline script
// writes the whole thing as argv — `command: [bash, -c, "echo hi"]` — with
// no `code:` at all.
//
// `args:` is appended last either way, so a step can pass argv that must
// follow the script.

// cliArgv builds the local argv: the command, then the code file when there
// is one, then args.
func cliArgv(command []string, codePath string, args []string) []string {
	argv := append([]string(nil), command...)
	if codePath != "" {
		argv = append(argv, codePath)
	}
	return append(argv, args...)
}

// execCLILocal runs a `use: cli` step as a local subprocess.
func (e *Executor) execCLILocal(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	if len(spec.Command) == 0 {
		return nil, errCLINoCommand
	}
	// argv[0] resolves on PATH unless it is a path, mirroring the host
	// interpreter: a step may pin an exact binary and conductor will not
	// second-guess it.
	prog := spec.Command[0]
	if !strings.Contains(prog, "/") {
		p, err := e.lookPath()(prog)
		if err != nil {
			return nil, fmt.Errorf("code: cli: %s not found on PATH: %w", prog, err)
		}
		prog = p
	}

	codePath := ""
	if spec.Code != "" {
		// A private 0700 dir + 0600 file, like the host-interpreter path:
		// a step's source never sits in a shared tmp location, even briefly.
		tmpDir, err := os.MkdirTemp("", "conductor-code-*")
		if err != nil {
			return nil, fmt.Errorf("code: cli: temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)
		codePath = filepath.Join(tmpDir, "code")
		if err := os.WriteFile(codePath, []byte(spec.Code), 0o600); err != nil {
			return nil, fmt.Errorf("code: cli: write code file: %w", err)
		}
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: cli: marshal ctx: %w", err)
	}

	argv := cliArgv(spec.Command[1:], codePath, spec.Args)
	cmd := exec.CommandContext(ctx, prog, argv...)
	cmd.Dir = spec.WorkDir
	// Allowlisted base env only — the daemon's own environment carries
	// secrets a spawned step must not inherit (see spawnBaseEnv).
	cmd.Env = append(spawnBaseEnv(), envSlice(spec.Env)...)
	cmd.Stdin = bytes.NewReader(dataJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("code: cli: %s: %w: %s", spec.Command[0], err, strings.TrimSpace(stderr.String()))
	}
	return ParseOutputs(stdout.String()), nil
}

// execCLIRemote runs a `use: cli` step on spec.Host over SSH. It is the
// host-interpreter remote path with an argv in place of an interpreter name:
// same generated sh script, same base64 code frame, same ctx-on-stdin, same
// exit-127 "not found" convention.
func (e *Executor) execCLIRemote(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	if len(spec.Command) == 0 {
		return nil, errCLINoCommand
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: remote cli: marshal ctx: %w", err)
	}
	script := remoteCLIScript(spec.Command, spec.Code, hostLabel(spec.Host), spec.Args)

	res, err := e.sshClient().Script(ctx, *spec.Host, script, dataJSON, spec.Env, spec.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("code: remote cli: %w", err)
	}
	switch {
	case res.ExitCode == remoteNotFoundExit:
		return nil, fmt.Errorf("code: remote cli: %s not found on host %s: %s", spec.Command[0], hostLabel(spec.Host), strings.TrimSpace(res.Stderr))
	case res.ExitCode != 0:
		return nil, fmt.Errorf("code: remote cli: exit %d: %s", res.ExitCode, tail(res.Stderr))
	}
	return ParseOutputs(res.Stdout), nil
}

// remoteCLIScript builds the generated remote sh script for a cli step:
//
//	t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
//	printf '%s' '<base64 of Code>' | base64 -d > "$t/code"     # only with code:
//	command -v <argv0> >/dev/null 2>&1 || { echo "…" >&2; exit 127; }
//	exec <argv…> "$t/code" <args…>
//
// Every word is shell-quoted and the code travels base64-encoded rather than
// interpolated, so an argv word or a script body containing quotes, newlines
// or control characters cannot break out of the generated script. ctx is not
// in the script at all — it is the stdin hosts.Client.Script attaches.
func remoteCLIScript(command []string, code, hostName string, args []string) string {
	var b strings.Builder
	b.WriteString("t=$(mktemp -d); trap 'rm -rf \"$t\"' EXIT\n")
	if code != "" {
		fmt.Fprintf(&b, "printf '%%s' %s | base64 -d > \"$t/code\"\n",
			shQuote(base64.StdEncoding.EncodeToString([]byte(code))))
	}
	fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 || { echo %s >&2; exit %d; }\n",
		shQuote(command[0]),
		shQuote(fmt.Sprintf("conductor: %s not found on remote %s", command[0], hostName)),
		remoteNotFoundExit)
	b.WriteString("exec")
	for _, w := range command {
		b.WriteString(" " + shQuote(w))
	}
	if code != "" {
		b.WriteString(" \"$t/code\"")
	}
	for _, a := range args {
		b.WriteString(" " + shQuote(a))
	}
	return b.String()
}

// errCLINoCommand is the belt to config validation's braces: the loader
// already rejects `use: cli` without a `command:`, but a Spec can be built
// directly (an expanded workflow call, a test) and an empty argv would
// otherwise reach exec as an opaque failure.
var errCLINoCommand = fmt.Errorf("code: cli: no command: — the cli engine runs an argv (e.g. command: [make, test])")
