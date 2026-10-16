//go:build linux

package browser

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// TestHeadedTakeoverOnAVirtualDisplay: the operator's takeover in a real
// window (on Xvfb in CI): a headed, sandboxed Chromium whose jail shows the
// display socket and nothing of the home seeds a session and registers a
// passkey. CONDUCTOR_TEST_HEADED=1 with DISPLAY set.
func TestHeadedTakeoverOnAVirtualDisplay(t *testing.T) {
	if os.Getenv("CONDUCTOR_TEST_HEADED") != "1" || os.Getenv("DISPLAY") == "" {
		t.Skip("CONDUCTOR_TEST_HEADED=1 with a DISPLAY (xvfb-run) runs the headed takeover")
	}
	chrome := requireBrowser(t)
	site := browsertest.New()
	srv := httptest.NewServer(site)
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	k, err := StartTakeover(ctx, LaunchSpec{Chrome: chrome, Dir: t.TempDir(), Allow: []string{"localhost:" + port}, SelfExe: selfExe(t),
		Headed: true, Display: []string{"DISPLAY=" + os.Getenv("DISPLAY")}, Show: []string{"/tmp/.X11-unix"},
		Args: []string{"--ozone-platform=x11"}, Stderr: &Ring{}},
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
