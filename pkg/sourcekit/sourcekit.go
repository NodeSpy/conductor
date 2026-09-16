// Package sourcekit is the PUBLIC connector-kit for building conductor SOURCE
// plugins (#59): the reusable, daemon-agnostic transport bits a webhook source
// needs — HMAC signature verification, a bounded HTTP webhook listener, an
// optional smee.io-style SSE relay for endpoints with no public URL, and a
// delivery-dedup set. It has ZERO dependencies beyond the standard library, so a
// plugin module stays small, and it composes with the plugin SDK
// (github.com/NodeSpy/conductor/pkg/plugin): the SDK carries the protocol, this
// carries the ingest.
//
// A minimal webhook source plugin:
//
//	ln := sourcekit.Listener{Addr: cfg["listen"], Path: "/sentry", Secret: cfg["secret"], SigHeader: "Sentry-Hook-Signature"}
//	ln.Serve(ctx, func(h http.Header, body []byte) { emit(parse(body)) })
//
// A source that also needs the query string (e.g. a shared `?token=`), and/or a
// smee.io relay so an endpoint with no public URL can still receive deliveries:
//
//	ln := sourcekit.Listener{Addr: cfg["listen"], Path: "/hook", Relay: cfg["relay"]}
//	ln.ServeReq(ctx, func(r *sourcekit.Request) {
//		if r.Query.Get("token") != secret { return }
//		emit(parse(r.Body))
//	})
package sourcekit

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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

// Listener is a bounded HTTP webhook receiver, optionally fed also (or instead)
// by a smee.io-style SSE relay. Serve/ServeReq run until ctx is cancelled.
type Listener struct {
	Addr      string // listen address, e.g. ":8099" (empty = relay-only)
	Path      string // request path, e.g. "/sentry" (default "/")
	Secret    string // HMAC secret; empty disables verification
	SigHeader string // header carrying the signature, e.g. "Sentry-Hook-Signature"
	MaxBytes  int64  // max body size (default 8 MiB)
	Relay     string // optional smee.io-style SSE relay URL; empty disables relay
}

// Serve listens for POSTs and calls h with each verified request's headers and
// body. Non-POST, oversized, or signature-mismatched requests are rejected
// before h runs. If Relay is set, forwarded deliveries are dispatched to the
// same handler. Blocks until ctx is cancelled.
//
// Serve is the header+body-only shim over ServeReq, kept for existing callers.
func (l Listener) Serve(ctx context.Context, h func(http.Header, []byte)) error {
	return l.ServeReq(ctx, func(r *Request) { h(r.Header, r.Body) })
}

// ServeReq is Serve with full Request access (headers, query, body). It runs the
// HTTP listener (when Addr is set) and the SSE relay (when Relay is set), both
// feeding h. At least one of Addr/Relay must be set. Signature verification
// (Secret + SigHeader) is applied identically on both paths. Blocks until ctx is
// cancelled.
func (l Listener) ServeReq(ctx context.Context, h func(*Request)) error {
	if l.Addr == "" && l.Relay == "" {
		return errors.New("sourcekit: Listener needs Addr or Relay")
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

	if l.Relay != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRelay(ctx, l.Relay, func(r *Request) {
				if verified(r) {
					h(r)
				}
			})
		}()
	}

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

// --- smee.io-style SSE relay client (stdlib) ---

// runRelay connects to a smee-style relay channel and feeds every forwarded
// request into dispatch, reconnecting with backoff until ctx is cancelled.
func runRelay(ctx context.Context, relayURL string, dispatch func(*Request)) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		_ = relayOnce(ctx, relayURL, dispatch)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// relayOnce holds one relay connection open, parsing the SSE stream into
// forwarded-request payloads until the connection drops or ctx cancels.
func relayOnce(ctx context.Context, relayURL string, dispatch func(*Request)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("sourcekit: relay stream status " + resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var data strings.Builder
	flush := func() {
		if data.Len() == 0 {
			return
		}
		payload := data.String()
		data.Reset()
		if r, ok := parseRelayPayload([]byte(payload)); ok {
			dispatch(r)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "":
			flush()
		default:
			// event:, id:, retry:, and comments (":...") carry no delivery
			// payload — the "ready"/keep-alive events a relay sends land here
			// and are silently ignored.
		}
	}
	flush()
	return scanner.Err()
}

// parseRelayPayload decodes one smee-forwarded `data:` JSON envelope into a
// Request. smee delivers the original request as headers at the top level, plus
// body/query/host/timestamp; body may arrive as a nested JSON object or as a raw
// string, and query as an object. Returns ok=false for an envelope with no body
// (a relay's own ready/keep-alive events).
func parseRelayPayload(raw []byte) (*Request, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	bodyVal, present := payload["body"]
	if !present {
		return nil, false
	}
	var body []byte
	switch b := bodyVal.(type) {
	case string:
		body = []byte(b)
	case nil:
		return nil, false
	default:
		marshaled, err := json.Marshal(b)
		if err != nil {
			return nil, false
		}
		body = marshaled
	}

	// Every top-level string value is a forwarded request header (smee preserves
	// the original casing). body/query are structural, not headers.
	header := http.Header{}
	for k, v := range payload {
		if strings.EqualFold(k, "body") || strings.EqualFold(k, "query") {
			continue
		}
		if s, ok := v.(string); ok {
			header.Set(k, s)
		}
	}

	query := url.Values{}
	if q, ok := payload["query"].(map[string]any); ok {
		for k, v := range q {
			switch vv := v.(type) {
			case string:
				query.Set(k, vv)
			case []any:
				for _, item := range vv {
					if s, ok := item.(string); ok {
						query.Add(k, s)
					}
				}
			}
		}
	}

	return &Request{Header: header, Query: query, Body: body}, true
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
