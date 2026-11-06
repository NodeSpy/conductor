//go:build linux

package plugin

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestDeclaredJailLinux runs a probe program through the real plugin launch
// (buildCommand: unshare + `conductor sandbox-net` with the minimal base and
// the egress proxy) and checks that it gets exactly what its manifest
// declares: the declared file, directory, command, variable, loopback
// destination and listen address work; everything else is denied.
func TestDeclaredJailLinux(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}
	dir := t.TempDir()
	conductor := filepath.Join(dir, "conductor")
	if out, err := exec.Command("go", "build", "-o", conductor, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}
	binDir := filepath.Join(dir, "plugins", "probe", "v1")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(binDir, "jailprobe")
	if out, err := exec.Command("go", "build", "-o", probe, "./testdata/jailprobe").CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}

	home := filepath.Join(dir, "home")
	readDir := filepath.Join(home, ".config", "tool")
	writeDir := filepath.Join(dir, "data")
	for _, d := range []string{readDir, writeDir, filepath.Join(home, ".ssh")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(readDir, "login.yml"), "token: declared")
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	write(secret, "undeclared")
	sibling := filepath.Join(dir, "sibling-secret")
	write(sibling, "undeclared")
	t.Setenv("HOME", home)
	jailHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { jailHome = os.UserHomeDir })
	t.Setenv("PROBE_DECLARED", "yes")
	t.Setenv("PROBE_UNDECLARED", "leak")

	// A host loopback service the probe declares, and one it does not.
	declared := httpOn(t, "declared-loopback")
	undeclared := httpOn(t, "undeclared-loopback")
	listenPort := freePort(t)

	pm := sandbox.NewProxyManager(nil)
	t.Cleanup(pm.Close)
	sd := SandboxDeps{Self: conductor, StagingRoot: filepath.Join(dir, "staging"), EgressUnix: pm.UnixEndpoint}
	s := Spec{
		Name: "probe", Kind: KindConnector, BinPath: probe,
		Conns: []map[string]any{{"svc": "http://" + declared}},
		Manifest: Manifest{
			Egress:   []string{"config:svc", "allowed.example.com:443"},
			Listen:   []string{"127.0.0.1:" + strconv.Itoa(listenPort)},
			Read:     []string{"~/.config/tool"},
			Write:    []string{writeDir},
			Commands: []string{"cat"},
			Env:      []string{"PROBE_DECLARED"},
		},
		Args: []string{
			"read=" + filepath.Join(readDir, "login.yml"),
			"read=" + secret,
			"read=" + sibling,
			"exists=" + conductor,
			"write=" + filepath.Join(writeDir, "out"),
			"write=" + filepath.Join(readDir, "tamper"),
			"writeexec=" + filepath.Join(writeDir, "x.sh"),
			"copyexec=" + filepath.Join(writeDir, "copy"),
			"copyexec=/tmp/copy",
			"copyexec=/dev/shm/copy",
			"mkdir=/",
			"mkdir=/usr/bin",
			"run=cat",
			"run=ls",
			"run=/usr/bin/ls",
			"ls=/usr/bin",
			"exists=/etc/fstab",
			"exists=/opt",
			"env=PROBE_DECLARED",
			"env=PROBE_UNDECLARED",
			"get=http://" + declared,
			"get=http://" + undeclared,
			"get=http://blocked.example.com/",
			"dial=1.1.1.1:443",
			"listen=127.0.0.1:" + strconv.Itoa(listenPort),
		},
	}
	cmd, cleanup, sandboxed, err := buildCommand(s, sd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !sandboxed {
		t.Fatal("the launch is not jailed")
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	sc := bufio.NewScanner(stdout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			line := sc.Text()
			if line == "READY" {
				return
			}
			if k, v, ok := strings.Cut(line, "\t"); ok {
				got[k] = v
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("probe did not finish; stderr:\n%s", stderr.String())
	}

	// The declared listen address, reached from the host.
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://127.0.0.1:" + strconv.Itoa(listenPort))
	if err != nil {
		t.Errorf("declared listen address unreachable from the host: %v", err)
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "from-inside" {
			t.Errorf("listen: got %q", b)
		}
	}
	stdin.Close()
	_ = cmd.Wait()

	want := map[string]string{
		"read=" + filepath.Join(readDir, "login.yml"): "ok",
		"read=" + secret:                               "denied",
		"read=" + sibling:                              "denied",
		"exists=" + conductor:                          "denied",
		"write=" + filepath.Join(writeDir, "out"):      "ok",
		"write=" + filepath.Join(readDir, "tamper"):    "denied",
		"writeexec=" + filepath.Join(writeDir, "x.sh"): "denied",
		"copyexec=" + filepath.Join(writeDir, "copy"):  "denied",
		"copyexec=/tmp/copy":                           "denied",
		"copyexec=/dev/shm/copy":                       "denied",
		"mkdir=/":                                      "denied",
		"mkdir=/usr/bin":                               "denied",
		"run=cat":                                      "ok",
		"run=ls":                                       "denied",
		"run=/usr/bin/ls":                              "denied",
		"ls=/usr/bin":                                  "ok:cat",
		"exists=/etc/fstab":                            "denied",
		"exists=/opt":                                  "denied",
		"env=PROBE_DECLARED":                           "set:yes",
		"env=PROBE_UNDECLARED":                         "unset",
		"get=http://" + declared:                       "ok:declared-loopback",
		"get=http://" + undeclared:                     "denied",
		"get=http://blocked.example.com/":              "denied",
		"dial=1.1.1.1:443":                             "denied",
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s: no result (stderr: %s)", k, stderr.String())
			continue
		}
		if !strings.HasPrefix(g, w) {
			t.Errorf("%s: got %q, want %s", k, g, w)
		}
	}
	if t.Failed() || os.Getenv("JAIL_VERBOSE") != "" {
		t.Logf("all results: %v\nstderr:\n%s", got, stderr.String())
	}
}

func httpOn(t *testing.T, body string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
