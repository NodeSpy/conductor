//go:build darwin

package sandbox

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// escServeLine serves one line to each connection on a fresh loopback port.
func escServeLine(t *testing.T, line string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(line + "\n"))
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// Red-team escape battery for the macOS Seatbelt agent profile. It runs the
// REAL `sandbox-exec` with the agent profile and attempts the escapes in the
// brief that are macOS-specific or that the kernel (not a namespace) must
// enforce: writing outside the allow-list, reading a decoy secret by a
// /var↔/private/var alias, reaching a non-allowlisted loopback port under the
// restricted-network profile, a child process escaping confinement, and — the
// headline — using LaunchServices/`open` (com.apple.coreservices.launchservicesd)
// to have launchd spawn an UNCONFINED process that touches a canary outside the
// sandbox.
//
// These run only on a real Mac (the sandbox-canary CI matrix's macos-latest
// leg); each FAILS LOUDLY if the wall is breached, and otherwise reports it held.

// escArea is a fresh workspace + an out-of-sandbox canary area for one probe.
type escArea struct {
	ws      string // read-write workspace (the agent's)
	outside string // a dir the profile does NOT allow writing
	canary  string // a file under outside the agent must never create
}

func newEscArea(t *testing.T) escArea {
	t.Helper()
	ws := t.TempDir()
	outside := t.TempDir() // a sibling temp root, NOT in the profile's allow-list
	return escArea{ws: ws, outside: outside, canary: filepath.Join(outside, "ESCAPED")}
}

// runAgent runs argv under the agent profile with a = ReadWrite the workspace
// only (never `outside`). cwd/HOME/TMPDIR are the workspace.
func runEscAgent(t *testing.T, ws string, a *AgentProfile, n AgentNet, argv ...string) (string, int) {
	t.Helper()
	prof := seatbeltAgentProfile([]BindMount{{Path: ws}}, a, n)
	cmd := exec.Command("sandbox-exec", append([]string{"-p", prof}, argv...)...)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "TMPDIR="+ws, "HOME="+ws)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return string(out), code
}

func TestSeatbeltEscapeWriteOutside(t *testing.T) {
	ar := newEscArea(t)
	a := &AgentProfile{Home: homeDir(), ReadWrite: []string{ar.ws}}
	runEscAgent(t, ar.ws, a, AgentNet{ProxyPort: 9}, "/bin/sh", "-c", "echo ESCAPED > "+ar.canary+" 2>/dev/null; true")
	if _, err := os.Stat(ar.canary); err == nil {
		t.Fatalf("ESCAPE: wrote a canary outside the allow-list at %s", ar.canary)
	}
	// A child process (exec'd) must stay confined too (Seatbelt is inherited).
	runEscAgent(t, ar.ws, a, AgentNet{ProxyPort: 9}, "/bin/sh", "-c", "/usr/bin/env sh -c 'echo ESCAPED > "+ar.canary+"' 2>/dev/null; true")
	if _, err := os.Stat(ar.canary); err == nil {
		t.Fatalf("ESCAPE: an exec'd child wrote outside the allow-list at %s", ar.canary)
	}
}

func TestSeatbeltEscapeDecoyAliasing(t *testing.T) {
	// A decoy secret the profile never allows. If it happens to live under
	// /var (a symlink to /private/var) prove neither spelling reads it.
	ar := newEscArea(t)
	secret := filepath.Join(ar.outside, "id_decoy")
	if err := os.WriteFile(secret, []byte("DECOY"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &AgentProfile{Home: homeDir(), ReadWrite: []string{ar.ws}}
	for _, p := range aliasSpellings(secret) {
		out, _ := runEscAgent(t, ar.ws, a, AgentNet{ProxyPort: 9}, "/bin/cat", p)
		if strings.Contains(out, "DECOY") {
			t.Fatalf("ESCAPE: read the decoy secret via %s", p)
		}
	}
	// The operator's real home secrets must be unreadable too.
	out, _ := runEscAgent(t, ar.ws, a, AgentNet{Open: true}, "/bin/ls", filepath.Join(homeDir(), ".ssh"))
	if strings.Contains(out, "id_") {
		t.Fatalf("ESCAPE: listed ~/.ssh: %s", out)
	}
}

// aliasSpellings returns a path and its /var↔/private/var alias.
func aliasSpellings(p string) []string {
	out := []string{p}
	if strings.HasPrefix(p, "/private/var/") {
		out = append(out, strings.TrimPrefix(p, "/private"))
	} else if strings.HasPrefix(p, "/var/") {
		out = append(out, "/private"+p)
	}
	return out
}

func TestSeatbeltEscapeNetworkBeyondProxy(t *testing.T) {
	// Under the restricted network the only outbound allowed is the proxy port
	// (and declared loopback model ports). A listener on ANOTHER loopback port
	// — standing in for another dispatch's server or a host service — must be
	// unreachable.
	ln := escServeLine(t, "LEAK")
	_, other := splitHostPort(ln.Addr().String())
	ar := newEscArea(t)
	a := &AgentProfile{Home: homeDir(), ReadWrite: []string{ar.ws}}
	script := "import socket,sys\n" +
		"try:\n  s=socket.create_connection(('127.0.0.1'," + other + "),2); b=s.recv(16); print('GOT',b)\nexcept Exception as e: print('blocked',type(e).__name__)\n"
	out, _ := runEscAgent(t, ar.ws, a, AgentNet{ProxyPort: 9}, "/usr/bin/python3", "-c", script)
	if strings.Contains(out, "LEAK") || strings.Contains(out, "GOT") {
		t.Fatalf("ESCAPE: reached a non-allowlisted loopback port: %s", out)
	}
}

// TestSeatbeltEscapeLaunchServicesCallHome is the headline macOS escape: a
// Seatbelt profile that allows mach-lookup to launchservicesd lets a confined
// process ask launchd (via `open` on an app bundle it writes) to spawn a
// process OUTSIDE the sandbox. If that process can touch a canary the confined
// one cannot, the sandbox is defeated. It tries several launchd-spawn
// primitives and fails if any lands the canary.
func TestSeatbeltEscapeLaunchServicesCallHome(t *testing.T) {
	ar := newEscArea(t)

	// A minimal .app whose executable writes the canary when launchd spawns it.
	app := filepath.Join(ar.ws, "Evil.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(macos, "Evil")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ESCAPED > "+ar.canary+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Evil</string>
<key>CFBundleIdentifier</key><string>test.conductor.evil</string>
<key>CFBundleName</key><string>Evil</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}

	// Several launchd-spawn primitives, each run confined.
	primitives := [][]string{
		{"/usr/bin/open", app},
		{"/usr/bin/open", "-a", app},
		{"/bin/sh", "-c", "/usr/bin/open -g " + app + "; /bin/launchctl submit -l conductor.evil -- /bin/sh " + exe + " 2>/dev/null; true"},
		{"/usr/bin/osascript", "-e", `do shell script "echo ESCAPED > ` + ar.canary + `"`},
	}
	a2 := &AgentProfile{Home: homeDir(), ReadWrite: []string{ar.ws}}
	for _, argv := range primitives {
		out, code := runEscAgent(t, ar.ws, a2, AgentNet{ProxyPort: 9}, argv...)
		t.Logf("launchd-spawn %v -> code=%d %s", argv, code, strings.TrimSpace(out))
	}
	// Give launchd a moment to spawn anything it accepted.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ar.canary); err == nil {
			t.Fatalf("ESCAPE: a launchd-spawned process wrote %s — the agent profile lets a confined process break out via LaunchServices/launchd. Deny mach-lookup to launchservicesd/coreservicesd (and process-exec of /usr/bin/open) for agents, or otherwise block the launchd-spawn path.", ar.canary)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
