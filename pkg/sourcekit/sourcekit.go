// Package sourcekit is the PUBLIC connector-kit for building conductor SOURCE
// plugins (#59): the reusable, daemon-agnostic transport bits a webhook source
// needs — HMAC signature verification, a bounded HTTP webhook listener, and a
// delivery-dedup set. It has ZERO dependencies beyond the standard library, so a
// plugin module stays small, and it composes with the plugin SDK
// (github.com/NodeSpy/conductor/pkg/plugin): the SDK carries the protocol, this
// carries the ingest.
//
// A minimal webhook source plugin:
//
//	ln := sourcekit.Listener{Addr: cfg["listen"], Path: "/sentry", Secret: cfg["secret"], SigHeader: "Sentry-Hook-Signature"}
//	ln.Serve(ctx, func(h http.Header, body []byte) { emit(parse(body)) })
package sourcekit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VerifyHMAC reports whether the signature header is a valid HMAC-SHA256 of body
// under secret. It accepts a bare hex digest (Sentry), a `sha256=<hex>` value
// (GitHub), OR a comma-separated list of `v1=<hex>` / `sha256=<hex>` / bare-hex
// values where ANY match passes (PagerDuty sends several during key rotation).
// An empty secret disables verification (returns true) — the caller decides
// whether that is acceptable. Comparison is constant-time.
func VerifyHMAC(secret string, body []byte, header string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		p = strings.TrimPrefix(p, "v1=")
		p = strings.TrimPrefix(p, "sha256=")
		if got, err := hex.DecodeString(p); err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// Listener is a bounded HTTP webhook receiver. Serve runs until ctx is cancelled.
type Listener struct {
	Addr      string // listen address, e.g. ":8099"
	Path      string // request path, e.g. "/sentry" (default "/")
	Secret    string // HMAC secret; empty disables verification
	SigHeader string // header carrying the signature, e.g. "Sentry-Hook-Signature"
	MaxBytes  int64  // max body size (default 8 MiB)
}

// Serve listens for POSTs and calls h with each verified request's headers and
// body. Non-POST, oversized, or signature-mismatched requests are rejected
// before h runs. Blocks until ctx is cancelled.
func (l Listener) Serve(ctx context.Context, h func(http.Header, []byte)) error {
	path := l.Path
	if path == "" {
		path = "/"
	}
	max := l.MaxBytes
	if max <= 0 {
		max = 8 << 20
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if l.Secret != "" && !VerifyHMAC(l.Secret, body, r.Header.Get(l.SigHeader)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		h(r.Header, body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := &http.Server{Addr: l.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Dedup is a bounded set of recently-seen delivery keys, so a redelivered
// webhook doesn't emit twice. Safe for concurrent use.
type Dedup struct {
	mu    sync.Mutex
	cap   int
	seen  map[string]struct{}
	order []string
}

// NewDedup returns a Dedup that remembers up to cap keys (oldest evicted).
func NewDedup(capacity int) *Dedup {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Dedup{cap: capacity, seen: make(map[string]struct{}, capacity)}
}

// Add records key and reports whether it was new (true) or a duplicate (false).
func (d *Dedup) Add(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.cap {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return true
}
