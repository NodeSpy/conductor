package sandbox

import (
	"fmt"
	"strings"
)

// The macOS agent jail (#154 §10): the same allow-list the Linux pivot_root
// jail mounts, rendered as a Seatbelt profile, with the two surfaces the
// plain base leaves wide open narrowed for agents:
//
//   - process-exec: allowed, except the host-set tools' REAL binaries — the
//     agent reaches them only through the shim dir first on PATH, so
//     `/opt/homebrew/bin/gh` by absolute path is refused.
//   - mach-lookup: an explicit list of the services a toolchain (node, bun,
//     Go, python, git, TLS validation) needs, instead of every service. The
//     DNS resolver's service is on it only when the network is open; the
//     Keychain's (securityd) only when a tool's login needs it.
//
// The network is either open, or outbound ONLY to conductor's loopback
// egress proxy port (plus the agent's own loopback model endpoint): DNS is
// closed with the rest, since the resolver's Mach service and socket are
// denied too.

// agentMachBase are the Mach services every agent launch may look up.
var agentMachBase = []string{
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.system.logger",
	"com.apple.system.notification_center",
	"com.apple.system.DirectoryService.libinfo_v1",
	"com.apple.cfprefsd.daemon",
	"com.apple.cfprefsd.agent",
	"com.apple.logd",
	"com.apple.diagnosticd",
	"com.apple.trustd",
	"com.apple.trustd.agent",
	"com.apple.bsd.dirhelper",
	"com.apple.coreservices.launchservicesd",
	"com.apple.CoreServices.coreservicesd",
	"com.apple.lsd.mapdb",
	"com.apple.distributed_notifications@Uv3",
	"com.apple.analyticsd",
	"com.apple.fonts",
	"com.apple.FontObjectsServer",
	"com.apple.PowerManagement.control",
	"com.apple.SystemConfiguration.configd",
	"com.apple.xpc.loginitemregisterd",
	"com.apple.containermanagerd",
	"com.apple.runningboard",
}

// agentMachNet are looked up only with an open network (name resolution and
// the network configuration it reads).
var agentMachNet = []string{
	"com.apple.dnssd.service",
	"com.apple.SystemConfiguration.DNSConfiguration",
	"com.apple.networkd",
	"com.apple.nehelper",
	"com.apple.symptomsd",
	"com.apple.usymptomsd",
}

// agentMachKeychain are the Security framework services a Keychain-held
// login needs.
var agentMachKeychain = []string{
	"com.apple.SecurityServer",
	"com.apple.securityd.xpc",
	"com.apple.security.agent",
	"com.apple.security.authhost",
	"com.apple.ocspd",
	"com.apple.security.syspolicy",
	"com.apple.CoreAuthentication.daemon",
}

// seatbeltAgentBase is the fixed head of an agent profile: the plain
// base's essentials (see seatbeltBase for why `(literal "/")` is required)
// without its blanket mach-lookup.
const seatbeltAgentBase = `(version 1)
(deny default)
(allow process-fork)
(allow process-exec*)
(allow signal (target same-sandbox))
(allow sysctl-read)
(allow file-read-metadata)
(allow ipc-posix-shm-read-data ipc-posix-shm-write-data ipc-posix-shm-write-create ipc-posix-shm-read-metadata)
(allow ipc-posix-sem)
(allow file-read* (literal "/"))
(allow file-read*
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/System")
  (subpath "/Library")
  (subpath "/Applications/Xcode.app")
  (subpath "/private/var/db/dyld")
  (subpath "/private/var/db/timezone")
  (subpath "/private/var/select")
  (subpath "/opt/homebrew")
  (subpath "/opt/local")
  (subpath "/usr/local")
  (subpath "/etc")
  (subpath "/private/etc"))
(allow file-read* file-write-data file-ioctl
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/random")
  (literal "/dev/urandom")
  (literal "/dev/dtracehelper")
  (literal "/dev/tty")
  (literal "/dev/ptmx")
  (regex #"^/dev/ttys[0-9]+$")
  (literal "/dev/stdout")
  (literal "/dev/stderr")
  (literal "/dev/fd")
  (literal "/dev/autofs_nowait"))
(allow pseudo-tty)
`

// AgentNet is the network verdict of an agent profile.
type AgentNet struct {
	// Open: host network, name resolution included.
	Open bool
	// ProxyPort: the only TCP destination (localhost) when not Open; 0 with
	// Open=false is a full cut.
	ProxyPort int
	// LoopbackPorts: further localhost ports allowed (the agent's own model
	// endpoint when it is a local router).
	LoopbackPorts []int
}

// seatbeltAgentProfile renders the agent profile.
func seatbeltAgentProfile(binds []BindMount, a *AgentProfile, n AgentNet) string {
	var b strings.Builder
	b.WriteString(seatbeltAgentBase)
	mach := append([]string(nil), agentMachBase...)
	if n.Open {
		mach = append(mach, agentMachNet...)
	}
	if a.Keychain {
		mach = append(mach, agentMachKeychain...)
	}
	b.WriteString("(allow mach-lookup")
	for _, s := range mach {
		b.WriteString("\n  (global-name " + sbplString(s) + ")")
	}
	b.WriteString(")\n")
	for _, m := range resolveBinds(binds) {
		if m.Path == "" {
			continue
		}
		if m.RO {
			b.WriteString("(allow file-read* (subpath " + sbplString(m.Path) + "))\n")
		} else {
			b.WriteString("(allow file-read* file-write* (subpath " + sbplString(m.Path) + "))\n")
		}
	}
	for _, p := range resolvePaths(a.ReadOnly) {
		b.WriteString("(allow file-read* (subpath " + sbplString(p) + "))\n")
	}
	for _, p := range resolvePaths(a.ReadWrite) {
		b.WriteString("(allow file-read* file-write* (subpath " + sbplString(p) + "))\n")
	}
	// Later rules win in SBPL: the read-only carve-outs inside read-write
	// paths (a git common dir's config/hooks/objects/info) and the denials.
	for _, p := range resolvePaths(a.WriteDeny) {
		b.WriteString("(deny file-write* (subpath " + sbplString(p) + "))\n")
	}
	for _, p := range resolvePaths(a.Hide) {
		b.WriteString("(deny file-read* file-write* (subpath " + sbplString(p) + "))\n")
	}
	for _, p := range resolvePaths(a.Unhide) {
		b.WriteString("(allow file-read* file-write* (subpath " + sbplString(p) + "))\n")
	}
	for _, p := range resolvePaths(a.ExecDeny) {
		b.WriteString("(deny process-exec (literal " + sbplString(p) + "))\n")
	}
	if a.Keychain {
		b.WriteString("(allow file-read* file-write* (subpath " + sbplString(a.Home+"/Library/Keychains") + "))\n")
	}
	for _, s := range resolvePaths(a.UnixSockets) {
		b.WriteString("(allow network-outbound (remote unix-socket (path-literal " + sbplString(s) + ")))\n")
	}
	switch {
	case n.Open:
		b.WriteString("(allow network*)\n")
	default:
		b.WriteString("(deny network*)\n")
		for _, s := range resolvePaths(a.UnixSockets) {
			b.WriteString("(allow network-outbound (remote unix-socket (path-literal " + sbplString(s) + ")))\n")
		}
		ports := append([]int(nil), n.LoopbackPorts...)
		if n.ProxyPort > 0 {
			ports = append(ports, n.ProxyPort)
		}
		for _, p := range ports {
			b.WriteString(fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%d\"))\n", p))
		}
	}
	return b.String()
}

// wrapSeatbeltAgent is wrapSeatbelt for an agent launch.
func wrapSeatbeltAgent(argv []string, binds []BindMount, a *AgentProfile, n AgentNet) []string {
	out := []string{"sandbox-exec", "-p", seatbeltAgentProfile(binds, a, n)}
	return append(out, argv...)
}

func resolvePaths(ps []string) []string {
	var bs []BindMount
	for _, p := range ps {
		if p != "" {
			bs = append(bs, BindMount{Path: p})
		}
	}
	var out []string
	for _, b := range resolveBinds(bs) {
		out = append(out, b.Path)
	}
	return out
}
