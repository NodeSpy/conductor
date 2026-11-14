package plugin

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/keyringbroker"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/scratch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The declared-capability jail (docs/wiki/Plugins.md "What a plugin
// declares"). Every plugin runs jailed to exactly what it declares, resolved
// against the instances its process serves; anything undeclared is denied.
// Conductor adds nothing a declaration does not name: no destination from a
// config key's name, no login directory for a CLI, no relay for a protocol.
// A plugin that needs more fails with the denial named, and the fix is its
// declaration. `trust: full` is the one opt-out.
//
// What the jail is, per OS:
//
//   - Linux: user, pid, mount, IPC and network namespaces; pivot_root into
//     the minimal base (sandbox.BaseMinimal: /usr's libraries and data but
//     none of its programs, a curated /etc, no /opt) plus the plugin's own
//     install dir (read-only), its staging dir (TMPDIR), the declared paths,
//     the declared commands bound one by one, and the declared exec paths
//     (read-only, executable). Everything writable is noexec. The network namespace has loopback only: TCP out
//     goes through conductor's egress proxy (allowlist = declared egress) via
//     HTTP(S)_PROXY, a declared loopback destination through a relay, and a
//     declared listen address is listened on by conductor on the host and
//     piped in.
//   - macOS: a deny-default Seatbelt profile (sandbox.PluginProfile) with the
//     same allow-list: exec of the plugin binary and declared commands only,
//     reads of the OS dirs and declared paths, writes of the declared paths,
//     and network to the egress proxy's loopback port, the declared loopback
//     ports and the declared listen ports only.

// jailHome is the user's home, for "~/" entries (a test var).
var jailHome = os.UserHomeDir

// keyringUpstream reaches the user's keyring for a jail's keyring broker (a
// test var; nil is the session bus's Secret Service).
var keyringUpstream func() (keyringbroker.Upstream, error)

// pluginGrant resolves s's declaration into what its jail allows, narrowed
// by the config (network:, allow_env), never widened by it.
func pluginGrant(s Spec) (sdk.Grant, error) {
	home, _ := jailHome()
	caps := s.Manifest.Caps()
	// An env: reference resolves from the env the config lets it have
	// (allow_env), as its env does.
	getenv := func(n string) (string, bool) {
		if s.AllowEnv != nil && !slices.Contains(s.AllowEnv, n) {
			return "", false
		}
		return os.LookupEnv(n)
	}
	g, err := caps.Resolve(sdk.ResolveEnv{Home: home, Getenv: getenv, Conns: s.Conns, Command: func(n string) (string, bool) {
		// Where the command is, as findCommands finds it: the program file
		// the config names, else PATH's. This only anchors a
		// "command:tool/…" path; the path it gives is checked as declared
		// (grantedPaths), and the command itself by findCommands.
		p, named := s.Commands[n]
		if !named {
			var err error
			if p, err = spawnLookPath(n); err != nil {
				return "", false
			}
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", false
		}
		// A /usr/bin developer-tool stub: "command:tool/…" is relative to
		// the tool it runs (findCommands).
		if dev, ok := developerTool(jailCommand{name: n, path: p, real: real, source: fromPATH}); ok && !named {
			return dev, true
		}
		return real, true
	}})
	if err != nil {
		return sdk.Grant{}, fmt.Errorf("plugin %s: %w", s.Name, err)
	}
	if s.Probe {
		// A type-level describe serves no instance: no network, no env,
		// no keyring.
		g.Egress, g.Listen, g.Env, g.Keyring = nil, nil, nil, nil
		return g, nil
	}
	// Each item is checked again as resolved (a record could have been
	// edited, or written by a build that checked less), and one of the
	// items conductor withholds is granted only when the config names it.
	for _, it := range g.Keyring {
		if err := it.Check(); err != nil {
			return sdk.Grant{}, fmt.Errorf("plugin %s: capabilities.keyring: %w", s.Name, err)
		}
	}
	var withheld []sdk.KeyringItem
	g.Keyring, withheld = allowedKeyring(g.Keyring, s.AllowKeyring)
	for _, it := range withheld {
		log.Printf("plugin %s: keyring item %s withheld: it is %s — name it in the connector's allow_keyring to grant it", s.Name, strconv.Quote(it.Selector()), keyringSensitivity(it))
	}
	// network: and allow_env narrow: unset grants everything declared, a
	// set list exactly its entries (none, when it is empty).
	if s.Network != nil {
		narrowed := []string{}
		for _, n := range s.Network {
			hp, err := normHostPort(n)
			if err != nil {
				return sdk.Grant{}, fmt.Errorf("plugin %s: network: %w", s.Name, err)
			}
			if !egressCovered(g.Egress, hp) {
				return sdk.Grant{}, fmt.Errorf("plugin %s: network: %s is not among what it declares (%s) — a config narrows a plugin's network, never widens it", s.Name, hp, strings.Join(g.Egress, ", "))
			}
			narrowed = append(narrowed, hp)
		}
		g.Egress = narrowed
	}
	if s.AllowEnv != nil {
		env := []string{}
		for _, n := range g.Env {
			for _, a := range s.AllowEnv {
				if a == n {
					env = append(env, n)
				}
			}
		}
		g.Env = env
	}
	return g, nil
}

// KeyringMatches counts the items in the user's keyring that carry attrs;
// nil (tests, and hosts that cannot search it) shows no count. The command
// line sets it.
var KeyringMatches func(attrs map[string]string) (int, error)

// keyringMatchCount is how many keyring items a declared one matches now,
// for the user to see what it reads: on macOS, where the jail reads the
// Keychain, none is counted.
func keyringMatchCount(it sdk.KeyringItem) string {
	if KeyringMatches == nil || spawnGOOS == "darwin" {
		return ""
	}
	n, err := KeyringMatches(it.Attributes)
	switch {
	case err != nil:
		return " (your keyring could not be searched: " + shown(err.Error()) + ")"
	case n == 1:
		return " (1 item matches now)"
	}
	return fmt.Sprintf(" (%d items match now)", n)
}

// HereLines spells out what s's declared paths and commands are on this
// machine: "command:git/../../lib/git-core" is /usr/lib/git-core here, a
// config: path is its configured value. Shown with the declaration at
// consent and in `plugin show`.
func HereLines(s Spec) []string {
	g, err := pluginGrant(s)
	if err != nil {
		return []string{"on this machine: " + err.Error()}
	}
	var out []string
	add := func(label string, ps []string) {
		if len(ps) > 0 {
			out = append(out, "on this machine it "+label+": "+strings.Join(ps, ", "))
		}
	}
	found, err := findCommands(s, newGrantChecks(SandboxDeps{}, g), g.Commands)
	if err != nil {
		return []string{"on this machine: " + err.Error()}
	}
	var cmds []string
	for _, c := range found {
		at := c.path
		if c.path != c.real {
			at += " → " + c.real
		}
		cmds = append(cmds, at+" ("+c.source+")")
	}
	add("runs", cmds)
	var loads []string
	for _, c := range found {
		for _, d := range c.reads {
			if !slices.Contains(loads, d) {
				loads = append(loads, d)
			}
		}
	}
	add("reads, to run them (what their package manager installed them with)", loads)
	add("runs programs in", g.Exec)
	add("reads", g.Read)
	var files, sockets []string
	for _, p := range g.Write {
		if isSocket(p) {
			sockets = append(sockets, p)
		} else {
			files = append(files, p)
		}
	}
	add("reads and writes", files)
	add("talks to the program serving the socket", sockets)
	var items []string
	for _, it := range g.Keyring {
		items = append(items, shown(it.Selector())+keyringMatchCount(it))
	}
	add("reads from your keyring the items", items)
	return out
}

func normHostPort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if h, p, err := net.SplitHostPort(s); err == nil {
		return net.JoinHostPort(h, p), nil
	}
	if s == "" || strings.Contains(s, ":") {
		return "", fmt.Errorf("%q is not host[:port]", s)
	}
	return net.JoinHostPort(s, "443"), nil
}

// loopbackHost: a declared destination on this machine.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// splitLoopback separates loopback destinations (reached without the proxy:
// a relay on Linux, a direct connection on macOS) from the rest.
func splitLoopback(egress []string) (remote []string, loopPorts []int) {
	seen := map[int]bool{}
	for _, e := range egress {
		h, p, err := net.SplitHostPort(e)
		if err != nil || !loopbackHost(h) {
			remote = append(remote, e)
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && !seen[n] {
			seen[n] = true
			loopPorts = append(loopPorts, n)
		}
	}
	return remote, loopPorts
}

// jailCommand is one declared command found on this machine.
type jailCommand struct {
	name string
	path string // where the daemon's PATH finds it, directories resolved
	real string // the file it resolves to
	// source is where it was found (locateCommand: fromConfig, fromPATH,
	// fromHomebrew), or what it is part of (an interpreter, a developer
	// tool), for `plugin show`.
	source string
	// reads are the directories it reads to run, as the package manager
	// that installed it says (its install tree, the libraries it loads),
	// checked when found (grantChecks.programDir).
	reads []string
}

// findCommands locates the declared commands (locateCommand: the config's
// `commands:`, PATH, the package manager that linked one onto PATH), with
// the #! interpreter of one that is a script. One that is not installed is
// left out (the plugin's own call fails, naming it) and logged. One found is
// checked like a declared path (grantChecks.program): a command or an
// interpreter that is refused fails the launch, naming it and the fix.
func findCommands(s Spec, gc *grantChecks, names []string) ([]jailCommand, error) {
	var out []jailCommand
	for _, n := range names {
		c, p, err := locateCommand(s, gc, n)
		if errors.Is(err, errNotInstalled) {
			log.Printf("plugin %s: declared command %s is not installed here", s.Name, n)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plugin %s: command %s (%s) refused: %w", s.Name, n, p, err)
		}
		out = append(out, c)
		// A command that is a script runs with its #! interpreter: that is
		// part of running it, and is bound with it.
		for _, ip := range interpreters(c.real) {
			ic, err := gc.program(n, ip)
			if err != nil {
				return nil, fmt.Errorf("plugin %s: command %s (%s) refused: its #! interpreter %s: %w", s.Name, n, c.real, ip, err)
			}
			ic.source = "#! of " + n
			out = append(out, ic)
		}
		if dev, ok := developerTool(c); ok {
			// A macOS /usr/bin developer tool is a stub that runs the real
			// one from the active developer directory: the stub is what the
			// plugin declared, the tool it runs is part of it.
			dc, err := gc.program(n, dev)
			if err != nil {
				return nil, fmt.Errorf("plugin %s: command %s (%s) refused: the developer tool it runs, %s: %w", s.Name, n, p, dev, err)
			}
			dc.source = "developer tool " + n + " runs"
			out = append(out, dc)
		}
	}
	return out, nil
}

// developerTool is the tool a macOS /usr/bin developer-tool stub found on
// PATH runs (`xcrun --find`), when it is another file. Only a /usr/bin hit
// is asked: for any other command xcrun answers from PATH, naming the
// command itself (or, for a symlink conductor did not follow, the link).
func developerTool(c jailCommand) (string, bool) {
	if spawnGOOS != "darwin" || c.source != fromPATH || filepath.Dir(c.path) != "/usr/bin" {
		return "", false
	}
	dev, err := xcrunFind(c.name)
	if err != nil || dev == "" {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(dev); err == nil {
		dev = r
	}
	return dev, dev != c.real
}

// interpreters are the programs a script needs to start: its #!
// interpreter, and for "#!/usr/bin/env prog" the prog env finds on PATH.
func interpreters(script string) []string {
	f, err := os.Open(script)
	if err != nil {
		return nil
	}
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	f.Close()
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	rest, ok := strings.CutPrefix(line, "#!")
	if !ok {
		return nil
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || !filepath.IsAbs(fields[0]) {
		return nil
	}
	progs := []string{fields[0]}
	if filepath.Base(fields[0]) == "env" {
		for _, a := range fields[1:] {
			if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
				continue
			}
			if p, err := spawnLookPath(a); err == nil {
				if p, err = filepath.Abs(p); err == nil {
					progs = append(progs, p)
				}
			}
			break
		}
	}
	return progs
}

// xcrunFind is `xcrun --find <tool>` on macOS (a test var).
var xcrunFind = func(tool string) (string, error) {
	if _, err := os.Stat("/usr/bin/xcrun"); err != nil {
		return "", err
	}
	out, err := exec.Command("/usr/bin/xcrun", "--find", tool).Output() //nolint:gosec // fixed binary, tool is a declared command name
	return strings.TrimSpace(string(out)), err
}

// jailPATH is the PATH a jailed plugin runs with: the directory of each
// declared command found outside the standard directories, then the
// standard directories (which hold only its declared commands). A command
// found elsewhere comes first, so its name runs what was found — Homebrew's
// git, not the /usr/bin stub of the same name.
func jailPATH(cmds []jailCommand) string {
	std := []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin"}
	var dirs []string
	for _, c := range cmds {
		if d := filepath.Dir(c.path); !slices.Contains(std, d) && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(append(dirs, std...), string(os.PathListSeparator))
}

// grantedPath is a declared path the jail gives the plugin: path is where the
// plugin sees it, real the host file it is. socket: it is one socket file
// (granted only under write:, inside a runtime dir).
type grantedPath struct {
	path, real string
	socket     bool
}

// grantChecks are what a launch's declared paths, commands and their
// interpreters are checked against (grantedPaths, findCommands).
type grantChecks struct {
	g                                              sdk.Grant
	homes, protected, sockets, execBroad, writable []string
}

func newGrantChecks(sd SandboxDeps, g sdk.Grant) *grantChecks {
	c := &grantChecks{g: g, sockets: runtimeDirs()}
	home, _ := jailHome()
	if home != "" && home != "/" {
		c.homes = append(c.homes, filepath.Clean(home))
		if h, err := filepath.EvalSymlinks(home); err == nil {
			c.homes = append(c.homes, h)
		}
	}
	for _, p := range sd.MaskPaths {
		if p == "" {
			continue
		}
		c.protected = append(c.protected, filepath.Clean(p))
		if r, err := filepath.EvalSymlinks(p); err == nil {
			c.protected = append(c.protected, r)
		}
	}
	c.execBroad = execRefused(c.homes)
	ws := append([]string(nil), g.Write...)
	if sd.StagingRoot != "" {
		ws = append(ws, sd.StagingRoot)
	}
	for _, w := range ws {
		c.writable = append(c.writable, filepath.Clean(w))
		if r, err := filepath.EvalSymlinks(w); err == nil {
			c.writable = append(c.writable, r)
		}
	}
	return c
}

// refusal is why the jail does not grant p (resolving to real) as kind
// ("read", "write", "exec", or "command": a declared command or the #!
// interpreter of one), or nil.
func (c *grantChecks) refusal(kind, p, real string) error {
	for _, q := range []string{p, real} {
		if q == "/" || slices.Contains(c.homes, q) {
			return fmt.Errorf("it is the whole of %s", q)
		}
		for _, prot := range c.protected {
			if within(q, prot) {
				return fmt.Errorf("it contains conductor's own %s", prot)
			}
			if !within(prot, q) {
				continue
			}
			if kind != "read" || !c.g.FromConfig(kind, p) {
				return fmt.Errorf("it lies inside conductor's own %s — only a file the plugin's config names may be read there", prot)
			}
			if fi, err := os.Stat(real); err != nil || fi.IsDir() {
				return fmt.Errorf("it lies inside conductor's own %s, and is not a file", prot)
			}
		}
		for _, d := range c.sockets {
			if within(q, d) {
				return fmt.Errorf("it contains %s, where other programs' sockets are (a directory grant includes connecting to them) — grant the one socket file it needs instead, under write:", d)
			}
			if !within(d, q) || grantExempt(q) {
				continue
			}
			if kind == "write" && isSocket(real) {
				continue
			}
			return fmt.Errorf("it lies inside %s, where other programs serve sockets (a grant of anything there may include connecting to them) — only one socket file, declared under write:, is granted there", d)
		}
		if kind != "exec" {
			continue
		}
		for _, d := range c.execBroad {
			if within(q, d) {
				return fmt.Errorf("it contains %s: what a plugin runs is named (commands:) or a tool's own directory, never a system or user directory of programs", d)
			}
		}
	}
	return nil
}

// insideWritable is why p, lying inside a path the plugin may write (its
// declared write paths, its staging dir), is refused, or nil: the plugin
// could replace what is there with a symlink to anything, or a program it
// runs with one of its own.
func (c *grantChecks) insideWritable(p string) error {
	for _, w := range c.writable {
		if w != p && within(w, p) {
			return fmt.Errorf("it lies inside %s, which it may write — it could replace what is there", w)
		}
	}
	return nil
}

// program checks a program the jail runs and binds (a declared command found
// at p, the #! interpreter of one): it resolves through root-owned symlinks
// only, is a regular executable file, and is refused like a declared path
// (refusal) and when it lies inside a path the plugin may write.
func (c *grantChecks) program(name, p string) (jailCommand, error) {
	dir, err := resolveOwned(filepath.Dir(p))
	if err == nil {
		var real string
		if real, err = resolveOwned(p); err == nil {
			cmd := jailCommand{name: name, path: filepath.Join(dir, filepath.Base(p)), real: real}
			return cmd, c.checkProgram(cmd)
		}
	}
	var link *symlinkError
	if errors.As(err, &link) {
		fix := "replace the symlink with what it points to"
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			fix += ", or, for a command found on PATH, put " + filepath.Dir(real) + " ahead of it on conductor's PATH"
		}
		return jailCommand{}, &linkRefusal{link: link, msg: fmt.Sprintf("it goes through %s, a symlink to %s that root does not own; the jail never follows a symlink a plugin could have made — %s, or set trust: full on the plugin's entry", link.link, link.target, fix)}
	}
	return jailCommand{}, err
}

// linkRefusal is program refusing a path for a symlink root does not own:
// the message names the fix, and the symlink is there for a caller that
// asks an authority where the program is instead (locateCommand).
type linkRefusal struct {
	link *symlinkError
	msg  string
}

func (e *linkRefusal) Error() string { return e.msg }
func (e *linkRefusal) Unwrap() error { return e.link }

func (c *grantChecks) checkProgram(cmd jailCommand) error {
	fi, err := os.Stat(cmd.real)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", cmd.real)
	}
	if err := c.refusal("command", cmd.path, cmd.real); err != nil {
		return err
	}
	for _, q := range []string{cmd.path, cmd.real} {
		if err := c.insideWritable(q); err != nil {
			return err
		}
	}
	return nil
}

func isSocket(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// grantExemptRoots are directories inside a runtime dir whose paths are
// granted like any other's: a test's temp dir (GrantInsideTempForTests;
// none in conductor).
var grantExemptRoots []string

// GrantInsideTempForTests grants paths inside this process's temp dir like
// any other's, for a test package whose fixtures (fake homes, a plugin's
// configured files) live there: the temp dir is a runtime dir the jail
// grants nothing inside otherwise. Only a TestMain calls it.
func GrantInsideTempForTests() {
	tmp := filepath.Clean(scratch.SystemTempDir())
	grantExemptRoots = []string{tmp}
	if r, err := filepath.EvalSymlinks(tmp); err == nil && r != tmp {
		grantExemptRoots = append(grantExemptRoots, r)
	}
}

func grantExempt(p string) bool {
	for _, r := range grantExemptRoots {
		if within(r, p) {
			return true
		}
	}
	return false
}

// grantedPaths checks the declared read, write and exec paths and keeps
// those that exist (a declared path that does not is skipped, as the
// declaration says). A path is refused when it:
//   - is the root, the user's home, or contains conductor's own state or
//     config dir (sd.MaskPaths) — a config: value naming "~" or "/" would
//     otherwise hand the plugin all of it;
//   - lies inside conductor's own state or config dir, except a file read
//     whose path is a config: value (an App key the user configured):
//     writing there would let a plugin rewrite what it was granted;
//   - is, contains or lies inside a directory where programs serve sockets
//     for others (runtimeDirs): a read-only bind does not stop connect(),
//     and a directory there may be another program's. Only one socket
//     file there is granted, declared under write:, which consent names as
//     a socket;
//   - to run programs in, is or contains a directory of the system's
//     programs or the user's (execRefused): what runs is named, not swept;
//   - lies inside a path the plugin may write (its own declared write paths,
//     its staging dir), where it could replace what is there with a symlink
//     to anything;
//   - goes through a symlink root does not own (resolveOwned).
//
// A refused path fails the launch, naming the declared entry and the fix,
// unless the entry is optional ("?…"): then it is skipped and logged.
func grantedPaths(s Spec, gc *grantChecks) (read, write, exec []grantedPath, err error) {
	g := gc.g
	check := func(kind string, ps []string) ([]grantedPath, error) {
		var out []grantedPath
		for _, p := range ps {
			declared := strings.Join(g.Origins(kind, p), ", ")
			if declared == "" {
				declared = p
			}
			why := gc.insideWritable(p)
			real, err := resolveOwned(p)
			if why == nil && os.IsNotExist(err) {
				continue
			}
			var link *symlinkError
			switch {
			case why != nil:
			case errors.As(err, &link):
				why = fmt.Errorf("it goes through %s, a symlink to %s that root does not own; the jail never follows a symlink a plugin could have made — replace the symlink with what it points to, give the real path where the plugin's config takes one, or set trust: full on the plugin's entry", link.link, link.target)
			case err != nil:
				why = err
			default:
				why = gc.refusal(kind, p, real)
			}
			if why == nil {
				out = append(out, grantedPath{path: p, real: real, socket: kind == "write" && isSocket(real)})
				continue
			}
			if g.Optional(kind, p) {
				log.Printf("plugin %s: %s %s (declared %s) skipped: %v", s.Name, kind, p, declared, why)
				continue
			}
			return nil, fmt.Errorf("plugin %s: %s %s (declared %s) refused: %w", s.Name, kind, p, declared, why)
		}
		return out, nil
	}
	if read, err = check("read", g.Read); err != nil {
		return nil, nil, nil, err
	}
	if write, err = check("write", g.Write); err != nil {
		return nil, nil, nil, err
	}
	if exec, err = check("exec", g.Exec); err != nil {
		return nil, nil, nil, err
	}
	return read, write, exec, nil
}

// symlinkError is a declared path going through a symlink root does not own
// (resolveOwned).
type symlinkError struct{ link, target string }

func (e *symlinkError) Error() string {
	return fmt.Sprintf("%s is a symlink root does not own (to %s)", e.link, e.target)
}

// runtimeDirs are where programs serve sockets for others (a session bus,
// systemd --user, an X or tmux server, a docker daemon): a grant of one of
// them, of a directory above one, or of a directory inside one lets the
// plugin connect to the sockets there (refusal).
func runtimeDirs() []string {
	ds := []string{"/run", "/var/run", "/tmp", "/var/tmp", "/dev/shm", "/private/tmp", "/private/var/tmp", "/private/var/run"}
	if x := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
		ds = append(ds, filepath.Clean(x))
	}
	if t := scratch.SystemTempDir(); filepath.IsAbs(t) {
		ds = append(ds, filepath.Clean(t))
	}
	return ds
}

// execRefused are the directories an exec path may not be or contain: the
// PATH directories, the user's own bin directories, and the system's
// top-level ones. A tool's own directory inside one (/usr/lib/git-core,
// /usr/local/aws-cli) is fine.
func execRefused(homes []string) []string {
	ds := []string{
		"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin",
		"/usr", "/usr/lib", "/usr/lib32", "/usr/lib64", "/usr/libexec", "/usr/local", "/usr/local/lib", "/usr/local/libexec", "/usr/share",
		"/lib", "/lib32", "/lib64", "/libexec", "/etc", "/run", "/var", "/tmp", "/proc", "/sys", "/dev",
		"/opt", "/home", "/root", "/srv", "/mnt", "/media", "/boot", "/nix", "/nix/store", "/snap",
		"/opt/homebrew", "/opt/homebrew/bin", "/opt/homebrew/lib", "/opt/local", "/opt/local/bin",
		"/private", "/private/etc", "/private/var", "/private/tmp", "/System", "/Library", "/Applications",
	}
	for _, h := range homes {
		ds = append(ds, filepath.Join(h, "bin"), filepath.Join(h, ".local", "bin"), filepath.Join(h, ".local"), filepath.Join(h, "go", "bin"), filepath.Join(h, ".cargo", "bin"))
	}
	return ds
}

// within: p is dir or inside it. On macOS, whose file systems ignore case
// by default, it ignores case too (/Private/Tmp is /private/tmp).
func within(dir, p string) bool {
	dir, p = filepath.Clean(dir), filepath.Clean(p)
	if spawnGOOS == "darwin" {
		dir, p = strings.ToLower(dir), strings.ToLower(p)
	}
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

func reals(ps []grantedPath) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.real)
	}
	return out
}

// relayed are the ports conductor listens on for plugins (their declared
// listen addresses): conductor's own listeners, but not its own services.
var relayed = struct {
	sync.Mutex
	ports map[int]int
}{ports: map[int]int{}}

// ownServicePort reports whether port is one conductor itself serves on (its
// dashboard, its webhook receiver, …), not counting the plugins' listen
// relays. A declared loopback destination may not be one: the plugin would
// reach conductor's own control surfaces from inside its jail.
var ownServicePort = func(port int) (bool, error) {
	ports, err := conductorListenPorts()
	if err != nil {
		return false, err
	}
	relayed.Lock()
	defer relayed.Unlock()
	return ports[port] && relayed.ports[port] == 0, nil
}

func refuseOwnPorts(s Spec, loopPorts []int) error {
	for _, p := range loopPorts {
		own, err := ownServicePort(p)
		if err != nil {
			return fmt.Errorf("plugin %s: cannot tell whether localhost:%d is conductor's own port: %w", s.Name, p, err)
		}
		if own {
			return fmt.Errorf("plugin %s: egress localhost:%d refused: conductor itself listens there", s.Name, p)
		}
	}
	return nil
}

// grantExposeTargets is what of s.ExposeTo, the listeners the engine wired
// to this exposure, its jail may connect to: nothing unless its recorded
// declaration says it connects to what it is handed to expose
// (Capabilities.ExposeLocal), and of those only 127.0.0.1 addresses that are
// not conductor's own ports. Each one not granted is logged with the fix.
func grantExposeTargets(s Spec, logf func(string, ...any)) []string {
	if len(s.ExposeTo) == 0 || s.Probe || s.TrustFull {
		return nil
	}
	if _, before := runsAsBefore(s); before {
		return nil // unjailed, as before the jail: there is nothing to grant
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if !s.Manifest.ExposeLocal {
		logf("plugin %s: NOT granted %s, the listener(s) it is wired to expose: %s", s.Name, strings.Join(s.ExposeTo, ", "), noExposeLocal(s.Name))
		return nil
	}
	var out []string
	for _, a := range s.ExposeTo {
		port, ok := exposePort(a)
		if !ok {
			logf("plugin %s: NOT granted %s: %s", s.Name, a, notLoopbackExpose)
			continue
		}
		if own, err := ownServicePort(port); err != nil || own {
			logf("plugin %s: NOT granted %s: conductor itself listens there, and a jailed plugin never reaches conductor's own ports", s.Name, a)
			continue
		}
		out = append(out, a)
	}
	return out
}

// noExposeLocal is why a build without expose_local is refused its targets.
func noExposeLocal(name string) string {
	return fmt.Sprintf("its install record does not declare expose_local, so from its jail it cannot deliver there — update it (`conductor plugin update %s`, then approve), or set trust: full on its connector", name)
}

const notLoopbackExpose = "an exposure in a jail reaches a listener on 127.0.0.1 only — listen on 127.0.0.1 (or 0.0.0.0)"

// exposeRefusal is why a jailed launch of s would not be granted the
// listener(s) in addrs ("" when it would, or runs unjailed), from its
// recorded manifest alone — grantExposeTargets' judgement without a launch. Conductor's own ports are
// not judged: only the running daemon knows which those are.
func exposeRefusal(s Spec, addrs []string) string {
	if len(addrs) == 0 || s.Probe || s.TrustFull {
		return ""
	}
	if _, before := runsAsBefore(s); before {
		return "" // unjailed, as before the jail: it reaches loopback as it did
	}
	if !s.Manifest.ExposeLocal {
		return fmt.Sprintf("plugin %s would not be granted %s, the listener(s) it is wired to expose: %s", s.Name, strings.Join(addrs, ", "), noExposeLocal(s.Name))
	}
	for _, a := range addrs {
		if _, ok := exposePort(a); !ok {
			return fmt.Sprintf("plugin %s would not be granted %s: %s", s.Name, a, notLoopbackExpose)
		}
	}
	return ""
}

// exposePort is the port of a listener address an exposure may be granted:
// 127.0.0.1 or localhost.
func exposePort(addr string) (int, bool) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil || (h != "127.0.0.1" && h != "localhost") {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	return n, err == nil && n > 0 && n < 65536
}

// exposePorts are the ports of s's granted expose targets (realDial has
// already narrowed s.ExposeTo with grantExposeTargets); conductor's own are
// dropped again here, as at every connection.
func exposePorts(s Spec) []int {
	if s.Probe || !s.Manifest.ExposeLocal {
		return nil
	}
	var out []int
	for _, a := range s.ExposeTo {
		if p, ok := exposePort(a); ok {
			if own, err := ownServicePort(p); err == nil && !own {
				out = append(out, p)
			}
		}
	}
	return out
}

// buildJailed builds the jailed launch of argv for s: the wrapped command,
// its environment, and what to run when it exits.
func buildJailed(s Spec, sd SandboxDeps, argv, env []string) (*exec.Cmd, func(), error) {
	g, err := pluginGrant(s)
	if err != nil {
		return nil, nil, err
	}
	env = withDeclaredEnv(env, g.Env)
	gc := newGrantChecks(sd, g)
	cmds, err := findCommands(s, gc, g.Commands)
	if err != nil {
		return nil, nil, err
	}
	env = replaceEnv(env, "PATH", jailPATH(cmds))
	remote, loopPorts := splitLoopback(g.Egress)
	if err := refuseOwnPorts(s, loopPorts); err != nil {
		return nil, nil, err
	}
	// The listeners the engine wired to this exposure are reached the way a
	// declared loopback destination is: a relay (Linux) or a loopback port
	// (macOS), each connection re-checked against conductor's own ports.
	for _, p := range exposePorts(s) {
		if !slices.Contains(loopPorts, p) {
			loopPorts = append(loopPorts, p)
		}
	}
	var stage string
	if sd.StagingRoot != "" {
		stage = filepath.Join(sd.StagingRoot, s.Name)
		if err := os.MkdirAll(stage, 0o700); err != nil {
			return nil, nil, fmt.Errorf("plugin %s: staging dir: %w", s.Name, err)
		}
	}
	paths, err := pathsFor(s, gc)
	if err != nil {
		return nil, nil, err
	}
	if spawnGOOS == "darwin" {
		return buildSeatbelt(s, sd, g, paths, argv, env, cmds, remote, loopPorts, stage)
	}
	if spawnGOOS != "linux" {
		return nil, nil, fmt.Errorf("plugin %s: no plugin jail on %s — set trust: full to run it unjailed", s.Name, spawnGOOS)
	}
	return buildNamespaced(s, sd, g, paths, argv, env, cmds, remote, loopPorts, stage)
}

// jailPaths are a launch's checked declared paths (grantedPaths).
type jailPaths struct{ read, write, exec []grantedPath }

func pathsFor(s Spec, gc *grantChecks) (jailPaths, error) {
	r, w, x, err := grantedPaths(s, gc)
	return jailPaths{r, w, x}, err
}

// jailEUID is the daemon's effective uid (a test var).
var jailEUID = os.Geteuid

func buildNamespaced(s Spec, sd SandboxDeps, g sdk.Grant, paths jailPaths, argv, env []string, cmds []jailCommand, remote []string, loopPorts []int, stage string) (*exec.Cmd, func(), error) {
	if jailEUID() == 0 {
		// A user namespace maps root to root: the jail would not hold it.
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: conductor runs as root, and a plugin jail is no boundary for root — run conductor as a non-root user, or set trust: full on the plugin's entry to run it unjailed", s.Name)
	}
	spec := &sandbox.Spec{Mode: "namespace", Deny: true}
	if err := spec.Check(spawnGOOS, jailEUID(), spawnLookPath); err != nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it here (%v) — enable unprivileged user namespaces, or set trust: full to run it unjailed", s.Name, err)
	}
	if sd.Self == "" {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: conductor's own binary (the jail's entry) is not wired", s.Name)
	}
	var binds []sandbox.BindMount
	if s.BinPath != "" {
		binds = append(binds, sandbox.BindMount{Path: filepath.Dir(s.BinPath), RO: true})
	}
	if stage != "" {
		binds = append(binds, sandbox.BindMount{Path: stage, NoExec: true})
	}
	for _, p := range paths.read {
		// Read is read: what runs is declared under exec.
		binds = append(binds, sandbox.BindMount{Path: p.path, Src: p.real, RO: true, NoExec: true, Optional: true, NoSymlink: true})
	}
	for _, p := range paths.write {
		binds = append(binds, sandbox.BindMount{Path: p.path, Src: p.real, NoExec: true, Optional: true, NoSymlink: true, NotDir: p.socket})
	}
	for _, p := range paths.exec {
		binds = append(binds, sandbox.BindMount{Path: p.path, Src: p.real, RO: true, Optional: true, NoSymlink: true})
	}
	for _, c := range cmds {
		// Checked when it was found (grantChecks.program): bound by the
		// file it was then, never through a symlink or a directory made
		// since.
		binds = append(binds, sandbox.BindMount{Path: c.path, Src: c.real, RO: true, NoSymlink: true, NotDir: true})
		for _, d := range c.reads {
			// The libraries it loads are mapped executable: read-only,
			// not noexec.
			binds = append(binds, sandbox.BindMount{Path: d, RO: true, Optional: true, NoSymlink: true})
		}
	}
	nf := &sandbox.NetForward{Self: sd.Self, Base: sandbox.BaseMinimal}
	var closers []func()
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	// Every launch gets the proxy: with nothing declared its allowlist is
	// empty and it refuses every destination, naming it.
	if sd.EgressUnix == nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: no egress proxy is wired", s.Name)
	}
	sock, cred, revoke, err := sd.EgressUnix(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
	}
	closers = append(closers, revoke)
	nf.UnixSocket = sock
	// The forwarder dials the socket after the pivot: the socket itself,
	// read-only (connecting needs no write), not its directory of others.
	binds = append(binds, sandbox.BindMount{Path: sock, RO: true})
	env = append(env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)

	if len(loopPorts) > 0 || len(g.Listen) > 0 || len(g.Keyring) > 0 {
		dir, err := os.MkdirTemp(scratch.SystemTempDir(), "cpj")
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("plugin %s: jail sockets: %w", s.Name, err)
		}
		closers = append(closers, func() { os.RemoveAll(dir) })
		if len(g.Keyring) > 0 {
			// The jail's only session bus: a Secret Service that serves the
			// declared keyring items from the user's keyring, and nothing
			// else (keyringbroker). The bus itself is never reachable.
			path := filepath.Join(dir, "bus")
			name := s.Name
			kb, err := keyringbroker.Serve(path, g.Keyring, keyringbroker.Options{
				Logf:  func(format string, args ...any) { log.Printf("plugin %s: "+format, append([]any{name}, args...)...) },
				Dial:  keyringUpstream,
				Allow: s.AllowKeyring,
			})
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("plugin %s: keyring: %w", s.Name, err)
			}
			closers = append(closers, kb.Close)
			binds = append(binds, sandbox.BindMount{Path: path, RO: true})
			env = replaceEnv(env, "DBUS_SESSION_BUS_ADDRESS", keyringbroker.Address(path))
		}
		for i, port := range loopPorts {
			path := filepath.Join(dir, "r"+strconv.Itoa(i))
			stop, err := serveUnixToTCP(path, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("plugin %s: relay for localhost:%d: %w", s.Name, port, err)
			}
			closers = append(closers, stop)
			nf.Relays = append(nf.Relays, sandbox.Relay{Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Unix: path})
			binds = append(binds, sandbox.BindMount{Path: path, RO: true})
		}
		for i, addr := range g.Listen {
			_, port, _ := net.SplitHostPort(addr)
			path := filepath.Join(dir, "l"+strconv.Itoa(i))
			stop, err := serveTCPToUnix(addr, path)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("plugin %s: listen %s: %w", s.Name, addr, err)
			}
			closers = append(closers, stop)
			nf.Exposes = append(nf.Exposes, sandbox.Expose{Unix: path, Dial: net.JoinHostPort("127.0.0.1", port)})
		}
	}
	nf.Binds = binds
	wrapped, err := spec.WrapLocal(argv, "", envKeys(env), nf)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("plugin %s: jail: %w", s.Name, err)
	}
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, jailed
	c.Env = env
	return c, cleanup, nil
}

func buildSeatbelt(s Spec, sd SandboxDeps, g sdk.Grant, paths jailPaths, argv, env []string, cmds []jailCommand, remote []string, loopPorts []int, stage string) (*exec.Cmd, func(), error) {
	if _, err := spawnLookPath("sandbox-exec"); err != nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it here (no sandbox-exec) — set trust: full to run it unjailed", s.Name)
	}
	home, _ := jailHome()
	// A declared keyring item is read from the Keychain, read-only: the
	// sandbox cannot narrow it to the item, and macOS gates each item by
	// the Keychain's own per-item access control.
	// keychain: true (read-write) is for conductors before keyring items:
	// a plugin that declares items reads the Keychain read-only.
	p := &sandbox.PluginProfile{Keychain: g.Keychain && len(g.Keyring) == 0, KeychainRead: len(g.Keyring) > 0, Home: home, LoopbackPorts: loopPorts}
	if s.BinPath != "" {
		p.Exec = append(p.Exec, s.BinPath)
		p.Read = append(p.Read, filepath.Dir(s.BinPath))
	}
	for _, c := range cmds {
		p.Exec = append(p.Exec, c.path, c.real)
		p.Read = append(p.Read, c.reads...)
	}
	p.Read = append(p.Read, reals(paths.read)...)
	p.Write = append(p.Write, reals(paths.write)...)
	p.ExecDirs = append(p.ExecDirs, reals(paths.exec)...)
	if stage != "" {
		p.Write = append(p.Write, stage)
	}
	for _, addr := range g.Listen {
		_, port, _ := net.SplitHostPort(addr)
		if n, err := strconv.Atoi(port); err == nil {
			p.ListenPorts = append(p.ListenPorts, n)
		}
	}
	cleanup := func() {}
	if sd.EgressAddr == nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: no egress proxy is wired", s.Name)
	}
	addr, cred, revoke, err := sd.EgressAddr(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
	}
	cleanup = revoke
	_, port, _ := net.SplitHostPort(addr)
	p.ProxyPort, _ = strconv.Atoi(port)
	env = append(env, sandbox.ProxyEnv(addr, cred)...)
	wrapped := sandbox.WrapSeatbeltPlugin(argv, p)
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, jailed
	c.Env = env
	return c, cleanup, nil
}

// serveUnixToTCP listens on a unix socket and pipes each connection to a
// TCP address on this machine: a declared loopback destination, reached
// from a jail with no network of its own.
func serveUnixToTCP(path, addr string) (func(), error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	_, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)
	ownPort := ownServicePort
	go pipeAll(ln, func() (net.Conn, error) {
		// Checked at each connection too: conductor may have started
		// serving there since the launch.
		if own, err := ownPort(port); err != nil || own {
			return nil, fmt.Errorf("localhost:%d is conductor's own port", port)
		}
		return net.Dial("tcp", addr)
	})
	return func() { ln.Close() }, nil
}

// serveTCPToUnix listens on a declared TCP address on the host and pipes
// each connection to the jail's exposed socket at path.
func serveTCPToUnix(addr, path string) (func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	relayed.Lock()
	relayed.ports[port]++
	relayed.Unlock()
	go pipeAll(ln, func() (net.Conn, error) { return net.Dial("unix", path) })
	return func() {
		ln.Close()
		relayed.Lock()
		if relayed.ports[port]--; relayed.ports[port] <= 0 {
			delete(relayed.ports, port)
		}
		relayed.Unlock()
	}, nil
}

func pipeAll(ln net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			u, err := dial()
			if err != nil {
				return
			}
			defer u.Close()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(u, c)
				if cw, ok := u.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
			}()
			_, _ = io.Copy(c, u)
			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			wg.Wait()
		}(c)
	}
}
