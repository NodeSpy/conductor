package connector

import (
	"context"
	"strings"
	"testing"
)

func TestCommandRunLocal(t *testing.T) {
	reg := buildSinkRegistry(t, "connectors:\n  box: { type: command, env: { GREETING: hello } }\n")
	in, _ := reg.Get("box")

	// argv form
	out, err := in.Invoke(context.Background(), "run", map[string]any{"command": []any{"echo", "a b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["stdout"] != "a b c\n" || out["exit_code"] != 0 {
		t.Fatalf("argv run: %v", out)
	}

	// string form runs through sh (env from the connection + the call).
	out, err = in.Invoke(context.Background(), "run", map[string]any{
		"command": `printf '%s-%s' "$GREETING" "$EXTRA"`,
		"env":     map[string]any{"EXTRA": "there"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["stdout"] != "hello-there" {
		t.Fatalf("string run env: %v", out)
	}

	// Non-zero exit is data, not an invocation error.
	out, err = in.Invoke(context.Background(), "run", map[string]any{"command": "echo oops >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if out["exit_code"] != 3 || out["stderr"] != "oops\n" {
		t.Fatalf("exit run: %v", out)
	}

	// cwd is honored.
	out, err = in.Invoke(context.Background(), "run", map[string]any{"command": "pwd", "cwd": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out["stdout"].(string), "/") {
		t.Fatalf("cwd run: %v", out)
	}

	// Bad command shape errors clearly.
	if _, err := in.Invoke(context.Background(), "run", map[string]any{"command": 42}); err == nil || !strings.Contains(err.Error(), "argv list") {
		t.Fatalf("bad command shape: %v", err)
	}
}

// TestCommandRunRemote proves the #36 example shape — a command connector
// with host: whose run verb executes over SSH — by injecting a hosts.Client
// Run that executes the built remote command through a local sh (the same
// technique the hosts package tests use).
func TestCommandRunRemote(t *testing.T) {
	cfgYAML := `
connectors:
  build-box: { type: command, host: build-box, cwd: /tmp, env: { CI: "1" } }
hosts:
  build-box: { host: build01.internal, user: ci }
`
	reg := buildSinkRegistry(t, cfgYAML)
	in, _ := reg.Get("build-box")
	if in.DisabledReason != "" {
		t.Fatalf("connector disabled: %s", in.DisabledReason)
	}
	impl := in.Impl.(*commandImpl)
	var sshArgv []string
	impl.ssh.Run = func(ctx context.Context, argv []string, stdin []byte) (string, string, int, error) {
		sshArgv = argv
		// Emulate the remote by running the final command string locally.
		out, err := runLocalScript(ctx, argv[len(argv)-1], "", nil)
		if err != nil {
			return "", "", 0, err
		}
		return out["stdout"].(string), out["stderr"].(string), out["exit_code"].(int), nil
	}

	// argv form: elements are exec-literal — the remote shell must NOT expand
	// them ("$CI" stays "$CI"), exactly like a local exec argv.
	out, err := in.Invoke(context.Background(), "run", map[string]any{"command": []any{"printf", "%s", "$CI"}})
	if err != nil {
		t.Fatal(err)
	}
	if sshArgv[0] != "ssh" || !strings.Contains(strings.Join(sshArgv, " "), "ci@build01.internal") {
		t.Fatalf("ssh argv: %v", sshArgv)
	}
	if out["stdout"] != "$CI" || out["exit_code"] != 0 {
		t.Fatalf("argv form must be exec-literal: %v", out)
	}

	// string form: runs through the remote sh, where the connection's env
	// export ($CI) and cwd apply.
	out, err = in.Invoke(context.Background(), "run", map[string]any{"command": `printf '%s@%s' "$CI" "$(pwd)"`})
	if err != nil {
		t.Fatal(err)
	}
	if out["stdout"] != "1@/tmp" {
		t.Fatalf("string form env/cwd: %v", out)
	}
}

func TestCommandConnectionValidation(t *testing.T) {
	reg := buildSinkRegistry(t, "connectors:\n  c: { type: command, host: nope }\n")
	in, _ := reg.Get("c")
	if in.DisabledReason == "" || !strings.Contains(in.DisabledReason, `unknown host "nope"`) {
		t.Fatalf("unknown host should disable: %q", in.DisabledReason)
	}
	reg = buildSinkRegistry(t, "connectors:\n  c: { type: command, host: a, ssh: { host: b } }\nhosts:\n  a: { host: x }\n")
	in, _ = reg.Get("c")
	if in.DisabledReason == "" || !strings.Contains(in.DisabledReason, "not both") {
		t.Fatalf("host+ssh should disable: %q", in.DisabledReason)
	}
}
