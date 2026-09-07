//go:build linux

package sandbox

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestNamespaceNetStructuralCutoff proves the promise the docs make for
// `mode: namespace` + `network: {deny: true}` (#36 §15): the launch is not
// merely *told* to have no network — the OS *structurally* removes it. The
// unit test TestWrapLocalNamespace asserts the argv carries `--net`; this test
// asserts the kernel then honours it.
//
// It re-execs the test binary as the sandboxed child (the standard Go pattern):
// the child dials a loopback listener the parent opened and reports reachability
// through its exit code. The ONLY difference between the two runs is `--net`:
//
//   - Deny:false → no `--net`: the child shares the host net namespace and
//     reaches the parent's 127.0.0.1 listener (exit 0).
//   - Deny:true  → WrapLocal adds `--net`: the child gets an empty net
//     namespace (loopback down, a different netns than the parent's), so the
//     SAME dial cannot reach the listener (non-zero exit).
//
// Gut the `--net` emission from WrapLocal and the deny run would reach the
// listener — this test goes red. That is the functional cutoff the paseo-only
// e2e stack cannot cover (validate rejects isolation on paseo runtimes).
func TestNamespaceNetStructuralCutoff(t *testing.T) {
	// Child role: dial the target and let the exit code carry the verdict.
	if addr := os.Getenv("SANDBOX_NET_PROBE"); addr != "" {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			os.Exit(3) // unreachable — the network was severed
		}
		_ = conn.Close()
		os.Exit(0) // reachable
	}

	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH — namespace isolation is Linux+util-linux only")
	}
	// Capability gate: the exact prefix WrapLocal emits must run at all on this
	// box (unprivileged user namespaces enabled, util-linux new enough). A loud
	// skip when it can't — never a false green.
	gate, err := (&Spec{Mode: "namespace", Deny: true}).WrapLocal([]string{"true"}, "", nil, nil)
	if err != nil {
		t.Fatalf("WrapLocal(namespace,deny): %v", err)
	}
	if err := exec.Command(gate[0], gate[1:]...).Run(); err != nil {
		t.Skipf("unprivileged network namespaces unavailable here (%v) — "+
			"set kernel.unprivileged_userns_clone=1 / run on a supporting kernel to exercise this", err)
	}

	// A listener in THIS (parent) net namespace, on loopback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().String()

	// Re-exec THIS test binary, scoped to the probe branch above.
	childArgs := []string{os.Args[0], "-test.run=^TestNamespaceNetStructuralCutoff$"}
	run := func(deny bool) error {
		argv, err := (&Spec{Mode: "namespace", Deny: deny}).WrapLocal(childArgs, "", nil, nil)
		if err != nil {
			t.Fatalf("WrapLocal(deny=%v): %v", deny, err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "SANDBOX_NET_PROBE="+addr)
		return cmd.Run()
	}

	// Shared host netns: the loopback listener is reachable.
	if err := run(false); err != nil {
		t.Fatalf("namespace WITHOUT --net should reach the loopback listener, got exit error %v "+
			"(if this env can't share the netns the capability gate should have skipped)", err)
	}
	// deny:true adds --net → empty netns: the SAME dial must fail. A nil error
	// here means the network was NOT severed — the core §15 guarantee is broken.
	if err := run(true); err == nil {
		t.Fatal("namespace WITH --net reached the loopback listener — network was NOT structurally severed " +
			"(--net missing from the launch or ineffective)")
	}
}
