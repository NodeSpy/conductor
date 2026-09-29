package jail

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ShimMain dispatches conductor's binary on argv[0] when it runs as one of
// the jail's shims: git-remote-conductor, the signing shims, the hook shim,
// or a host-set tool (gh, aws, …). It reports handled=false for conductor's
// own name, so the ordinary CLI runs.
func ShimMain(argv []string) (handled bool, code int) {
	if len(argv) == 0 {
		return false, 0
	}
	name := filepath.Base(argv[0])
	args := argv[1:]
	switch name {
	case ShimRemoteHelper:
		return true, RunRemoteHelper(args, os.Stdin, os.Stdout)
	case ShimSSHSign:
		return true, RunSSHSignShim(args)
	case ShimGPGSign:
		return true, RunGPGSignShim(args)
	case ShimHook:
		return true, RunHookShim(args)
	}
	if name == "conductor" || strings.HasPrefix(name, "conductor") || strings.HasPrefix(name, "__") {
		return false, 0
	}
	// Any other name is a tool shim — but only inside a jail. Outside one the
	// binary under another name is a mistake, not a pass-through.
	if os.Getenv(EnvSock) == "" {
		// A test binary or `go run` artifact keeps its own name; only a name
		// that looks like a tool we shim is claimed.
		if !knownShim(name) {
			return false, 0
		}
		fmt.Fprintf(os.Stderr, "%s: this is conductor's host-command shim, and it only works inside a conductor jail\n", name)
		return true, 127
	}
	return true, RunToolShim(name, args)
}

func knownShim(name string) bool {
	switch name {
	case "gh", "aws", "kubectl", "docker", "terraform", "gcloud", "az", "ssh", "scp", "npm", "pnpm":
		return true
	}
	return false
}

// RunToolShim forwards one host command to the broker and relays its output
// and exit code. stdin is forwarded only when an argument asks for it (`-`,
// `-F -`, `--input -`): a shim must never block on a stdin the agent's shell
// left open, and a host command never gets an interactive stream.
func RunToolShim(tool string, args []string) int {
	req := Request{Op: "exec", Tool: tool, Args: args, Env: map[string]string{}}
	req.Cwd, _ = os.Getwd()
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if AgentEnvAllowed(k) {
			req.Env[k] = v
		}
	}
	if wantsStdin(args) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err == nil {
			req.Stdin = b
		}
	}
	rep, err := Call(req, func(r Reply) {
		if len(r.Out) > 0 {
			_, _ = os.Stdout.Write(r.Out)
		}
		if len(r.Err) > 0 {
			_, _ = os.Stderr.Write(r.Err)
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor: %s: %v\n", tool, err)
		return 125
	}
	if rep.Native {
		return execNative(tool, args)
	}
	if rep.Refused != "" {
		fmt.Fprintf(os.Stderr, "conductor: %s refused: %s\n", shortArgs(tool, args), rep.Refused)
		if rep.Exit != 0 {
			return rep.Exit
		}
		return 126
	}
	if rep.Error != "" {
		fmt.Fprintln(os.Stderr, rep.Error)
		if rep.Exit != 0 {
			return rep.Exit
		}
		return 125
	}
	return rep.Exit
}

func wantsStdin(args []string) bool {
	for i, a := range args {
		if a == "-" || a == "--body-file=-" || a == "--input=-" || a == "-F-" || a == "--filename=-" || a == "-f-" {
			return true
		}
		if (a == "-F" || a == "--body-file" || a == "--input" || a == "-f" || a == "--filename") && i+1 < len(args) && args[i+1] == "-" {
			return true
		}
		if strings.HasSuffix(a, "=@-") {
			return true
		}
	}
	return false
}

// execNative replaces the shim with the real binary of that name inside
// the jail — the first one on PATH that is not a shim.
func execNative(tool string, args []string) int {
	real := findReal(tool)
	if real == "" {
		fmt.Fprintf(os.Stderr, "conductor: %s: no native binary in the jail\n", tool)
		return 127
	}
	err := syscall.Exec(real, append([]string{tool}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "conductor: exec %s: %v\n", real, err)
	return 127
}

func findReal(tool string) string {
	self, _ := os.Executable()
	selfFI, _ := os.Stat(self)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || dir == BinDir || strings.HasSuffix(dir, "/conductor-shims") {
			continue
		}
		p := filepath.Join(dir, tool)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if selfFI != nil && os.SameFile(fi, selfFI) {
			continue // conductor bound over the real path
		}
		if lp, err := exec.LookPath(p); err == nil {
			return lp
		}
	}
	return ""
}
