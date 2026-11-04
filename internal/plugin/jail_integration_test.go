//go:build linux || darwin

package plugin

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// requireJail skips (or, with CONDUCTOR_REQUIRE_SANDBOX=1, fails) where this
// box cannot build the plugin jail.
func requireJail(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short")
	}
	if why := agentsandbox.Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the plugin jail cannot be built here: %s", why)
		}
		t.Skipf("no jail here: %s", why)
	}
}

// buildGo builds pkg into dir/name.
func buildGo(t *testing.T, pkg, dir, name string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// jailSelf is the conductor binary a jailed launch re-executes as its
// in-namespace forwarder (Linux only).
func jailSelf(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return ""
	}
	return buildGo(t, "github.com/NodeSpy/conductor/cmd/conductor", t.TempDir(), "conductor")
}

// realDir is a fresh directory with symlinks resolved (macOS /var → /private/var).
func realDir(t *testing.T, parent, pattern string) string {
	t.Helper()
	d, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	return d
}

func serveText(t *testing.T, body string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

// TestPluginJailHolds launches a connector plugin under the default jail
// (sandbox.plugins: required, so a jail that cannot be built fails the test
// rather than falling back) and has it try what the jail must refuse — HOME
// outside its allowances, the daemon's state, an undeclared host through the
// egress proxy, a direct dial, an undeclared command — next to what it must
// allow: its declared host through the proxy, its declared command, its own
// HOME, an operator fs: path, a file its config points at (read-only), and
// serving the address its config sets.
func TestPluginJailHolds(t *testing.T) {
	requireJail(t)
	self := jailSelf(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// Decoys in the real HOME (on macOS the Seatbelt profile is an
	// allow-list over real paths, so where the decoy lives matters).
	h := realDir(t, home, ".jailprobe-")
	secret := filepath.Join(h, "id_decoy")
	state := filepath.Join(h, "state.json")
	allowedDir := filepath.Join(h, "allowed")
	pointed := filepath.Join(h, "pointed.pem")
	os.MkdirAll(allowedDir, 0o700)
	for p, b := range map[string]string{secret: "DECOY-KEY", state: "DECOY-STATE", filepath.Join(allowedDir, "f"): "ALLOWED", pointed: "PEM"} {
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := buildGo(t, "github.com/NodeSpy/conductor/test/plugins/acme-jailprobe", realDir(t, "", "jp-bin-"), "acme-jailprobe")
	stage := realDir(t, "", "jp-stage-")

	_, up := serveText(t, "UP")
	_, other := serveText(t, "LEAK")
	listen := freeLoopbackAddr(t)

	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	var logs []string
	spec := Spec{
		Name: "jailprobe", Kind: KindConnector, Provides: "acme-jailprobe", BinPath: bin, Local: true,
		Manifest: Manifest{Egress: []string{up}, Commands: []string{"true"}},
		FS:       []string{allowedDir},
		Paths:    []string{pointed},
		Listen:   []string{listen},
	}
	c := NewClient(spec, Deps{
		Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		Sandbox: SandboxDeps{
			Self: self, StagingRoot: stage, MaskPaths: []string{h},
			EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sh := "/bin/sh"
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "jailprobe", Verb: "probe", Options: map[string]any{
		"read":      strings.Join([]string{secret, state, filepath.Join(allowedDir, "f"), pointed}, "\n"),
		"write":     strings.Join([]string{filepath.Join(h, "planted"), pointed, filepath.Join(allowedDir, "w")}, "\n"),
		"proxy_get": strings.Join([]string{"http://" + up + "/", "http://" + other + "/"}, "\n"),
		"dial":      strings.Join([]string{other, up, "1.1.1.1:443"}, "\n"),
		"exec":      strings.Join([]string{"true", sh, "/usr/bin/env"}, "\n"),
		"listen":    listen,
	}})
	t.Logf("spawn log (%s):\n  %s", runtime.GOOS, strings.Join(logs, "\n  "))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	report, _ := out["report"].(map[string]any)
	for k, v := range report {
		t.Logf("  %-60s %v", k, v)
	}
	want := map[string]bool{
		"read " + secret:                          false,
		"read " + state:                           false,
		"read " + filepath.Join(allowedDir, "f"):  true,
		"read " + pointed:                         true,
		"write " + pointed:                        false,
		"write " + filepath.Join(allowedDir, "w"): false,
		"proxy_get http://" + up + "/":            true,
		"proxy_get http://" + other + "/":         false,
		"dial " + other:                           false,
		"dial " + up:                              false,
		"dial 1.1.1.1:443":                        false,
		"exec true":                               true,
		"exec " + sh:                              false,
		"exec /usr/bin/env":                       false,
		"listen " + listen:                        true,
		"home writable":                           true,
	}
	for k, ok := range want {
		got, present := report[k].(string)
		if !present {
			t.Errorf("%s: no result", k)
			continue
		}
		if strings.HasPrefix(got, "ok") != ok {
			t.Errorf("%s: %s (want allowed=%v)", k, got, ok)
		}
	}
	if got := report["proxy_get http://"+up+"/"]; got != "ok UP" {
		t.Errorf("the declared host through the proxy answered %v", got)
	}
	// The jail's root is a private tmpfs; a write outside what is bound in
	// (here, into a directory that only exists as a mount's parent) may
	// land there, but never on the host.
	if _, err := os.Stat(filepath.Join(h, "planted")); err == nil {
		t.Error("a write into HOME reached the host")
	}
	if hm, _ := report["home"].(string); !strings.HasPrefix(hm, stage) {
		t.Errorf("HOME in the jail is %q, want under the staging dir %s", hm, stage)
	}

	// The address the config sets is served from the host side.
	conn, err := net.DialTimeout("tcp", listen, 5*time.Second)
	if err != nil {
		t.Fatalf("the plugin's listen address is not reachable from the host: %v", err)
	}
	line, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if strings.TrimSpace(line) != "JAILPROBE-OK" {
		t.Fatalf("listen address answered %q", line)
	}

	var jailLine bool
	for _, l := range logs {
		if strings.Contains(l, "OS jail") {
			jailLine = true
		}
	}
	if !jailLine {
		t.Errorf("no spawn log line names the OS jail: %v", logs)
	}
}
