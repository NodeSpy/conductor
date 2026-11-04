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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
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
// egress proxy, a direct dial, an undeclared command, running a program it
// wrote into HOME, TMPDIR or /tmp — next to what it must allow: its declared
// host (a host-loopback one: through the proxy and relayed in), its
// declared command, its own HOME, an operator fs: path, a file its config
// points at (read-only), and serving the address its config sets.
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
		"drop":      strings.Join([]string{"$HOME", "$TMPDIR", "/tmp"}, "\n"),
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
		"dial " + up:                              true, // a host-loopback target: relayed in
		"drop $HOME":                              false,
		"drop $TMPDIR":                            false,
		"drop /tmp":                               false,
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

// Red-team regression (phase 4, H3): a connector that declares no egress
// used to get the host network, and could run a program it had dropped in
// its own HOME. Now it has its own namespace with only the targets its
// config names: a host-loopback service its config points at is relayed in,
// another one is not reachable, directly or through the proxy, and the
// dropped program does not run.
func TestPluginJailNoEgressConnector(t *testing.T) {
	requireJail(t)
	self := jailSelf(t)
	bin := buildGo(t, "github.com/NodeSpy/conductor/test/plugins/acme-jailprobe", realDir(t, "", "jp-bin-"), "acme-jailprobe")
	stage := realDir(t, "", "jp-stage-")
	_, configured := serveText(t, "CONFIGURED")
	_, c2 := serveText(t, "C2")

	// A program already in its HOME (from an earlier run).
	home := filepath.Join(stage, "lanconn", ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(home, "payload")
	src, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Skip("no /usr/bin/true to copy")
	}
	if err := os.WriteFile(payload, src, 0o755); err != nil {
		t.Fatal(err)
	}

	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	var logs []string
	spec := Spec{
		Name: "lanconn", Kind: KindConnector, Provides: "acme-jailprobe", BinPath: bin, Local: true,
		Targets: []string{configured}, // what its connection config points at
	}
	c := NewClient(spec, Deps{
		Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		Sandbox: SandboxDeps{
			Self: self, StagingRoot: stage,
			EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "lanconn", Verb: "probe", Options: map[string]any{
		"dial":      strings.Join([]string{configured, c2, "1.1.1.1:443"}, "\n"),
		"proxy_get": "http://" + c2 + "/",
		"exec":      payload,
	}})
	t.Logf("spawn log (%s):\n  %s", runtime.GOOS, strings.Join(logs, "\n  "))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	report, _ := out["report"].(map[string]any)
	for k, v := range report {
		t.Logf("  %-60s %v", k, v)
	}
	for k, ok := range map[string]bool{
		"dial " + configured:           true,
		"dial " + c2:                   false,
		"dial 1.1.1.1:443":             false,
		"proxy_get http://" + c2 + "/": false,
		"exec " + payload:              false,
	} {
		got, _ := report[k].(string)
		if strings.HasPrefix(got, "ok") != ok {
			t.Errorf("%s: %q (want allowed=%v)", k, got, ok)
		}
	}
	var line string
	for _, l := range logs {
		if strings.Contains(l, "OS jail") {
			line = l
		}
	}
	if strings.Contains(line, "host network") || !strings.Contains(line, "network: no direct network") {
		t.Errorf("the spawn log does not say it has its own network: %q", line)
	}

	// The same plugin with nothing declared and nothing configured: no way
	// out at all.
	bare := NewClient(Spec{Name: "bareconn", Kind: KindConnector, Provides: "acme-jailprobe", BinPath: bin, Local: true}, Deps{
		Log: func(string, ...any) {},
		Sandbox: SandboxDeps{
			Self: self, StagingRoot: stage,
			EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	defer bare.Close()
	out, err = bare.Invoke(ctx, InvokeRequest{Instance: "bareconn", Verb: "probe", Options: map[string]any{
		"dial": strings.Join([]string{configured, c2}, "\n"),
	}})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	report, _ = out["report"].(map[string]any)
	for _, a := range []string{configured, c2} {
		if got, _ := report["dial "+a].(string); strings.HasPrefix(got, "ok") {
			t.Errorf("a plugin with no egress and no targets reached %s: %s", a, got)
		}
	}
}

// A plugin whose declared command reads the operator's CLI login from its
// usual place under HOME (gh's ~/.config/gh) finds it in the jail: the
// declared fs: path is bound read-only and linked at the same place under the
// jail's HOME. Nothing else under the operator's home is there, and the
// login cannot be written.
func TestPluginJailReadsCLILoginFromHome(t *testing.T) {
	requireJail(t)
	self := jailSelf(t)
	real, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildGo(t, "github.com/NodeSpy/conductor/test/plugins/acme-jailprobe", realDir(t, "", "jp-bin-"), "acme-jailprobe")
	stage := realDir(t, "", "jp-stage-")
	// The operator's home for this test, under the real one (on macOS the
	// Seatbelt profile is an allow-list over real paths).
	op := realDir(t, real, ".jailprobe-home-")
	t.Setenv("HOME", op)
	files := map[string]string{
		".config/acme/login":    "LOGIN-OK",
		".config/other/secret":  "DECOY",
		".config/gh/hosts.yml":  "github.com:\n    oauth_token: gho_JAILPROBE\n    user: jailprobe\n    git_protocol: https\n",
		".config/gh/config.yml": "version: 1\n",
	}
	for rel, b := range files {
		p := filepath.Join(op, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commands := []string{"cat"}
	_, ghErr := exec.LookPath("gh")
	if ghErr == nil {
		commands = append(commands, "gh")
	}
	var logs []string
	c := NewClient(Spec{
		Name: "loginprobe", Kind: KindConnector, Provides: "acme-jailprobe", BinPath: bin, Local: true,
		Manifest: Manifest{Commands: commands, FS: []string{"~/.config/acme", "~/.config/gh"}},
	}, Deps{
		Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		Sandbox: SandboxDeps{
			Self: self, StagingRoot: stage, Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runs := []string{"cat $HOME/.config/acme/login", "cat $HOME/.config/other/secret", "cat " + filepath.Join(op, ".config/other/secret")}
	if ghErr == nil {
		runs = append(runs, "gh auth token")
	}
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "loginprobe", Verb: "probe", Options: map[string]any{
		"run":   strings.Join(runs, "\n"),
		"write": strings.Join([]string{filepath.Join(op, ".config/acme/planted"), filepath.Join(op, ".config/gh/hosts.yml")}, "\n"),
	}})
	t.Logf("spawn log (%s):\n  %s", runtime.GOOS, strings.Join(logs, "\n  "))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	report, _ := out["report"].(map[string]any)
	for k, v := range report {
		t.Logf("  %-60s %v", k, v)
	}
	want := map[string]string{"run cat $HOME/.config/acme/login": "ok LOGIN-OK"}
	if ghErr == nil {
		want["run gh auth token"] = "ok gho_JAILPROBE"
	} else {
		t.Log("gh is not on PATH: the gh case is skipped")
	}
	for k, w := range want {
		if got, _ := report[k].(string); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
	for k, v := range report {
		if got, _ := v.(string); (strings.HasPrefix(k, "write ") || strings.Contains(k, "other/secret")) && strings.HasPrefix(got, "ok") {
			t.Errorf("%s = %q, want it refused", k, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(op, ".config/gh/hosts.yml")); string(b) != files[".config/gh/hosts.yml"] {
		t.Errorf("the operator's gh login changed: %q", b)
	}
}

// A local build (`use: ./path`) is jailed with the capabilities it declares,
// as an installed plugin is with its recorded manifest — not with none. The
// probe declares one command, true: run from the Manager's shared client it
// is found and runs.
func TestPluginJailLocalBuildDeclaredManifest(t *testing.T) {
	requireJail(t)
	self := jailSelf(t)
	bin := buildGo(t, "github.com/NodeSpy/conductor/test/plugins/acme-jailprobe", realDir(t, "", "jp-bin-"), "acme-jailprobe")
	t.Setenv("XDG_STATE_HOME", realDir(t, "", "jp-state-")) // the local-build snapshot
	ref := config.PluginRef{Name: "acme-jailprobe", Use: config.Use{Origin: config.OriginLocal, Path: bin, Name: "acme-jailprobe", Kind: config.UseKindConnector}}
	var logs []string
	mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", nil, Deps{
		Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		Sandbox: SandboxDeps{
			Self: self, StagingRoot: realDir(t, "", "jp-stage-"), Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	defer mgr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := mgr.StartAndDescribe(ctx, ref.Key()); err != nil {
		t.Fatalf("describe: %v\n  %s", err, strings.Join(logs, "\n  "))
	}
	if s, _ := mgr.Spec(ref.Key()); !slices.Equal(s.Manifest.Commands, []string{"true"}) {
		t.Errorf("the local build's manifest = %+v, want its declared commands", s.Manifest)
	}
	c, _ := mgr.Client(ref.Key())
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "local", Verb: "probe", Options: map[string]any{"exec": "true"}})
	t.Logf("spawn log (%s):\n  %s", runtime.GOOS, strings.Join(logs, "\n  "))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	report, _ := out["report"].(map[string]any)
	if got, _ := report["exec true"].(string); got != "ok" {
		t.Errorf("exec true = %q: the local build ran without its declared command", got)
	}
}
