//go:build linux

package agentsandbox

import (
	"archive/zip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

// moduleZip is a module zip for example.test/m@v1.0.0.
func moduleZip(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	zw := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"go.mod": "module example.test/m\n\ngo 1.21\n",
		"m.go":   "package m\n\nconst V = 1\n",
	} {
		w, err := zw.Create("example.test/m@v1.0.0/" + name)
		if err == nil {
			_, err = w.Write([]byte(body))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(b.String())
}

// TestGoPrefetchFollowsTheProxyRedirect: a Go module proxy that answers a
// zip with a redirect to a second host (proxy.golang.org sends large zips
// to a signed Cloud Storage URL). The confined prefetch fetches it when that
// host is one of the proxy's redirect hosts, and fails cleanly — the signed
// query never in its error — when it is not.
func TestGoPrefetchFollowsTheProxyRedirect(t *testing.T) {
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

	zipBytes := moduleZip(t)
	// Both hosts speak TLS, as proxy.golang.org and Cloud Storage do: a
	// refused redirect is then a refused CONNECT, and Go's error carries the
	// redirect's full URL — what put a signed URL in the box's log.
	caPEM, leaf := namesCA(t, "goproxy.test", "blobs.test")
	caDir := t.TempDir()
	caFile := filepath.Join(caDir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	tlsServer := func(h http.HandlerFunc) *httptest.Server {
		s := httptest.NewUnstartedServer(h)
		s.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
		s.StartTLS()
		return s
	}
	var mu sync.Mutex
	blobHits := 0
	blobs := tlsServer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		blobHits++
		mu.Unlock()
		w.Write(zipBytes)
	})
	defer blobs.Close()
	_, blobPort, _ := net.SplitHostPort(blobs.Listener.Addr().String())
	blobHost := "blobs.test:" + blobPort
	proxy := tlsServer(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.test/m/@v/list":
			io.WriteString(w, "v1.0.0\n")
		case "/example.test/m/@v/v1.0.0.info":
			io.WriteString(w, `{"Version":"v1.0.0","Time":"2020-01-01T00:00:00Z"}`)
		case "/example.test/m/@v/v1.0.0.mod":
			io.WriteString(w, "module example.test/m\n\ngo 1.21\n")
		case "/example.test/m/@v/v1.0.0.zip":
			http.Redirect(w, r, "https://"+blobHost+"/proxy-prod/m.zip?Expires=1&Signature=SIGNEDSECRET", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	defer proxy.Close()
	_, proxyPort, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	proxyHost := "goproxy.test:" + proxyPort

	self := selfConductor(t)
	pm := sandbox.NewProxyManager(nil)
	// The made-up names are the two local servers. (A loopback address
	// itself would not do: Go never sends one through HTTP_PROXY.)
	pm.LookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "goproxy.test" || host == "blobs.test" {
			return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
		}
		return nil, fmt.Errorf("no such host %s", host)
	}
	defer pm.Close()
	defer func(m map[string][]string) { goProxyRedirects = m }(goProxyRedirects)

	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n\ngo 1.21\n\nrequire example.test/m v1.0.0\n")}}
	fill := func(name string) (string, error) {
		cache := filepath.Join(root, name)
		g := &GoPrefetch{
			Cache: cache, Go: realGo, Proxy: "https://" + proxyHost,
			// The made-up module is in no checksum database.
			Env: []string{"PATH=" + os.Getenv("PATH"), "GOSUMDB=off", "SSL_CERT_FILE=" + caFile},
			Sandbox: sandbox.LocalWrapDeps{
				SelfExe: self, EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
				ExtraBinds: []sandbox.BindMount{{Path: realRoot, RO: true}, {Path: caDir, RO: true}},
			},
		}
		return cache, g.Fill(context.Background(), m)
	}

	t.Run("redirect host allowed", func(t *testing.T) {
		goProxyRedirects = map[string][]string{proxyHost: {blobHost}}
		cache, err := fill("allowed")
		if err != nil {
			t.Fatalf("prefetch: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cache, "example.test", "m@v1.0.0", "m.go")); err != nil {
			t.Fatalf("the module is not in the cache: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if blobHits != 1 {
			t.Fatalf("the redirect host served %d zips, want 1", blobHits)
		}
	})
	t.Run("redirect host not allowed", func(t *testing.T) {
		goProxyRedirects = map[string][]string{}
		cache, err := fill("refused")
		if err == nil {
			t.Fatal("the prefetch reached a host it was not given")
		}
		t.Logf("refused: %v", err)
		if strings.Contains(err.Error(), "SIGNEDSECRET") || strings.Contains(err.Error(), "Expires") {
			t.Fatalf("the signed query is in the error: %v", err)
		}
		if !strings.Contains(err.Error(), "https://"+blobHost+"/proxy-prod/m.zip?…") {
			t.Fatalf("the error does not name the refused host: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cache, ".conductor-prefetched-v2", m.Digest())); err == nil {
			t.Fatal("a failed prefetch was marked done")
		}
		mu.Lock()
		defer mu.Unlock()
		if blobHits != 1 {
			t.Fatalf("the refused redirect reached the host: %d hits", blobHits)
		}
	})
}

// namesCA is a private CA and one leaf it signed for names.
func namesCA(t *testing.T, names ...string) (caPEM []byte, leaf tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	lt := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
