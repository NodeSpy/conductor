package browser

import (
	"context"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// TestHeadedTakeoverOnAVirtualDisplay: the operator's takeover in a real
// window (Linux: on Xvfb in CI, the jail showing the display socket; macOS:
// the runner's own window server, through the headed Seatbelt variant): a
// headed, sandboxed Chromium seeds a session and registers a passkey.
func TestHeadedTakeoverOnAVirtualDisplay(t *testing.T) {
	if os.Getenv("CONDUCTOR_TEST_HEADED") != "1" || (runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "") {
		t.Skip("CONDUCTOR_TEST_HEADED=1 (on Linux with a DISPLAY: xvfb-run) runs the headed takeover")
	}
	chrome := requireBrowser(t)
	site := browsertest.New()
	srv := httptest.NewServer(site)
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	spec := LaunchSpec{Chrome: chrome, Dir: t.TempDir(), Allow: []string{"localhost:" + port}, SelfExe: selfExe(t), Headed: true, Stderr: &Ring{}}
	if runtime.GOOS == "linux" {
		spec.Display, spec.Show, spec.Args = []string{"DISPLAY=" + os.Getenv("DISPLAY")}, []string{"/tmp/.X11-unix"}, []string{"--ozone-platform=x11"}
		if xa := os.Getenv("XAUTHORITY"); xa != "" {
			spec.Display, spec.Show = append(spec.Display, "XAUTHORITY="+xa), append(spec.Show, xa)
		}
	}
	defer func() {
		if t.Failed() {
			t.Logf("the browser's stderr: %s", spec.Stderr.String())
		}
	}()
	k, err := StartTakeover(ctx, spec,
		[]Cookie{{Name: "sid", Value: site.Session(), Domain: "localhost", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	// It is a window: the browser reports no headless user agent.
	v, err := k.In.eval(ctx, k.Tab, "navigator.userAgent")
	if err != nil || strings.Contains(string(v), "Headless") {
		t.Fatalf("not headed: %s %v", v, err)
	}
	if err := k.Open(ctx, "http://localhost:"+port+"/account"); err != nil {
		t.Fatal(err)
	}
	cs, err := k.Cookies(ctx, "localhost")
	if err != nil || len(cs) != 1 || cs[0].Name != "sid" {
		t.Fatalf("the seeded session, kept for its site: %+v %v", cs, err)
	}
	auth, err := k.ArmPasskey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(ctx, "http://localhost:"+port+"/passkey/register?auto=1"); err != nil {
		t.Fatal(err)
	}
	pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
	defer pcancel()
	pk, err := k.TakePasskey(pctx, auth, "localhost", nil)
	if err != nil {
		page, _ := k.In.eval(ctx, k.Tab, "document.body.innerText.slice(0, 300)")
		t.Logf("the page: %s", page)
	}
	if err != nil || pk.RPID != "localhost" {
		t.Fatalf("a passkey registered in the window: %+v %v", pk, err)
	}
	for i := 0; i < 50 && site.Passkeys() == 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if site.Passkeys() != 1 {
		t.Fatal("the site has no passkey")
	}
}
