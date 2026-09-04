package hosts

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

func TestClientArgs(t *testing.T) {
	cases := []struct {
		name string
		cl   Client
		tgt  Target
		want []string
	}{
		{
			name: "bare host",
			tgt:  Target{Name: "box", Cfg: config.HostConfig{Host: "example.com"}},
			want: []string{"ssh", "-o", "BatchMode=yes", "example.com", "--", "echo hi"},
		},
		{
			name: "user+host",
			tgt:  Target{Name: "box", Cfg: config.HostConfig{Host: "example.com", User: "deploy"}},
			want: []string{"ssh", "-o", "BatchMode=yes", "deploy@example.com", "--", "echo hi"},
		},
		{
			name: "port",
			tgt:  Target{Cfg: config.HostConfig{Host: "example.com", Port: 2222}},
			want: []string{"ssh", "-o", "BatchMode=yes", "-p", "2222", "example.com", "--", "echo hi"},
		},
		{
			name: "key",
			tgt:  Target{Cfg: config.HostConfig{Host: "example.com", Key: "/home/u/.ssh/id_ed25519"}},
			want: []string{"ssh", "-o", "BatchMode=yes", "-i", "/home/u/.ssh/id_ed25519", "example.com", "--", "echo hi"},
		},
		{
			name: "known_hosts enables strict checking",
			tgt:  Target{Cfg: config.HostConfig{Host: "example.com", KnownHosts: "/etc/conductor/known_hosts"}},
			want: []string{
				"ssh", "-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/etc/conductor/known_hosts", "-o", "StrictHostKeyChecking=yes",
				"example.com", "--", "echo hi",
			},
		},
		{
			name: "all options, in order",
			tgt: Target{Cfg: config.HostConfig{
				Host: "example.com", User: "deploy", Port: 2222,
				Key: "/k", KnownHosts: "/kh",
			}},
			want: []string{
				"ssh", "-o", "BatchMode=yes",
				"-p", "2222", "-i", "/k",
				"-o", "UserKnownHostsFile=/kh", "-o", "StrictHostKeyChecking=yes",
				"deploy@example.com", "--", "echo hi",
			},
		},
		{
			name: "custom ssh binary",
			cl:   Client{SSHBin: "/opt/bin/ssh"},
			tgt:  Target{Cfg: config.HostConfig{Host: "example.com"}},
			want: []string{"/opt/bin/ssh", "-o", "BatchMode=yes", "example.com", "--", "echo hi"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cl.Args(tc.tgt, "echo hi")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Args() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestShQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"hello", "'hello'"},
		{"hello world", "'hello world'"},
		{"it's", `'it'\''s'`},
		{"a 'b' $c", `'a '\''b'\'' $c'`},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
	}
	for _, tc := range cases {
		if got := shQuote(tc.in); got != tc.want {
			t.Errorf("shQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClientScript_WrapsEnvAndCwd(t *testing.T) {
	var gotArgv []string
	var gotStdin []byte
	cl := &Client{Run: func(_ context.Context, argv []string, stdin []byte) (string, string, int, error) {
		gotArgv = argv
		gotStdin = stdin
		return "out", "", 0, nil
	}}

	tgt := Target{Name: "box", Cfg: config.HostConfig{
		Host: "example.com",
		Cwd:  "/srv/app",
		Env:  map[string]string{"A": "target-a", "B": "target-b"},
	}}

	res, err := cl.Script(context.Background(), tgt, "run-the-thing", []byte("stdin-data"), map[string]string{"A": "call-wins", "C": "call-c"}, "")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	if res.Stdout != "out" || res.ExitCode != 0 {
		t.Fatalf("Script result = %+v", res)
	}
	if string(gotStdin) != "stdin-data" {
		t.Fatalf("stdin = %q, want %q", gotStdin, "stdin-data")
	}

	remote := gotArgv[len(gotArgv)-1]
	wantRemote := "sh -c " + shQuote(
		"export A="+shQuote("call-wins")+"; "+
			"export B="+shQuote("target-b")+"; "+
			"export C="+shQuote("call-c")+"; "+
			"cd "+shQuote("/srv/app")+" && "+
			"run-the-thing",
	)
	if remote != wantRemote {
		t.Errorf("remote command =\n%s\nwant\n%s", remote, wantRemote)
	}
}

func TestClientScript_CwdFallsBackToTargetCwd(t *testing.T) {
	var gotArgv []string
	cl := &Client{Run: func(_ context.Context, argv []string, _ []byte) (string, string, int, error) {
		gotArgv = argv
		return "", "", 0, nil
	}}
	tgt := Target{Cfg: config.HostConfig{Host: "h", Cwd: "/default/dir"}}
	if _, err := cl.Script(context.Background(), tgt, "cmd", nil, nil, ""); err != nil {
		t.Fatalf("Script: %v", err)
	}
	wantRemote := "sh -c " + shQuote("cd "+shQuote("/default/dir")+" && cmd")
	if got := gotArgv[len(gotArgv)-1]; got != wantRemote {
		t.Errorf("expected fallback cwd in remote command:\ngot  %s\nwant %s", got, wantRemote)
	}

	if _, err := cl.Script(context.Background(), tgt, "cmd", nil, nil, "/override"); err != nil {
		t.Fatalf("Script: %v", err)
	}
	wantRemote = "sh -c " + shQuote("cd "+shQuote("/override")+" && cmd")
	if got := gotArgv[len(gotArgv)-1]; got != wantRemote {
		t.Errorf("expected explicit cwd to override target cwd:\ngot  %s\nwant %s", got, wantRemote)
	}
}

func TestClientScript_ExitCodeMapping(t *testing.T) {
	cl := &Client{Run: func(_ context.Context, _ []string, _ []byte) (string, string, int, error) {
		return "partial out", "boom", 17, nil
	}}
	res, err := cl.Script(context.Background(), Target{Cfg: config.HostConfig{Host: "h"}}, "cmd", nil, nil, "")
	if err != nil {
		t.Fatalf("non-255 nonzero exit should not itself be an error, got %v", err)
	}
	if res.ExitCode != 17 || res.Stdout != "partial out" || res.Stderr != "boom" {
		t.Errorf("Result = %+v", res)
	}
}

func TestClientScript_ConnectionErrorExit255(t *testing.T) {
	cl := &Client{Run: func(_ context.Context, _ []string, _ []byte) (string, string, int, error) {
		return "", "ssh: connect to host example.com port 22: Connection refused", 255, nil
	}}
	res, err := cl.Script(context.Background(), Target{Name: "prod-box", Cfg: config.HostConfig{Host: "example.com"}}, "cmd", nil, nil, "")
	if err == nil {
		t.Fatal("expected an error for ssh exit code 255")
	}
	if !strings.Contains(err.Error(), "prod-box") {
		t.Errorf("error should name the target: %v", err)
	}
	if res.ExitCode != 255 {
		t.Errorf("ExitCode = %d, want 255", res.ExitCode)
	}
}

func TestClientScript_RunError(t *testing.T) {
	wantErr := errors.New("exec: \"ssh\": executable file not found in $PATH")
	cl := &Client{Run: func(_ context.Context, _ []string, _ []byte) (string, string, int, error) {
		return "", "", -1, wantErr
	}}
	_, err := cl.Script(context.Background(), Target{Name: "box", Cfg: config.HostConfig{Host: "h"}}, "cmd", nil, nil, "")
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped run error, got %v", err)
	}
	if !strings.Contains(err.Error(), "box") {
		t.Errorf("error should name the target: %v", err)
	}
}

// TestClientScript_LocalIntegration proves the sh -c wrapper produced by
// Script actually behaves like a real shell would run it: env exported, cwd
// honored, stdin reaching the inner script, and a value containing quotes/
// $-metacharacters surviving the quoting round trip. It injects a Run that
// shells the built remote command locally (via /bin/sh, not a real ssh
// connection) rather than substituting SSHBin="sh" — the argv shape ssh vs.
// a bare shell expect is different (see Args), so the fake has to unwrap the
// same way a remote sshd would: exec the trailing argv element as shell
// input.
func TestClientScript_LocalIntegration(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no /bin/sh on this system")
	}

	localRun := func(ctx context.Context, argv []string, stdin []byte) (string, string, int, error) {
		remote := argv[len(argv)-1]
		return runLocal(ctx, []string{"sh", "-c", remote}, stdin)
	}
	cl := &Client{Run: localRun}

	tgt := Target{Cfg: config.HostConfig{
		Host: "unused", // localRun never actually connects anywhere
		Cwd:  "/tmp",
		Env:  map[string]string{"GREETING": "a 'b' $c"},
	}}
	script := `printf '%s|%s|' "$GREETING" "$(pwd)"; cat`
	res, err := cl.Script(context.Background(), tgt, script, []byte("stdin-payload"), nil, "")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	want := "a 'b' $c|/tmp|stdin-payload"
	if res.Stdout != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestRunLocal_ExitCode(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no /bin/sh on this system")
	}
	stdout, stderr, code, err := runLocal(context.Background(), []string{"sh", "-c", "echo out; echo err >&2; exit 3"}, nil)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	if code != 3 || strings.TrimSpace(stdout) != "out" || strings.TrimSpace(stderr) != "err" {
		t.Errorf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
}

func TestRunLocal_BinaryNotFound(t *testing.T) {
	_, _, code, err := runLocal(context.Background(), []string{"definitely-not-a-real-binary-xyz"}, nil)
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
}

func TestWArgs(t *testing.T) {
	c := &Client{}
	tgt := Target{Name: "box", Cfg: config.HostConfig{
		Host: "b01", User: "ci", Port: 2222, Key: "/k", KnownHosts: "/kh",
	}}
	got := strings.Join(c.WArgs(tgt, "127.0.0.1:39481"), " ")
	want := "ssh -o BatchMode=yes -W 127.0.0.1:39481 -p 2222 -i /k -o UserKnownHostsFile=/kh -o StrictHostKeyChecking=yes ci@b01"
	if got != want {
		t.Fatalf("WArgs:\n got %s\nwant %s", got, want)
	}
}

// TestDialViaRoundTrip proves the ssh -W stdio adapter behaves as a net.Conn:
// a stub "ssh" that just echoes stdin back (exec cat) round-trips bytes, and
// Close reaps the subprocess.
func TestDialViaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "ssh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{SSHBin: stub}
	conn, err := c.DialVia(context.Background(), Target{Name: "box", Cfg: config.HostConfig{Host: "b"}}, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping\n" {
		t.Fatalf("round trip: %q", buf)
	}
	if conn.LocalAddr().Network() != "ssh-w" {
		t.Errorf("addr network: %s", conn.LocalAddr().Network())
	}
}
