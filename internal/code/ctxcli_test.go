package code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// The whole round trip, with a real subprocess on the other end: a `use: cli`
// command reads a value out of the store through the socket, writes one back,
// and finds that the guard it cannot see refused the write it was not allowed
// to make.

// ctxClientHelperEnv switches the test binary into "be the reference client"
// mode (see TestCtxClientHelperProcess).
const ctxClientHelperEnv = "CONDUCTOR_CTX_TEST_CLIENT"

// TestCtxClientHelperProcess is not a test: re-executed with
// CONDUCTOR_CTX_TEST_CLIENT=1 it IS the `conductor ctx` client, so the
// subprocess tests below exercise the shipped CtxClientMain rather than a
// second implementation written for the occasion.
func TestCtxClientHelperProcess(t *testing.T) {
	if os.Getenv(ctxClientHelperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a ctx socket test")
	}
	args := []string{}
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	// The real command line is `conductor ctx <kind> …`; main.go strips the
	// subcommand word before calling CtxClientMain, so strip it here too and
	// the snippets below read exactly as an operator would write them.
	if len(args) > 0 && args[0] == "ctx" {
		args = args[1:]
	}
	os.Exit(CtxClientMain(args, os.Getenv, os.Stdout, os.Stderr))
}

// ctxHelperScript writes a wrapper standing in for the conductor binary: a
// step's $CONDUCTOR_CTX_HELPER, pointed at this test binary's client mode.
func ctxHelperScript(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "conductor")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestCtxClientHelperProcess -- \"$@\"\n", self)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A cli step reads ctx.store through the socket, writes back through it, and
// is refused host-side on the write its policy forbids — all from a plain sh
// command that holds no store handle of its own.
func TestCLICtxStoreRoundTrip(t *testing.T) {
	needSh(t)
	st := tempKV(t)
	if err := st.Set("ns", "seed", "hello", 0); err != nil {
		t.Fatal(err)
	}
	helper := ctxHelperScript(t)

	// The guard the step cannot see or reach: the shape flow installs for an
	// agent-authored step (planDataGuard).
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		// Values cross the wire as JSON in both directions, so $seed is the
		// JSON text "hello" (quotes included) and feeding it straight back to
		// `set` round-trips the string, while wrapping it composes an object.
		Run: "cli", Command: []string{"sh"}, Code: `
seed=$("$HELPER" ctx kv s get ns seed) || exit 1
"$HELPER" ctx kv s set ns wrote "$seed" >/dev/null || exit 1
"$HELPER" ctx kv s set ns shaped "{\"from\": $seed}" >/dev/null || exit 1
"$HELPER" ctx kv s set ns parked "token=hunter2" >/dev/null 2>&1
refused=$?
"$HELPER" ctx kv other set ns k v >/dev/null 2>&1
offlist=$?
printf '{"seed": %s, "refused": %d, "offlist": %d}' "$seed" "$refused" "$offlist"
`,
		Env:       map[string]string{"HELPER": helper, ctxClientHelperEnv: "1"},
		DataGuard: guardDenyingStore("s", "hunter2"),
	}, map[string]any{"repo": "acme/api"})
	if err != nil {
		t.Fatal(err)
	}

	if out["seed"] != "hello" {
		t.Errorf("the step did not read the store through the socket: %#v", out)
	}
	// The writes it was allowed to make landed in the real store, with their
	// types intact: a string stayed a string, an object stayed an object.
	if v, found, _ := st.Get("ns", "wrote"); !found || v != "hello" {
		t.Errorf("write-back = %#v (found %v)", v, found)
	}
	if v, _, _ := st.Get("ns", "shaped"); fmt.Sprint(v) != "map[from:hello]" {
		t.Errorf("composed write-back = %#v", v)
	}
	// The two it was not are refused, with the exit code that says "policy",
	// and left no trace.
	if toIntT(t, out["refused"]) != ctxExitRefused {
		t.Errorf("secret write exit = %v, want %d (refused)", out["refused"], ctxExitRefused)
	}
	if _, found, _ := st.Get("ns", "parked"); found {
		t.Error("the guard-refused write reached the store")
	}
	if toIntT(t, out["offlist"]) != ctxExitRefused {
		t.Errorf("out-of-allowlist exit = %v, want %d (refused)", out["offlist"], ctxExitRefused)
	}
}

// The helper is exported and points at conductor's own binary; the socket
// and token are exported too, and a step's own env: cannot shadow them.
func TestCLICtxEnvExported(t *testing.T) {
	needSh(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c",
			`printf '{"sock": "%s", "tok": %s, "helper": "%s"}' \
			   "$CONDUCTOR_CTX_SOCK" "${#CONDUCTOR_CTX_TOKEN}" "$CONDUCTOR_CTX_HELPER"`},
		// A step trying to point the data plane at something of its own.
		Env: map[string]string{"CONDUCTOR_CTX_SOCK": "/tmp/mine.sock", "CONDUCTOR_CTX_TOKEN": "mine"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sock, _ := out["sock"].(string)
	if sock == "/tmp/mine.sock" || !strings.Contains(sock, "conductor-ctx-") {
		t.Fatalf("step env: shadowed the socket address: %#v", out)
	}
	if toIntT(t, out["tok"]) != ctxTokenBytes*2 { // hex
		t.Errorf("token length = %v", out["tok"])
	}
	self, _ := os.Executable()
	if out["helper"] != self {
		t.Errorf("helper = %v, want %v", out["helper"], self)
	}
}

// A command that ignores the socket is the inputs+outputs step it always
// was: the data plane costs it nothing and grants it nothing.
func TestCLICtxIsOptIn(t *testing.T) {
	needSh(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", "cat"},
	}, map[string]any{"pr": 7})
	if err != nil {
		t.Fatal(err)
	}
	if toIntT(t, out["pr"]) != 7 {
		t.Fatalf("outputs = %#v", out)
	}
}

// The client says so plainly where there is no data plane — the REMOTE cli
// case, where the socket cannot cross the ssh hop.
func TestCtxClientWithoutDataPlane(t *testing.T) {
	var stdout, stderr strings.Builder
	code := CtxClientMain([]string{"kv", "s", "get", "ns", "k"},
		func(string) string { return "" }, &stdout, &stderr)
	if code != ctxExitError || !strings.Contains(stderr.String(), "no ctx data plane") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "LOCAL") {
		t.Errorf("the message should say why: %q", stderr.String())
	}
}

// A remote cli step gets inputs and outputs but no socket — the variables
// are absent rather than pointing at a socket the remote box cannot reach.
func TestCLIRemoteHasNoCtxSocket(t *testing.T) {
	needSh(t)
	e := &Executor{SSH: localSSH(t)}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c",
			`printf '{"sock": "%s"}' "${CONDUCTOR_CTX_SOCK:-absent}"`},
		Host: &hosts.Target{Name: "build-box"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["sock"] != "absent" {
		t.Fatalf("a remote cli step was handed a local socket: %#v", out)
	}
}

// Teardown on every exit path: the socket directory is gone once Exec
// returns, whether the command succeeded, failed, or was killed by the
// step's own deadline.
func TestCLICtxSocketTornDown(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	// The command records the socket path where the test can read it after
	// the fact — including on the paths where nothing comes back on stdout.
	record := filepath.Join(dir, "sock")
	e := &Executor{}

	cases := []struct {
		name string
		code string
		run  func(Spec) error
	}{
		{"success", `printf '%s' "$CONDUCTOR_CTX_SOCK" > "$REC"; printf '{}'`, nil},
		{"failure", `printf '%s' "$CONDUCTOR_CTX_SOCK" > "$REC"; echo boom >&2; exit 3`, nil},
		// `exec sleep` rather than `sleep`: the shell REPLACES itself, so the
		// process the deadline kills is the one holding the step's pipes.
		{"timeout", `printf '%s' "$CONDUCTOR_CTX_SOCK" > "$REC"; exec sleep 10`, func(s Spec) error {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err := e.Exec(ctx, s, nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(record)
			spec := Spec{Run: "cli", Command: []string{"sh"}, Code: tc.code,
				Env: map[string]string{"REC": record}}
			var err error
			if tc.run != nil {
				err = tc.run(spec)
			} else {
				_, err = e.Exec(context.Background(), spec, nil)
			}
			if tc.name != "success" && err == nil {
				t.Fatalf("%s: expected the step to fail", tc.name)
			}
			b, rerr := os.ReadFile(record)
			if rerr != nil {
				t.Fatalf("the command never recorded its socket: %v", rerr)
			}
			sock := strings.TrimSpace(string(b))
			if sock == "" {
				t.Fatal("no socket was exported to the command")
			}
			if _, serr := os.Stat(sock); !os.IsNotExist(serr) {
				t.Fatalf("socket survived the step (%s): %v", tc.name, serr)
			}
			if _, serr := os.Stat(filepath.Dir(sock)); !os.IsNotExist(serr) {
				t.Fatalf("socket directory survived the step (%s): %v", tc.name, serr)
			}
		})
	}
}
