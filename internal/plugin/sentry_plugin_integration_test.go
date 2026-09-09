package plugin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSentryPluginEndToEnd is the extraction proof: it builds the REAL
// conductor-sentry plugin (built only against pkg/plugin + pkg/sourcekit),
// drives it through the daemon's plugin client, delivers a signed synthetic
// Sentry webhook to the plugin's HTTP listener, and asserts the daemon receives
// the normalized event over the source stream. This exercises the whole
// extracted path: SDK Serve + SourceHandler, sourcekit HMAC + listener, the
// StartSource protocol, and the daemon client's event routing.
func TestSentryPluginEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "conductor-sentry")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/plugins/conductor-sentry")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build conductor-sentry: %v\n%s", err, out)
	}

	// A free localhost port for the plugin's webhook listener.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	spec := Spec{Name: "sentry1", Kind: KindConnector, Provides: "sentry", BinPath: bin, AllowUnverified: true, AllowUnsandboxed: true}
	c := NewClient(spec, Deps{})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	secret := "sentry-hmac-secret"
	var (
		mu   sync.Mutex
		got  []map[string]any
		done = make(chan struct{}, 1)
	)
	emit := func(raw json.RawMessage) {
		var ev map[string]any
		if json.Unmarshal(raw, &ev) == nil {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}
	req := StartSourceRequest{Instance: "sentry1", Config: map[string]any{
		"listen": addr, "path": "/sentry", "client_secret": secret,
	}}
	if err := c.StartSource(ctx, req, emit); err != nil {
		t.Fatalf("StartSource: %v", err)
	}

	// Wait for the plugin's listener to come up.
	body := []byte(`{"action":"created","data":{"issue":{"title":"NPE in checkout","level":"error","shortID":"WEB-1","culprit":"checkout.go","permalink":"https://sentry.io/x","project":{"slug":"web"}}}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	url := "http://" + addr + "/sentry"
	var posted bool
	for i := 0; i < 100; i++ {
		r, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if r != nil {
			r.Header.Set("Sentry-Hook-Resource", "issue")
			r.Header.Set("Sentry-Hook-Signature", sig)
			if resp, err := http.DefaultClient.Do(r); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusAccepted {
					posted = true
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !posted {
		t.Fatal("never delivered a webhook the plugin accepted (listener up? signature ok?)")
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the streamed sentry event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no event received")
	}
	ev := got[0]
	if ev["event"] != "issue_alert" {
		t.Fatalf("event = %v, want issue_alert", ev["event"])
	}
	ectx, _ := ev["context"].(map[string]any)
	if ectx["level"] != "error" || ectx["project"] != "web" || ectx["short_id"] != "WEB-1" {
		t.Fatalf("event context wrong: %+v", ectx)
	}

	// A bad-signature delivery must be rejected (401) and produce no event.
	r, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	r.Header.Set("Sentry-Hook-Resource", "issue")
	r.Header.Set("Sentry-Hook-Signature", "deadbeef")
	if resp, err := http.DefaultClient.Do(r); err == nil {
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad-signature webhook returned %d, want 401", resp.StatusCode)
		}
		resp.Body.Close()
	}
}
