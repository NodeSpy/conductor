package pkgproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
)

// maxWarm bounds the requests one warm makes.
const maxWarm = 20000

// warmWorkers is how many warm requests run at once (fills are bounded by
// the cache as well).
const warmWorkers = 4

// Warm fills the cache from a workspace's lockfiles (workspace path →
// content) on behalf of c, for the ecosystems named in only (nil: every
// one). It returns how many requests were served and the failures, joined
// (a package the upstream lacks is a failure the agent would hit too).
func (s *Server) Warm(ctx context.Context, files map[string][]byte, c Requester, only []string) (int, error) {
	type job struct {
		e    Ecosystem
		rest string
	}
	var jobs []job
	for _, n := range s.ord {
		if only != nil && !contains(only, n) {
			continue
		}
		w, ok := s.ecos[n].(Warmer)
		if !ok {
			continue
		}
		mine := map[string][]byte{}
		for _, lf := range w.Lockfiles() {
			for name, b := range files {
				if ok, _ := path.Match(lf, path.Base(name)); ok {
					mine[name] = b
				}
			}
		}
		if len(mine) == 0 {
			continue
		}
		seen := map[string]bool{}
		for _, p := range w.WarmPaths(ctx, mine) {
			if !seen[p] && len(jobs) < maxWarm {
				seen[p] = true
				jobs = append(jobs, job{s.ecos[n], p})
			}
		}
	}
	var (
		mu      sync.Mutex
		ok      int
		missing int
		errs    []error
		wg      sync.WaitGroup
		next    = make(chan job)
	)
	for i := 0; i < warmWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range next {
				status, msg := s.serveInternal(ctx, j.e, j.rest, c)
				mu.Lock()
				switch {
				case status == http.StatusOK:
					ok++
				case status == http.StatusNotFound:
					missing++
				case len(errs) < 20:
					errs = append(errs, fmt.Errorf("%s %s: %d %s", j.e.Name(), j.rest, status, msg))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		next <- j
	}
	close(next)
	wg.Wait()
	if missing > 0 {
		errs = append(errs, fmt.Errorf("%d pinned artifacts are not in the upstream registry", missing))
	}
	return ok, errors.Join(errs...)
}

// serveInternal runs one request through e as c, the body discarded.
func (s *Server) serveInternal(ctx context.Context, e Ecosystem, rest string, c Requester) (int, string) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://conductor.invalid/", nil)
	if err != nil {
		return 0, err.Error()
	}
	w := &discard{h: http.Header{}}
	name := e.Name()
	e.Serve(w, r, &Call{Rest: rest, Base: "http://" + s.Addr() + "/-/" + name, Requester: c, Emit: func(ev Event) {
		ev.Requester, ev.Ecosystem = c, name
		if s.OnEvent != nil {
			s.OnEvent(ev)
		}
	}})
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.code, strings.TrimSpace(w.msg.String())
}

// discard is a ResponseWriter keeping the status and the start of an
// error body.
type discard struct {
	h    http.Header
	code int
	msg  strings.Builder
}

func (d *discard) Header() http.Header { return d.h }
func (d *discard) WriteHeader(code int) {
	if d.code == 0 {
		d.code = code
	}
}
func (d *discard) Write(b []byte) (int, error) {
	if d.code == 0 {
		d.code = http.StatusOK
	}
	if d.code != http.StatusOK && d.msg.Len() < 300 {
		d.msg.Write(b[:min(len(b), 300)])
	}
	return len(b), nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
