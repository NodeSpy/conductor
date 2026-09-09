package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuf is a concurrency-safe log sink (pumpStderr writes from a goroutine).
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) log(format string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.WriteString(fmt.Sprintf(format, a...))
	s.b.WriteByte('\n')
}
func (s *safeBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestExamplePluginStderrRedaction proves §8.1: a credential the plugin leaks to
// its own stderr never reaches the daemon log — the transport redacts it.
func TestExamplePluginStderrRedaction(t *testing.T) {
	bin, sum := buildExamplePlugin(t)
	const secret = "s3cr3t-token-value"

	buf := &safeBuf{}
	// Stand-in for secrets.Resolver.Redact over a Tracked value.
	redact := func(s string) string { return strings.ReplaceAll(s, secret, "«redacted»") }
	spec := Spec{Name: "echo", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: sum, AllowUnsandboxed: true}
	c := NewClient(spec, Deps{Log: buf.log, Redact: redact})
	defer c.Close()

	// leak:true makes the plugin print the received token to its stderr.
	if _, err := c.Invoke(context.Background(), InvokeRequest{
		Instance: "e1", Verb: "echo",
		Options:    map[string]any{"message": "hi", "leak": true},
		Connection: map[string]any{"token": secret},
	}); err != nil {
		t.Fatal(err)
	}

	// Wait for the stderr pump to flush the (redacted) DEBUG line.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "DEBUG received token") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs := buf.String()
	if !strings.Contains(logs, "DEBUG received token") {
		t.Fatalf("plugin stderr never reached the daemon log:\n%s", logs)
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("credential leaked into daemon log:\n%s", logs)
	}
}
