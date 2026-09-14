package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// buildTickerPlugin compiles the reference acme-ticker SOURCE plugin.
func buildTickerPlugin(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-ticker")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-ticker")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build ticker plugin: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// TestSourcePluginStreamsEvents drives the REAL subprocess over the real
// transport: StartSource, then the plugin streams events back as notifications
// which the daemon Client routes to the emit sink. Proves the source half of the
// plugin protocol end-to-end (the missing StartSource interface, #59).
func TestSourcePluginStreamsEvents(t *testing.T) {
	bin, sum := buildTickerPlugin(t)
	spec := Spec{Name: "ticker", Kind: KindConnector, Provides: "acme-ticker", BinPath: bin, Sha256: sum}
	c := NewClient(spec, Deps{})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		got  []map[string]any
		done = make(chan struct{})
	)
	emit := func(raw json.RawMessage) {
		var ev map[string]any
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Errorf("bad event payload: %v", err)
			return
		}
		mu.Lock()
		got = append(got, ev)
		n := len(got)
		mu.Unlock()
		if n == 3 {
			close(done)
		}
	}

	req := StartSourceRequest{Instance: "t1", Config: map[string]any{"count": 3, "prefix": "T-"}}
	if err := c.StartSource(ctx, req, emit); err != nil {
		t.Fatalf("StartSource: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		mu.Lock()
		n := len(got)
		mu.Unlock()
		t.Fatalf("timed out waiting for 3 events, got %d", n)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	// Config reached the source (prefix applied) and the instance was threaded.
	if got[0]["title"] != "T-tick 0" || got[0]["instance"] != "t1" {
		t.Fatalf("first event = %+v, want title T-tick 0 / instance t1", got[0])
	}
	if got[2]["kind"] != "tick" {
		t.Fatalf("event kind = %v, want tick", got[2]["kind"])
	}
}
