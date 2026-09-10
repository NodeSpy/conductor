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

// §9: the M2 singleflight made one caller the LEADER for everyone. If that
// caller's ctx was already cancelled or past its deadline, the resulting
// error was cached durably in r.failed and never cleared — one unlucky
// caller bare-launching the runtime for the life of the process.
func TestACancelledLeaderDoesNotPoisonTheRoster(t *testing.T) {
	var calls atomic.Int32
	Register("poisontest", func(Runtime, *Catalog) Lister {
		return listerFunc(func() (Roster, error) {
			calls.Add(1)
			return Roster{{ID: "m1"}}, nil
		})
	})
	r := NewResolver(&config.Config{Runtimes: config.RuntimeSet{
		"rt": {Use: "poisontest"},
	}}, nil)

	// A caller whose context is already dead.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	r.roster(dead, "rt")

	// A later, healthy caller must still get a roster.
	if got := r.roster(context.Background(), "rt"); len(got) != 1 {
		t.Fatalf("a cancelled caller must not poison the runtime for everyone: got %v", got)
	}
}

// §10: ensure() set loaded=true BEFORE fetching, so one failed attempt —
// a flaky moment at boot — was remembered for the life of the process and
// every later dispatch ran with no catalog and no retry.
func TestCatalogRetriesAfterAFailedLoad(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"anthropic":{"id":"anthropic","name":"Anthropic","models":{"claude-x":{"id":"claude-x","name":"Claude X"}}}}`)
	}))
	defer srv.Close()

	c := &Catalog{HTTP: srv.Client(), URL: srv.URL, Dir: t.TempDir(), RetryAfter: time.Nanosecond}
	if err := c.ensure(context.Background()); err == nil {
		t.Fatal("the first load should fail")
	}
	// A later call must try again rather than serve the remembered error.
	if err := c.ensure(context.Background()); err != nil {
		t.Fatalf("a later call must refetch and succeed, got %v", err)
	}
	if hits.Load() < 2 {
		t.Fatalf("the catalog was never refetched (hits=%d)", hits.Load())
	}
}

// …but a hard-down endpoint is not refetched on every dispatch.
func TestCatalogFailureIsRateLimited(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Catalog{HTTP: srv.Client(), URL: srv.URL, Dir: t.TempDir(), RetryAfter: time.Hour}
	for i := 0; i < 5; i++ {
		if err := c.ensure(context.Background()); err == nil {
			t.Fatal("every load should fail here")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("a failed load should be remembered for RetryAfter, got %d fetches", hits.Load())
	}
}

// §12: CheckRequired had no callers at all, so `required: true` — the one
// hard guarantee the fleet ladder offers — was never enforced anywhere.
// It is wired into `conductor validate` now (an operator is present);
// dispatch stays degrade-safe on purpose.
func TestCheckRequiredErrorsWhenDiscoveryAnswers(t *testing.T) {
	Register("reqtest", func(Runtime, *Catalog) Lister {
		return listerFunc(func() (Roster, error) {
			return Roster{{ID: "only-this"}}, nil // answers, and does NOT have the model
		})
	})
	cfg := &config.Config{
		Runtimes: config.RuntimeSet{"rt": {Use: "reqtest", Default: true}},
		Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
			{ID: "a", Type: "agent", Prompt: "p", Model: mustRequiredSpec(t, "nope-*")},
		}}},
	}
	r := NewResolver(cfg, nil)
	if err := r.CheckRequired(context.Background()); err == nil {
		t.Fatal("an unsatisfiable required: fleet must fail validation")
	}
}

// …but a box that cannot reach its providers has learned NOTHING, and
// must not be failed for it — that is the degraded-boot invariant.
func TestCheckRequiredStaysQuietWhenDiscoveryCannotAnswer(t *testing.T) {
	Register("reqdead", func(Runtime, *Catalog) Lister {
		return listerFunc(func() (Roster, error) { return nil, ErrNoDiscovery })
	})
	cfg := &config.Config{
		Runtimes: config.RuntimeSet{"rt": {Use: "reqdead", Default: true}},
		Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
			{ID: "a", Type: "agent", Prompt: "p", Model: mustRequiredSpec(t, "nope-*")},
		}}},
	}
	r := NewResolver(cfg, nil)
	if err := r.CheckRequired(context.Background()); err != nil {
		t.Fatalf("an unreachable provider is not a negative answer: %v", err)
	}
}

func mustRequiredSpec(t *testing.T, pattern string) config.ModelSpec {
	t.Helper()
	var spec config.ModelSpec
	if err := unmarshalSpec(&spec, `{ any: ["`+pattern+`"], required: true }`); err != nil {
		t.Fatal(err)
	}
	return spec
}
