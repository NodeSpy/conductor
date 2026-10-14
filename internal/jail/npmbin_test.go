package jail

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// npmInstall lays out a global npm install the way `npm i -g` does:
// <prefix>/lib/node_modules/<pkg>/{package.json,node_modules/dep,dist/index.js}
// and <prefix>/bin/<cli> → ../lib/node_modules/<pkg>/dist/index.js.
func npmInstall(t *testing.T) (prefix, bin, pkg string) {
	t.Helper()
	prefix = t.TempDir()
	pkg = filepath.Join(prefix, "lib", "node_modules", "@acme", "agent-acp")
	for _, d := range []string{filepath.Join(pkg, "dist"), filepath.Join(pkg, "node_modules", "dep"), filepath.Join(prefix, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{
		filepath.Join(pkg, "package.json"):                        `{"name":"@acme/agent-acp"}`,
		filepath.Join(pkg, "node_modules", "dep", "package.json"): `{"name":"dep"}`,
		filepath.Join(pkg, "dist", "index.js"):                    "#!/usr/bin/env node\nrequire('dep')\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin = filepath.Join(prefix, "bin", "agent-acp")
	if err := os.Symlink("../lib/node_modules/@acme/agent-acp/dist/index.js", bin); err != nil {
		t.Fatal(err)
	}
	return prefix, bin, pkg
}

// An npm-installed CLI is a node script that loads its dependencies from its
// package's node_modules: the jail must show the whole package, not just
// the script's own directory.
func TestBinaryRootsShowANodeScriptsPackage(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	_, bin, pkg := npmInstall(t)
	roots := binaryRoots(bin, exec.LookPath)
	found := false
	for _, r := range roots {
		if r == pkg {
			found = true
		}
		if r == filepath.Dir(pkg) || r == filepath.Join(filepath.Dir(pkg), "..") {
			t.Fatalf("never the whole node_modules tree: %v", roots)
		}
	}
	if !found {
		t.Fatalf("the package root %s is shown: %v", pkg, roots)
	}
	if packageRoot("/") != "" || packageRoot(t.TempDir()) != "" {
		t.Fatal("no package, no root")
	}
}

// The symlink an agent's launch goes through (~/.local/bin/x → ../lib/…)
// is recreated in the jail — the scratch home has none of the operator's,
// and a bind would follow the link instead.
func TestJailRecreatesTheLaunchSymlink(t *testing.T) {
	_, bin, _ := npmInstall(t)
	m := &Manager{Root: t.TempDir(), Home: t.TempDir(), LookPath: exec.LookPath, SelfExe: func() (string, error) { return "/bin/true", nil }}
	d := &Dispatch{LaunchSpec: LaunchSpec{Tool: "claude-code", Argv0: bin}, Dir: t.TempDir(), TmpDir: t.TempDir()}
	binds, err := m.layout(d, "/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	want := sandbox.BindMount{Path: bin, Link: "../lib/node_modules/@acme/agent-acp/dist/index.js"}
	for _, b := range binds {
		if b == want {
			return
		}
	}
	t.Fatalf("no %+v in %+v", want, binds)
}
