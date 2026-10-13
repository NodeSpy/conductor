// Package sourcekit is the PUBLIC connector-kit for building conductor SOURCE
// plugins (#59): the reusable, daemon-agnostic transport bits a webhook source
// needs — HMAC signature verification, a bounded HTTP webhook listener, a
// delivery-dedup set, and the trigger filter IR. It has ZERO dependencies
// beyond the standard library, so a plugin module stays small, and it composes
// with the plugin SDK (github.com/NodeSpy/conductor/pkg/plugin): the SDK
// carries the protocol, this carries the ingest. Making a listener reachable
// from outside is an exposure connector's job, not the kit's.
//
// A minimal webhook source plugin:
//
//	ln := sourcekit.Listener{Addr: cfg["listen"], Path: "/hook", Secret: cfg["secret"], SigHeader: "X-Signature"}
//	ln.Serve(ctx, func(h http.Header, body []byte) { emit(parse(body)) })
//
// A source that also needs the query string (e.g. a shared `?token=`):
//
//	ln := sourcekit.Listener{Addr: cfg["listen"], Path: "/hook"}
//	ln.ServeReq(ctx, func(r *sourcekit.Request) {
//		if r.Query.Get("token") != secret { return }
//		emit(parse(r.Body))
//	})
package sourcekit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// VerifyHMAC reports whether the signature header is a valid HMAC-SHA256 of body
// under secret. It accepts a bare hex digest, a `sha256=<hex>` value, OR a
// comma-separated list of `v1=<hex>` / `sha256=<hex>` / bare-hex values where
// ANY match passes (senders that rotate keys send several).
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

// Request is one received webhook delivery — whether it arrived over the HTTP
// listener or was relayed in over SSE. It exposes the query string (which the
// bare Serve callback cannot), so connectors that authenticate with a shared
// `?token=` param can use the standard Listener instead of rolling their own
// HTTP server.
type Request struct {
	Header http.Header
	Query  url.Values
	Body   []byte
}

// Listener is a bounded HTTP webhook receiver. Serve/ServeReq run until ctx is
// cancelled. Reaching it from outside — a tunnel, a relay — is an exposure
// connector's job (plugin-contract.md §2.3: the plugin declares the listener
// under `listeners`), so the SDK carries no relay client of its own.
type Listener struct {
	Addr      string // listen address, e.g. ":8099" (empty = relay-only)
	Path      string // request path, e.g. "/hook" (default "/")
	Secret    string // HMAC secret; empty disables verification
	SigHeader string // header carrying the signature, e.g. "X-Signature"
	MaxBytes  int64  // max body size (default 8 MiB)
}

// Serve listens for POSTs and calls h with each verified request's headers and
// body. Non-POST, oversized, or signature-mismatched requests are rejected
// before h runs. Blocks until ctx is cancelled.
//
// Serve is the header+body-only shim over ServeReq, kept for existing callers.
func (l Listener) Serve(ctx context.Context, h func(http.Header, []byte)) error {
	return l.ServeReq(ctx, func(r *Request) { h(r.Header, r.Body) })
}

// ServeReq is Serve with full Request access (headers, query, body).
// Signature verification (Secret + SigHeader) is applied before h runs.
// Blocks until ctx is cancelled.
func (l Listener) ServeReq(ctx context.Context, h func(*Request)) error {
	if l.Addr == "" {
		return errors.New("sourcekit: Listener needs Addr")
	}
	max := l.MaxBytes
	if max <= 0 {
		max = 8 << 20
	}
	verified := func(r *Request) bool {
		return l.Secret == "" || VerifyHMAC(l.Secret, r.Body, r.Header.Get(l.SigHeader))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	var wg sync.WaitGroup

	if l.Addr != "" {
		path := l.Path
		if path == "" {
			path = "/"
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
			req := &Request{Header: r.Header, Query: r.URL.Query(), Body: body}
			if !verified(req) {
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
			h(req)
			w.WriteHeader(http.StatusAccepted)
		})
		srv := &http.Server{Addr: l.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				select {
				case errc <- err:
				default:
				}
				cancel()
			}
		}()
	}

	<-ctx.Done()
	wg.Wait()
	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
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
