//go:build linux || darwin

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brewRig is a Homebrew-shaped prefix in a temp dir: prefix/bin/tool is a
// symlink (conductor's user owns it) to the formula keg's program
// prefix/Cellar/tool/1.0/bin/tool, installed with libfoo 2.0, and
// prefix/bin/brew a fake brew that answers --prefix, --cellar, deps and
// list --versions, logging every call.
type brewRig struct {
	prefix, cellar, link, real, brew, log, lib string
}

func newBrewRig(t *testing.T, home string) brewRig {
	t.Helper()
	r := brewRig{prefix: filepath.Join(home, "brew")}
	r.cellar = filepath.Join(r.prefix, "Cellar")
	r.real = filepath.Join(r.cellar, "tool", "1.0", "bin", "tool")
	r.link = filepath.Join(r.prefix, "bin", "tool")
	r.brew = filepath.Join(r.prefix, "bin", "brew")
	r.log = filepath.Join(home, "brew.log")
	r.lib = filepath.Join(r.cellar, "libfoo", "2.0")
	for _, d := range []string{filepath.Dir(r.real), filepath.Dir(r.link), filepath.Join(r.lib, "lib")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(r.real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/tool/1.0/bin/tool", r.link); err != nil {
		t.Fatal(err)
	}
	r.answer(t, r.prefix, r.cellar, "tool 1.0")
	t.Setenv("PATH", filepath.Dir(r.link)+string(os.PathListSeparator)+"/usr/bin:/bin")
	was := brewPrefixes
	brewPrefixes = nil
	t.Cleanup(func() { brewPrefixes = was })
	return r
}

// answer rewrites the fake brew to answer prefix, cellar and the tool's
// versions; libfoo is its one dependency, at libVersions.
func (r brewRig) answer(t *testing.T, prefix, cellar, versions string) {
	t.Helper()
	r.answerDeps(t, prefix, cellar, versions, "libfoo 2.0")
}

func (r brewRig) answerDeps(t *testing.T, prefix, cellar, versions, libVersions string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\ncase \"$1\" in\n--prefix) echo %q ;;\n--cellar) echo %q ;;\ndeps) echo libfoo ;;\nlist) case \"$3\" in libfoo) echo %q ;; *) echo %q ;; esac ;;\nesac\n", r.log, prefix, cellar, libVersions, versions)
	if err := os.WriteFile(r.brew, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (r brewRig) calls() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

func findTool(t *testing.T, s Spec, sd SandboxDeps) ([]jailCommand, error) {
	t.Helper()
	g, err := pluginGrant(s)
	if err != nil {
		t.Fatal(err)
	}
	return findCommands(s, newGrantChecks(sd, g), []string{"tool"})
}

// A command PATH finds as a Homebrew symlink is found by asking brew: the
// formula keg's program that is the same file is bound, at its real path,
// and its directory is on the jail's PATH. Conductor never read the link.
func TestCommandFromHomebrew(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}
	cmds, err := findTool(t, s, sd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 || cmds[0].path != r.real || cmds[0].real != r.real || cmds[0].source != fromHomebrew {
		t.Fatalf("commands = %+v, want the keg's program from homebrew", cmds)
	}
	if want := []string{filepath.Join(r.cellar, "tool", "1.0"), r.lib}; strings.Join(cmds[0].reads, ",") != strings.Join(want, ",") {
		t.Fatalf("reads = %v, want its keg and its dependency's: %v", cmds[0].reads, want)
	}
	if !strings.Contains(r.calls(), "list --versions tool") {
		t.Fatalf("brew was not asked about the formula: %q", r.calls())
	}
	if p := jailPATH(cmds); !strings.HasPrefix(p, filepath.Dir(r.real)+string(os.PathListSeparator)) || strings.Contains(p, filepath.Dir(r.link)) {
		t.Fatalf("jail PATH %q must lead with the keg's bin (ahead of a /usr/bin namesake), not carry the linked bin", p)
	}
	here := strings.Join(HereLines(s), "\n")
	if !strings.Contains(here, r.real+" (homebrew)") || !strings.Contains(here, r.lib) {
		t.Fatalf("plugin show must say where it came from, and what it reads to run:\n%s", here)
	}
}

// What brew says a program was installed with is read only as a real keg
// directory in its Cellar: a version that climbs out of it is skipped, a
// keg reached through a symlink conductor's user owns, or one the plugin
// may write, fails the launch.
func TestHomebrewDependencyKegsAreChecked(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}

	r.answerDeps(t, r.prefix, r.cellar, "tool 1.0", "libfoo ../../../../.. 2.0")
	cmds, err := findTool(t, s, sd)
	if err != nil || len(cmds[0].reads) != 2 || cmds[0].reads[1] != r.lib {
		t.Fatalf("commands %+v, err %v; want the climbing version skipped", cmds, err)
	}

	r.answerDeps(t, r.prefix, r.cellar, "tool 1.0", "libfoo 2.0")
	s.Manifest.Write = []string{"~/brew/Cellar/libfoo"}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "may write") {
		t.Fatalf("commands %+v, err %v; want a dependency keg the plugin may write refused", cmds, err)
	}
	s.Manifest.Write = nil

	// A Cellar outside the prefix (HOMEBREW_CELLAR): the prefix check does
	// not cover it, the keg's own does.
	xcellar := filepath.Join(home, "xcellar")
	for _, d := range []string{filepath.Join(xcellar, "tool", "1.0", "bin"), filepath.Join(xcellar, "libfoo", "2.0")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(r.real, filepath.Join(xcellar, "tool", "1.0", "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	r.answerDeps(t, r.prefix, xcellar, "tool 1.0", "libfoo 2.0")
	if _, err := findTool(t, s, sd); err != nil {
		t.Fatalf("a Cellar outside the prefix: %v", err)
	}
	s.Manifest.Write = []string{"~/xcellar/libfoo"}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "installed it with") {
		t.Fatalf("commands %+v, err %v; want a dependency keg the plugin may write refused", cmds, err)
	}
	s.Manifest.Write = nil
	r.answerDeps(t, r.prefix, r.cellar, "tool 1.0", "libfoo 2.0")

	// A dependency keg that holds conductor's own dir is refused like any
	// read of it.
	inLib := filepath.Join(r.lib, "conductor-state")
	if err := os.MkdirAll(inLib, 0o700); err != nil {
		t.Fatal(err)
	}
	sdLib := sd
	sdLib.MaskPaths = append([]string{inLib}, sd.MaskPaths...)
	if cmds, err := findTool(t, s, sdLib); err == nil || !strings.Contains(err.Error(), "conductor's own") {
		t.Fatalf("commands %+v, err %v; want a keg holding conductor's dir refused", cmds, err)
	}

	elsewhere := filepath.Join(home, "elsewhere-libfoo")
	if err := os.Rename(filepath.Join(r.cellar, "libfoo"), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(r.cellar, "libfoo")); err != nil {
		t.Fatal(err)
	}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "root does not own") {
		t.Fatalf("commands %+v, err %v; want a dependency keg through a user-owned symlink refused", cmds, err)
	}
}

// Whatever brew answers, what runs is what PATH already runs: a brew that
// names a prefix PATH's hit is not in, a Cellar holding another program of
// that name, or a version it does not list, finds nothing, and the launch
// fails naming the config line, with the program filled in.
func TestHomebrewAnswerMustBeTheSameProgram(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	decoy := filepath.Join(home, "decoy")
	if err := os.MkdirAll(filepath.Join(decoy, "tool", "1.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "tool", "1.0", "bin", "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}
	for _, c := range []struct {
		name                     string
		prefix, cellar, versions string
		want                     string
	}{
		{"another prefix", decoy, r.cellar, "tool 1.0", "commands: { tool: " + r.real + " }"},
		{"another program in its cellar", r.prefix, decoy, "tool 1.0", "commands: { tool: " + r.real + " }"},
		{"a version it does not list", r.prefix, r.cellar, "tool 2.0", "does not list as installed"},
		{"no answer", "", "", "", "commands: { tool: " + r.real + " }"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r.answer(t, c.prefix, c.cellar, c.versions)
			cmds, err := findTool(t, s, sd)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "command tool") {
				t.Fatalf("commands %+v, err %v; want it refused with %q", cmds, err, c.want)
			}
		})
	}
	// No brew at all: the symlink is refused as before, naming the fix.
	if err := os.Remove(r.brew); err != nil {
		t.Fatal(err)
	}
	if _, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "no package manager conductor asks says it made it") || !strings.Contains(err.Error(), "commands: { tool: "+r.real+" }") {
		t.Fatalf("no brew: %v", err)
	}
}

// brew runs unjailed, so conductor never runs one the plugin could have
// planted or changed: brew, its prefix or a setting file it reads inside a
// path the plugin may write is refused before brew is run. A keg inside
// one is refused too.
func TestBrewThePluginCouldWriteIsNeverRun(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	rel := func(p string) string { return "~/" + strings.TrimPrefix(p, home+"/") }
	for _, w := range []string{rel(filepath.Dir(r.brew)), rel(r.prefix), rel(filepath.Join(r.cellar, "tool")), "~/.homebrew"} {
		t.Run(w, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Join(home, strings.TrimPrefix(w, "~/")), 0o755); err != nil {
				t.Fatal(err)
			}
			os.Remove(r.log)
			s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Write: []string{w}}}
			cmds, err := findTool(t, s, sd)
			if err == nil || !strings.Contains(err.Error(), "which the plugin may write") {
				t.Fatalf("commands %+v, err %v; want it refused over the write grant", cmds, err)
			}
			if r.calls() != "" {
				t.Fatalf("brew ran: %q", r.calls())
			}
		})
	}
	// The staging dir is a path it may write too.
	sd.StagingRoot = r.prefix
	if _, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd); err == nil {
		t.Fatal("a brew inside the staging dir was asked")
	}
}

// A brew earlier on PATH that is not the prefix's own is asked like any:
// it can only point at the program PATH already runs, so the one that
// answers for another prefix finds nothing and the next is asked.
func TestBrewEarlierOnPATHForAnotherPrefix(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	other := filepath.Join(home, "other", "bin")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "brew"), []byte("#!/bin/sh\necho "+filepath.Dir(other)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other+string(os.PathListSeparator)+os.Getenv("PATH"))
	brewPrefixes = []string{r.prefix}
	cmds, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd)
	if err != nil || cmds[0].real != r.real || cmds[0].source != fromHomebrew {
		t.Fatalf("commands %+v, err %v; want the prefix's own brew to answer", cmds, err)
	}
}

// The config's `commands:` names the program file: it wins over PATH, is
// checked like a command PATH finds, and is followed through a symlink the
// operator named (as a runtime's bin: is run) unless the symlink is one
// the plugin could have made. A file not named for the command, or one
// that is not there, fails the launch.
func TestCommandNamedByTheConfig(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	if err := os.Remove(r.brew); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Commands: []string{"tool"}}

	cmds, err := findTool(t, Spec{Name: "p", Manifest: m, Commands: map[string]string{"tool": r.real}}, sd)
	if err != nil || cmds[0].path != r.real || cmds[0].source != fromConfig {
		t.Fatalf("commands %+v, err %v; want the named file from config", cmds, err)
	}
	here := strings.Join(HereLines(Spec{Name: "p", Manifest: m, Commands: map[string]string{"tool": r.real}}), "\n")
	if !strings.Contains(here, r.real+" (config)") {
		t.Fatalf("plugin show must say it came from the config:\n%s", here)
	}

	// The operator named the link: followed, bound at the file it names.
	cmds, err = findTool(t, Spec{Name: "p", Manifest: m, Commands: map[string]string{"tool": r.link}}, sd)
	if err != nil || cmds[0].path != r.real || cmds[0].source != fromConfig {
		t.Fatalf("commands %+v, err %v; want the named link followed", cmds, err)
	}

	writable := filepath.Join(home, "w")
	if err := os.MkdirAll(writable, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(writable, "tool")
	if err := os.Symlink(r.real, planted); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "other")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, named string
		write       []string
		want        string
	}{
		{"a link the plugin could have made", planted, []string{"~/w"}, "which the plugin may write"},
		{"a file inside a write grant", r.real, []string{"~/brew/Cellar"}, "which it may write"},
		{"a link to a file inside a write grant", r.link, []string{"~/brew/Cellar"}, "which it may write"},
		{"not named for the command", other, nil, "name a file called tool"},
		{"not there", filepath.Join(home, "nope", "tool"), nil, "no such file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Write: c.write}, Commands: map[string]string{"tool": c.named}}
			cmds, err := findTool(t, s, sd)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "commands.tool") {
				t.Fatalf("commands %+v, err %v; want it refused with %q", cmds, err, c.want)
			}
		})
	}
}

// On macOS xcrun is asked only about a /usr/bin developer-tool stub: for
// any other command it answers from PATH, naming the command again.
func TestDeveloperToolOnlyForUsrBin(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	was, wasFind := spawnGOOS, xcrunFind
	t.Cleanup(func() { spawnGOOS, xcrunFind = was, wasFind })
	spawnGOOS = "darwin"
	var asked []string
	xcrunFind = func(tool string) (string, error) {
		asked = append(asked, tool)
		return r.link, nil
	}
	cmds, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd)
	if err != nil || len(cmds) != 2 || len(asked) != 0 {
		t.Fatalf("commands %+v, err %v, xcrun asked about %v; want the keg program and its interpreter, xcrun not asked", cmds, err, asked)
	}
	if _, ok := developerTool(jailCommand{name: "git", path: "/usr/bin/git", real: "/usr/bin/git", source: fromPATH}); !ok || len(asked) != 1 {
		t.Fatalf("a /usr/bin stub must be asked about: %v", asked)
	}
}

// brew's answer is checked like any program: a Cellar it names inside
// conductor's state dir, holding a hard link to the very program PATH runs
// (the same file), is refused. And a prefix the plugin may write, where it
// could have placed the link PATH finds, is refused even when the brew
// asked lives elsewhere and answers for it.
func TestHomebrewAnswerIsCheckedLikeAnyProgram(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}

	stateCellar := filepath.Join(sd.MaskPaths[0], "Cellar")
	if err := os.MkdirAll(filepath.Join(stateCellar, "tool", "1.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(r.real, filepath.Join(stateCellar, "tool", "1.0", "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	r.answer(t, r.prefix, stateCellar, "tool 1.0")
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "conductor's own") {
		t.Fatalf("commands %+v, err %v; want a Cellar in conductor's state refused", cmds, err)
	}

	r.answer(t, r.prefix, r.cellar, "tool 1.0")
	elsewhere := filepath.Join(home, "elsewhere", "bin")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.brew)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "brew"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.brew); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", elsewhere+string(os.PathListSeparator)+os.Getenv("PATH"))
	if cmds, err := findTool(t, s, sd); err != nil || cmds[0].real != r.real {
		t.Fatalf("commands %+v, err %v; a brew elsewhere answering for the prefix must find the keg", cmds, err)
	}
	s.Manifest.Write = []string{"~/brew/bin"}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "which the plugin may write") {
		t.Fatalf("commands %+v, err %v; want a prefix the plugin may write refused", cmds, err)
	}
}

// A brew inside conductor's own dirs is never run, even one that would
// answer for the prefix correctly.
func TestBrewInsideConductorsDirsIsNeverRun(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	inState := filepath.Join(sd.MaskPaths[0], "bin")
	if err := os.MkdirAll(inState, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.brew, filepath.Join(inState, "brew")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", inState+string(os.PathListSeparator)+os.Getenv("PATH"))
	if cmds, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd); err == nil || !strings.Contains(err.Error(), "conductor's own") {
		t.Fatalf("commands %+v, err %v; want a brew in conductor's state refused", cmds, err)
	}
	if r.calls() != "" {
		t.Fatalf("brew ran: %q", r.calls())
	}
}
