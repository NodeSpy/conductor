package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// buildSlowPlugin compiles the acme-slow test plugin (test/plugins/acme-slow).
func buildSlowPlugin(t *testing.T) Spec {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-slow")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-slow")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-slow: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	// These tests are about call timeouts, restarts and liveness, not the
	// jail — trust: full skips needing a built conductor binary wired as
	// the jail's entry.
	return Spec{Name: "slow", Kind: KindConnector, Provides: "acme-slow", BinPath: bin, Sha256: hex.EncodeToString(sum[:]), TrustFull: true}
}

// streamTicks starts the plugin's source and counts its events.
func streamTicks(t *testing.T, c *Client) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.StartSource(ctx, StartSourceRequest{Instance: "s1"}, func(json.RawMessage) { n.Add(1) }); err != nil {
		t.Fatalf("StartSource: %v", err)
	}
	return &n
}

// waitTicks waits until the stream has delivered more than `after` events.
func waitTicks(t *testing.T, n *atomic.Int64, after int64, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() <= after+2 {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the source stream stopped (%d events, was %d)", what, n.Load(), after)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSlowCallDoesNotKillThePlugin: a verb that outlives its deadline returns
// the deadline error to its caller and nothing else. The process, its source
// stream and later calls are untouched, and the verb itself is cancelled.
func TestSlowCallDoesNotKillThePlugin(t *testing.T) {
	spec := buildSlowPlugin(t)
	var (
		logs  strings.Builder
		logMu sync.Mutex
	)
	logf := func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logs, f+"\n", a...)
	}
	c := NewClient(spec, Deps{CallTimeout: 300 * time.Millisecond, Log: logf})
	defer c.Close()
	ticks := streamTicks(t, c)
	pid := c.PID()
	if pid == 0 {
		t.Fatal("no pid after StartSource")
	}

	mark := filepath.Join(t.TempDir(), "mark")
	before := ticks.Load()
	start := time.Now()
	_, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "sleep", Options: map[string]any{"ms": 3000, "mark": mark}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the call's deadline error, got %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the caller waited %s; it should get its error at the deadline", el)
	}
	if !strings.Contains(err.Error(), "did not answer within 300ms") {
		t.Fatalf("the error should say which bound passed: %v", err)
	}
	// plugin.cancel reached the verb's ctx.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, _ := os.ReadFile(mark); string(b) == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the verb was never cancelled: plugin.cancel did not reach InvokeContext's ctx")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := c.PID(); got != pid {
		t.Fatalf("a slow call must not restart the plugin: pid was %d, now %d", pid, got)
	}
	waitTicks(t, ticks, before, "after a timed-out call")

	out, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "echo", Options: map[string]any{"message": "hi"}})
	if err != nil || out["message"] != "hi" {
		t.Fatalf("the next call must succeed on the same process: out=%v err=%v", out, err)
	}
	if got := int(out["pid"].(float64)); got != pid {
		t.Fatalf("the next call ran on pid %d, want %d", got, pid)
	}
	// The liveness check after the timeout got its answer: nothing restarted.
	time.Sleep(200 * time.Millisecond)
	if got := c.PID(); got != pid {
		t.Fatalf("the liveness check restarted a healthy plugin: pid was %d, now %d", pid, got)
	}
	logMu.Lock()
	l := logs.String()
	logMu.Unlock()
	if strings.Contains(l, "restarting") {
		t.Fatalf("unexpected restart log:\n%s", l)
	}
}

// TestLateAnswerIsDropped: a verb that ignores cancellation answers after the
// caller gave up. The answer is dropped and the connection stays usable.
func TestLateAnswerIsDropped(t *testing.T) {
	spec := buildSlowPlugin(t)
	c := NewClient(spec, Deps{CallTimeout: 200 * time.Millisecond})
	defer c.Close()
	// ignore_cancel makes the verb finish its 250ms sleep and answer after
	// the 200ms deadline, so a response arrives for an id nobody waits on.
	// Two calls: a third missed deadline in a row restarts the plugin
	// (TestMissedDeadlinesRestartAndPark).
	for i := 0; i < maxMissedDeadlines-1; i++ {
		_, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "sleep", Options: map[string]any{"ms": 250, "ignore_cancel": true}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline, got %v", err)
		}
	}
	pid := c.PID()
	time.Sleep(300 * time.Millisecond)
	out, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "echo", Options: map[string]any{"message": "after"}})
	if c.PID() != pid {
		t.Fatalf("late answers restarted the plugin: pid %d -> %d", pid, c.PID())
	}
	if err != nil || out["message"] != "after" {
		t.Fatalf("a late answer must not be taken for a later call's: out=%v err=%v", out, err)
	}
}

// TestDeclaredCallTimeoutIsHonoured: verb "slow" declares call_timeout 5s, so
// a 1s call succeeds under a 300ms default, while "sleep" (no declaration)
// keeps the default.
func TestDeclaredCallTimeoutIsHonoured(t *testing.T) {
	spec := buildSlowPlugin(t)
	c := NewClient(spec, Deps{CallTimeout: 300 * time.Millisecond})
	defer c.Close()
	if _, err := c.Describe(context.Background()); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got := c.verbTimeout("s1", "slow"); got != 5*time.Second {
		t.Fatalf("declared bound for slow = %s, want 5s", got)
	}
	out, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "slow", Options: map[string]any{"ms": 1000}})
	if err != nil || out["slept"] != float64(1000) {
		t.Fatalf("a verb with a declared call_timeout must get it: out=%v err=%v", out, err)
	}
	if _, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "sleep", Options: map[string]any{"ms": 1000}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an undeclared verb keeps the default bound: %v", err)
	}
}

// TestDeclaredCallTimeoutIsCapped: a declaration above MaxCallTimeout is
// capped, and an instance's own declaration wins over the type-level one.
func TestDeclaredCallTimeoutIsCapped(t *testing.T) {
	c := NewClient(connectorSpec(), Deps{CallTimeout: time.Second})
	c.recordCallTimeouts("", Decl{Verbs: []Verb{
		{Name: "huge", Semantics: &sdk.VerbSemantics{CallTimeout: "24h"}},
		{Name: "both", Semantics: &sdk.VerbSemantics{CallTimeout: "1m"}},
	}})
	c.recordCallTimeouts("i1", Decl{Verbs: []Verb{{Name: "both", Semantics: &sdk.VerbSemantics{CallTimeout: "2m"}}}})
	if got := c.verbTimeout("x", "huge"); got != MaxCallTimeout {
		t.Fatalf("huge = %s, want the cap %s", got, MaxCallTimeout)
	}
	if got := c.verbTimeout("i1", "both"); got != 2*time.Minute {
		t.Fatalf("instance declaration = %s, want 2m", got)
	}
	if got := c.verbTimeout("i2", "both"); got != time.Minute {
		t.Fatalf("type-level declaration = %s, want 1m", got)
	}
	if got := c.verbTimeout("i1", "other"); got != time.Second {
		t.Fatalf("undeclared verb = %s, want the default 1s", got)
	}
}

// TestWedgedPluginIsRestarted: a process that stops answering everything
// (SIGSTOP) fails the liveness check after a timed-out call and is replaced;
// its source stream resumes on the new process.
func TestWedgedPluginIsRestarted(t *testing.T) {
	old := livenessTimeout
	livenessTimeout = 300 * time.Millisecond
	t.Cleanup(func() { livenessTimeout = old })
	oldDelay := streamRestartDelay
	streamRestartDelay = 50 * time.Millisecond
	t.Cleanup(func() { streamRestartDelay = oldDelay })

	spec := buildSlowPlugin(t)
	c := NewClient(spec, Deps{CallTimeout: 300 * time.Millisecond})
	defer c.Close()
	var n atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_ = c.StreamSource(ctx, StartSourceRequest{Instance: "s1"}, func(json.RawMessage) { n.Add(1) })
	}()
	// Stop the stream before the cleanups restore the package vars it reads.
	defer func() { cancel(); <-streamDone }()
	waitTicks(t, &n, 0, "before the wedge")
	pid := c.PID()

	if _, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "wedge"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call to a wedged process must time out, got %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p := c.PID(); p != 0 && p != pid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wedged process (pid %d) was never restarted; pid now %d", pid, c.PID())
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitTicks(t, &n, n.Load(), "after the restart")
	out, err := c.Invoke(context.Background(), InvokeRequest{Instance: "s1", Verb: "echo", Options: map[string]any{"message": "back"}})
	if err != nil || out["message"] != "back" {
		t.Fatalf("calls must work on the restarted process: out=%v err=%v", out, err)
	}
}

// TestMissedDeadlinesRestartAndPark: a plugin that answers the liveness ping
// but never finishes a call (it ignores plugin.cancel) is restarted after
// maxMissedDeadlines calls in a row miss their deadline, and those restarts
// count toward the crash-loop guards: it ends up parked like a plugin that
// crashes. An answered call in between resets the count.
func TestMissedDeadlinesRestartAndPark(t *testing.T) {
	spec := buildSlowPlugin(t)
	var mu sync.Mutex
	var logs []string
	c := NewClient(spec, Deps{CallTimeout: 200 * time.Millisecond, Log: func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}})
	defer c.Close()
	ctx := context.Background()
	stuck := func() error {
		_, err := c.Invoke(ctx, InvokeRequest{Instance: "s1", Verb: "sleep", Options: map[string]any{"ms": 800, "ignore_cancel": true}})
		return err
	}
	echo := func() error {
		_, err := c.Invoke(ctx, InvokeRequest{Instance: "s1", Verb: "echo", Options: map[string]any{"message": "hi"}})
		return err
	}
	if err := echo(); err != nil {
		t.Fatal(err)
	}
	pid := c.PID()

	// An answered call between misses resets the count: no restart.
	for i := 0; i < maxMissedDeadlines-1; i++ {
		if err := stuck(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want the deadline, got %v", err)
		}
	}
	if err := echo(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxMissedDeadlines-1; i++ {
		_ = stuck()
	}
	if c.PID() != pid {
		t.Fatalf("restarted before %d misses in a row: pid %d -> %d", maxMissedDeadlines, pid, c.PID())
	}

	// The next miss is the third in a row: the process is torn down.
	if err := stuck(); err == nil || !strings.Contains(err.Error(), "the plugin was restarted") {
		t.Fatalf("the third miss in a row does not say it restarted the plugin: %v", err)
	}
	// The next call starts a new process, whose count starts clean: one miss
	// on it does not restart it.
	_ = stuck()
	fresh := c.PID()
	if fresh == 0 || fresh == pid {
		t.Fatalf("after the restart and one miss: pid %d (was %d)", fresh, pid)
	}
	if err := echo(); err != nil {
		t.Fatalf("the restarted plugin does not answer: %v", err)
	}
	if c.PID() != fresh {
		t.Fatalf("pid changed again: %d -> %d", fresh, c.PID())
	}

	// Keep doing it: the restarts hit the crash-loop guard and it is parked.
	var parked error
	for i := 0; i < 4*maxMissedDeadlines*restartBurst && parked == nil; i++ {
		if err := stuck(); err != nil && strings.Contains(err.Error(), "is down") {
			parked = err
		}
	}
	if parked == nil {
		t.Fatal("a plugin that never finishes a call was never parked")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"calls in a row did not answer within their deadline", "parking down"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no log line saying %q:\n%s", want, joined)
		}
	}
}
