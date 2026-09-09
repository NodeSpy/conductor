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

// TestPagerDutyPluginEndToEnd builds the REAL conductor-pagerduty plugin and
// proves the extracted path incl. the multi-value X-PagerDuty-Signature (v1=…,v1=…)
// verified by sourcekit.
func TestPagerDutyPluginEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "conductor-pagerduty")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/plugins/conductor-pagerduty")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build conductor-pagerduty: %v\n%s", err, out)
	}

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()

	spec := Spec{Name: "pd1", Kind: KindConnector, Provides: "pagerduty", BinPath: bin, AllowUnverified: true, AllowUnsandboxed: true}
	c := NewClient(spec, Deps{})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	secret := "pd-signing-secret"
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
	req := StartSourceRequest{Instance: "pd1", Config: map[string]any{
		"listen": addr, "path": "/pagerduty", "signing_secret": secret,
	}}
	if err := c.StartSource(ctx, req, emit); err != nil {
		t.Fatalf("StartSource: %v", err)
	}

	body := []byte(`{"event":{"event_type":"incident.triggered","data":{"id":"PD1","number":42,"status":"triggered","title":"DB down","html_url":"https://pd/x","urgency":"high","priority":{"summary":"P1"},"service":{"id":"SVC1","summary":"api"}}}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	// Two v1= values, only the second valid — proves ANY-match rotation handling.
	sig := "v1=deadbeef,v1=" + hex.EncodeToString(mac.Sum(nil))

	url := "http://" + addr + "/pagerduty"
	posted := false
	for i := 0; i < 100; i++ {
		r, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if r != nil {
			r.Header.Set("X-PagerDuty-Signature", sig)
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
		t.Fatal("never delivered an accepted webhook")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the streamed pagerduty event")
	}

	mu.Lock()
	defer mu.Unlock()
	ev := got[0]
	if ev["event"] != "incident" || ev["kind"] != "incident.triggered" {
		t.Fatalf("event/kind wrong: %+v", ev)
	}
	ectx, _ := ev["context"].(map[string]any)
	if ectx["urgencies"] != "high" || ectx["event_types"] != "incident.triggered" {
		t.Fatalf("filter-key context wrong: %+v", ectx)
	}
}
