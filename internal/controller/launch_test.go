package controller

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// stubProxy wires a fake EgressProxyFor for a test, recording the allowlists
// requested, and restores the previous seam on cleanup.
func stubProxy(t *testing.T, addr string) *[][]string {
	t.Helper()
	var calls [][]string
	old := EgressProxyFor
	EgressProxyFor = func(allow []string) (string, string, error) {
		calls = append(calls, allow)
		return addr, "testcred", nil
	}
	t.Cleanup(func() { EgressProxyFor = old })
	return &calls
}

// stubPlatform pretends every wrapper binary exists on a Linux box so the
// wrap paths run hermetically wherever the tests do.
func stubPlatform(t *testing.T) {
	t.Helper()
	oldOS, oldLook := launchGOOS, launchLookPath
	launchGOOS = "linux"
	launchLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { launchGOOS, launchLookPath = oldOS, oldLook })
}

func TestPrepareLaunchNoIsolationUnchanged(t *testing.T) {
	argv, dir, env, remote, err := prepareLaunch("", "/wt", []string{"A=1"}, []string{"tool", "x"}, launchOpts{})
	if err != nil || remote || dir != "/wt" ||
		strings.Join(argv, " ") != "tool x" || strings.Join(env, " ") != "A=1" {
		t.Fatalf("plain local launch changed: %v %q %v %v %v", argv, dir, env, remote, err)
	}
}

func TestPrepareLaunchUserModeWrapsArgv(t *testing.T) {
	stubPlatform(t)
	iso := &config.IsolationConfig{Mode: "user", User: "sbx"}
	argv, _, _, _, err := prepareLaunch("", "/wt", nil, []string{"claude", "-p", "x"}, launchOpts{iso: iso})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != "sudo -n -u sbx -- claude -p x" {
		t.Fatalf("user wrap: %q", got)
	}
}

func TestPrepareLaunchEgressProxyEnv(t *testing.T) {
	stubPlatform(t)
	calls := stubProxy(t, "127.0.0.1:5555")
	iso := &config.IsolationConfig{Mode: "user", User: "sbx",
		Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}}
	_, _, env, _, err := prepareLaunch("", "/wt", []string{"A=1"}, []string{"tool"}, launchOpts{iso: iso})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "A=1") ||
		!strings.Contains(joined, "HTTPS_PROXY=http://conductor:testcred@127.0.0.1:5555") ||
		!strings.Contains(joined, "NO_PROXY=127.0.0.1,localhost,::1") {
		t.Fatalf("proxy env: %s", joined)
	}
	if len(*calls) != 1 || len((*calls)[0]) != 1 || (*calls)[0][0] != "api.example.com:443" {
		t.Fatalf("allowlist handed to the proxy: %v", *calls)
	}
}

func TestPrepareLaunchAgentAuthoredDeniesByDefault(t *testing.T) {
	stubPlatform(t)
	// No isolation at all + agent-authored → the deny-all proxy governs.
	calls := stubProxy(t, "127.0.0.1:5556")
	_, _, env, _, err := prepareLaunch("", "/wt", nil, []string{"tool"}, launchOpts{agentAuthored: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, "\n"), "HTTP_PROXY=http://conductor:testcred@127.0.0.1:5556") {
		t.Fatalf("agent-authored launch must carry deny-all proxy env: %v", env)
	}
	if len(*calls) != 1 || len((*calls)[0]) != 0 {
		t.Fatalf("deny-all = empty allowlist: %v", *calls)
	}

	// An explicit egress allowlist on the profile overrides the implicit deny.
	iso := &config.IsolationConfig{Mode: "user", User: "s",
		Network: &config.IsolationNetwork{Egress: []string{"api.example.com"}}}
	_, _, _, _, err = prepareLaunch("", "", nil, []string{"tool"}, launchOpts{iso: iso, agentAuthored: true})
	if err != nil {
		t.Fatal(err)
	}
	if last := (*calls)[len(*calls)-1]; len(last) != 1 || last[0] != "api.example.com" {
		t.Fatalf("explicit allowlist must win over implicit deny: %v", last)
	}
}

func TestPrepareLaunchStructuralDenySkipsProxy(t *testing.T) {
	stubPlatform(t)
	// namespace + deny is structural: no proxy involved, even agent-authored.
	calls := stubProxy(t, "127.0.0.1:5557")
	iso := &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}}
	argv, _, _, _, err := prepareLaunch("", "/wt", nil, []string{"tool"}, launchOpts{iso: iso, agentAuthored: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--net") {
		t.Fatalf("structural deny must unshare the network: %q", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("structural deny must not start a proxy: %v", *calls)
	}
}

func TestPrepareLaunchFailsClosedWithoutProxy(t *testing.T) {
	old := EgressProxyFor
	EgressProxyFor = nil
	t.Cleanup(func() { EgressProxyFor = old })
	iso := &config.IsolationConfig{Mode: "user", User: "s", Network: &config.IsolationNetwork{}}
	if _, _, _, _, err := prepareLaunch("", "", nil, []string{"t"}, launchOpts{iso: iso}); err == nil ||
		!strings.Contains(err.Error(), "egress proxy") {
		t.Fatalf("network policy without a wired proxy must fail closed: %v", err)
	}
	if _, _, _, _, err := prepareLaunch("", "", nil, []string{"t"}, launchOpts{agentAuthored: true}); err == nil {
		t.Fatal("agent-authored without a wired proxy must fail closed")
	}
}

func TestPrepareLaunchRemoteIsolationWrapsCommand(t *testing.T) {
	old := HostArgvPrefix
	HostArgvPrefix = func(name string) ([]string, error) {
		if name != "box" {
			return nil, fmt.Errorf("unknown host")
		}
		return []string{"ssh", "ci@box"}, nil
	}
	t.Cleanup(func() { HostArgvPrefix = old })

	iso := &config.IsolationConfig{Mode: "user", User: "sbx"}
	argv, dir, env, remote, err := prepareLaunch("box", "/wt", []string{"A=1"}, []string{"tool", "x"}, launchOpts{iso: iso})
	if err != nil || !remote || dir != "" || env != nil {
		t.Fatalf("remote isolated launch: %v %q %v %v %v", argv, dir, env, remote, err)
	}
	cmd := argv[len(argv)-1]
	if !strings.HasPrefix(cmd, "sudo -n -u 'sbx' -- sh -c '") ||
		!strings.Contains(cmd, "tool") || !strings.Contains(cmd, "A=") {
		t.Fatalf("remote wrapped command: %q", cmd)
	}
}

func TestLaunchOptsResolution(t *testing.T) {
	rtIso := &config.IsolationConfig{Mode: "user", User: "rt"}
	profIso := &config.IsolationConfig{Mode: "user", User: "prof"}

	// Runtime-level applies when the profile has none.
	opt := launchOptsFor(rtIso, dispatch.Request{})
	if opt.iso != rtIso || opt.agentAuthored {
		t.Fatalf("runtime iso: %+v", opt)
	}
	// The profile's own isolation wins.
	req := dispatch.Request{Profile: config.AgentProfile{Isolation: profIso}, AgentAuthored: true}
	opt = launchOptsFor(rtIso, req)
	if opt.iso != profIso || !opt.agentAuthored {
		t.Fatalf("profile iso must win: %+v", opt)
	}
	// Resume keeps the runtime's isolation and replays the persisted
	// provenance (#36 iso-review H5).
	if got := resumeOpts(rtIso, false); got.iso != rtIso || got.agentAuthored {
		t.Fatalf("resume opts: %+v", got)
	}
	if got := resumeOpts(rtIso, true); !got.agentAuthored {
		t.Fatalf("agent-authored resume must keep the flag: %+v", got)
	}
}

// Regression (#36 iso-review C1): a deny+egress launch gets the ENFORCED
// wiring — proxy env pinned at the in-sandbox forwarder address, the unix
// endpoint resolved, and the argv re-entered through sandbox-net. Unwired,
// it fails closed.
func TestPrepareLaunchEnforcedEgress(t *testing.T) {
	stubPlatform(t)
	oldU, oldSelf := EgressProxyUnix, launchSelfExe
	var asked [][]string
	EgressProxyUnix = func(allow []string) (string, string, error) {
		asked = append(asked, allow)
		return "/run/sock", "ucred", nil
	}
	launchSelfExe = func() (string, error) { return "/opt/conductor", nil }
	t.Cleanup(func() { EgressProxyUnix, launchSelfExe = oldU, oldSelf })

	iso := &config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}}
	argv, _, env, _, err := prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: iso})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--net") ||
		!strings.Contains(joined, "/opt/conductor sandbox-net --listen "+sandbox.ForwardAddr+" --unix /run/sock -- claude") {
		t.Fatalf("enforced argv: %q", joined)
	}
	if !strings.Contains(strings.Join(env, "\n"), "HTTPS_PROXY=http://conductor:ucred@"+sandbox.ForwardAddr) {
		t.Fatalf("proxy env must point at the in-sandbox forwarder: %v", env)
	}
	if len(asked) != 1 || strings.Join(asked[0], ",") != "api.example.com:443" {
		t.Fatalf("unix endpoint allowlist: %v", asked)
	}

	// Unwired → fail closed, never an unfiltered launch.
	EgressProxyUnix = nil
	if _, _, _, _, err := prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: iso}); err == nil {
		t.Fatal("enforced egress without a wired unix endpoint must fail closed")
	}
}

// Regression (#36 iso-review H7): namespace mode shares the daemon's uid, so
// by DEFAULT the daemon's own state/config paths are masked away inside the
// mount namespace; `privileged: true` is the explicit opt-in to the full
// filesystem view.
func TestPrepareLaunchNamespaceMasksDaemonFilesByDefault(t *testing.T) {
	stubPlatform(t)
	oldMasks, oldSelf := DaemonMaskPaths, launchSelfExe
	DaemonMaskPaths = []string{"/var/lib/conductor", "/etc/conductor"}
	launchSelfExe = func() (string, error) { return "/opt/conductor", nil }
	t.Cleanup(func() { DaemonMaskPaths, launchSelfExe = oldMasks, oldSelf })

	iso := &config.IsolationConfig{Mode: "namespace"}
	argv, _, _, _, err := prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: iso})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "/opt/conductor sandbox-net --mask /var/lib/conductor --mask /etc/conductor -- claude") {
		t.Fatalf("default namespace launch must mask daemon paths: %q", joined)
	}

	// privileged: true is the deliberate opt-out — plain unshare, no masking.
	priv := &config.IsolationConfig{Mode: "namespace", Privileged: true}
	argv, _, _, _, err = prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: priv})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(argv, " ")
	if strings.Contains(joined, "sandbox-net") || strings.Contains(joined, "--mask") {
		t.Fatalf("privileged namespace must skip masking: %q", joined)
	}

	// Other modes are untouched by the mask seam (user switches uid;
	// container never sees the daemon fs).
	user := &config.IsolationConfig{Mode: "user", User: "sbx"}
	argv, _, _, _, err = prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: user})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(argv, " "), "--mask") {
		t.Fatalf("user mode must not carry masks: %v", argv)
	}

	// Masks and enforced egress compose: one helper invocation carries both.
	oldU := EgressProxyUnix
	EgressProxyUnix = func([]string) (string, string, error) { return "/run/sock", "c", nil }
	t.Cleanup(func() { EgressProxyUnix = oldU })
	both := &config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}
	argv, _, _, _, err = prepareLaunch("", "/wt", nil, []string{"claude"}, launchOpts{iso: both})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(argv, " ")
	if !strings.Contains(joined, "--mask /var/lib/conductor") || !strings.Contains(joined, "--unix /run/sock") {
		t.Fatalf("masks + enforced egress must compose: %q", joined)
	}
}
