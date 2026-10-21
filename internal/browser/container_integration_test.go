//go:build linux || darwin

package browser

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// requireContainer finds an engine and the browser image (built on first
// use) — skipped unless CONDUCTOR_TEST_CONTAINER=1.
func requireContainer(t *testing.T) (*ContainerEngine, string) {
	t.Helper()
	if os.Getenv("CONDUCTOR_TEST_CONTAINER") != "1" {
		t.Skip("CONDUCTOR_TEST_CONTAINER=1 runs the container browser")
	}
	e, err := FindEngine(os.Getenv("CONDUCTOR_TEST_ENGINE"), runtime.GOOS, exec.LookPath, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	img, err := e.EnsureImage(ctx, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return e, img
}

// TestContainerBrowserIsSandboxed: Chromium in the container, with its own
// sandbox — chrome://sandbox says so.
func TestContainerBrowserIsSandboxed(t *testing.T) {
	e, img := requireContainer(t)
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	ring := &Ring{}
	p, err := Launch(LaunchSpec{Dir: t.TempDir(), Allow: []string{"example.invalid"}, Proxy: pm, Engine: e, Image: img, Stderr: ring})
	if err != nil {
		t.Fatalf("%v\n%s", err, ring.String())
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := SandboxStatus(ctx, p)
	t.Logf("chrome://sandbox in the container:\n%s", st)
	if err != nil || !SandboxedOK(st) {
		t.Fatalf("Chromium's own sandbox is not on in the container: %v\n%s", err, ring.String())
	}
	if p.Isolation() != "container" {
		t.Fatal(p.Isolation())
	}
}
