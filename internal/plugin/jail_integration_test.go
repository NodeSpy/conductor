//go:build linux || darwin

package plugin

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestDeclaredJail runs a probe program through the real plugin launch
// (buildCommand: on Linux unshare + `conductor sandbox-net` with the minimal
// base and the egress proxy; on macOS sandbox-exec with the plugin profile
// and the egress proxy) and checks that it gets exactly what its manifest
// declares: the declared file, directory, command, variable, loopback
// destination and listen address work; everything else is denied.
func TestDeclaredJail(t *testing.T) {
	darwin := runtime.GOOS == "darwin"
	skip := t.Skip
	if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") != "" { // the sandbox canary: fail, never skip
		skip = t.Fatal
	}
	if darwin {
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			skip("sandbox-exec not on PATH")
		}
	} else {
		if _, err := exec.LookPath("unshare"); err != nil {
			skip("unshare not on PATH")
		}
		if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
			skip("requires unprivileged root-mapped user namespaces:", err)
		}
	}
	dir, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
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

	// A host loopback service the probe declares, and one it does not. They
	// run in this test process, which stands in for conductor here: the
	// own-port refusal (TestLoopbackEgressToConductorsOwnPortIsRefused)
	// must not count them.
	declared := httpOn(t, "declared-loopback")
	undeclared := httpOn(t, "undeclared-loopback")
	listenPort := freePort(t)
	testServers := map[string]bool{}
	for _, a := range []string{declared, undeclared} {
		_, p, _ := net.SplitHostPort(a)
		testServers[p] = true
	}
	ownWas := ownServicePort
	ownServicePort = func(port int) (bool, error) {
		if testServers[strconv.Itoa(port)] {
			return false, nil
		}
		return ownWas(port)
	}
	t.Cleanup(func() { ownServicePort = ownWas })
	execDecl, execArgs, execWant := execChecks(t)

	pm := sandbox.NewProxyManager(nil)
	t.Cleanup(pm.Close)
	sd := SandboxDeps{Self: conductor, StagingRoot: filepath.Join(dir, "staging"), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint}
	s := Spec{
		Name: "probe", Kind: KindConnector, BinPath: probe,
		Conns: []map[string]any{{"svc": "http://" + declared}},
		Manifest: Manifest{
			Egress:   []string{"config:svc", "allowed.example.com:443"},
			Listen:   []string{"127.0.0.1:" + strconv.Itoa(listenPort)},
			Read:     []string{"~/.config/tool"},
			Write:    []string{writeDir},
			Commands: []string{"cat"},
			Exec:     execDecl,
			Env:      []string{"PROBE_DECLARED"},
		},
		Args: append([]string{
			"read=" + filepath.Join(readDir, "login.yml"),
			"read=" + secret,
			"read=" + sibling,
			"exists=" + conductor,
			"write=" + filepath.Join(writeDir, "out"),
			"write=" + filepath.Join(readDir, "tamper"),
			"writeexec=" + filepath.Join(writeDir, "x.sh"),
			"copyexec=" + filepath.Join(writeDir, "copy"),
			"copyexec=/tmp/copy",
			"mkdir=/",
			"mkdir=/usr/bin",
			"run=cat",
			"run=ls",
			"run=/bin/ls",
			"env=PROBE_DECLARED",
			"env=PROBE_UNDECLARED",
			"get=http://" + declared,
			"get=http://" + undeclared,
			"get=http://blocked.example.com/",
			"dial=1.1.1.1:443",
			"listen=127.0.0.1:" + strconv.Itoa(listenPort),
		}, append(osChecks(), execArgs...)...),
	}
	if darwin {
		// Seatbelt allows every file's metadata (stat), not its contents.
		s.Args = append(s.Args, "read="+conductor)
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
		"write=" + filepath.Join(writeDir, "out"):      "ok",
		"write=" + filepath.Join(readDir, "tamper"):    "denied",
		"writeexec=" + filepath.Join(writeDir, "x.sh"): "denied",
		"copyexec=" + filepath.Join(writeDir, "copy"):  "denied",
		"copyexec=/tmp/copy":                           "denied",
		"mkdir=/":                                      "denied",
		"mkdir=/usr/bin":                               "denied",
		"run=cat":                                      "ok",
		"run=ls":                                       "denied",
		"run=/bin/ls":                                  "denied",
		"env=PROBE_DECLARED":                           "set:yes",
		"env=PROBE_UNDECLARED":                         "unset",
		"get=http://" + declared:                       "ok:declared-loopback",
		"get=http://" + undeclared:                     "denied",
		"get=http://blocked.example.com/":              "denied",
		"dial=1.1.1.1:443":                             "denied",
	}
	for k, w := range osWant(darwin, conductor) {
		want[k] = w
	}
	for k, w := range execWant {
		want[k] = w
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

// execChecks are the Linux checks of what runs from the base system: none of
// its programs, except what the plugin declares. Two programs in two
// subdirectories of /usr/lib (a tool's helpers, a daemon's binaries): the
// directory of one is declared exec, the other is not. And, when there is
// one, a program directly in /usr/lib among the libraries.
func execChecks(t *testing.T) (decl, args []string, want map[string]string) {
	t.Helper()
	want = map[string]string{}
	if runtime.GOOS != "linux" {
		return nil, nil, want
	}
	var progs []string
	var top string
	dirs, _ := os.ReadDir("/usr/lib")
	for _, d := range dirs {
		p := filepath.Join("/usr/lib", d.Name())
		if d.Type().IsRegular() && top == "" && isELFProgram(p) {
			top = p
		}
		if !d.IsDir() || d.Name() == "glibc-hwcaps" || len(progs) == 2 {
			continue
		}
		files, _ := os.ReadDir(p)
		for _, f := range files {
			if fp := filepath.Join(p, f.Name()); f.Type().IsRegular() && isELFProgram(fp) {
				progs = append(progs, fp)
				break
			}
		}
	}
	if len(progs) < 2 {
		t.Logf("fewer than two programs in /usr/lib subdirectories: exec checks skipped")
		return nil, nil, want
	}
	decl = []string{filepath.Dir(progs[0])}
	want["exec="+progs[0]] = "ok"
	want["exec="+progs[1]] = "denied"
	args = []string{"exec=" + progs[0], "exec=" + progs[1]}
	if top != "" {
		want["exec="+top] = "denied"
		args = append(args, "exec="+top)
	}
	return decl, args, want
}

// isELFProgram: an executable ELF file that is not a shared object by name.
func isELFProgram(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.Mode()&0o111 == 0 || fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || strings.HasSuffix(p, ".so") || strings.Contains(filepath.Base(p), ".so.") {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	_, err = io.ReadFull(f, b)
	return err == nil && string(b) == "\x7fELF"
}

// osChecks are the checks whose answer differs by how each OS jails.
func osChecks() []string {
	if runtime.GOOS == "darwin" {
		return []string{"read=/etc/shells", "ls=/usr/bin", "ls=/opt/homebrew", "ls=/usr/local"}
	}
	return []string{"copyexec=/dev/shm/copy", "ls=/usr/bin", "exists=/etc/fstab", "exists=/opt"}
}

func osWant(darwin bool, conductor string) map[string]string {
	if darwin {
		return map[string]string{
			"read=" + conductor: "denied",
			"read=/etc/shells":  "denied",
			"ls=/usr/bin":       "denied",
			"ls=/opt/homebrew":  "denied",
			"ls=/usr/local":     "denied",
		}
	}
	return map[string]string{
		// The jail's root holds none of the host's files: not even the
		// conductor binary that set it up exists in it.
		"exists=" + conductor:    "denied",
		"copyexec=/dev/shm/copy": "denied",
		"ls=/usr/bin":            "ok:cat",
		"exists=/etc/fstab":      "denied",
		"exists=/opt":            "denied",
	}
}
