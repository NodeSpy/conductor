package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// reloadableSpec is a connector spec pointing at a real (verifiable) file so
// ensureLocked's verify-before-execute passes.
func reloadableSpec(t *testing.T) Spec {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	return sp
}

// waitUntilReloading polls c.reloading under its own lock until it is true,
// deterministically replacing a fixed time.Sleep("let Reload observe
// reloading and enter its drain wait") — the flag flips almost instantly (a
// bool write under c.mu, nothing blocking before it), so this loop just
// waits for that specific, observable transition instead of guessing how
// long it takes, and fails loudly (rather than silently racing) if it never
// happens within the deadline.
func waitUntilReloading(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		reloading := c.reloading
		c.mu.Unlock()
		if reloading {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Reload never set c.reloading")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestClientReloadSwapsProcessInPlace: after Reload, the SAME *Client re-dials
// the new binary transparently — the next call lands on the second connection.
func TestClientReloadSwapsProcessInPlace(t *testing.T) {
	c1, c2 := newFakeConn(), newFakeConn()
	c1.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	c2.describe = c1.describe
	c1.invoke = func(InvokeRequest) (map[string]any, error) { return map[string]any{"conn": "c1"}, nil }
	c2.invoke = func(InvokeRequest) (map[string]any, error) { return map[string]any{"conn": "c2"}, nil }
	var n int
	var mu sync.Mutex
	dial := func(context.Context, Spec, Deps) (transport, func(), error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return c1, func() { c1.Close() }, nil
		}
		return c2, func() { c2.Close() }, nil
	}
	c := NewClient(reloadableSpec(t), Deps{dial: dial})
	ctx := context.Background()

	out, err := c.Invoke(ctx, InvokeRequest{Verb: "x"})
	if err != nil || out["conn"] != "c1" {
		t.Fatalf("first call should hit c1: %v %v", out, err)
	}
	if err := c.Reload(reloadableSpec(t)); err != nil {
		t.Fatalf("reload: %v", err)
	}
	out, err = c.Invoke(ctx, InvokeRequest{Verb: "x"})
	if err != nil || out["conn"] != "c2" {
		t.Fatalf("post-reload call should hit c2 (new binary): %v %v", out, err)
	}
	// Crash-loop bookkeeping reset — a deliberate reload is not a crash.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.totalStart != 1 || len(c.starts) != 1 {
		t.Fatalf("reload must reset restart bookkeeping (totalStart=%d starts=%d)", c.totalStart, len(c.starts))
	}
}

// TestClientReloadDrainsInFlight: Reload waits for a bounded call to finish
// before swapping, and succeeds once it returns.
func TestClientReloadDrainsInFlight(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true // Call blocks on ctx.Done()
	fc.entered = make(chan struct{})
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(fc)})

	callCtx, cancelCall := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = c.Invoke(callCtx, InvokeRequest{Verb: "x"}) // hangs until callCtx cancelled
	}()
	<-started
	<-fc.entered // the call is now genuinely in flight (inflight++, parked in Call)

	reloadErr := make(chan error, 1)
	go func() { reloadErr <- c.Reload(reloadableSpec(t)) }()
	select {
	case <-reloadErr:
		t.Fatal("Reload returned before the in-flight call drained")
	case <-time.After(50 * time.Millisecond):
	}
	cancelCall() // in-flight call returns → inflight drains → Reload proceeds
	if err := <-reloadErr; err != nil {
		t.Fatalf("reload after drain: %v", err)
	}
}

// TestClientReloadAfterCloseReturnsErrorWithoutMutating is the finding-4
// corollary (the `if c.closed` guard Reload rechecks AFTER its drain wait,
// client.go ~1054-1058): a Close() that lands WHILE Reload is draining a
// stuck in-flight call must make Reload refuse the swap once it resumes —
// even if the drain itself then succeeds — rather than mutate the client's
// spec on top of a torn-down process. Without this second check (only the
// first, pre-drain `if c.closed` survived), a Reload that started just
// before Close landed would drain successfully and go on to overwrite
// BinPath/Sha256/Resolved and reset the restart bookkeeping of a Client
// Close() already tore down.
func TestClientReloadAfterCloseReturnsErrorWithoutMutating(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true // every call blocks on ctx.Done()
	fc.entered = make(chan struct{})
	origSpec := reloadableSpec(t)
	c := NewClient(origSpec, Deps{dial: fakeDial(fc)})

	callCtx, cancelCall := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = c.Invoke(callCtx, InvokeRequest{Instance: "x", Verb: "go"}) // hangs until cancelCall
	}()
	<-started
	<-fc.entered // the call is now genuinely in flight (inflight++, served)

	reloadErr := make(chan error, 1)
	go func() { reloadErr <- c.Reload(reloadableSpec(t)) }() // new BinPath, would overwrite if applied
	waitUntilReloading(t, c)                                 // Reload has observed reloading and entered its drain wait

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Let the hung call finish so Reload's select resolves via <-done
	// (drained=true) rather than reloadDrainTimeout — proving the closed
	// check wins even when the drain itself succeeded.
	cancelCall()

	select {
	case err := <-reloadErr:
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("Reload racing a concurrent Close must return a \"closed\" error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload never returned after Close + cancelCall")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spec.BinPath != origSpec.BinPath || c.spec.Sha256 != origSpec.Sha256 {
		t.Fatalf("Reload must not mutate the spec after losing the race to Close: got BinPath=%q Sha256=%q, want the original %q/%q",
			c.spec.BinPath, c.spec.Sha256, origSpec.BinPath, origSpec.Sha256)
	}
	if c.reloading {
		t.Fatal("reloading must be cleared even when Reload bails out on closed")
	}
}

// TestClientReloadTimesOutBusy: a call that never drains makes Reload give up
// with ErrReloadBusy (caller restarts) rather than killing it mid-flight.
func TestClientReloadTimesOutBusy(t *testing.T) {
	old := reloadDrainTimeout
	reloadDrainTimeout = 40 * time.Millisecond
	defer func() { reloadDrainTimeout = old }()

	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true
	fc.entered = make(chan struct{})
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(fc)})
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	go func() { _, _ = c.Invoke(callCtx, InvokeRequest{Verb: "x"}) }()
	<-fc.entered // the call is now genuinely in flight

	if err := c.Reload(reloadableSpec(t)); !errors.Is(err, ErrReloadBusy) {
		t.Fatalf("want ErrReloadBusy on undrainable call, got %v", err)
	}
}

// TestClientReloadRefusesLiveSource: a client with an active source stream can't
// be swapped in place (StartSource only ends on teardown).
func TestClientReloadRefusesLiveSource(t *testing.T) {
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(newFakeConn())})
	c.mu.Lock()
	c.onEvent = func(json.RawMessage) {}
	c.mu.Unlock()
	if err := c.Reload(reloadableSpec(t)); !errors.Is(err, ErrReloadUnsupported) {
		t.Fatalf("a live source must refuse in-place reload, got %v", err)
	}
}

// SameReloadSurface gates in-place reload: identical (or verb-reordered) surface
// reloads; a changed kind/ABI/verb-set or widened permission forces a restart.
func TestSameReloadSurface(t *testing.T) {
	base := &Decl{Kind: KindConnector, ABI: 1, Type: "jira",
		Verbs:        []Verb{{Name: "search"}, {Name: "create"}},
		Capabilities: Capabilities{Egress: []string{"api.jira.com:443"}}}
	cp := func() Decl { d := *base; return d }

	same := cp()
	if !SameReloadSurface(base, &same) {
		t.Fatal("identical decl must be reloadable")
	}
	ro := cp()
	ro.Verbs = []Verb{{Name: "create"}, {Name: "search"}}
	if !SameReloadSurface(base, &ro) {
		t.Fatal("verb reorder must still be reloadable")
	}
	add := cp()
	add.Verbs = []Verb{{Name: "search"}, {Name: "create"}, {Name: "delete"}}
	if SameReloadSurface(base, &add) {
		t.Fatal("an added verb must NOT be reloadable")
	}
	abi := cp()
	abi.ABI = 2
	if SameReloadSurface(base, &abi) {
		t.Fatal("an ABI bump must NOT be reloadable")
	}
	eg := cp()
	eg.Capabilities = Capabilities{Egress: []string{"api.jira.com:443", "evil.com:443"}}
	if SameReloadSurface(base, &eg) {
		t.Fatal("a widened egress must NOT be reloadable")
	}
	// single_process is part of the recorded manifest (manifestFromDecl)
	// exactly like egress/commands/fs/env: a build that newly declares it
	// (or drops it) changes the Manager's SHAPE for this key (one process
	// vs one per instance) — never something a live in-place swap may do
	// silently — so it must force a restart just like a widened egress.
	sp := cp()
	sp.Capabilities = Capabilities{SingleProcess: true}
	if SameReloadSurface(base, &sp) {
		t.Fatal("a newly-declared single_process must NOT be reloadable")
	}
	spBoth := cp()
	spBoth.Capabilities = Capabilities{SingleProcess: true}
	baseSP := cp()
	baseSP.Capabilities = Capabilities{SingleProcess: true}
	if !SameReloadSurface(&baseSP, &spBoth) {
		t.Fatal("single_process unchanged on both sides must still be reloadable")
	}
	if SameReloadSurface(&baseSP, base) {
		t.Fatal("DROPPING single_process must NOT be reloadable either")
	}
	if SameReloadSurface(nil, base) || SameReloadSurface(base, nil) {
		t.Fatal("nil decl must not be reloadable")
	}
}

// TestManagerReload swaps a client by key and updates the recorded spec's
// binary-identity fields, leaving the invariant fields alone.
func TestManagerReload(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	sp := reloadableSpec(t)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	m := &Manager{
		clients:   map[string]*Client{"connectors/jira": c},
		specs:     map[string]Spec{"connectors/jira": sp},
		decls:     map[string]*Decl{},
		order:     []string{"connectors/jira"},
		reloading: map[string]bool{},
	}
	m.instCond = sync.NewCond(&m.instMu)
	newSpec := reloadableSpec(t)
	newSpec.Sha256 = "deadbeef"
	newSpec.Resolved = "v2"
	if err := m.Reload("connectors/jira", newSpec); err != nil {
		t.Fatalf("manager reload: %v", err)
	}
	got, _ := m.Spec("connectors/jira")
	if got.BinPath != newSpec.BinPath || got.Sha256 != "deadbeef" || got.Resolved != "v2" {
		t.Fatalf("spec binary-identity not updated: %+v", got)
	}
	if got.Name != sp.Name || got.Kind != sp.Kind {
		t.Fatalf("invariant spec fields must be preserved: %+v", got)
	}
	if err := m.Reload("nope", newSpec); err == nil {
		t.Fatal("reload of an unknown key must error")
	}
}

// TestClientReloadRace exercises concurrent bounded calls against repeated
// Reloads — the drain gate + spec swap must be race-clean under `-race`.
func TestClientReloadRace(t *testing.T) {
	mk := func() *fakeConn {
		c := newFakeConn()
		c.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
		c.invoke = func(InvokeRequest) (map[string]any, error) { return map[string]any{}, nil }
		return c
	}
	dial := func(context.Context, Spec, Deps) (transport, func(), error) {
		c := mk()
		return c, func() { c.Close() }, nil
	}
	c := NewClient(reloadableSpec(t), Deps{dial: dial})
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = c.Invoke(ctx, InvokeRequest{Verb: "x"})
				}
			}
		}()
	}
	for i := 0; i < 10; i++ {
		_ = c.Reload(reloadableSpec(t))
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
}
