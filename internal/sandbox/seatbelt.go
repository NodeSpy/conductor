package sandbox

import "strings"

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
// NOTE: this allow-set is deliberately conservative and is the piece verified +
// tightened on real macOS (a too-tight profile means the interpreter won't
// start). file-read-metadata is allowed broadly so absolute-path resolution
// works; file *contents* remain denied outside the allow-list, which is the
// property that matters (a sandboxed step still cannot READ the daemon's
// secrets, only observe that some path exists).
const seatbeltBase = `(version 1)
(deny default)
(allow process-fork)
(allow process-exec*)
(allow signal (target self))
(allow sysctl-read)
(allow mach-lookup)
(allow file-read-metadata)
(allow file-read*
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/System")
  (subpath "/Library")
  (subpath "/private/var/db/dyld")
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
  (literal "/dev/stderr"))
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
// the command directly — there is no `--` separator (unlike unshare).
func wrapSeatbelt(argv []string, binds []BindMount, denyNetwork bool) []string {
	out := []string{"sandbox-exec", "-p", seatbeltProfile(binds, denyNetwork)}
	return append(out, argv...)
}

// sbplString quotes s as an SBPL string literal (Scheme-style: wrap in double
// quotes, backslash-escape backslashes and quotes). Bind paths are daemon-built
// temp/worktree paths, but quoting keeps a path with an odd character from
// breaking — or reshaping — the profile.
func sbplString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
