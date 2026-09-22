package sandbox

import (
	"path/filepath"
	"strings"
)

// Seatbelt is the macOS backend for `mode: namespace` — the same
// least-privilege intent the Linux pivot_root jail realizes, rendered as an
// SBPL profile and enforced by the kernel via `sandbox-exec`. macOS has no
// unprivileged user namespaces, but Seatbelt (shipped on every Mac, used by
// Nix / Chromium / Bazel) gives the same deny-by-default + filesystem/network
// allow-list with no root and no container:
//
//   (deny default)                 ⇔ the daemon's config/state/secrets are
//                                    unreadable (our "hidden by absence")
//   (allow file-read* (subpath P)) ⇔ a read-only BindMount
//   (allow file-write* (subpath P))⇔ a read-write BindMount (workdir, fs:)
//   (deny network*)                ⇔ network: {deny: true}
//
// So the SAME NetForward.Binds allow-list the Linux jail consumes drives the
// Mac profile — one config surface, backend chosen by GOOS.

// seatbeltBase is the fixed preamble every generated profile carries: deny by
// default, then the minimum an interpreter/shell (bash, python3, node) needs
// to actually launch — the dyld shared cache, the system frameworks, the
// standard character devices, and the process/mach/sysctl operations a normal
// exec performs. Contents outside the allow-list stay denied; this is only the
// machinery of running a program at all.
//
// This allow-set was verified on real macOS (26.x, Apple Silicon): a shell,
// /bin/echo, python3, and python urllib all launch and run under it, while a
// path outside the allow-list stays unreadable. Two hard-won essentials:
//   - `(allow file-read* (literal "/"))` — reading the ROOT inode; without it
//     EVERY launch aborts (`Abort trap: 6`) because the loader reads "/". The
//     literal grants only the root directory entry, not its subtree, so the
//     jail holds.
//   - file-read-metadata is allowed broadly so absolute-path resolution works;
//     file *contents* stay denied outside the allow-list — the property that
//     matters (a step cannot READ the daemon's secrets, only observe a path
//     exists).
//
// Caller paths are symlink-resolved before they become subpath rules (see
// resolveBinds): macOS temp/workdirs are `/var/…`, which the sandbox evaluates
// as the canonical `/private/var/…`; an unresolved `/var` subpath silently
// matches nothing.
const seatbeltBase = `(version 1)
(deny default)
(allow process-fork)
(allow process-exec*)
(allow signal (target self))
(allow sysctl-read)
(allow mach-lookup)
(allow file-read-metadata)
(allow file-read* (literal "/"))
(allow file-read*
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/System")
  (subpath "/Library")
  (subpath "/private/var/db/dyld")
  (subpath "/private/var/db/timezone")
  (subpath "/private/var/select")
  (subpath "/opt/homebrew")
  (subpath "/usr/local")
  (subpath "/etc")
  (subpath "/private/etc"))
(allow file-read* file-write-data
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/random")
  (literal "/dev/urandom")
  (literal "/dev/dtracehelper")
  (literal "/dev/tty")
  (literal "/dev/stdout")
  (literal "/dev/stderr")
  (literal "/dev/autofs_nowait"))
`

// seatbeltProfile renders the SBPL profile for a launch: the fixed base, then
// the caller's allow-list (each BindMount as a read-only or read-write subpath
// rule), then the network verdict. denyNetwork mirrors the Linux `--net` cut;
// when false the process keeps host network (open, or the advisory proxy).
func seatbeltProfile(binds []BindMount, denyNetwork bool) string {
	var b strings.Builder
	b.WriteString(seatbeltBase)
	for _, m := range binds {
		if m.Path == "" {
			continue
		}
		if m.RO {
			b.WriteString("(allow file-read* (subpath " + sbplString(m.Path) + "))\n")
		} else {
			b.WriteString("(allow file-read* file-write* (subpath " + sbplString(m.Path) + "))\n")
		}
	}
	if denyNetwork {
		b.WriteString("(deny network*)\n")
	} else {
		b.WriteString("(allow network*)\n")
	}
	return b.String()
}

// wrapSeatbelt builds the argv that runs argv under a generated profile:
// `sandbox-exec -p <profile> <argv...>`. sandbox-exec takes the profile then
// the command directly — there is no `--` separator (unlike unshare). Bind
// paths are symlink-resolved first (see resolveBinds).
func wrapSeatbelt(argv []string, binds []BindMount, denyNetwork bool) []string {
	out := []string{"sandbox-exec", "-p", seatbeltProfile(resolveBinds(binds), denyNetwork)}
	return append(out, argv...)
}

// resolveBinds canonicalizes each bind path via EvalSymlinks so the SBPL
// subpath rule matches the path the SANDBOX sees. On macOS a temp/workdir is
// `/var/folders/…`, but `/var` is a symlink to `/private/var`; the sandbox
// evaluates the resolved `/private/var/folders/…`, so an unresolved `/var`
// subpath allows nothing. Best-effort: a path that can't be resolved (e.g.
// doesn't exist yet) is left as-is. On Linux the bind paths are already real,
// so this is a no-op there.
func resolveBinds(binds []BindMount) []BindMount {
	out := make([]BindMount, len(binds))
	for i, b := range binds {
		out[i] = b
		if b.Path != "" {
			if r, err := filepath.EvalSymlinks(b.Path); err == nil {
				out[i].Path = r
			}
		}
	}
	return out
}

// sbplString quotes s as an SBPL string literal (Scheme-style: wrap in double
// quotes, backslash-escape backslashes and quotes). Bind paths are daemon-built
// temp/worktree paths, but quoting keeps a path with an odd character from
// breaking — or reshaping — the profile.
func sbplString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
