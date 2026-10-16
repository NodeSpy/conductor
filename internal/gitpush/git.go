package gitpush

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// gitBin is the git binary (a var for tests).
var gitBin = "git"

// trustedSSH is core.sshCommand from the operator's OWN config (global, then
// system) — never a repository's.
var trustedSSH = sync.OnceValue(func() string {
	for _, scope := range []string{"--global", "--system"} {
		out, err := exec.Command(gitBin, "config", scope, "--get", "core.sshCommand").Output()
		if err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	return "ssh"
})

// runGit runs conductor's own git in a trusted git dir, hardened: hooks,
// fsmonitor, external diff and the ext transport off, the ssh command from
// trusted config, replace refs ignored, no prompt, and no inherited variable
// that would redirect it. extraEnv adds object-directory wiring.
func runGit(ctx context.Context, gitDir string, extraEnv []string, stdin []byte, args ...string) (string, error) {
	full := append([]string{"--git-dir=" + gitDir,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never", "-c", "diff.external=",
		"-c", "core.sshCommand=" + trustedSSH()}, args...)
	cmd := exec.CommandContext(ctx, gitBin, full...)
	cmd.Env = append(scrubGitEnv(os.Environ()), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// scrubGitEnv drops inherited variables that steer git (a daemon started
// from a hook carries GIT_DIR; GIT_CONFIG_* would inject config). It keeps
// where the operator's own config lives, so conductor's git reads the
// identity and credential helpers the operator's does.
func scrubGitEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_CONFIG_GLOBAL" || k == "GIT_CONFIG_SYSTEM" || k == "GIT_CONFIG_NOSYSTEM" {
			out = append(out, kv)
			continue
		}
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
}

// RemoteURL is remote.origin.url as a trusted git dir's config names it.
func RemoteURL(ctx context.Context, gitDir string) (string, error) {
	out, err := runGit(ctx, gitDir, nil, nil, "config", "--get", "remote.origin.url")
	return strings.TrimSpace(out), err
}
