package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

func hostLogClient(t *testing.T) (*Client, func() []string) {
	t.Helper()
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	var mu sync.Mutex
	var lines []string
	c := NewClient(sp, Deps{Log: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}})
	c.markActive("i")
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func hostLog(t *testing.T, c *Client, msg string) sdk.HostLogResult {
	t.Helper()
	p, _ := json.Marshal(sdk.HostLogRequest{Instance: "i", Message: msg})
	res, rpcErr := c.handleRequest(context.Background(), sdk.MethodHostLog, p)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	return res.(sdk.HostLogResult)
}

// A plugin's log line stays one inert line: no newline to start a forged
// daemon line, no terminal escapes, and a bounded size.
func TestHostLogEscapesControlCharactersAndCapsSize(t *testing.T) {
	c, lines := hostLogClient(t)
	hostLog(t, c, "ok\nconductor: forged line\x1b[2K\r")
	hostLog(t, c, strings.Repeat("a", 10*hostLogMaxBytes))
	got := lines()
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(got), got)
	}
	if strings.ContainsAny(got[0], "\n\r\x1b") || !strings.Contains(got[0], `ok\x0aconductor: forged line\x1b[2K\x0d`) {
		t.Fatalf("control characters not escaped: %q", got[0])
	}
	if len(got[1]) > hostLogMaxBytes+200 || !strings.HasSuffix(got[1], "(truncated)") {
		t.Fatalf("oversized line not capped: %d bytes", len(got[1]))
	}
}

// Each instance gets a fixed number of lines per window; the rest are
// dropped and counted, and the count is reported when the window turns.
func TestHostLogIsRateLimitedPerInstance(t *testing.T) {
	old := hostLogWindow
	t.Cleanup(func() { hostLogWindow = old })
	c, lines := hostLogClient(t)
	for i := 0; i < hostLogBurst+40; i++ {
		hostLog(t, c, "spam")
	}
	if n := len(lines()); n != hostLogBurst {
		t.Fatalf("want %d admitted lines, got %d", hostLogBurst, n)
	}
	if res := hostLog(t, c, "more"); res.OK || res.Error == "" {
		t.Fatalf("a line over budget must be refused, got %+v", res)
	}
	hostLogWindow = 0 // the window turns over
	if res := hostLog(t, c, "after"); !res.OK {
		t.Fatalf("a new window admits again, got %+v", res)
	}
	got := lines()
	if !strings.Contains(got[len(got)-2], "41 log line(s) dropped") {
		t.Fatalf("dropped count not reported: %q", got[len(got)-2])
	}
}
