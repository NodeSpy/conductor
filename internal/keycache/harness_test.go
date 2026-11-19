package keycache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// big is content past the harness's learning threshold.
var big = strings.Repeat("x", 64<<10)

type harness struct {
	m     *Manager
	homes string
	logs  []string
	mu    sync.Mutex
	n     int
}

func newHarness(t *testing.T, overlay bool) *harness {
	t.Helper()
	// syncfs on a busy filesystem takes seconds; TestPublishSyncsBeforeCommit
	// covers it.
	syncFS = func(string) {}
	t.Cleanup(func() { syncFS = syncDir })
	h := &harness{homes: t.TempDir()}
	h.m = &Manager{
		Store: &Store{Dir: t.TempDir()}, Protected: []string{".claude"}, MaxSize: 1 << 30,
		MinSize: 32 << 10, MinRuns: 1, FinishWait: 2 * time.Second,
		Overlay: func() (bool, string) { return overlay, "test" },
		Logf: func(f string, a ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(f, a...))
			h.mu.Unlock()
		},
	}
	return h
}

func key(v string) Key { return Key{Template: "k-{{.x}}", Value: v} }

// launch restores for a fresh launch of a dispatch, in its own home (or
// the given one).
func (h *harness) launch(t *testing.T, dispatch string, k Key, from ...Key) (Request, Grant) {
	t.Helper()
	h.mu.Lock()
	h.n++
	n := h.n
	h.mu.Unlock()
	home := filepath.Join(h.homes, fmt.Sprintf("%s-%d", dispatch, n))
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Workspace: t.TempDir(), Home: home, Run: dispatch, Dispatch: dispatch, Key: k, RestoreFrom: from}
	return req, h.restore(t, req)
}

func (h *harness) restore(t *testing.T, req Request) Grant {
	t.Helper()
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if g.Lock == "" {
		t.Fatal("a keyed launch got no ledger entry")
	}
	return g
}

// runWrites runs a launch: it holds its lock, writes files (home-relative
// → content) where an overlaid path's writes land (its upper layer) or
// into the home, then ends.
func runWrites(t *testing.T, req Request, g Grant, files map[string]string) {
	t.Helper()
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(req.Home, rel)
		for _, mt := range g.Mounts {
			if r, err := filepath.Rel(mt.Target, p); err == nil && !strings.HasPrefix(r, "..") {
				p = filepath.Join(mt.Upper, r)
				break
			}
		}
		write(t, p, body)
	}
	EndLaunch(g.Lock)
	release()
}

func (h *harness) finish(t *testing.T, dispatch string, ok bool) *Published {
	t.Helper()
	p, err := h.m.Finish(context.Background(), dispatch, ok)
	if err != nil {
		t.Fatalf("finish %s: %v\n%s", dispatch, err, strings.Join(h.logs, "\n"))
	}
	return p
}

// lowerFile is a file as a launch's grant shows it: under an overlay's
// lower layer, or (copy restore) in the home.
func lowerFile(req Request, g Grant, rel string) string {
	p := filepath.Join(req.Home, rel)
	for _, mt := range g.Mounts {
		if r, err := filepath.Rel(mt.Target, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.Join(mt.Lower, r)
		}
	}
	return p
}
