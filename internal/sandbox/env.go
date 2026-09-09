package sandbox

import (
	"os"
	"strings"
)

// baseEnvAllow is the minimal operational environment a spawned, less-trusted
// child inherits. The daemon's full environment routinely carries credentials
// (webhook secrets, tokens passed to conductor via env), so forwarding
// os.Environ() wholesale to third-party code (an external plugin, a sandboxed
// agent) hands every one of them over. LC_* (locale) and this fixed set are the
// only pass-throughs; anything else a child genuinely needs is provided
// explicitly, not inherited ambiently.
var baseEnvAllow = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "TERM": true, "TZ": true, "LANG": true,
	"TMPDIR": true, "TMP": true, "TEMP": true,
}

// MinimalEnv returns os.Environ() filtered to the minimal operational allowlist
// (baseEnvAllow plus LC_* locale vars) — the base environment for a spawned
// child whose access to the daemon's credential-bearing environment must be
// denied. Callers append any explicitly-provided child env AFTER this.
func MinimalEnv() []string {
	return FilterEnv(os.Environ())
}

// FilterEnv applies the minimal allowlist to an arbitrary env slice (exposed for
// testing and for callers that already hold an env list).
func FilterEnv(env []string) []string {
	out := make([]string, 0, len(baseEnvAllow))
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if baseEnvAllow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}
