package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Homebrew-installed command runs jailed on Linux with the libraries of
// what it was installed with, while nothing else in those install trees
// can run: each keg is bound noexec but for its lib dir (a library is
// mapped executable), so the keg's other programs, and its dependencies',
// are refused, and an undeclared name does not resolve into a keg's bin.
func TestHomebrewKegsRunOnlyTheDeclaredProgram(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skip("requires unprivileged root-mapped user namespaces:", err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	prefix := filepath.Join(dir, "brew")
	cellar := filepath.Join(prefix, "Cellar")
	keg := filepath.Join(cellar, "tool", "1.0")
	lib := filepath.Join(cellar, "libfoo", "2.0")
	for _, d := range []string{home, filepath.Join(prefix, "bin"), filepath.Join(keg, "bin"), filepath.Join(lib, "lib"), filepath.Join(lib, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := func(name, body string) string {
		p := filepath.Join(dir, name+".c")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	build := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
			t.Fatalf("cc %v: %v\n%s", args, err, out)
		}
	}
	build("-shared", "-fPIC", "-o", filepath.Join(lib, "lib", "libfoo.so"), src("foo", "int foo(void){return 42;}\n"))
	build("-o", filepath.Join(keg, "bin", "tool"), src("tool", "#include <stdio.h>\nint foo(void);\nint main(void){printf(\"foo=%d\\n\",foo());return 0;}\n"),
		"-L"+filepath.Join(lib, "lib"), "-lfoo", "-Wl,-rpath,"+filepath.Join(lib, "lib"))
	other := src("other", "int main(void){return 0;}\n")
	build("-o", filepath.Join(keg, "bin", "other"), other)
	build("-o", filepath.Join(lib, "bin", "helper"), other)
	if err := os.WriteFile(filepath.Join(keg, "INSTALL_RECEIPT.json"), []byte(`{"runtime_dependencies":[{"full_name":"libfoo"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/tool/1.0/bin/tool", filepath.Join(prefix, "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(prefix, "bin")+":"+os.Getenv("PATH"))
	jailHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { jailHome = os.UserHomeDir })
	sd := SandboxDeps{StagingRoot: filepath.Join(dir, "staging")}
	m := Manifest{Commands: []string{"tool"}}
	g, err := pluginGrant(Spec{Name: "probe", Manifest: m})
	if err != nil {
		t.Fatal(err)
	}
	found, err := findCommands(Spec{Name: "probe"}, newGrantChecks(sd, g), m.Commands)
	if err != nil || found[0].source != fromHomebrew {
		t.Fatalf("found %+v, err %v; want the keg's tool from homebrew", found, err)
	}
	keys := []string{"out=tool", "exec=" + filepath.Join(keg, "bin", "other"), "exec=" + filepath.Join(lib, "bin", "helper"), "exec=other", "env=PATH", "writepath=planted"}
	got := runProbePlugin(t, dir, sd, m, keys...)
	if got[keys[0]] != "ok:foo=42" {
		t.Fatalf("the declared program with its dependency's library: %q", got[keys[0]])
	}
	for _, k := range []string{keys[1], keys[2], keys[3], keys[5]} {
		if !strings.HasPrefix(got[k], "denied") {
			t.Errorf("%s: %q, want it refused", k, got[k])
		}
	}
	// No keg's bin dir is on the jail's PATH: tool is reached through the
	// launch's links to its declared commands, which the plugin cannot
	// change (writepath, above, is refused).
	if p := got["env=PATH"]; strings.Contains(p, cellar) || !strings.Contains(p, filepath.Join(sd.StagingRoot, ".command-links", "probe-")) {
		t.Errorf("jail PATH %q, want the command links and no keg bin dir", p)
	}
}
