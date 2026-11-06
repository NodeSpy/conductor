package sandbox

import (
	"fmt"
	"strings"
)

// PluginProfile is what the macOS plugin jail allows beyond the base system:
// the Seatbelt rendering of a plugin's resolved declaration. Everything not
// here is denied: no program runs but Exec, no file outside the system dirs
// is read but Read and Write, nothing is written but Write, and the network
// reaches only conductor's egress proxy and the declared loopback ports.
type PluginProfile struct {
	// Exec are the absolute paths that may be executed: the plugin binary
	// and each declared command (as found on PATH and its resolved target).
	Exec []string
	// Read and Write are absolute paths (subtrees).
	Read, Write []string
	// ProxyPort is the loopback port of the plugin's egress proxy (0: none).
	ProxyPort int
	// LoopbackPorts are declared loopback destinations reached directly.
	LoopbackPorts []int
	// ListenPorts are declared ports the plugin serves on.
	ListenPorts []int
	// Keychain allows the login Keychain's services and files.
	Keychain bool
	// Home is the user's home (for the Keychain's directory).
	Home string
}

// seatbeltPluginBase is the agent base without its blanket process-exec,
// and without the package-manager trees (/opt/homebrew, /opt/local,
// /usr/local): a plugin reads the OS, not what is installed on it.
const seatbeltPluginBase = `(version 1)
(deny default)
(allow process-fork)
(allow signal (target same-sandbox))
(allow sysctl-read)
(allow file-read-metadata)
(allow file-read* (literal "/"))
(allow file-read*
  (subpath "/usr/lib")
  (subpath "/usr/share")
  (subpath "/System")
  (subpath "/Library/Preferences")
  (subpath "/private/var/db/dyld")
  (subpath "/private/var/db/timezone")
  (subpath "/private/etc/ssl")
  (literal "/private/etc/hosts")
  (literal "/private/etc/passwd")
  (literal "/private/etc/group")
  (literal "/private/etc/localtime")
  (literal "/etc"))
(allow file-read* file-write-data file-ioctl
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/random")
  (literal "/dev/urandom")
  (literal "/dev/dtracehelper")
  (literal "/dev/stdout")
  (literal "/dev/stderr")
  (literal "/dev/fd")
  (literal "/dev/autofs_nowait"))
`

// seatbeltPluginProfile renders the plugin profile.
func seatbeltPluginProfile(p *PluginProfile) string {
	var b strings.Builder
	b.WriteString(seatbeltPluginBase)
	b.WriteString("(allow mach-lookup")
	mach := append([]string(nil), agentMachBase...)
	if p.Keychain {
		mach = append(mach, agentMachKeychain...)
	}
	for _, s := range mach {
		b.WriteString("\n  (global-name " + sbplString(s) + ")")
	}
	b.WriteString(")\n")
	if exec := resolvePaths(p.Exec); len(exec) > 0 {
		b.WriteString("(allow process-exec")
		for _, e := range exec {
			b.WriteString("\n  (literal " + sbplString(e) + ")")
		}
		b.WriteString(")\n")
		// A program's own bundle or install tree (its libraries) is
		// readable: what it loads, not what it may run.
		for _, e := range exec {
			b.WriteString("(allow file-read* (literal " + sbplString(e) + "))\n")
		}
	}
	for _, r := range resolvePaths(p.Read) {
		b.WriteString("(allow file-read* (subpath " + sbplString(r) + "))\n")
	}
	for _, w := range resolvePaths(p.Write) {
		b.WriteString("(allow file-read* file-write* (subpath " + sbplString(w) + "))\n")
	}
	if p.Keychain && p.Home != "" {
		b.WriteString("(allow file-read* file-write* (subpath " + sbplString(p.Home+"/Library/Keychains") + "))\n")
	}
	b.WriteString("(deny network*)\n")
	// A declared unix socket is a written path: connecting to it is allowed.
	for _, w := range resolvePaths(p.Write) {
		b.WriteString("(allow network-outbound (remote unix-socket (subpath " + sbplString(w) + ")))\n")
	}
	ports := append([]int(nil), p.LoopbackPorts...)
	if p.ProxyPort > 0 {
		ports = append(ports, p.ProxyPort)
	}
	for _, port := range ports {
		b.WriteString(fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%d\"))\n", port))
	}
	for _, port := range p.ListenPorts {
		b.WriteString(fmt.Sprintf("(allow network-bind network-inbound (local ip \"*:%d\"))\n", port))
	}
	return b.String()
}

// WrapSeatbeltPlugin wraps argv in the macOS plugin jail.
func WrapSeatbeltPlugin(argv []string, p *PluginProfile) []string {
	return append([]string{"sandbox-exec", "-p", seatbeltPluginProfile(p)}, argv...)
}
