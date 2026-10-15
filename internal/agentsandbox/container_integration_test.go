//go:build linux || darwin

package agentsandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The container backend against a real engine: CONDUCTOR_CONTAINER_TEST
// names it (docker | podman); unset, these tests skip. Every container and
// volume they create carries a conductor.test label of this run, and is
// removed by it — nothing else on the engine is touched.

// TestContainerProbeChild is the probe a container launch runs (a static
// build of this test binary, copied into the workspace): "ok …"/"FAIL …".
func TestContainerProbeChild(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_CPROBE") == "" {
		t.Skip("runs only inside an agent container")
	}
	say := func(ok bool, what string) {
		if ok {
			fmt.Println("ok " + what)
		} else {
			fmt.Println("FAIL " + what)
		}
	}
	switch os.Getenv("AGENTSANDBOX_CPROBE") {
	case "write-home":
		say(os.WriteFile(filepath.Join(os.Getenv("HOME"), "installed-tool"), []byte(os.Getenv("PROBE_MARK")), 0o700) == nil, "wrote to HOME")
		return
	case "read-home":
		b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "installed-tool"))
		say(err == nil && string(b) == os.Getenv("PROBE_MARK"), "HOME kept what an earlier launch installed")
		return
	case "sleep":
		fmt.Println("sleeping")
		time.Sleep(time.Hour)
		return
	}
	_, err := os.ReadFile(os.Getenv("PROBE_SECRET"))
	say(err != nil, "operator secret unreadable")
	_, err = os.Stat(os.Getenv("PROBE_STATE"))
	say(err != nil, "conductor state absent")
	say(os.WriteFile(filepath.Join(os.Getenv("PROBE_WS"), "out.txt"), []byte("x"), 0o600) == nil, "workspace writable")
	say(os.Getenv("HOME") == sandbox.ContainerHome, "HOME is the container's")
	say(os.WriteFile(filepath.Join(os.Getenv("HOME"), "state.txt"), []byte("x"), 0o600) == nil, "HOME writable")
	say(os.Getuid() != 0, "not root")
	status, _ := os.ReadFile("/proc/self/status")
	say(strings.Contains(string(status), "CapEff:\t0000000000000000"), "no capabilities")
	say(strings.Contains(string(status), "NoNewPrivs:\t1"), "no-new-privileges")
	say(strings.Contains(string(status), "Seccomp:\t2"), "seccomp filter on")
	for _, s := range []string{"/var/run/docker.sock", "/run/docker.sock", "/run/podman/podman.sock", "/var/run/podman/podman.sock"} {
		if _, err := os.Stat(s); err == nil {
			say(false, "engine socket absent: "+s)
		}
	}
	say(os.Getenv("DOCKER_HOST") == "" && os.Getenv("CONTAINER_HOST") == "", "no engine address in env")
	if os.Getenv("PROBE_RO_ROOT") == "1" {
		say(os.WriteFile("/etc/conductor-probe", []byte("x"), 0o600) != nil, "root filesystem read-only")
	}
	ep := strings.TrimPrefix(os.Getenv("CONDUCTOR_ENDPOINT"), "unix://")
	c, err := net.DialTimeout("unix", ep, 3*time.Second)
	if err == nil {
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		say(strings.TrimSpace(line) == "CONDUCTOR-OK", "conductor socket reachable")
	} else {
		say(false, "conductor socket reachable: "+err.Error())
	}
	say(os.Getenv("CONDUCTOR_SKILL_TOKEN") == os.Getenv("PROBE_TOKEN"), "this launch's token")
	c, err = net.DialTimeout("tcp", os.Getenv("PROBE_MODEL"), 3*time.Second)
	if err == nil {
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		say(strings.TrimSpace(line) == "MODEL-OK", "model endpoint relayed")
	} else {
		say(false, "model endpoint relayed: "+err.Error())
	}
	_, err = net.DialTimeout("tcp", os.Getenv("PROBE_OTHER"), 2*time.Second)
	say(err != nil, "another host-loopback port unreachable")
	_, err = net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
	say(err != nil, "no network of its own")
	// The allowlist proxy: the allowed target answers, another is refused.
	// (The proxy named explicitly: NO_PROXY exempts the loopback test
	// targets, which would otherwise be dialed directly.)
	pu, _ := url.Parse(os.Getenv("HTTP_PROXY"))
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if r, err := cl.Get("http://" + os.Getenv("PROBE_ALLOWED") + "/"); err == nil {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		say(strings.TrimSpace(string(b)) == "EGRESS-OK", "allowlisted egress through the proxy")
	} else {
		say(false, "allowlisted egress through the proxy: "+err.Error())
	}
	if r, err := cl.Get("http://" + os.Getenv("PROBE_OTHER") + "/"); err == nil {
		r.Body.Close()
		say(r.StatusCode == http.StatusForbidden, fmt.Sprintf("egress off the allowlist refused (%d)", r.StatusCode))
	} else {
		say(true, "egress off the allowlist refused")
	}
	say(os.Getenv("GH_TOKEN") == "" && os.Getenv("SSH_AUTH_SOCK") == "", "credentials not in env")
	// The other identity's HOME and workspace are not here.
	if o := os.Getenv("PROBE_OTHER_WS"); o != "" {
		_, err := os.Stat(o)
		say(err != nil, "another identity's workspace absent")
	}
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), "other-identity-mark"))
	say(err != nil, "another identity's HOME absent")
}

// containerTestEngine is the engine under test, or a skip.
func containerTestEngine(t *testing.T) *CLIEngine {
	t.Helper()
	name := os.Getenv("CONDUCTOR_CONTAINER_TEST")
	if name == "" {
		t.Skip("CONDUCTOR_CONTAINER_TEST (docker|podman) not set")
	}
	e, err := ResolveEngine(name, runtime.GOOS, exec.LookPath, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// staticBuild builds a static Linux binary (`go build`, or `go test -c`
// when test) into dir.
func staticBuild(t *testing.T, dir, out string, args ...string) string {
	t.Helper()
	dst := filepath.Join(dir, out)
	n := len(args)
	cmd := exec.Command("go", append(append(append([]string(nil), args[:n-1]...), "-o", dst), args[n-1])...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, b)
	}
	return dst
}

// containerHarness is a Containers on the real engine, its test label, and
// the cleanup of everything carrying it.
func containerHarness(t *testing.T) (*Containers, string) {
	t.Helper()
	e := containerTestEngine(t)
	image := os.Getenv("CONDUCTOR_CONTAINER_TEST_IMAGE")
	if image == "" {
		image = "alpine:3"
	}
	root := scratchDir(t)
	helper := filepath.Join(root, "helper")
	if err := os.MkdirAll(helper, 0o755); err != nil {
		t.Fatal(err)
	}
	modRoot, _ := filepath.Abs("../..")
	cmdDir := filepath.Join(modRoot, "cmd", "conductor")
	staticBuild(t, helper, "conductor", "build", "-trimpath", cmdDir)
	run := "ct-" + nonce(4)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, _ := e.Output(ctx, "ps", "-aq", "--filter", "label=conductor.test="+run)
		for _, id := range strings.Fields(string(out)) {
			_, _ = e.Output(ctx, "rm", "-f", id)
		}
		out, _ = e.Output(ctx, "volume", "ls", "-q", "--filter", "label=conductor.test="+run)
		for _, v := range strings.Fields(string(out)) {
			_, _ = e.Output(ctx, "volume", "rm", "-f", v)
		}
	}
	t.Cleanup(cleanup)
	keepID := false
	if e.Kind() == "podman" {
		out, _ := e.Output(context.Background(), "info", "--format", "{{.Host.Security.Rootless}}")
		keepID = strings.TrimSpace(string(out)) == "true"
	}
	c := &Containers{
		Engine: e, Root: root, DaemonID: "t" + nonce(5), Labels: map[string]string{"conductor.test": run},
		Helper: helper, UID: os.Getuid(), GID: os.Getgid(), KeepID: keepID, IdleStop: time.Hour,
		Defaults: Container{Image: image},
		Logf:     t.Logf,
	}
	pm := sandbox.NewProxyManager(nil)
	t.Cleanup(pm.Close)
	c.Proxy = pm.UnixEndpoint
	t.Cleanup(c.Close)
	return c, run
}

// containerPolicy is a policy in a fresh workspace, with the probe there.
func containerPolicy(t *testing.T, root, identity string, persistent bool, probe string) Policy {
	t.Helper()
	ws := filepath.Join(root, identity+"-ws")
	home := filepath.Join(root, identity+"-home")
	for _, d := range []string{ws, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "probe"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	return Policy{Identity: identity, Workspace: canon(ws), Home: canon(home), Container: &Container{Persistent: persistent}}
}

// runContainer prepares and runs one probe launch, returning its output.
// env are the probe's variables (the policy's own: AgentEnv drops
// anything else); inherited is the launcher's environment.
func runContainer(t *testing.T, c *Containers, p Policy, env ...string) (string, func()) {
	t.Helper()
	var inherited []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "AGENTSANDBOX_") || strings.HasPrefix(kv, "PROBE_") {
			p.Env = append(p.Env, kv)
		} else {
			inherited = append(inherited, kv)
		}
	}
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	l, err := WrapContainer(p, []string{filepath.Join(p.Workspace, "probe"), "-test.run", "^TestContainerProbeChild$", "-test.v=false"}, Deps{
		Inherited: append(os.Environ(), inherited...),
	})
	if err != nil {
		rel()
		t.Fatalf("wrap: %v", err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	if os.Getenv("CT_DEBUG") != "" {
		t.Logf("argv %q\nenv %q", l.Argv, l.Env)
	}
	out, err := cmd.CombinedOutput()
	l.Cleanup()
	if err != nil && !strings.Contains(string(out), "ok ") {
		rel()
		t.Fatalf("probe launch: %v\n%s", err, out)
	}
	return string(out), rel
}

func checkProbe(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FAIL ") {
			t.Errorf("container probe: %s", line)
		}
	}
	for _, w := range want {
		if !strings.Contains(out, "ok "+w) {
			t.Errorf("container probe did not report %q:\n%s", w, out)
		}
	}
}

// The container's boundary, from inside: everything the jail guarantees.
func TestContainerBoundary(t *testing.T) {
	c, _ := containerHarness(t)
	root := c.Root
	probe := staticBuild(t, root, "probe.test", "test", "-c", "-trimpath", ".")
	secretDir := filepath.Join(root, "realhome", ".ssh")
	_ = os.MkdirAll(secretDir, 0o700)
	secret := filepath.Join(secretDir, "id_decoy")
	_ = os.WriteFile(secret, []byte("DECOY"), 0o600)
	state := filepath.Join(root, "state")
	_ = os.MkdirAll(state, 0o700)
	sockDir, _ := SocketDir("cct")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "c.sock")
	serveLine(t, "unix", sock, "CONDUCTOR-OK")
	model := serveLine(t, "tcp", "127.0.0.1:0", "MODEL-OK").Addr().String()
	other := serveLine(t, "tcp", "127.0.0.1:0", "OTHER").Addr().String()
	allowed := serveHTTP(t, "EGRESS-OK")

	p := containerPolicy(t, root, "agent-a", false, probe)
	p.Socket, p.Token = sock, "tok-"+nonce(4)
	p.Loopback = []string{model}
	p.Egress = []string{allowed}
	p.Container.ReadOnlyRoot = true
	out, rel := runContainer(t, c, p,
		"AGENTSANDBOX_CPROBE=1", "PROBE_SECRET="+secret, "PROBE_STATE="+state, "PROBE_WS="+p.Workspace,
		"PROBE_MODEL="+model, "PROBE_OTHER="+other, "PROBE_ALLOWED="+allowed, "PROBE_TOKEN="+p.Token,
		"PROBE_RO_ROOT=1", "GH_TOKEN=leak", "DOCKER_HOST=unix:///var/run/docker.sock")
	rel()
	checkProbe(t, out, "operator secret unreadable", "conductor state absent", "workspace writable",
		"HOME is the container's", "not root", "no capabilities", "no-new-privileges", "seccomp filter on",
		"no engine address in env", "root filesystem read-only", "conductor socket reachable", "this launch's token",
		"model endpoint relayed", "another host-loopback port unreachable", "no network of its own",
		"allowlisted egress through the proxy", "egress off the allowlist refused", "credentials not in env")
	if b, err := os.ReadFile(filepath.Join(p.Workspace, "out.txt")); err != nil || string(b) != "x" {
		t.Errorf("the workspace write did not land on the host: %v", err)
	}
	// A non-persistent container is gone with its launch.
	rows, err := c.list(context.Background(), "")
	if err != nil || len(rows) != 0 {
		t.Errorf("a per-launch container outlived its launch: %v %v", rows, err)
	}
}

// A persistent identity keeps what it installs across launches, its
// container stopping and a new daemon (Containers) picking it up; two
// identities see nothing of each other; archiving removes the container
// and its volume.
func TestContainerPersistentIdentity(t *testing.T) {
	c, _ := containerHarness(t)
	root := c.Root
	probe := staticBuild(t, root, "probe.test", "test", "-c", "-trimpath", ".")
	mark := "tool-" + nonce(4)
	a := containerPolicy(t, root, "member-a", true, probe)
	out, rel := runContainer(t, c, a, "AGENTSANDBOX_CPROBE=write-home", "PROBE_MARK="+mark)
	rel()
	checkProbe(t, out, "wrote to HOME")
	out, rel = runContainer(t, c, a, "AGENTSANDBOX_CPROBE=read-home", "PROBE_MARK="+mark)
	rel()
	checkProbe(t, out, "HOME kept what an earlier launch installed")

	// The daemon restarts: a new backend with the same daemon id, its
	// container stopped meanwhile.
	ctx := context.Background()
	rows, _ := c.list(ctx, IdentityKey("member-a"))
	if len(rows) != 1 {
		t.Fatalf("member-a has %d containers, want 1", len(rows))
	}
	c.Close()
	if _, err := c.Engine.Output(ctx, "stop", "--time", "1", rows[0].Name); err != nil {
		t.Fatal(err)
	}
	c2 := &Containers{Engine: c.Engine, Root: c.Root, DaemonID: c.DaemonID, Labels: c.Labels, Helper: c.Helper,
		UID: c.UID, GID: c.GID, KeepID: c.KeepID, IdleStop: time.Hour, Defaults: c.Defaults, Proxy: c.Proxy, Logf: t.Logf}
	t.Cleanup(c2.Close)
	if err := c2.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	out, rel = runContainer(t, c2, a, "AGENTSANDBOX_CPROBE=read-home", "PROBE_MARK="+mark)
	rel()
	checkProbe(t, out, "HOME kept what an earlier launch installed")
	if rows2, _ := c2.list(ctx, IdentityKey("member-a")); len(rows2) != 1 || rows2[0].Name != rows[0].Name {
		t.Errorf("the restart made another container: %v (was %v)", rows2, rows)
	}

	// Another identity: its own container and HOME.
	b := containerPolicy(t, root, "member-b", true, probe)
	sockDir, _ := SocketDir("cct")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "c.sock")
	serveLine(t, "unix", sock, "CONDUCTOR-OK")
	_ = os.WriteFile(filepath.Join(a.Workspace, "other-identity-mark"), []byte("a"), 0o600)
	out, rel = runContainer(t, c2, b, "AGENTSANDBOX_CPROBE=read-home", "PROBE_MARK="+mark)
	rel()
	if !strings.Contains(out, "FAIL HOME kept") {
		t.Errorf("member-b sees member-a's HOME:\n%s", out)
	}
	b.Socket, b.Token = sock, "t"
	out, rel = runContainer(t, c2, b, "AGENTSANDBOX_CPROBE=1", "PROBE_WS="+b.Workspace, "PROBE_OTHER_WS="+a.Workspace, "PROBE_TOKEN=t")
	rel()
	if !strings.Contains(out, "ok another identity's workspace absent") || !strings.Contains(out, "ok another identity's HOME absent") {
		t.Errorf("member-b sees member-a:\n%s", out)
	}

	// Archive member-a: its container and volume are gone, member-b's stay.
	removed, err := c2.Archive(ctx, "member-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("archive removed %v", removed)
	}
	if rows, _ := c2.list(ctx, IdentityKey("member-a")); len(rows) != 0 {
		t.Errorf("member-a's container survived the archive: %v", rows)
	}
	if _, err := c2.Engine.Output(ctx, "volume", "inspect", c2.VolumeName("member-a")); err == nil {
		t.Error("member-a's HOME volume survived the archive")
	}
	if rows, _ := c2.list(ctx, IdentityKey("member-b")); len(rows) != 1 {
		t.Errorf("member-b's container went with member-a's archive: %v", rows)
	}
	if _, err := c2.Engine.Output(ctx, "volume", "inspect", c2.VolumeName("member-b")); err != nil {
		t.Error("member-b's HOME volume went with member-a's archive")
	}
}

// A launch's processes end with it: the host kills its group in the
// container (the engine client forwards no signal).
func TestContainerLaunchEndsWithItsHost(t *testing.T) {
	c, _ := containerHarness(t)
	probe := staticBuild(t, c.Root, "probe.test", "test", "-c", "-trimpath", ".")
	p := containerPolicy(t, c.Root, "member-k", true, probe)
	p.Env = []string{"AGENTSANDBOX_CPROBE=sleep"}
	rel, err := c.Prepare(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	l, err := WrapContainer(p, []string{filepath.Join(p.Workspace, "probe"), "-test.run", "^TestContainerProbeChild$"}, Deps{
		Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if strings.TrimSpace(line) != "sleeping" {
		t.Fatalf("probe said %q", line)
	}
	l.Signal("TERM")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the launch outlived its TERM")
	}
	out, _ := c.Engine.Output(context.Background(), "top", p.Container.Session.Name)
	if strings.Contains(string(out), "probe") {
		t.Errorf("the probe still runs in the container:\n%s", out)
	}
}

func serveHTTP(t *testing.T, body string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, body) })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// scratchDir is a test dir the engine can bind (its real path).
func scratchDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
