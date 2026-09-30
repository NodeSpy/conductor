package sandbox

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// The darwin counterpart of TestWrapLocalNamespace: the same specs on the
// Seatbelt backend render `sandbox-exec -p <profile> <argv>` — the workdir in
// the allow-list, the network open by default and cut for deny — with no
// Linux plumbing. Cgroup limits have no macOS analog and are dropped (no
// systemd-run prefix).
func TestWrapLocalNamespaceSeatbelt(t *testing.T) {
	withBackend(t, "darwin")
	argv, err := FromConfig(&config.IsolationConfig{Mode: "namespace"}).WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 4 || argv[0] != "sandbox-exec" || argv[1] != "-p" || argv[3] != "tool" {
		t.Fatalf("seatbelt wrap: want [sandbox-exec -p <profile> tool], got %q", argv)
	}
	p := argv[2]
	for _, want := range []string{"(version 1)", "(deny default)", `(allow file-read* file-write* (subpath "/wt"))`, "(allow network*)"} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "(deny network*)") {
		t.Fatalf("no deny: the network stays open:\n%s", p)
	}

	s := FromConfig(&config.IsolationConfig{
		Mode:    "namespace",
		Network: &config.IsolationNetwork{Deny: true},
		Limits:  &config.IsolationLimits{Memory: "2g", CPU: "200%", Pids: 128},
	})
	argv, err = s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "sandbox-exec" || argv[len(argv)-1] != "tool" {
		t.Fatalf("seatbelt wrap with deny+limits: %q", argv)
	}
	if !strings.Contains(argv[2], "(deny network*)") || strings.Contains(argv[2], "(allow network*)") {
		t.Fatalf("deny must cut the network:\n%s", argv[2])
	}
	joined := strings.Join(argv, " ")
	for _, linux := range []string{"unshare", "systemd-run", "--net", "sandbox-net", "MemoryMax"} {
		if strings.Contains(joined, linux) {
			t.Fatalf("the Seatbelt wrap must carry no Linux plumbing (%q): %q", linux, joined)
		}
	}
}

// The darwin counterpart of TestWrapLocalEnforcedEgress: an enforced
// allowlist never launches unfiltered on Seatbelt either. Without the wiring
// it refuses to launch; a plain namespace spec (config validation rejects
// deny+egress there — no in-sandbox forwarder) gets a full network cut; the
// agent jail reaches nothing but conductor's proxy port.
func TestWrapLocalEnforcedEgressSeatbelt(t *testing.T) {
	withBackend(t, "darwin")
	ns := FromConfig(&config.IsolationConfig{Mode: "namespace",
		Network: &config.IsolationNetwork{Deny: true, Egress: []string{"api.example.com:443"}}})
	if !ns.EnforcedEgress() {
		t.Fatal("namespace deny+egress must be enforced")
	}
	if _, err := ns.WrapLocal([]string{"claude"}, "/wt", nil, nil); err == nil {
		t.Fatal("enforced egress without wiring must refuse to launch")
	}
	nf := &NetForward{Self: "/usr/bin/conductor", UnixSocket: "/tmp/egress.sock"}
	argv, err := ns.WrapLocal([]string{"claude", "-p", "x"}, "/wt", nil, nf)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "sandbox-exec" || strings.Join(argv[3:], " ") != "claude -p x" {
		t.Fatalf("seatbelt wrap: %q", argv)
	}
	if p := argv[2]; !strings.Contains(p, "(deny network*)") || strings.Contains(p, "(allow network") {
		t.Fatalf("a plain enforced allowlist is a full network cut on Seatbelt:\n%s", p)
	}

	// The agent jail: outbound only to the proxy's loopback port.
	nf = &NetForward{Self: "/usr/bin/conductor", ProxyPort: 18123, Agent: &AgentProfile{Home: "/Users/op"}}
	argv, err = ns.WrapLocal([]string{"claude"}, "/wt", nil, nf)
	if err != nil {
		t.Fatal(err)
	}
	p := argv[2]
	if !strings.Contains(p, "(deny network*)") || !strings.Contains(p, `(allow network-outbound (remote ip "localhost:18123"))`) {
		t.Fatalf("the agent jail must reach only the proxy port:\n%s", p)
	}
	if strings.Contains(p, "(allow network*)") {
		t.Fatalf("the agent jail's network must not be open:\n%s", p)
	}
	if strings.Contains(strings.Join(argv, " "), "sandbox-net") {
		t.Fatalf("no in-sandbox forwarder on Seatbelt: %q", argv)
	}
}

// The unpinned backend is the host's own, and on a Mac the profiles it
// renders are accepted by sandbox-exec and enforced: the payload runs in the
// workdir, and a deny profile cuts the network.
func TestWrapLocalNamespaceNativeBackend(t *testing.T) {
	argv, err := FromConfig(&config.IsolationConfig{Mode: "namespace"}).WrapLocal([]string{"/usr/bin/true"}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "linux":
		if argv[0] != "unshare" {
			t.Fatalf("linux: want the unshare backend, got %q", argv)
		}
	case "darwin":
		if argv[0] != "sandbox-exec" {
			t.Fatalf("darwin: want the Seatbelt backend, got %q", argv)
		}
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("sandbox-exec rejected the rendered profile: %v\n%s", err, out)
		}
		ws := t.TempDir()
		deny := FromConfig(&config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}})
		argv, err = deny.WrapLocal([]string{"/bin/sh", "-c", "echo ok > f && cat f && /usr/bin/nc -z -G 3 1.1.1.1 443 && echo NET-REACHED"}, ws, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = ws
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "ok") {
			t.Fatalf("the payload must write its workdir under the profile:\n%s", out)
		}
		if strings.Contains(string(out), "NET-REACHED") {
			t.Fatalf("a deny profile must cut the network:\n%s", out)
		}
	default:
		t.Skipf("no namespace backend on %s", runtime.GOOS)
	}
}
