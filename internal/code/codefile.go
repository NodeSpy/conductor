package code

import (
	"fmt"
	"os"
	"strings"
)

// resolveCodeFile lets a code step load its `code:` from a file instead of
// inlining the source: a `file:` prefix names a path on the DAEMON's
// filesystem (where the config lives), whose contents become the code. A
// leading `~/` is expanded. Anything without the prefix is returned unchanged —
// it is inline source, exactly as before.
//
// The `file:` prefix is required (rather than guessing whether a bare string is
// a path) because a code step's `code:` can legitimately be a short inline
// command like `echo hi` or a jq expression like `keys`, which must never be
// mistaken for a filename. This runs for the host-interpreter, `cli`, and `go`
// paths; a PLUGIN engine receives `code:` verbatim and resolves `file:` itself
// (see conductor-plugins' enginekit / the wasm engine).
func resolveCodeFile(code string) (string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(code), "file:")
	if !ok {
		return code, nil
	}
	path := expandHome(strings.TrimSpace(rest))
	if path == "" {
		return "", fmt.Errorf("code: file: needs a path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("code: reading %q: %w", path, err)
	}
	return string(b), nil
}

// expandHome replaces a leading "~/" (or a bare "~") with the user's home dir.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + strings.TrimPrefix(p, "~")
		}
	}
	return p
}
