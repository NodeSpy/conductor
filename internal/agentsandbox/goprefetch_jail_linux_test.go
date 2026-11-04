//go:build linux

package agentsandbox

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// probeGo writes a fake "go" that behaves like the real `go mod download`
// for our purposes (exits 0, no args checked) but FIRST checks whether
// secret is visible — exiting loudly if so. The path is baked into the
// script text (like fakeGo's record/fail paths elsewhere in this package),
// so no extra env wiring is needed to reach it from inside the jail.
func probeGo(t *testing.T, secret string) (bin string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "go")
	script := "#!/bin/sh\n" +
		"if [ -e " + secret + " ]; then echo 'SECRET VISIBLE' >&2; exit 9; fi\n" +
		"mkdir -p \"$GOMODCACHE\" 2>/dev/null; touch \"$GOMODCACHE/wrote\" || { echo 'cache not writable' >&2; exit 8; }\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestGoPrefetchFillRunsConfined proves the FULL production path (rt2 #11):
// GoPrefetch.Fill wraps `go mod download` through sandbox.WrapLocalCommand
// with Confine, and the download really runs inside the jail — it CANNOT
// read a file planted in a decoy "operator HOME" well outside the
// allow-list, and it CAN write into the module cache it was given.
func TestGoPrefetchFillRunsConfined(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the conductor binary; skipped under -short")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--pid", "--fork",
		"--mount-proc", "--kill-child", "true").Run(); err != nil {
		t.Skipf("root-mapped user namespaces unavailable here: %v", err)
	}

	// Build a real conductor binary — sandbox-net (the in-jail helper) is a
	// subcommand of it.
	bin := filepath.Join(t.TempDir(), "conductor")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}

	// A decoy "operator HOME" with a secret file, under a DIFFERENT temp
	// root than the download's own scratch dir or the cache — never bound
	// into the jail. If the confined download can still see it, the jail
	// failed to hide it by absence.
	decoyHome := t.TempDir()
	secret := filepath.Join(decoyHome, ".netrc")
	if err := os.WriteFile(secret, []byte("machine example.com login op password TOPSECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goBin := probeGo(t, secret)

	cache := filepath.Join(t.TempDir(), "modcache")
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	g := &GoPrefetch{
		Cache: cache,
		Go:    goBin,
		Proxy: "https://proxy.golang.org",
		Env:   []string{"PATH=" + os.Getenv("PATH")},
		Sandbox: sandbox.LocalWrapDeps{
			SelfExe:    func() (string, error) { return bin, nil },
			EgressUnix: pm.UnixEndpoint,
			EgressAddr: pm.Endpoint,
			// The fake go's own directory must be visible (and executable)
			// inside the jail for the re-exec to find it at all — the same
			// allow-list entry a code step's own code temp dir gets.
			ExtraBinds: []sandbox.BindMount{{Path: filepath.Dir(goBin), RO: true}},
		},
	}

	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n\ngo 1.23\n")}}
	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatalf("confined prefetch failed (exit 9 = the decoy secret was VISIBLE, exit 8 = the cache was not writable): %v", err)
	}

	// The probe wrote into GOMODCACHE from inside the jail — the module
	// cache bind is really read-write there.
	if _, err := os.Stat(filepath.Join(cache, "wrote")); err != nil {
		t.Fatalf("the confined download could not write its module cache: %v", err)
	}
	// The marker proves it actually ran (not silently skipped).
	if _, err := os.Stat(filepath.Join(cache, ".conductor-prefetched-v2", m.Digest())); err != nil {
		t.Fatalf("the confined download did not complete: %v", err)
	}
}

// fakeToolchainProxy is a GOPROXY tree serving golang.org/toolchain for a
// made-up go1.99.0. Its bin/go answers what the prefetch asks (mod
// download, env GOROOT) and builds with realGo, on a copy of the module
// whose go line realGo can read.
func fakeToolchainProxy(t *testing.T, root, realGo string) string {
	t.Helper()
	ver := "v0.0.1-go1.99.0." + runtime.GOOS + "-" + runtime.GOARCH
	dir := filepath.Join(root, "proxy", "golang.org", "toolchain", "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	goSh := "#!/bin/sh\n" +
		"root=$(cd \"$(dirname \"$0\")/..\" && pwd)\n" +
		"[ \"$1\" = -C ] && { cd \"$2\" || exit 1; shift 2; }\n" +
		"case \"$1 $2\" in\n" +
		"'mod download') exit 0;;\n" +
		"'env GOROOT') echo \"$root\"; exit 0;;\n" +
		"'version '*) echo 'go version go1.99.0 " + runtime.GOOS + "/" + runtime.GOARCH + "'; exit 0;;\n" +
		"esac\n" +
		"grep -qx 'go 1.99.0' go.mod || { echo 'fake go1.99.0: not my module' >&2; exit 3; }\n" +
		"t=$(mktemp -d) && cp -R . \"$t\" && sed 's/^go 1\\.99\\.0$/go 1.21/' go.mod >\"$t/go.mod\" && cd \"$t\" || exit 1\n" +
		"unset GOROOT; GOTOOLCHAIN=local exec " + realGo + " \"$@\"\n"
	zf, err := os.Create(filepath.Join(dir, ver+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	// Go makes pkg/tool, lib, bin/gofmt and bin executable once it has
	// unpacked them.
	for name, body := range map[string]string{"bin/go": goSh, "bin/gofmt": "#!/bin/sh\n", "pkg/tool/README": "fake\n", "lib/README": "fake\n"} {
		h := &zip.FileHeader{Name: "golang.org/toolchain@" + ver + "/" + name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, werr := zw.CreateHeader(h)
		if werr == nil {
			_, werr = w.Write([]byte(body))
		}
		if werr != nil {
			t.Fatal(werr)
		}
	}
	err = zw.Close()
	if err == nil {
		err = zf.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"list": ver + "\n", ver + ".info": `{"Version":"` + ver + `"}`, ver + ".mod": "module golang.org/toolchain\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "proxy")
}

// TestGoPrefetchToolchainBuildsOffline: a workspace whose go.mod needs a
// newer Go than the host's builds in the real agent sandbox, offline. The
// confined prefetch fetches the toolchain the go line selects (a made-up
// go1.99.0 from a file:// proxy: no network) into the cache, and the launch
// runs that toolchain's go, first on PATH, with GOTOOLCHAIN=local.
func TestGoPrefetchToolchainBuildsOffline(t *testing.T) {
	root := requireSandbox(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	if r, err := filepath.EvalSymlinks(realGo); err == nil {
		realGo = r
	}
	out, err := exec.Command(realGo, "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	realRoot := strings.TrimSpace(string(out))
	defer func(f func() bool) { systemdUserBusAvailableFunc = f }(systemdUserBusAvailableFunc)
	systemdUserBusAvailableFunc = func() bool { return false }

	proxy := fakeToolchainProxy(t, root, realGo)
	cache := filepath.Join(root, "modcache")
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module x\n\ngo 1.99.0\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc main() { println(\"built offline\") }\n"), 0o644)

	self := selfConductor(t)
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	g := &GoPrefetch{
		Cache: cache, Go: realGo, Proxy: "file://" + proxy,
		// A made-up toolchain is in no checksum database: Go takes one from
		// a file:// proxy unverified only with GOSUMDB=off.
		Env: []string{"PATH=" + os.Getenv("PATH"), "GOSUMDB=off"},
		Sandbox: sandbox.LocalWrapDeps{
			SelfExe: self, EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			ExtraBinds: []sandbox.BindMount{{Path: proxy, RO: true}, {Path: realRoot, RO: true}},
		},
	}
	if err := g.Workspace(context.Background(), ws); err != nil {
		t.Fatalf("prefetch: %v", err)
	}
	bin := g.ToolchainBin(ws)
	if !strings.HasPrefix(bin, filepath.Join(cache, "golang.org", "toolchain@v0.0.1-go1.99.0.")) {
		t.Fatalf("the prefetch recorded no toolchain in the cache: %q", bin)
	}

	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(root, "st"), Key: "agent:go", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	pol := Policy{
		Identity: "agent:go", Tool: "claude-code", Workspace: ws, Home: home, Logins: logins,
		ReadOnly: []string{realRoot},
		Detect:   DetectSpec{Action: DetectOff}, Limits: LimitSpec{Off: true},
	}
	g.Apply(&pol, ws)
	l, err := Wrap(pol, []string{"/bin/sh", "-c", "go version && go run ."}, Deps{
		SelfExe: self, EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, "GOCACHE="+filepath.Join(ws, ".gocache"))
	got, err := cmd.CombinedOutput()
	t.Logf("in the sandbox:\n%s", got)
	if err != nil || !strings.Contains(string(got), "go version go1.99.0") || !strings.Contains(string(got), "built offline") {
		t.Fatalf("a module needing go1.99.0 did not build in the sandbox: %v", err)
	}
}
