package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestManagerReloadRaceNewInstanceGetsNewBuild is the deterministic
// reproduction of finding 2 (Manager.Reload race): Reload snapshots its live
// clients, drains/reloads them, and only THEN updates specs[key]. Before the
// fix, a brand-new instance's FIRST InstanceClient call landing in that
// window built its per-instance Client from the PRE-reload spec (the OLD
// binary) and cached it — nothing ever revisits an already-created
// per-instance client, so that instance would silently keep running the old
// binary forever, even though Reload reports success and every other
// instance is already on the new build.
//
// This test drives the exact scenario: Reload is blocked draining instance
// "a" (one in-flight bounded call, held open until the test releases it)
// while instance "b" is asked for — its very first InstanceClient call —
// during that window. The fix (instMu + `reloading` + instCond) must make
// "b"'s construction wait for Reload to finish and re-read the spec, so "b"
// ends up on the NEW build deterministically, never the old one.
func TestManagerReloadRaceNewInstanceGetsNewBuild(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	var blockMu sync.Mutex
	block := false // only the deliberately-in-flight call below should block

	oldConn := newFakeConn()
	oldConn.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "widget"}
	oldConn.invoke = func(req InvokeRequest) (map[string]any, error) {
		blockMu.Lock()
		shouldBlock := block && req.Instance == "a"
		blockMu.Unlock()
		if shouldBlock {
			close(entered)
			<-release
		}
		return map[string]any{}, nil
	}
	newConn := newFakeConn()
	newConn.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "widget"}
	newConn.invoke = func(InvokeRequest) (map[string]any, error) { return map[string]any{}, nil }

	oldData, newData := []byte("old-binary"), []byte("new-binary")
	oldSha, newSha := shaOf(oldData), shaOf(newData)

	// The SAME dial function serves every client this Manager ever creates
	// (production invariant: Deps is shared — see Manager.deps's doc); it
	// picks the connection by the Spec it is handed, so instance "a"'s
	// ALREADY-LIVE client, instance "a"'s post-reload re-dial, and instance
	// "b"'s FIRST dial are all exercised through one real code path rather
	// than test-only indirection.
	dial := func(_ context.Context, spec Spec, _ Deps) (transport, func(), error) {
		if spec.Sha256 == newSha {
			return newConn, func() { newConn.Close() }, nil
		}
		return oldConn, func() { oldConn.Close() }, nil
	}

	base := connectorSpec()
	base.Name, base.Provides = "widget", "widget"
	// Both configured instances isolate: this test is specifically about the
	// per-instance client race (Manager.Reload vs. a brand-new instance's
	// first InstanceClient call); the default (shared) path has no such race
	// to construct a NEW per-instance client in the first place.
	base.Instances = map[string]config.ConnectorGrant{
		"a": {Isolate: true},
		"b": {Isolate: true},
	}
	oldSpec := base
	oldSpec.Sha256 = oldSha
	oldSpec.BinPath = writeBin(t, t.TempDir(), "widget", oldData, 0o755)
	newSpec := base
	newSpec.Sha256 = newSha
	newSpec.BinPath = writeBin(t, t.TempDir(), "widget2", newData, 0o755)

	const key = "connectors/widget"
	m := &Manager{
		clients:     map[string]*Client{},
		specs:       map[string]Spec{key: oldSpec},
		decls:       map[string]*Decl{},
		order:       []string{key},
		instClients: map[string]map[string]*Client{},
		reloading:   map[string]bool{},
		deps:        Deps{dial: dial},
	}
	m.instCond = sync.NewCond(&m.instMu)

	// Instance "a" is already live, on the OLD build.
	a, err := m.InstanceClient(key, "a")
	if err != nil {
		t.Fatalf("instance a: %v", err)
	}
	if _, err := a.Invoke(context.Background(), InvokeRequest{Instance: "a", Verb: "warm"}); err != nil {
		t.Fatalf("instance a warm-up invoke: %v", err)
	}

	// Put ONE bounded call in flight on "a" — this is what Reload's drain
	// must wait on.
	blockMu.Lock()
	block = true
	blockMu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = a.Invoke(context.Background(), InvokeRequest{Instance: "a", Verb: "x"})
	}()
	<-entered // the call is now parked inside oldConn.invoke, holding the drain

	// Reload: must block in a's drain (the in-flight call above) before it
	// ever reaches the specs[key] update.
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- m.Reload(key, newSpec) }()

	// Confirm Reload has actually marked `key` reloading before asking for
	// "b" — otherwise this test would not be exercising the race window at
	// all. The flag flips almost instantly (a map write under instMu, no
	// blocking before it); this loop just waits for that, bounded so a
	// regression hangs the test loudly instead of silently passing.
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.instMu.Lock()
		marked := m.reloading[key]
		m.instMu.Unlock()
		if marked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Reload never marked the key as reloading")
		}
		time.Sleep(time.Millisecond)
	}

	// Instance "b" asked for, for the FIRST time, DURING the reload window.
	type result struct {
		c   *Client
		err error
	}
	bCh := make(chan result, 1)
	go func() {
		c, err := m.InstanceClient(key, "b")
		bCh <- result{c, err}
	}()

	// "b" must not have been created yet — InstanceClient must be blocked on
	// instCond, not racing ahead with the stale spec.
	select {
	case r := <-bCh:
		t.Fatalf("InstanceClient(b) returned BEFORE Reload finished (not blocked on the reload): %+v", r)
	case <-time.After(50 * time.Millisecond):
	}

	// Release instance a's in-flight call — Reload's drain completes, it
	// swaps a's process and updates specs[key].
	close(release)
	wg.Wait()
	if err := <-reloadDone; err != nil {
		t.Fatalf("reload: %v", err)
	}

	r := <-bCh
	if r.err != nil {
		t.Fatalf("instance b: %v", r.err)
	}
	if r.c.spec.Sha256 != newSha {
		t.Fatalf("instance \"b\", first created DURING the reload window, must be built on the NEW spec — got sha %q, want %q", r.c.spec.Sha256, newSha)
	}

	// And "a" itself, naturally, ended up on the new build too.
	if a.spec.Sha256 != newSha {
		t.Fatalf("instance a must also be on the new build after reload: got sha %q", a.spec.Sha256)
	}
}
