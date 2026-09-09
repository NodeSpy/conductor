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
	t.Run("unpinned refused without opt-in", func(t *testing.T) {
		_, err := verify(Spec{Name: "p", BinPath: bin})
		if err == nil || !strings.Contains(err.Error(), "no sha256 pin") {
			t.Fatalf("want unpinned refusal, got %v", err)
		}
	})
	t.Run("unpinned allowed with opt-in", func(t *testing.T) {
		if _, err := verify(Spec{Name: "p", BinPath: bin, AllowUnverified: true}); err != nil {
			t.Fatalf("opt-in should pass: %v", err)
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
	t.Run("relative path refused", func(t *testing.T) {
		_, err := verify(Spec{Name: "p", BinPath: "rel/path", Sha256: sha(data)})
		if err == nil || !strings.Contains(err.Error(), "not absolute") {
			t.Fatalf("want abs-path refusal, got %v", err)
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
	return Spec{Name: "jira", Kind: KindConnector, Provides: "jira", BinPath: "/bin/true", AllowUnverified: true}
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
