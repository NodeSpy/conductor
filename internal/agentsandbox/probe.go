package agentsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Probe reports whether this box can build the agent sandbox: "" when it
// can, else why not. A box that cannot gets every agent launch refused, with
// this reason — never an unsandboxed agent.
func Probe() string {
	switch runtime.GOOS {
	case "linux":
		if os.Geteuid() == 0 {
			return "conductor runs as root, where a user namespace is no privilege boundary — run the daemon as a normal user"
		}
		if _, err := exec.LookPath("unshare"); err != nil {
			return "unshare (util-linux) is not installed"
		}
		out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "--pid", "--net", "--fork", "true").CombinedOutput()
		if err != nil {
			why := fmt.Sprintf("unprivileged user namespaces are unavailable (%v: %s)", err, strings.TrimSpace(string(out)))
			if b, rerr := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); rerr == nil && strings.TrimSpace(string(b)) == "1" {
				why += " — AppArmor restricts them (Ubuntu 23.10+): set kernel.apparmor_restrict_unprivileged_userns=0, or load an AppArmor profile that allows user namespaces for conductor"
			}
			return why
		}
		return ""
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return "sandbox-exec is not available"
		}
		return ""
	default:
		return "conductor's agent sandbox has no backend on " + runtime.GOOS
	}
}
