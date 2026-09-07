// Package inbound is the shared plumbing for integrations that receive events
// over HTTP or a smee.io channel (the generic webhook receiver, sentry, …). It
// deliberately does NOT touch the github integration, which has its own copy of
// this shape — this package is for the newer siblings.
package inbound

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A process-global registry of HTTP listeners keyed by bind address, so several
// integration instances that share a `listen:` share one http.Server (one port to
// expose) while each owns distinct paths. Registration is safe at any time: the
// server is started on first use for an address, and later Register calls just add
// routes to a mutex-guarded map the dispatch handler reads under RLock.
var (
	lmu       sync.Mutex
	listeners = map[string]*listener{}
)

type listener struct {
	ctx      context.Context // the first registrant's ctx — governs shutdown
	mu       sync.RWMutex
	routes   map[string]http.Handler // exact-path routes
	prefixes map[string]http.Handler // prefix routes (longest match wins)
	server   *http.Server
}

// getListener returns the shared listener for addr, starting its server the
// first time addr is seen. Callers hold no lock; it takes lmu internally.
func getListener(ctx context.Context, addr string, logf func(string, ...any)) *listener {
	lmu.Lock()
	defer lmu.Unlock()
	l, ok := listeners[addr]
	if ok && l.ctx.Err() != nil {
		// The existing entry's governing ctx is already done — its shutdown
		// goroutine may not have evicted it yet. Attaching here would mount
		// the route on a dying server; replace the entry instead (the old
		// goroutine's eviction is guarded by identity and won't touch the
		// replacement; the fresh bind retries while the old socket closes).
		delete(listeners, addr)
		ok = false
	}
	if ok {
		return l
	}
	l = &listener{ctx: ctx, routes: map[string]http.Handler{}, prefixes: map[string]http.Handler{}}
	listeners[addr] = l
	mux := http.NewServeMux()
	mux.HandleFunc("/", l.dispatch) // one static route; per-path lookup happens in dispatch
	l.server = &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		// Evict BEFORE the shutdown grace: a Register racing the (up to
		// 5s) drain must build a fresh listener, never attach routes to
		// this dying one — an attached route would look registered while
		// serving nothing.
		lmu.Lock()
		if listeners[addr] == l {
			delete(listeners, addr)
		}
		lmu.Unlock()
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = l.server.Shutdown(sd)
	}()
	go func() {
		logf("inbound: listener on %s", addr)
		// A fresh listener can race a dying predecessor: Shutdown closes
		// the old listening socket early but not instantly, so retry a
		// transient address-in-use briefly instead of dying silent.
		var err error
		for i := 0; i < 100; i++ {
			err = l.server.ListenAndServe()
			if !errors.Is(err, syscall.EADDRINUSE) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil && err != http.ErrServerClosed {
			logf("inbound: listener %s stopped: %v", addr, err)
		}
	}()
	return l
}

// Register mounts h at exact path on the listener for addr, starting the shared
// server (with graceful shutdown on ctx cancel) the first time addr is seen. All
// integrations receive the same ctx from main, so the first registrant's ctx
// governs shutdown for the whole shared server.
func Register(ctx context.Context, addr, path string, h http.Handler, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	l := getListener(ctx, addr, logf)
	l.mu.Lock()
	l.routes[path] = h
	l.mu.Unlock()
}

// RegisterPrefix mounts h for every request path that begins with prefix (used
// by the §13 invoke surface, whose paths carry a dynamic tail: /invoke/<name>,
// /runs/<id>). An exact-path route always wins over a prefix, and among
// prefixes the longest match wins, so a specific mount is never shadowed by a
// broader one on the same server.
func RegisterPrefix(ctx context.Context, addr, prefix string, h http.Handler, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	l := getListener(ctx, addr, logf)
	l.mu.Lock()
	l.prefixes[prefix] = h
	l.mu.Unlock()
}

func (l *listener) dispatch(w http.ResponseWriter, r *http.Request) {
	l.mu.RLock()
	h := l.routes[r.URL.Path]
	if h == nil {
		// Longest-prefix match, so a more specific mount wins over a broader one.
		best := -1
		for p, ph := range l.prefixes {
			if len(p) > best && strings.HasPrefix(r.URL.Path, p) {
				best, h = len(p), ph
			}
		}
	}
	l.mu.RUnlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}
