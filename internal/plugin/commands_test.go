//go:build linux || darwin

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// brewRig is a Homebrew-shaped prefix in a temp dir: prefix/bin/tool is a
// symlink (conductor's user owns it) to the formula keg's program
// prefix/Cellar/tool/1.0/bin/tool, whose INSTALL_RECEIPT.json records it
// was installed with libfoo (prefix/Cellar/libfoo/2.0). prefix/bin/brew is
// a brew that logs every call: conductor must never run it.
type brewRig struct {
	prefix, cellar, link, real, keg, brew, log, lib string
}

func newBrewRig(t *testing.T, home string) brewRig {
	t.Helper()
	r := brewRig{prefix: filepath.Join(home, "brew")}
	r.cellar = filepath.Join(r.prefix, "Cellar")
	r.keg = filepath.Join(r.cellar, "tool", "1.0")
	r.real = filepath.Join(r.keg, "bin", "tool")
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
	r.receipt(t, `{"runtime_dependencies":[{"full_name":"libfoo","version":"2.0"}]}`)
	if err := os.WriteFile(r.brew, []byte(fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\n", r.log)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(r.link)+string(os.PathListSeparator)+"/usr/bin:/bin")
	return r
}

// receipt writes the tool keg's INSTALL_RECEIPT.json.
func (r brewRig) receipt(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.keg, "INSTALL_RECEIPT.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// noBrewRun fails the test when anything ran brew.
func (r brewRig) noBrewRun(t *testing.T) {
	t.Helper()
	if b, _ := os.ReadFile(r.log); len(b) > 0 {
		t.Fatalf("brew ran: %q", b)
	}
}

func findTool(t *testing.T, s Spec, sd SandboxDeps) ([]jailCommand, error) {
	t.Helper()
	g, err := pluginGrant(s)
	if err != nil {
		t.Fatal(err)
	}
	return findCommands(s, newGrantChecks(sd, g), []string{"tool"})
}

// A command PATH finds as a Homebrew symlink is found from what Homebrew
// recorded installing, read as data: the formula keg's program that is the
// same file is bound, at its real path, with its keg and the kegs its
// receipt says it was installed with. Conductor never read the link, and
// never ran brew.
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
	if want := []string{r.keg, r.lib}; strings.Join(cmds[0].reads, ",") != strings.Join(want, ",") {
		t.Fatalf("reads = %v, want its keg and its dependency's: %v", cmds[0].reads, want)
	}
	r.noBrewRun(t)
	here := strings.Join(HereLines(s), "\n")
	if !strings.Contains(here, r.real+" (homebrew)") || !strings.Contains(here, r.lib) {
		t.Fatalf("plugin show must say where it came from, and what it reads to run:\n%s", here)
	}
	r.noBrewRun(t)
}

// The jail's PATH leads with the standard directories; a declared command
// found elsewhere is reached through a per-launch directory of links named
// for the declared commands only, never through its keg's bin dir, where
// an undeclared name would find the keg's other programs.
func TestJailPATHCarriesDeclaredNamesOnly(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	if err := os.WriteFile(filepath.Join(r.keg, "bin", "other"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd)
	if err != nil {
		t.Fatal(err)
	}
	std := "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"
	if p := jailPATH(cmds, ""); !strings.HasPrefix(p, std+":") || strings.Contains(p, filepath.Dir(r.link)) {
		t.Fatalf("jail PATH %q must lead with the standard dirs", p)
	}
	stage := filepath.Join(sd.StagingRoot, "p")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	links, done, err := commandLinks(stage, cmds)
	if err != nil {
		t.Fatal(err)
	}
	if p := jailPATH(cmds, links); p != links+":"+std {
		t.Fatalf("jail PATH %q, want the links dir then the standard dirs", p)
	}
	ents, _ := os.ReadDir(links)
	if len(ents) != 1 || ents[0].Name() != "tool" {
		t.Fatalf("links %v, want the declared tool only (its interpreter is in a standard dir)", ents)
	}
	if tgt, _ := os.Readlink(filepath.Join(links, "tool")); tgt != r.real {
		t.Fatalf("tool links to %q, want %s", tgt, r.real)
	}
	done()
	if _, err := os.Stat(links); !os.IsNotExist(err) {
		t.Fatalf("links dir left behind: %v", err)
	}
	if links, _, _ := commandLinks(stage, cmds[1:]); links != "" {
		t.Fatalf("a command in a standard dir needs no link dir: %s", links)
	}
}

// The kegs a receipt names are read only as real directories in the
// prefix's Cellar: a name that climbs is taken by its last element, a tap
// formula by its name, a file or a link in the Cellar is skipped, and a keg
// some plugin may
// write, or one that holds conductor's own dir, fails the launch; a keg
// that is a symlink is not read.
func TestHomebrewDependencyKegsAreChecked(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}

	if err := os.WriteFile(filepath.Join(r.cellar, "libfoo", "3.0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(r.cellar, "libfoo", "4.0")); err != nil {
		t.Fatal(err)
	}
	r.receipt(t, `{"runtime_dependencies":[{"full_name":"../../../.."},{"full_name":".."},{"full_name":"me/tap/libfoo"},{"full_name":"notinstalled"}]}`)
	cmds, err := findTool(t, s, sd)
	if err != nil || strings.Join(cmds[0].reads, ",") != r.keg+","+r.lib {
		t.Fatalf("commands %+v, err %v; want the keg and libfoo 2.0 alone", cmds, err)
	}
	r.receipt(t, `{"runtime_dependencies":[{"full_name":"libfoo"}]}`)

	s.Manifest.Write = []string{"~/brew/Cellar/libfoo"}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "may write") {
		t.Fatalf("commands %+v, err %v; want a dependency keg the plugin may write refused", cmds, err)
	}
	s.Manifest.Write = nil

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
	if err := os.Rename(r.lib, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, r.lib); err != nil {
		t.Fatal(err)
	}
	if cmds, err := findTool(t, s, sd); err != nil || strings.Join(cmds[0].reads, ",") != r.keg {
		t.Fatalf("commands %+v, err %v; want a dependency keg that is a symlink skipped", cmds, err)
	}
	r.noBrewRun(t)
}

// What Homebrew recorded must name the program PATH already runs: a link
// to a file no keg holds, a keg with no receipt (or one that is not a
// regular file, or not JSON), a link outside a bin dir, or a Cellar that
// is a symlink finds nothing, and the launch fails naming the config line,
// with the program filled in.
func TestHomebrewRecordMustNameTheSameProgram(t *testing.T) {
	home, sd := jailTestDirs(t)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, r brewRig) string
		want  string
	}{
		{"a link to a file no keg holds", func(t *testing.T, r brewRig) string {
			decoy := filepath.Join(home, "decoy", "tool")
			if err := os.MkdirAll(filepath.Dir(decoy), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(decoy, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			os.Remove(r.link)
			if err := os.Symlink(decoy, r.link); err != nil {
				t.Fatal(err)
			}
			return "no package manager conductor asks says it made it"
		}, ""},
		{"no receipt", func(t *testing.T, r brewRig) string {
			os.Remove(filepath.Join(r.keg, "INSTALL_RECEIPT.json"))
			return "did not record installing"
		}, ""},
		{"a receipt that is a link", func(t *testing.T, r brewRig) string {
			rp := filepath.Join(r.keg, "INSTALL_RECEIPT.json")
			if err := os.Rename(rp, rp+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rp+".real", rp); err != nil {
				t.Fatal(err)
			}
			return "not a receipt file"
		}, ""},
		{"a receipt that is not JSON", func(t *testing.T, r brewRig) string {
			r.receipt(t, "{")
			return "did not record installing"
		}, ""},
		{"a link outside a bin dir", func(t *testing.T, r brewRig) string {
			other := filepath.Join(r.prefix, "libexec")
			if err := os.MkdirAll(other, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(r.link, filepath.Join(other, "tool")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", other+":/usr/bin:/bin")
			return "no package manager conductor asks says it made it"
		}, ""},
		{"a Cellar that is a symlink", func(t *testing.T, r brewRig) string {
			moved := filepath.Join(r.prefix, "RealCellar")
			if err := os.Rename(r.cellar, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, r.cellar); err != nil {
				t.Fatal(err)
			}
			return "no package manager conductor asks says it made it"
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.RemoveAll(filepath.Join(home, "brew"))
			os.RemoveAll(filepath.Join(home, "decoy"))
			r := newBrewRig(t, home)
			want := c.setup(t, r)
			cmds, err := findTool(t, s, sd)
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "command tool") || !strings.Contains(err.Error(), "commands: { tool: ") {
				t.Fatalf("commands %+v, err %v; want it refused with %q and the config line", cmds, err, want)
			}
			r.noBrewRun(t)
		})
	}
}

// Ported from the review that found brew run unjailed while a plugin could
// write its cache (bootsnap, loaded by `brew deps`): brew is never run, so
// a plugin that may write Homebrew's cache, or the cache dir above it,
// changes nothing — the command is still found from what is on disk.
func TestBrewIsNeverRunWhateverThePluginMayWrite(t *testing.T) {
	for _, w := range []string{"~/Library/Caches/Homebrew", "~/Library/Caches", "~/.cache/Homebrew", "~/.cache"} {
		t.Run(w, func(t *testing.T) {
			home, sd := jailTestDirs(t)
			r := newBrewRig(t, home)
			if err := os.MkdirAll(filepath.Join(home, strings.TrimPrefix(w, "~/")), 0o755); err != nil {
				t.Fatal(err)
			}
			s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Write: []string{w}}}
			cmds, err := findTool(t, s, sd)
			if err != nil || cmds[0].real != r.real {
				t.Fatalf("commands %+v, err %v; want the keg program", cmds, err)
			}
			r.noBrewRun(t)
		})
	}
}

// A prefix the plugin may write, where it could have placed the link, the
// keg or the receipt, is refused: a write on its bin dir, on it, on its
// keg, or the staging dir holding it.
func TestHomebrewPrefixThePluginMayWriteIsRefused(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	rel := func(p string) string { return "~/" + strings.TrimPrefix(p, home+"/") }
	for _, w := range []string{rel(filepath.Dir(r.link)), rel(r.prefix), rel(filepath.Join(r.cellar, "tool")), "~"} {
		t.Run(w, func(t *testing.T) {
			s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Write: []string{w}}}
			cmds, err := findTool(t, s, sd)
			if err == nil || !strings.Contains(err.Error(), "may write") {
				t.Fatalf("commands %+v, err %v; want it refused over the write grant", cmds, err)
			}
		})
	}
	sd.StagingRoot = r.prefix
	if _, err := findTool(t, Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}, sd); err == nil {
		t.Fatal("a prefix inside the staging dir was trusted")
	}
	r.noBrewRun(t)
}

// Ported from the review that found the checks saw only the launching
// plugin's writes: another plugin conductor runs that may write the
// prefix (or a keg, or a program PATH finds) could plant what this one's
// jail binds, so it is refused just the same — through the Manager, which
// gives every launch the union of its plugins' writes.
func TestOtherPluginsWritesAreRefused(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	rel := "~/" + strings.TrimPrefix(r.prefix, home+"/")
	m := NewManager(nil, "", stateAt(t), Deps{Sandbox: sd})
	defer m.Close()
	m.specs["b"] = Spec{Name: "b", Manifest: Manifest{Write: []string{rel}}}
	m.order = append(m.order, "b")
	m.publishSpecs()
	peer := m.deps.Sandbox
	if !slices.Contains(peer.PeerWritable(), r.prefix) {
		t.Fatalf("the Manager's launches must see plugin b's writes: %v", peer.PeerWritable())
	}
	a := Spec{Name: "a", Manifest: Manifest{Commands: []string{"tool"}}}
	if cmds, err := findTool(t, a, sd); err != nil || cmds[0].real != r.real {
		t.Fatalf("alone, a finds the keg program: %+v %v", cmds, err)
	}
	cmds, err := findTool(t, a, peer)
	if err == nil || !strings.Contains(err.Error(), "a plugin conductor runs may write") {
		t.Fatalf("commands %+v, err %v; want a prefix another plugin may write refused", cmds, err)
	}
	for _, w := range []string{r.lib, filepath.Join(r.cellar, "tool")} {
		sd := sd
		sd.PeerWritable = func() []string { return []string{w} }
		if cmds, err := findTool(t, a, sd); err == nil || !strings.Contains(err.Error(), "a plugin conductor runs may write") {
			t.Fatalf("another plugin writes %s: commands %+v, err %v", w, cmds, err)
		}
	}
	// A program PATH finds directly, inside another plugin's write.
	plain := filepath.Join(home, "plain", "bin")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", plain+":/usr/bin:/bin")
	sd.PeerWritable = func() []string { return []string{filepath.Dir(plain)} }
	if cmds, err := findTool(t, a, sd); err == nil || !strings.Contains(err.Error(), "a plugin conductor runs may write") {
		t.Fatalf("a PATH program another plugin may write: commands %+v, err %v", cmds, err)
	}
	r.noBrewRun(t)
}

// The config's `commands:` names the program file: it wins over PATH, is
// checked like a command PATH finds, and is followed through a symlink the
// operator named (as a runtime's bin: is run) unless the symlink is one
// the plugin could have made. A file not named for the command, or one
// that is not there, fails the launch.
func TestCommandNamedByTheConfig(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
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
		{"a link the plugin could have made", planted, []string{"~/w"}, "which it may write"},
		{"a file inside a write grant", r.real, []string{"~/brew/Cellar"}, "which it may write"},
		{"a link to a file inside a write grant", r.link, []string{"~/brew/Cellar"}, "which it may write"},
		{"not named for the command", other, nil, "name a file called tool"},
		{"a file another plugin may write", r.real, nil, "a plugin conductor runs may write"},
		{"not there", filepath.Join(home, "nope", "tool"), nil, "no such file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Write: c.write}, Commands: map[string]string{"tool": c.named}}
			sd := sd
			if c.write == nil && strings.Contains(c.want, "plugin conductor runs") {
				sd.PeerWritable = func() []string { return []string{r.cellar} }
			}
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
