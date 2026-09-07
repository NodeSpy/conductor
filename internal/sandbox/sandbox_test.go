package sandbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func TestFromConfigNil(t *testing.T) {
	if FromConfig(nil) != nil {
		t.Fatal("nil config must stay nil")
	}
	var s *Spec
	if _, ok := s.ProxyPolicy(); ok {
		t.Fatal("nil spec has no proxy policy")
	}
	if err := s.Check("linux", 1000, nil); err != nil {
		t.Fatalf("nil spec check: %v", err)
	}
	argv, err := s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil || strings.Join(argv, " ") != "tool" {
		t.Fatalf("nil spec wrap: %v %v", argv, err)
	}
	cmd, err := s.WrapRemote("do it")
	if err != nil || cmd != "do it" {
		t.Fatalf("nil spec remote wrap: %q %v", cmd, err)
	}
}

func TestWrapLocalUser(t *testing.T) {
	s := FromConfig(&config.IsolationConfig{Mode: "user", User: "sbx"})
	argv, err := s.WrapLocal([]string{"claude", "-p", "x"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != "sudo -n -u sbx -- claude -p x" {
		t.Fatalf("user wrap: %q", got)
	}
	// Missing user is an error, not a silent no-op.
	if _, err := (&Spec{Mode: "user"}).WrapLocal([]string{"t"}, "", nil, nil); err == nil {
		t.Fatal("mode user without user must error")
	}
}

func TestWrapLocalNamespace(t *testing.T) {
	s := FromConfig(&config.IsolationConfig{Mode: "namespace"})
	argv, err := s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "unshare --user --map-current-user --pid --fork --mount-proc --kill-child -- tool"
	if got := strings.Join(argv, " "); got != want {
		t.Fatalf("namespace wrap:\n got %q\nwant %q", got, want)
	}

	// deny adds the network namespace; limits add the systemd-run scope.
	s = FromConfig(&config.IsolationConfig{
		Mode:    "namespace",
		Network: &config.IsolationNetwork{Deny: true},
		Limits:  &config.IsolationLimits{Memory: "2g", CPU: "200%", Pids: 128},
	})
	argv, err = s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	for _, frag := range []string{
		"systemd-run --user --scope --quiet --collect",
		"-p MemoryMax=2g", "-p CPUQuota=200%", "-p TasksMax=128",
		"--net", "-- tool",
	} {
		if !strings.Contains(got, frag) {
			t.Fatalf("namespace wrap missing %q in %q", frag, got)
		}
	}
}

func TestWrapLocalContainer(t *testing.T) {
	s := FromConfig(&config.IsolationConfig{
		Mode:      "container",
		Container: &config.ContainerIsolation{Image: "agents:latest", Engine: "podman"},
		Network:   &config.IsolationNetwork{Deny: true},
		Limits:    &config.IsolationLimits{Memory: "1g", CPU: "2", Pids: 64},
	})
	argv, err := s.WrapLocal([]string{"claude", "-p", "x"}, "/wt", []string{"GH_TOKEN", "HTTP_PROXY"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	for _, frag := range []string{
		"podman run --rm -i", "-v /wt:/wt -w /wt", "--network=none",
		"--memory 1g", "--cpus 2", "--pids-limit 64",
		"-e GH_TOKEN", "-e HTTP_PROXY",
		"agents:latest claude -p x",
	} {
		if !strings.Contains(got, frag) {
			t.Fatalf("container wrap missing %q in %q", frag, got)
		}
	}
	// No image → error.
	if _, err := (&Spec{Mode: "container"}).WrapLocal([]string{"t"}, "", nil, nil); err == nil {
		t.Fatal("container without image must error")
	}
	// Default engine is docker.
	s = FromConfig(&config.IsolationConfig{Mode: "container", Container: &config.ContainerIsolation{Image: "img"}})
	argv, _ = s.WrapLocal([]string{"t"}, "", nil, nil)
	if argv[0] != "docker" {
		t.Fatalf("default engine: %q", argv[0])
	}
}

func TestWrapRemote(t *testing.T) {
	s := FromConfig(&config.IsolationConfig{Mode: "user", User: "sbx"})
	cmd, err := s.WrapRemote(`cd /x && export A='b'; tool`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "sudo -n -u 'sbx' -- sh -c '") || !strings.Contains(cmd, "tool") {
		t.Fatalf("remote user wrap: %q", cmd)
	}
	// The inner command's single quotes survive the re-quoting.
	if !strings.Contains(cmd, `'\''b'\''`) {
		t.Fatalf("remote wrap quoting: %q", cmd)
	}

	ns := FromConfig(&config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}})
	cmd, err = ns.WrapRemote("tool x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "unshare --user --map-current-user --pid --fork --mount-proc --kill-child --net -- sh -c ") {
		t.Fatalf("remote namespace wrap: %q", cmd)
	}

	// Container mode never wraps a remote command.
	ct := FromConfig(&config.IsolationConfig{Mode: "container", Container: &config.ContainerIsolation{Image: "i"}})
	if _, err := ct.WrapRemote("tool"); err == nil {
		t.Fatal("container remote wrap must error")
	}
}

func TestCheck(t *testing.T) {
	have := func(bins ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, b := range bins {
				if b == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", fmt.Errorf("%s: not found", name)
		}
	}
	const nonRoot, root = 1000, 0
	cases := []struct {
		name string
		spec *Spec
		goos string
		euid int
		look func(string) (string, error)
		ok   bool
	}{
		{"user ok", &Spec{Mode: "user", User: "s"}, "darwin", nonRoot, have("sudo"), true},
		{"user no sudo", &Spec{Mode: "user", User: "s"}, "linux", nonRoot, have(), false},
		{"namespace ok", &Spec{Mode: "namespace"}, "linux", nonRoot, have("unshare"), true},
		{"namespace non-linux", &Spec{Mode: "namespace"}, "darwin", nonRoot, have("unshare"), false},
		{"namespace limits need systemd-run", &Spec{Mode: "namespace", Memory: "1g"}, "linux", nonRoot, have("unshare"), false},
		{"namespace limits ok", &Spec{Mode: "namespace", Memory: "1g"}, "linux", nonRoot, have("unshare", "systemd-run"), true},
		// #36 iso-review round 2, item 2: namespace mode as root is not a
		// boundary (--map-current-user maps root→root) — refuse by default,
		// permit only with the explicit allow_root opt-in.
		{"namespace root refused", &Spec{Mode: "namespace"}, "linux", root, have("unshare"), false},
		{"namespace root allow_root", &Spec{Mode: "namespace", AllowRoot: true}, "linux", root, have("unshare"), true},
		{"user root ok", &Spec{Mode: "user", User: "s"}, "linux", root, have("sudo"), true},
		{"container root ok", &Spec{Mode: "container", Image: "i"}, "linux", root, have("docker"), true},
		{"container ok", &Spec{Mode: "container", Image: "i"}, "darwin", nonRoot, have("docker"), true},
		{"container podman", &Spec{Mode: "container", Image: "i", Engine: "podman"}, "linux", nonRoot, have("podman"), true},
		{"container missing engine", &Spec{Mode: "container", Image: "i"}, "linux", nonRoot, have(), false},
		{"unknown mode", &Spec{Mode: "jail"}, "linux", nonRoot, have(), false},
	}
	for _, tc := range cases {
		err := tc.spec.Check(tc.goos, tc.euid, tc.look)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestProxyPolicy(t *testing.T) {
	// Absent network block → no proxy.
	if _, ok := FromConfig(&config.IsolationConfig{Mode: "user", User: "s"}).ProxyPolicy(); ok {
		t.Fatal("no network block → no proxy policy")
	}
	// Present-but-empty → deny-all proxy.
	allow, ok := FromConfig(&config.IsolationConfig{Mode: "user", User: "s", Network: &config.IsolationNetwork{}}).ProxyPolicy()
	if !ok || len(allow) != 0 {
		t.Fatalf("empty network block → deny-all proxy: %v %v", allow, ok)
	}
	// Allowlist → proxy with patterns.
	allow, ok = FromConfig(&config.IsolationConfig{Mode: "user", User: "s",
		Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}}).ProxyPolicy()
	if !ok || len(allow) != 1 {
		t.Fatalf("allowlist: %v %v", allow, ok)
	}
	// Structural deny → NOT proxy-governed.
	if _, ok := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true}}).ProxyPolicy(); ok {
		t.Fatal("deny: true is structural, not proxied")
	}
}

func TestEgressAllowed(t *testing.T) {
	cases := []struct {
		allow []string
		host  string
		want  bool
	}{
		{nil, "api.example.com:443", false},
		{[]string{}, "api.example.com:443", false},
		{[]string{"*"}, "anything:1", true},
		{[]string{"api.example.com"}, "api.example.com:443", true},
		{[]string{"api.example.com"}, "api.example.com:80", false},
		{[]string{"api.example.com:443"}, "api.example.com:443", true},
		{[]string{"api.example.com:443"}, "api.example.com:80", false},
		{[]string{"api.example.com:443"}, "evil.example.com:443", false},
		{[]string{"*.example.com:443"}, "api.example.com:443", true},
		{[]string{"*.example.com:443"}, "example.com:443", false},
		{[]string{"API.Example.COM"}, "api.example.com:443", true},
		{[]string{"", "  "}, "x:1", false},
		{[]string{"[::1]:443"}, "[::1]:443", true},
	}
	for _, tc := range cases {
		if got := EgressAllowed(tc.allow, tc.host); got != tc.want {
			t.Errorf("EgressAllowed(%v, %q) = %v, want %v", tc.allow, tc.host, got, tc.want)
		}
	}
}

func TestSplitHostPort(t *testing.T) {
	for _, tc := range []struct{ in, host, port string }{
		{"a.b:443", "a.b", "443"},
		{"a.b", "a.b", ""},
		{"[::1]:8080", "::1", "8080"},
		{"[::1]", "::1", ""},
	} {
		h, p := splitHostPort(tc.in)
		if h != tc.host || p != tc.port {
			t.Errorf("splitHostPort(%q) = %q,%q want %q,%q", tc.in, h, p, tc.host, tc.port)
		}
	}
}

// Regression (#36 iso-review M10): WrapRemote's systemd-run prefix lands in
// a remote shell string — limit values must be quoted like the command is,
// or a hostile value becomes shell.
func TestWrapRemoteQuotesSystemdPrefix(t *testing.T) {
	ns := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Limits: &config.IsolationLimits{Memory: "2g; rm -rf /", CPU: "200%", Pids: 9}})
	cmd, err := ns.WrapRemote("tool x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `'MemoryMax=2g; rm -rf /'`) {
		t.Fatalf("memory value must be one quoted token: %q", cmd)
	}
	if strings.Contains(cmd, `-p MemoryMax=2g;`) {
		t.Fatalf("unquoted limit escaped into shell: %q", cmd)
	}
	if !strings.Contains(cmd, `'CPUQuota=200%'`) || !strings.Contains(cmd, `'TasksMax=9'`) {
		t.Fatalf("all limit values quoted: %q", cmd)
	}
}

// Regression (#36 iso-review M8): a bare-host allowlist entry means :443
// ONLY — it must not imply every port on that host (ssh, smtp, a debug
// port). Any-port is the explicit "host:*" opt-in.
func TestEgressBareHostIsHTTPSOnly(t *testing.T) {
	allow := []string{"api.example.com"}
	if !EgressAllowed(allow, "api.example.com:443") {
		t.Fatal("bare host must allow :443")
	}
	for _, p := range []string{"22", "25", "80", "8443", "53"} {
		if EgressAllowed(allow, "api.example.com:"+p) {
			t.Fatalf("bare host must not imply port %s", p)
		}
	}
	// The explicit opt-ins still work.
	if !EgressAllowed([]string{"api.example.com:22"}, "api.example.com:22") {
		t.Fatal("explicit host:port")
	}
	star := []string{"api.example.com:*"}
	if !EgressAllowed(star, "api.example.com:22") || !EgressAllowed(star, "api.example.com:443") {
		t.Fatal("host:* is the any-port opt-in")
	}
	if EgressAllowed(star, "other.example.com:22") {
		t.Fatal("host:* is scoped to the host")
	}
}

// Regression (#36 iso-review C1): deny+egress under namespace/container is
// the ENFORCED allowlist — the wrap drops the network structurally and
// re-enters through the sandbox-net forwarder, whose unix socket is the only
// path out.
func TestWrapLocalEnforcedEgress(t *testing.T) {
	nf := &NetForward{Self: "/usr/bin/conductor", UnixSocket: "/tmp/egress.sock"}

	ns := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}})
	if !ns.EnforcedEgress() {
		t.Fatal("namespace deny+egress must be enforced")
	}
	argv, err := ns.WrapLocal([]string{"claude", "-p", "x"}, "/wt", nil, nf)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--net") {
		t.Fatalf("enforced egress must still drop the network: %q", joined)
	}
	if !strings.Contains(joined, "/usr/bin/conductor sandbox-net --listen "+ForwardAddr+" --unix /tmp/egress.sock -- claude -p x") {
		t.Fatalf("launch must re-enter through the forwarder: %q", joined)
	}
	// Without the forwarder wiring the launch fails closed, never launches
	// unfiltered.
	if _, err := ns.WrapLocal([]string{"claude"}, "/wt", nil, nil); err == nil {
		t.Fatal("enforced egress without wiring must refuse to launch")
	}

	ct := FromConfig(&config.IsolationConfig{Mode: "container",
		Container: &config.ContainerIsolation{Image: "img"},
		Network:   &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}})
	argv, err = ct.WrapLocal([]string{"claude"}, "/wt", []string{"HTTPS_PROXY"}, nf)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(argv, " ")
	for _, want := range []string{
		"--network=none",
		"-v /usr/bin/conductor:/run/conductor/conductor:ro",
		"-v /tmp/egress.sock:/run/conductor/egress.sock",
		"img /run/conductor/conductor sandbox-net --listen " + ForwardAddr + " --unix /run/conductor/egress.sock -- claude",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("container enforced egress missing %q: %q", want, joined)
		}
	}

	// Plain deny (no list) and advisory egress (no deny) are NOT the enforced
	// path.
	plain := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true}})
	if plain.EnforcedEgress() {
		t.Fatal("plain deny is not enforced-egress")
	}
	adv := FromConfig(&config.IsolationConfig{Mode: "user", User: "s",
		Network: &config.IsolationNetwork{Egress: []string{"x:443"}}})
	if adv.EnforcedEgress() {
		t.Fatal("user-mode egress is advisory, never enforced")
	}
}
