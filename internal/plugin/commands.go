package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Where a declared command is found, in order (locateCommand):
//
//  1. the config's `commands:` block names the program file (fromConfig);
//  2. PATH, as `command -v` finds it, when that is the program itself or a
//     symlink root owns (fromPATH);
//  3. when PATH finds a symlink conductor's user owns, the package manager
//     that made it, asked where the program it installed is (fromHomebrew);
//
// and otherwise the launch fails, naming the `commands:` line that names it.
// Conductor never follows a symlink its own user owns to find a command: a
// plugin running as that user could have made it. It asks an authority —
// the operator's config, or the package manager — and checks the program
// file that names like any other (grantChecks.program).
const (
	fromConfig   = "config"
	fromPATH     = "PATH"
	fromHomebrew = "homebrew"
)

// errNotInstalled: PATH does not find the command, and the config names
// none.
var errNotInstalled = errors.New("not installed")

// packageManager is one that links the programs it installs onto PATH
// through symlinks its user owns. locate answers where the program PATH
// finds at p (a symlink) is, when this manager installed it: ok false when
// it is not this manager's (or the manager is not here).
type packageManager struct {
	name   string
	locate func(gc *grantChecks, name, p string) (real string, ok bool, err error)
}

// packageManagers are asked in order. Only Homebrew today; npm, pipx and
// uv link the same way and would be asked the same way.
var packageManagers = []packageManager{{name: fromHomebrew, locate: homebrewLocate}}

// locateCommand finds the declared command name. at is the path the answer
// came from (the config's, or PATH's), for the error.
func locateCommand(s Spec, gc *grantChecks, name string) (c jailCommand, at string, err error) {
	if p, ok := s.Commands[name]; ok {
		c, err := gc.named(name, p)
		return c, p, err
	}
	p, err := spawnLookPath(name)
	if err != nil {
		return jailCommand{}, "", errNotInstalled
	}
	p, _ = filepath.Abs(p)
	c, err = gc.program(name, p)
	var link *symlinkError
	if !errors.As(err, &link) {
		c.source = fromPATH
		return c, p, err
	}
	for _, m := range packageManagers {
		real, ok, merr := m.locate(gc, name, p)
		if merr != nil {
			return jailCommand{}, p, fmt.Errorf("it goes through %s, a symlink that root does not own, and asking %s where %s is failed: %w — %s", link.link, m.name, name, merr, commandFix(name, p))
		}
		if !ok {
			continue
		}
		mc, err := gc.program(name, real)
		if err != nil {
			return jailCommand{}, p, fmt.Errorf("%s installed it as %s: %w", m.name, real, err)
		}
		mc.source = m.name
		return mc, p, nil
	}
	return jailCommand{}, p, fmt.Errorf("it goes through %s, a symlink to %s that root does not own, and no package manager conductor asks says it made it; the jail never follows a symlink a plugin could have made — %s", link.link, link.target, commandFix(name, p))
}

// commandFix is the config line that names the command's program file,
// with the path filled in where conductor can tell it (shown, never used).
func commandFix(name, p string) string {
	file := "/path/to/" + name
	if real, err := filepath.EvalSymlinks(p); err == nil {
		file = real
	}
	return fmt.Sprintf("name the program in conductor's config (`commands: { %s: %s }`), or set trust: full on the plugin's entry", name, file)
}

// named checks a program file the config names for command name. It is
// checked like a command PATH finds (program); a symlink in it is followed,
// as a runtime's `bin:` is run through its symlinks — the operator named
// it — except one that is, or lies inside, a path the plugin may write,
// which the plugin could have made. The file the plugin runs must be named
// name: it runs it by that name, from its jail's PATH.
func (c *grantChecks) named(name, p string) (jailCommand, error) {
	if !filepath.IsAbs(p) {
		return jailCommand{}, fmt.Errorf("commands.%s: %s is not an absolute path", name, p)
	}
	cmd, err := c.program(name, p)
	var link *symlinkError
	if errors.As(err, &link) {
		real, lerr := c.followNamed(p)
		if lerr != nil {
			return jailCommand{}, fmt.Errorf("commands.%s names %s: %w", name, p, lerr)
		}
		cmd, err = c.program(name, real)
	}
	if err != nil {
		return jailCommand{}, fmt.Errorf("commands.%s names %s: %w", name, p, err)
	}
	if filepath.Base(cmd.path) != name {
		return jailCommand{}, fmt.Errorf("commands.%s names %s, which is %s: the plugin runs the command by its name, so name a file called %s", name, p, cmd.path, name)
	}
	cmd.source = fromConfig
	return cmd, nil
}

// followNamed resolves an operator-named path through every symlink on the
// way, refusing one that is, or lies inside, a path the plugin may write.
func (c *grantChecks) followNamed(p string) (string, error) {
	hops, real, err := linkHops(p)
	if err != nil {
		return "", err
	}
	for _, h := range hops {
		if w := c.overlapsWritable(h); w != "" {
			return "", fmt.Errorf("it goes through %s, a symlink inside %s, which the plugin may write — it could have made it", h, w)
		}
	}
	return real, nil
}

// overlapsWritable is the path the plugin may write that p lies inside, or
// that lies inside p, or "".
func (c *grantChecks) overlapsWritable(p string) string {
	for _, w := range c.writable {
		if within(w, p) || within(p, w) {
			return w
		}
	}
	return ""
}

// linkHops resolves p, returning every symlink on the way (each a path
// that was read as a link) and the file it resolves to.
func linkHops(p string) (hops []string, real string, err error) {
	sep := string(filepath.Separator)
	cur := sep
	rest := strings.Split(strings.TrimPrefix(filepath.Clean(p), sep), sep)
	for n := 0; len(rest) > 0; {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err != nil {
			return nil, "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if n++; n > 40 {
			return nil, "", fmt.Errorf("%s: too many symlinks", p)
		}
		hops = append(hops, next)
		tgt, err := os.Readlink(next)
		if err != nil {
			return nil, "", err
		}
		if !filepath.IsAbs(tgt) {
			tgt = filepath.Join(cur, tgt)
		}
		rest = append(strings.Split(strings.TrimPrefix(filepath.Clean(tgt), sep), sep), rest...)
		cur = sep
	}
	return hops, cur, nil
}

// brewPrefixes are where Homebrew installs, for a brew PATH does not find
// (a test var).
var brewPrefixes = []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"}

// brewTimeout bounds one brew call (a test var).
var brewTimeout = 10 * time.Second

// homebrewLocate asks Homebrew where the program PATH finds at p is. A brew
// that says p is in its prefix's bin (or sbin) names its Cellar; the
// formula keg's program there that is the same file p runs (os.SameFile,
// the kernel's resolution, not conductor reading links) is the answer, when
// brew also lists that formula at that version. So whatever brew answers,
// what runs is what PATH already runs, now at its keg's real path.
//
// brew itself runs unjailed, as conductor's user, so one is asked only when
// nothing the plugin may write overlaps its path, the symlinks on the way
// to it, its prefix or repository, or the files brew reads its settings
// from (brewRunnable): a plugin cannot plant or change the brew conductor
// runs.
func homebrewLocate(gc *grantChecks, name, p string) (string, bool, error) {
	dir := filepath.Dir(p)
	for _, brew := range brewCandidates() {
		if err := gc.brewRunnable(brew); err != nil {
			return "", false, err
		}
		prefix, err := runBrew(brew, "--prefix")
		if err != nil || !filepath.IsAbs(prefix) {
			continue
		}
		prefix = filepath.Clean(prefix)
		if dir != filepath.Join(prefix, "bin") && dir != filepath.Join(prefix, "sbin") {
			continue
		}
		if w := gc.overlapsWritable(prefix); w != "" {
			return "", false, fmt.Errorf("its prefix %s overlaps %s, which the plugin may write", prefix, w)
		}
		cellar, err := runBrew(brew, "--cellar")
		if err != nil || !filepath.IsAbs(cellar) {
			return "", false, fmt.Errorf("%s --cellar: %v", brew, err)
		}
		want, err := os.Stat(p)
		if err != nil {
			return "", false, err
		}
		for _, sub := range []string{"bin", "sbin"} {
			kegs, _ := filepath.Glob(filepath.Join(filepath.Clean(cellar), "*", "*", sub))
			for _, k := range kegs {
				f := filepath.Join(k, name)
				fi, err := os.Stat(f)
				if err != nil || !os.SameFile(fi, want) {
					continue
				}
				verDir := filepath.Dir(filepath.Dir(f))
				formula, version := filepath.Base(filepath.Dir(verDir)), filepath.Base(verDir)
				out, err := runBrew(brew, "list", "--versions", formula)
				if err != nil {
					return "", false, fmt.Errorf("%s list --versions %s: %v", brew, formula, err)
				}
				if fields := strings.Fields(out); len(fields) < 2 || fields[0] != formula || !slices.Contains(fields[1:], version) {
					return "", false, fmt.Errorf("%s is in %s's keg %s, which brew does not list as installed (%q)", p, formula, version, out)
				}
				return f, true, nil
			}
		}
		// brew's prefix, but no formula's keg program: not one brew
		// installed (a cask, a link someone else made).
		return "", false, nil
	}
	return "", false, nil
}

// brewCandidates are the brews to ask: PATH's, then each standard prefix's.
func brewCandidates() []string {
	var out []string
	add := func(p string) {
		if p == "" || slices.Contains(out, p) {
			return
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			out = append(out, p)
		}
	}
	if p, err := spawnLookPath("brew"); err == nil {
		if p, err = filepath.Abs(p); err == nil {
			add(p)
		}
	}
	for _, pre := range brewPrefixes {
		add(filepath.Join(pre, "bin", "brew"))
	}
	return out
}

// brewRunnable is why conductor will not run brew (nil: it may): brew, a
// symlink on the way to it, the repository it runs from (its bin's
// parent), or a file it reads its settings from overlaps a path the plugin
// may write, or brew lies in conductor's own dirs.
func (c *grantChecks) brewRunnable(brew string) error {
	hops, real, err := linkHops(brew)
	if err != nil {
		return fmt.Errorf("brew %s: %w", brew, err)
	}
	// Its prefix (where it is linked) and repository (where it runs from)
	// hold the code and settings it reads.
	check := append([]string{brew, real, filepath.Dir(filepath.Dir(brew)), filepath.Dir(filepath.Dir(real))}, hops...)
	home, _ := jailHome()
	if home != "" {
		check = append(check, filepath.Join(home, ".homebrew"))
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		check = append(check, filepath.Join(x, "homebrew"))
	}
	check = append(check, "/etc/homebrew")
	for _, q := range check {
		if w := c.overlapsWritable(q); w != "" {
			return fmt.Errorf("brew %s: %s overlaps %s, which the plugin may write — conductor does not run a brew it could have changed", brew, q, w)
		}
		for _, prot := range c.protected {
			if within(prot, q) {
				return fmt.Errorf("brew %s lies inside conductor's own %s", brew, prot)
			}
		}
	}
	return nil
}

// runBrew runs brew with args and returns its trimmed output. Homebrew's
// automatic update, analytics and hints are off: this is a lookup.
var runBrew = func(brew string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), brewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, brew, args...) //nolint:gosec // a brew brewRunnable checked, fixed arguments and a formula name from its own Cellar
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_INSTALL_FROM_API=1")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
