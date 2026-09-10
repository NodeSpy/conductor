package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeBin(t *testing.T, dir, name string, data []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	data := []byte("#!/bin/true\n")
	bin := writeBin(t, dir, "plug", data, 0o755)

	t.Run("match", func(t *testing.T) {
		d, err := verify(Spec{Name: "p", BinPath: bin, Sha256: sha(data)})
		if err != nil || d != sha(data) {
			t.Fatalf("want match, got d=%s err=%v", d, err)
		}
	})
	t.Run("mismatch refuses", func(t *testing.T) {
		_, err := verify(Spec{Name: "p", BinPath: bin, Sha256: strings.Repeat("a", 64)})
		if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("want mismatch error, got %v", err)
		}
	})
	t.Run("a fetched plugin with no recorded sha is refused", func(t *testing.T) {
		_, err := verify(Spec{Name: "p", BinPath: bin})
		if err == nil || !strings.Contains(err.Error(), "no verified sha recorded") {
			t.Fatalf("want no-recorded-sha refusal, got %v", err)
		}
	})
	t.Run("a local development binary needs no sha", func(t *testing.T) {
		if _, err := verify(Spec{Name: "p", BinPath: bin, Local: true}); err != nil {
			t.Fatalf("a local build should verify on permissions alone: %v", err)
		}
	})
	t.Run("world-writable refused", func(t *testing.T) {
		ww := writeBin(t, dir, "ww", data, 0o755)
		if err := os.Chmod(ww, 0o757); err != nil { // chmod bypasses umask
			t.Fatal(err)
		}
		_, err := verify(Spec{Name: "p", BinPath: ww, Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "world-writable") {
			t.Fatalf("want world-writable refusal, got %v", err)
		}
	})
	t.Run("group-writable refused", func(t *testing.T) {
		gw := writeBin(t, dir, "gw", data, 0o755)
		if err := os.Chmod(gw, 0o770); err != nil { // group-writable
			t.Fatal(err)
		}
		_, err := verify(Spec{Name: "p", BinPath: gw, Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("want group-writable refusal, got %v", err)
		}
	})
	t.Run("symlink resolves to real target", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(bin, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := verify(Spec{Name: "p", BinPath: link, Sha256: sha(data)}); err != nil {
			t.Fatalf("symlink to a safe target should verify: %v", err)
		}
	})
	t.Run("relative path refused", func(t *testing.T) {
		_, err := verify(Spec{Name: "p", BinPath: "rel/path", Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "not absolute") {
			t.Fatalf("want abs-path refusal, got %v", err)
		}
	})
	t.Run("world-writable non-sticky parent refused (TOCTOU)", func(t *testing.T) {
		// A world-writable parent dir lets an attacker swap the binary between
		// verify and exec. This exercises checkParentPerms directly — the
		// binary's own mode is safe (0o755), so the refusal must come from the
		// ancestor walk.
		sub := filepath.Join(dir, "wwdir")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sub, 0o777); err != nil { // world-writable, NOT sticky
			t.Fatal(err)
		}
		bin2 := writeBin(t, sub, "plug", data, 0o755)
		_, err := verify(Spec{Name: "p", BinPath: bin2, Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "world-writable") {
			t.Fatalf("want parent-perm refusal, got %v", err)
		}
	})
	t.Run("group-writable non-sticky parent refused", func(t *testing.T) {
		// A group-writable ancestor lets a same-group attacker swap the binary
		// even when the file's own mode is safe (M1).
		gwdir := filepath.Join(dir, "gwdir")
		if err := os.Mkdir(gwdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(gwdir, 0o775); err != nil { // group-writable, not sticky
			t.Fatal(err)
		}
		gbin := writeBin(t, gwdir, "plug", data, 0o755)
		_, err := verify(Spec{Name: "p", BinPath: gbin, Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("want group-writable parent refusal, got %v", err)
		}
	})
	t.Run("sticky world-writable parent accepted", func(t *testing.T) {
		// /tmp is world-writable but sticky, which prevents cross-user swaps —
		// checkParentPerms must NOT refuse it (else every plugin under /tmp
		// breaks). t.TempDir() lives under the OS temp root, whose sticky
		// ancestor(s) the walk crosses; a plain verify of the base binary
		// confirms the sticky path is allowed.
		if _, err := verify(Spec{Name: "p", BinPath: bin, Sha256: sha(data)}); err != nil {
			t.Fatalf("sticky-ancestor path should be accepted: %v", err)
		}
	})
}

func TestBoundedReader(t *testing.T) {
	// one small line then a huge line
	huge := strings.Repeat("x", 100)
	r := newBoundedReader(strings.NewReader("ok\n"+huge), 10)
	buf := make([]byte, 8)
	var got []byte
	var err error
	for {
		n, e := r.Read(buf)
		got = append(got, buf[:n]...)
		if e != nil {
			err = e
			break
		}
	}
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("want size-cap error, got %v", err)
	}
}

// --- fake transport for supervision/round-trip tests ---

type fakeConn struct {
	mu       sync.Mutex
	describe *Decl
	invoke   func(InvokeRequest) (map[string]any, error)
	hang     bool
	done     chan struct{}
	calls    int
}

func newFakeConn() *fakeConn { return &fakeConn{done: make(chan struct{})} }

func (f *fakeConn) Call(ctx context.Context, method string, params, result any) error {
	f.mu.Lock()
	f.calls++
	hang := f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	switch method {
	case MethodDescribe:
		*result.(*Decl) = *f.describe
		return nil
	case MethodInvoke:
		req := params.(InvokeRequest)
		out, err := f.invoke(req)
		if err != nil {
			return err
		}
		*result.(*InvokeResult) = InvokeResult{Outputs: out}
		return nil
	}
	return errors.New("unknown method")
}
func (f *fakeConn) Close() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	return nil
}
func (f *fakeConn) Done() <-chan struct{} { return f.done }

func fakeDial(conn *fakeConn) func(context.Context, Spec, Deps) (transport, func(), error) {
	return func(context.Context, Spec, Deps) (transport, func(), error) {
		return conn, func() { conn.Close() }, nil
	}
}

func connectorSpec() Spec {
	return Spec{Name: "jira", Kind: KindConnector, Provides: "jira", BinPath: "/bin/true", Local: true}
}

func TestClientDescribeInvoke(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira", Verbs: []Verb{{Name: "search"}}}
	fc.invoke = func(r InvokeRequest) (map[string]any, error) {
		return map[string]any{"echo": r.Options["q"], "tok": r.Connection["token"]}, nil
	}
	// verify() runs against BinPath, so point at a real file with opt-in.
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	ctx := context.Background()

	decl, err := c.Describe(ctx)
	if err != nil || decl.Type != "jira" {
		t.Fatalf("describe: %v decl=%+v", err, decl)
	}
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "j", Verb: "search",
		Options: map[string]any{"q": "bug"}, Connection: map[string]any{"token": "sekret"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["echo"] != "bug" || out["tok"] != "sekret" {
		t.Fatalf("bad output: %+v", out)
	}
}

func TestClientRejectsForgedIdentity(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "evil"} // claims a different type
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	if _, err := c.Describe(context.Background()); err == nil || !strings.Contains(err.Error(), "identity forgery") {
		t.Fatalf("want identity-forgery refusal, got %v", err)
	}
}

func TestClientRejectsBadProtocol(t *testing.T) {
	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: 999, Type: "jira"}
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc)})
	if _, err := c.Describe(context.Background()); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("want protocol refusal, got %v", err)
	}
}

func TestClientCallTimeout(t *testing.T) {
	fc := newFakeConn()
	fc.hang = true
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: fakeDial(fc), CallTimeout: 50 * time.Millisecond})
	_, err := c.Invoke(context.Background(), InvokeRequest{Verb: "x"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want timeout, got %v", err)
	}
	// after a transport failure the conn is torn down
	c.mu.Lock()
	down := c.conn == nil
	c.mu.Unlock()
	if !down {
		t.Fatal("expected teardown after timeout")
	}
}

func TestClientRestartBackoff(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	attempts := 0
	c := NewClient(sp, Deps{dial: func(context.Context, Spec, Deps) (transport, func(), error) {
		attempts++
		return nil, nil, errors.New("boom") // always fails to launch
	}})
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = c.Start(ctx)
	}
	if attempts > restartBurst {
		t.Fatalf("crash-loop guard failed: %d launch attempts (cap %d)", attempts, restartBurst)
	}
	c.mu.Lock()
	parked := time.Now().Before(c.downUntil)
	c.mu.Unlock()
	if !parked {
		t.Fatal("expected plugin parked down after burst")
	}
}

// TestNoIsolationIsTheDefaultPath proves the app-extension posture: a plugin
// with NO isolation: block launches. OS confinement is opt-in hardening, not a
// precondition — the default confinement is the declared permission manifest.
func TestNoIsolationIsTheDefaultPath(t *testing.T) {
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	base := Spec{Name: "p", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Local: true}

	cmd, cleanup, sandboxed, err := buildCommand(base, SandboxDeps{})
	if err != nil || cmd == nil {
		t.Fatalf("a plugin with no isolation must launch: cmd=%v err=%v", cmd, err)
	}
	defer cleanup()
	if sandboxed {
		t.Fatal("no isolation block should report sandboxed=false")
	}
	// The env is still scrubbed — the daemon's credential-bearing environment
	// is never forwarded, isolation or not.
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "CONDUCTOR_SECRET") {
			t.Fatalf("daemon env leaked to the plugin: %q", kv)
		}
	}
}

// TestManifestConfinesCommands proves the declared-commands half of the
// permission manifest: PATH is replaced with a directory holding exactly the
// declared commands, so an undeclared tool is not resolvable by name.
func TestManifestConfinesCommands(t *testing.T) {
	dir := t.TempDir()
	bin := writeBin(t, dir, "b", []byte("x"), 0o755)
	s := Spec{
		Name: "p", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Local: true,
		Manifest: Manifest{Commands: []string{"sh"}},
	}
	cmd, cleanup, _, err := buildCommand(s, SandboxDeps{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var path string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if path == "" {
		t.Fatal("declared commands did not produce a confined PATH")
	}
	if _, err := os.Stat(filepath.Join(path, "sh")); err != nil {
		t.Fatalf("declared command not linked into the confined PATH: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "curl")); err == nil {
		t.Fatal("an undeclared command is present in the confined PATH")
	}
}

// A plugin that declares NOTHING gets no PATH rewrite: it declared no needs, so
// there is no allowlist to build, and inventing one would break plugins that
// predate the manifest.
func TestNoManifestLeavesPathAlone(t *testing.T) {
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	s := Spec{Name: "p", Kind: KindConnector, BinPath: bin, Local: true}
	cmd, cleanup, _, err := buildCommand(s, SandboxDeps{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	base := spawnBaseEnv()
	if len(cmd.Env) != len(base) {
		t.Fatalf("env changed for a plugin that declared nothing: %v vs %v", cmd.Env, base)
	}
}

// TestClientLifetimeCap proves a plugin crashing at a steady, sub-burst cadence
// is still parked permanently once it exceeds the lifetime restart cap (the
// trailing-window burst guard alone would let it respawn forever).
func TestClientLifetimeCap(t *testing.T) {
	sp := connectorSpec()
	sp.BinPath = writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	c := NewClient(sp, Deps{dial: func(context.Context, Spec, Deps) (transport, func(), error) {
		return nil, nil, errors.New("boom")
	}})
	// Simulate a long life of spread-out restarts that never trip the burst
	// window: jump the lifetime counter to the cap directly.
	c.mu.Lock()
	c.totalStart = restartLifetimeCap
	c.mu.Unlock()
	if err := c.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "down for good") {
		t.Fatalf("want lifetime-cap refusal, got %v", err)
	}
	// Stays down for good even after the burst window would have elapsed.
	c.mu.Lock()
	dfg := c.downForGood
	c.mu.Unlock()
	if !dfg {
		t.Fatal("expected downForGood after lifetime cap")
	}
}
