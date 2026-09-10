package models

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// M1: the catalog body comes from a remote host. An unbounded ReadAll let
// it decide how much of the daemon's memory to take.
func TestCatalogFetchIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Stream past the cap without ever allocating it here.
		chunk := strings.Repeat("x", 1<<20)
		for i := int64(0); i <= (maxCatalogBytes>>20)+1; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := &Catalog{HTTP: srv.Client(), URL: srv.URL}
	_, err := c.fetch(context.Background())
	if err == nil {
		t.Fatal("an oversized catalog must be refused, not buffered")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("the error should name the cap: %v", err)
	}
}

// M2: roster consults its cache under the lock but calls List outside it,
// so every branch of a `parallel:` step probed the provider at once.
func TestConcurrentResolveSharesOneDiscovery(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	Register("slowtest", func(Runtime, *Catalog) Lister {
		return listerFunc(func() (Roster, error) {
			calls.Add(1)
			<-release // hold the first caller inside List, where the lock is not held
			return Roster{{ID: "m1"}}, nil
		})
	})
	r := NewResolver(&config.Config{Runtimes: config.RuntimeSet{
		"rt": {Use: "slowtest"},
	}}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.roster(context.Background(), "rt")
		}()
	}
	// Let them all pile up behind the one in-flight List, then release it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("concurrent callers must share one discovery, got %d List calls", n)
	}
}
