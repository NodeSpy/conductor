package sandbox

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// stubCheck pretends every wrapper binary exists on a Linux, non-root box so
// WrapLocalCommand's internal spec.Check call runs hermetically regardless of
// what the test box actually has installed.
func stubCheck(t *testing.T) {
	t.Helper()
	oldOS, oldLook, oldEuid := CheckGOOS, CheckLookPath, CheckGeteuid
	CheckGOOS = "linux"
	CheckLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	CheckGeteuid = func() int { return 1000 }
	t.Cleanup(func() { CheckGOOS, CheckLookPath, CheckGeteuid = oldOS, oldLook, oldEuid })
}

// (b) nil spec is a complete no-op: argv/env pass through unchanged, cleanup
// is callable and harmless, err is nil — a launch with no isolation: must
// behave exactly as if WrapLocalCommand were never called.
func TestWrapLocalCommandNilSpecNoop(t *testing.T) {
	argv := []string{"python3", "run.py"}
	env := []string{"A=1"}
	wrapped, outEnv, cleanup, err := WrapLocalCommand(nil, argv, "/wt", env, LocalWrapDeps{})
	if err != nil {
		t.Fatalf("nil spec must not error: %v", err)
	}
	if strings.Join(wrapped, " ") != "python3 run.py" {
		t.Fatalf("nil spec must leave argv unchanged: %v", wrapped)
	}
	if strings.Join(outEnv, " ") != "A=1" {
		t.Fatalf("nil spec must leave env unchanged: %v", outEnv)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil (a callable no-op)")
	}
	cleanup() // must not panic
}

// (a) a fake LocalWrapDeps + IsolationConfig{Mode: "namespace"} produces an
// unshare-prefixed argv — the same wrap controller.prepareLaunch applies to
// an agent runtime's own local launch, now reusable by any local-process
// caller (internal/code among them).
func TestWrapLocalCommandNamespaceWrapsArgv(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace"})
	deps := LocalWrapDeps{SelfExe: func() (string, error) { return "/opt/conductor", nil }}
	wrapped, _, cleanup, err := WrapLocalCommand(spec, []string{"python3", "run.py"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("namespace wrap: %v", err)
	}
	defer cleanup()
	got := strings.Join(wrapped, " ")
	want := "unshare --user --map-current-user --pid --fork --mount-proc --kill-child -- python3 run.py"
	if got != want {
		t.Fatalf("namespace wrap:\n got %q\nwant %q", got, want)
	}
}

// A `user:` mode spec wraps through sudo -n -u, exactly like
// controller.prepareLaunch's own user-mode wrap.
func TestWrapLocalCommandUserWrapsArgv(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "user", User: "sbx"})
	wrapped, _, cleanup, err := WrapLocalCommand(spec, []string{"make", "test"}, "/wt", nil, LocalWrapDeps{})
	if err != nil {
		t.Fatalf("user wrap: %v", err)
	}
	defer cleanup()
	if got := strings.Join(wrapped, " "); got != "sudo -n -u sbx -- make test" {
		t.Fatalf("user wrap: %q", got)
	}
}

// The advisory egress proxy: a network policy with no structural deny routes
// through deps.EgressAddr and carries HTTP(S)_PROXY env, same as
// controller.prepareLaunch's own advisory path.
func TestWrapLocalCommandAdvisoryEgressEnv(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "user", User: "sbx",
		Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}})
	var asked []string
	deps := LocalWrapDeps{
		EgressAddr: func(allow []string) (string, string, func(), error) {
			asked = allow
			return "127.0.0.1:5555", "cred", func() {}, nil
		},
	}
	_, env, cleanup, err := WrapLocalCommand(spec, []string{"tool"}, "", []string{"A=1"}, deps)
	if err != nil {
		t.Fatalf("advisory egress: %v", err)
	}
	defer cleanup()
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "A=1") || !strings.Contains(joined, "HTTPS_PROXY=http://conductor:cred@127.0.0.1:5555") {
		t.Fatalf("proxy env: %s", joined)
	}
	if len(asked) != 1 || asked[0] != "api.example.com:443" {
		t.Fatalf("allowlist handed to the proxy: %v", asked)
	}
}

// Fail-closed: a network policy that needs the proxy, with no EgressAddr
// wired, must refuse the launch rather than run bare/unconfined.
func TestWrapLocalCommandFailsClosedWithoutEgressAddr(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "user", User: "sbx",
		Network: &config.IsolationNetwork{}})
	if _, _, _, err := WrapLocalCommand(spec, []string{"tool"}, "", nil, LocalWrapDeps{}); err == nil ||
		!strings.Contains(err.Error(), "egress proxy") {
		t.Fatalf("network policy without a wired proxy must fail closed: %v", err)
	}
}

// Fail-closed: enforced egress (deny + allowlist under namespace) with no
// EgressUnix wired must refuse rather than launch without the wall it named.
func TestWrapLocalCommandFailsClosedWithoutEgressUnix(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}})
	deps := LocalWrapDeps{SelfExe: func() (string, error) { return "/opt/conductor", nil }}
	if _, _, _, err := WrapLocalCommand(spec, []string{"tool"}, "", nil, deps); err == nil ||
		!strings.Contains(err.Error(), "unix endpoint") {
		t.Fatalf("enforced egress without a wired unix endpoint must fail closed: %v", err)
	}
}

// Enforced egress composes masks + the sandbox-net forwarder + the isolation
// wrap, and the credential's revoke func comes back as cleanup.
func TestWrapLocalCommandEnforcedEgress(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}})
	var revoked bool
	deps := LocalWrapDeps{
		SelfExe: func() (string, error) { return "/opt/conductor", nil },
		EgressUnix: func(allow []string) (string, string, func(), error) {
			return "/run/egress.sock", "ucred", func() { revoked = true }, nil
		},
	}
	wrapped, env, cleanup, err := WrapLocalCommand(spec, []string{"claude"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("enforced egress: %v", err)
	}
	joined := strings.Join(wrapped, " ")
	if !strings.Contains(joined, "--net") ||
		!strings.Contains(joined, "/opt/conductor sandbox-net --listen "+ForwardAddr+" --unix /run/egress.sock -- claude") {
		t.Fatalf("enforced argv: %q", joined)
	}
	if !strings.Contains(strings.Join(env, "\n"), "HTTPS_PROXY=http://conductor:ucred@"+ForwardAddr) {
		t.Fatalf("proxy env must point at the in-sandbox forwarder: %v", env)
	}
	cleanup()
	if !revoked {
		t.Fatal("cleanup must revoke the egress credential")
	}
}

// Confine turns a namespace launch into the pivot_root fs-jail: the unshare
// maps the daemon uid to ROOT-in-userns (required for the mounts), and
// sandbox-net receives the allow-list as --bind / --bind-ro flags — the
// workdir, the spec's declared fs: paths, then the caller's ExtraBinds. No
// --mask flags: the daemon's dirs are hidden by absence, not overmounted.
func TestWrapLocalCommandJailBinds(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace", FS: []string{"/data/books"}})
	deps := LocalWrapDeps{
		SelfExe: func() (string, error) { return "/opt/conductor", nil },
		Confine: true,
		ExtraBinds: []BindMount{
			{Path: "/tmp/conductor-code-x", RO: true},
			{Path: "/tmp/conductor-ctx-y"},
		},
		MaskPaths: []string{"/var/lib/conductor"}, // must be IGNORED once jailing
	}
	wrapped, _, cleanup, err := WrapLocalCommand(spec, []string{"python3", "sync.py"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("jail wrap: %v", err)
	}
	defer cleanup()
	got := strings.Join(wrapped, " ")
	want := "unshare --user --map-root-user --pid --fork --mount-proc --kill-child -- " +
		"/opt/conductor sandbox-net --bind /wt --bind /data/books --bind-ro /tmp/conductor-code-x --bind /tmp/conductor-ctx-y -- python3 sync.py"
	if got != want {
		t.Fatalf("jail wrap:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "--mask") || strings.Contains(got, "--map-current-user") {
		t.Fatalf("jail must not mask or map-current-user: %q", got)
	}
}

// Confine + enforced egress: the jail binds the egress socket's dir (so the
// post-pivot forwarder can dial it) alongside the allow-list, and still wires
// --listen/--unix.
func TestWrapLocalCommandJailWithEnforcedEgress(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"abs.example.com:443"}}})
	deps := LocalWrapDeps{
		SelfExe: func() (string, error) { return "/opt/conductor", nil },
		Confine: true,
		EgressUnix: func(allow []string) (string, string, func(), error) {
			return "/run/egress-abc/egress.sock", "ucred", func() {}, nil
		},
	}
	wrapped, _, cleanup, err := WrapLocalCommand(spec, []string{"python3", "sync.py"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("jail+egress wrap: %v", err)
	}
	defer cleanup()
	got := strings.Join(wrapped, " ")
	if !strings.Contains(got, "--map-root-user") || !strings.Contains(got, "--net") {
		t.Fatalf("jail+egress must root-map and drop the net: %q", got)
	}
	if !strings.Contains(got, "--bind /wt") || !strings.Contains(got, "--bind /run/egress-abc") {
		t.Fatalf("jail+egress must bind the workdir and the egress socket dir: %q", got)
	}
	if !strings.Contains(got, "--listen "+ForwardAddr+" --unix /run/egress-abc/egress.sock") {
		t.Fatalf("jail+egress must wire the forwarder: %q", got)
	}
}

// Namespace-mode masking of the daemon's own paths, wired the same way
// controller.prepareLaunch wires DaemonMaskPaths.
func TestWrapLocalCommandNamespaceMasks(t *testing.T) {
	stubCheck(t)
	spec := FromConfig(&config.IsolationConfig{Mode: "namespace"})
	deps := LocalWrapDeps{
		SelfExe:   func() (string, error) { return "/opt/conductor", nil },
		MaskPaths: []string{"/var/lib/conductor", "/etc/conductor"},
	}
	wrapped, _, cleanup, err := WrapLocalCommand(spec, []string{"claude"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("namespace masks: %v", err)
	}
	defer cleanup()
	joined := strings.Join(wrapped, " ")
	if !strings.Contains(joined, "/opt/conductor sandbox-net --mask /var/lib/conductor --mask /etc/conductor -- claude") {
		t.Fatalf("default namespace launch must mask daemon paths: %q", joined)
	}

	// privileged: true opts out of masking.
	priv := FromConfig(&config.IsolationConfig{Mode: "namespace", Privileged: true})
	wrapped, _, cleanup2, err := WrapLocalCommand(priv, []string{"claude"}, "/wt", nil, deps)
	if err != nil {
		t.Fatalf("privileged namespace: %v", err)
	}
	defer cleanup2()
	if strings.Contains(strings.Join(wrapped, " "), "sandbox-net") {
		t.Fatalf("privileged namespace must skip masking: %v", wrapped)
	}
}
