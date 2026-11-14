package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Where a declared command is found, in order (locateCommand):
//
//  1. the config's `commands:` block names the program file (fromConfig);
//  2. PATH, as `command -v` finds it (commandLookPath), when that is the
//     program itself or a symlink root owns (fromPATH);
//  3. when PATH finds a symlink conductor's user owns, what the package
//     manager that made it recorded installing, read as data — conductor
//     never runs the package manager (fromHomebrew);
//
// and otherwise the launch fails, naming the `commands:` line that names it.
// Conductor never follows a symlink its own user owns to find a command: a
// plugin running as that user could have made it. It takes an authority's
// word — the operator's config, or the package manager's records — and
// checks the program file that names like any other (grantChecks.program),
// against what every jailed plugin may write.
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
// finds at p (a symlink) is, when this manager installed it, and the
// directories the program reads to run (its own install tree, and those of
// what it was installed with: the libraries it loads): ok false when it is
// not this manager's (or the manager is not here).
type packageManager struct {
	name   string
	locate func(gc *grantChecks, name, p string) (found located, ok bool, err error)
}

// located is a package manager's answer: the program file, and the
// directories it reads to run.
type located struct {
	real  string
	reads []string
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
	p, err := commandLookPath(name)
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
		found, ok, merr := m.locate(gc, name, p)
		if merr != nil {
			return jailCommand{}, p, fmt.Errorf("it goes through %s, a symlink that root does not own, and what %s recorded about %s is refused: %w — %s", link.link, m.name, name, merr, commandFix(name, p))
		}
		if !ok {
			continue
		}
		mc, err := gc.program(name, found.real)
		if err != nil {
			return jailCommand{}, p, fmt.Errorf("%s installed it as %s: %w", m.name, found.real, err)
		}
		for _, d := range found.reads {
			if err := gc.programDir(d); err != nil {
				return jailCommand{}, p, fmt.Errorf("%s installed it with %s: %w", m.name, d, err)
			}
		}
		mc.source, mc.reads = m.name, found.reads
		return mc, p, nil
	}
	return jailCommand{}, p, fmt.Errorf("it goes through %s, a symlink to %s that root does not own, and no package manager conductor asks says it made it; the jail never follows a symlink a plugin could have made — %s", link.link, link.target, commandFix(name, p))
}

// programDir checks a directory a program found by a package manager reads
// to run: a real directory (reached through symlinks root owns only),
// refused like a declared read and when it overlaps a path the plugin may
// write.
func (c *grantChecks) programDir(d string) error {
	real, err := resolveOwned(d)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", d)
	}
	if err := c.refusal("read", d, real); err != nil {
		return err
	}
	return c.programWritable(real)
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
		if err := c.programWritable(h); err != nil {
			return "", fmt.Errorf("it goes through the symlink %s: %w", h, err)
		}
	}
	return real, nil
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

// maxReceipt bounds the INSTALL_RECEIPT.json read (a few KB in practice).
const maxReceipt = 4 << 20

// homebrewLocate finds the program PATH finds at p, a link in a Homebrew
// prefix's bin (or sbin), from what Homebrew recorded when it installed it,
// read as data: conductor never runs brew. brew is a program of conductor's
// user, with code, settings and caches a plugin could reach; run unjailed
// to answer a lookup, it would be the way out of the jail this lookup
// serves. What brew would answer is on disk:
//
//   - the prefix is the directory above p's, and its Cellar is
//     <prefix>/Cellar (a custom HOMEBREW_CELLAR is not followed: name the
//     program under commands:);
//   - the formula keg's program that is the same file p runs
//     (os.SameFile: the kernel's resolution, not conductor reading the
//     link) is the program;
//   - the keg holds the INSTALL_RECEIPT.json Homebrew writes when it
//     installs one, and its runtime_dependencies (every formula the
//     program loads, transitively) name the kegs it reads to run: each
//     installed version of each.
//
// A prefix that overlaps a path some plugin may write is refused: a plugin
// could have placed the link, the keg or the receipt.
func homebrewLocate(gc *grantChecks, name, p string) (located, bool, error) {
	dir := filepath.Dir(p)
	if b := filepath.Base(dir); b != "bin" && b != "sbin" {
		return located{}, false, nil
	}
	prefix := filepath.Dir(dir)
	cellar := filepath.Join(prefix, "Cellar")
	if fi, err := os.Lstat(cellar); err != nil || !fi.IsDir() {
		return located{}, false, nil
	}
	if err := gc.programWritable(prefix); err != nil {
		return located{}, false, fmt.Errorf("the Homebrew prefix %s: %w", prefix, err)
	}
	want, err := os.Stat(p)
	if err != nil {
		return located{}, false, err
	}
	for _, sub := range []string{"bin", "sbin"} {
		kegs, _ := filepath.Glob(filepath.Join(cellar, "*", "*", sub))
		for _, k := range kegs {
			f := filepath.Join(k, name)
			fi, err := os.Stat(f)
			if err != nil || !os.SameFile(fi, want) {
				continue
			}
			keg := filepath.Dir(k)
			deps, err := receiptDeps(keg)
			if err != nil {
				return located{}, false, fmt.Errorf("%s is in %s, which Homebrew did not record installing: %w", p, keg, err)
			}
			reads := []string{keg}
			for _, d := range deps {
				reads = append(reads, installedKegs(cellar, d)...)
			}
			return located{real: f, reads: reads}, true, nil
		}
	}
	// A Homebrew prefix, but no formula's keg program: not one Homebrew
	// installed as a formula (a cask, a link someone else made).
	return located{}, false, nil
}

// receiptDeps reads a keg's INSTALL_RECEIPT.json, as data, for the names of
// the formulas it depends on at run time.
func receiptDeps(keg string) ([]string, error) {
	rp := filepath.Join(keg, "INSTALL_RECEIPT.json")
	fi, err := os.Lstat(rp)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxReceipt {
		return nil, fmt.Errorf("%s is not a receipt file", rp)
	}
	b, err := os.ReadFile(rp)
	if err != nil {
		return nil, err
	}
	var r struct {
		Deps []struct {
			FullName string `json:"full_name"`
		} `json:"runtime_dependencies"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", rp, err)
	}
	var out []string
	for _, d := range r.Deps {
		// A tap formula (user/tap/name) is installed under its name.
		n := path.Base(d.FullName)
		if n == "" || n == "." || n == ".." || n == "/" || strings.ContainsAny(n, "/\\") || slices.Contains(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// installedKegs are the installed versions of formula: the real
// directories in <cellar>/<formula>.
func installedKegs(cellar, formula string) []string {
	ents, err := os.ReadDir(filepath.Join(cellar, formula))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsDir() {
			out = append(out, filepath.Join(cellar, formula, e.Name()))
		}
	}
	return out
}
