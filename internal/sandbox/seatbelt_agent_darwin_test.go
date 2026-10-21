//go:build darwin

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These run on a real Mac (they are skipped elsewhere) and prove the agent
// profile's walls with the kernel's own enforcement.

func runProfile(t *testing.T, prof string, argv ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sandbox-exec", append([]string{"-p", prof}, argv...)...)
	// As in the jail: the workspace is the cwd and the scratch dir TMPDIR.
	ws := t.TempDir()
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "TMPDIR="+ws, "HOME="+ws)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("sandbox-exec: %v", err)
	}
	return string(out), code
}

func agentFixture(t *testing.T) (ws string, a *AgentProfile) {
	home, _ := os.UserHomeDir()
	ws = t.TempDir()
	// t.TempDir() roots are allowed read-write too (runProfile uses one as
	// cwd/TMPDIR/HOME).
	return ws, &AgentProfile{Home: home, ReadWrite: []string{ws, filepath.Dir(ws)}}
}

func TestSeatbeltAgentRunsToolchainAndHidesHome(t *testing.T) {
	ws, a := agentFixture(t)
	home, _ := os.UserHomeDir()
	prof := seatbeltAgentProfile([]BindMount{{Path: ws}}, a, AgentNet{Open: true})
	out, code := runProfile(t, prof, "/bin/sh", "-c", "echo ok > "+ws+"/f && cat "+ws+"/f && /usr/bin/python3 -c 'print(1+1)' && /usr/bin/git --version")
	if code != 0 || !strings.Contains(out, "ok") || !strings.Contains(out, "2") {
		t.Fatalf("toolchain under the agent profile: %d %s", code, out)
	}
	if _, code := runProfile(t, prof, "/bin/ls", filepath.Join(home, ".ssh")); code == 0 {
		t.Fatal("~/.ssh must be unreadable")
	}
	if _, code := runProfile(t, prof, "/usr/bin/touch", filepath.Join(home, "conductor-seatbelt-probe")); code == 0 {
		os.Remove(filepath.Join(home, "conductor-seatbelt-probe"))
		t.Fatal("the real home must not be writable")
	}
}

func TestSeatbeltAgentExecDeny(t *testing.T) {
	ws, a := agentFixture(t)
	a.ExecDeny = []string{"/usr/bin/curl"}
	prof := seatbeltAgentProfile([]BindMount{{Path: ws}}, a, AgentNet{Open: true})
	out, code := runProfile(t, prof, "/bin/sh", "-c", "/usr/bin/curl --version; echo rc=$?")
	if code != 0 || !strings.Contains(out, "rc=126") && !strings.Contains(out, "rc=1") {
		t.Fatalf("exec-denied binary must not run: %s", out)
	}
	if strings.Contains(out, "libcurl") {
		t.Fatalf("curl ran: %s", out)
	}
}

func TestSeatbeltAgentRestrictedNetworkAndDNS(t *testing.T) {
	ws, a := agentFixture(t)
	prof := seatbeltAgentProfile([]BindMount{{Path: ws}}, a, AgentNet{ProxyPort: 9})
	out, code := runProfile(t, prof, "/usr/bin/python3", "-c",
		"import socket\ntry:\n  socket.getaddrinfo('example.com',443); print('dns-ok')\nexcept Exception as e: print('dns-fail', type(e).__name__)\n"+
			"try:\n  socket.create_connection(('93.184.215.14',443),3); print('net-ok')\nexcept Exception as e: print('net-fail', type(e).__name__)\n")
	t.Logf("restricted: code=%d %s", code, out)
	if strings.Contains(out, "dns-ok") || strings.Contains(out, "net-ok") {
		t.Fatalf("restricted network must block DNS and direct connections: %s", out)
	}
}

// TestSeatbeltKeychainProbe answers #154 §10's Keychain question on a real
// Mac: with and without the Security services, can a jailed process read a
// Keychain item it did not create? It prints only the exit status and byte
// count, never the item. CONDUCTOR_KEYCHAIN_PROBE names the service.
func TestSeatbeltKeychainProbe(t *testing.T) {
	svc := os.Getenv("CONDUCTOR_KEYCHAIN_PROBE")
	if svc == "" {
		t.Skip("set CONDUCTOR_KEYCHAIN_PROBE=<service> to probe")
	}
	ws, a := agentFixture(t)
	probe := "/usr/bin/security find-generic-password -s '" + svc + "' -w 2>/dev/null | wc -c | tr -d ' '; echo rc=${PIPESTATUS:-?}"
	for _, kc := range []bool{false, true} {
		a.Keychain = kc
		prof := seatbeltAgentProfile([]BindMount{{Path: ws}}, a, AgentNet{Open: true})
		out, code := runProfile(t, prof, "/bin/bash", "-c", "set -o pipefail; "+probe)
		t.Logf("keychain services allowed=%v: exit=%d bytes/rc=%s", kc, code, strings.TrimSpace(out))
	}
}
