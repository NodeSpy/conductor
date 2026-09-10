package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The public model catalog (docs/design/runtimes-models-packs.md §3.3).
//
// https://models.dev/api.json is public, needs no auth, and carries both the
// current model ids and the metadata neither a vendor SDK's type enumeration
// nor a live /v1/models response provides (context window, pricing,
// modalities). It is the catalog for every adapter that cannot enumerate
// natively.
//
// It is ~4MB, so it is never fetched on a hot path: it is cached in the state
// dir with a TTL and every failure degrades one step at a time —
//
//	memory → fresh disk cache → network → STALE disk cache → cannot enumerate
//
// The last step is a bare launch, not a crash. A box with no network keeps
// dispatching agents.

// CatalogURL is the public catalog endpoint.
const CatalogURL = "https://models.dev/api.json"

// DefaultCatalogTTL is how long a cached catalog is served without refetching.
const DefaultCatalogTTL = 24 * time.Hour

// catalogFile is the cache filename inside the catalog directory.
const catalogFile = "models.dev.json"

// catalogFetchTimeout bounds one catalog fetch.
const catalogFetchTimeout = 30 * time.Second

// HTTPDoer is the http.Client seam (tests supply a stub; nothing in the unit
// tests reaches the network).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Catalog is a lazily-loaded, cached models.dev snapshot.
//
// The zero value is not usable — build one with NewCatalog. It is safe for
// concurrent use; the first caller through does the load and the rest wait.
type Catalog struct {
	// URL is the catalog endpoint (overridden in tests).
	URL string
	// Dir is where the cached copy lives (the state dir by default).
	Dir string
	// TTL is how long a cached copy is served before a refetch is attempted.
	TTL time.Duration
	// HTTP is the client used for the fetch.
	HTTP HTTPDoer
	// Now is the clock (overridden in tests).
	Now func() time.Time

	mu       sync.Mutex
	loaded   bool
	loadErr  error
	byProv   map[string]Roster
	provided []string
}

// NewCatalog builds a catalog cached under dir (use config.StateDir()). An
// empty dir disables the on-disk cache — the catalog is then fetched once per
// process and kept in memory, which is what a test or a read-only box wants.
func NewCatalog(dir string) *Catalog {
	return &Catalog{
		URL:  CatalogURL,
		Dir:  dir,
		TTL:  DefaultCatalogTTL,
		HTTP: &http.Client{Timeout: catalogFetchTimeout},
		Now:  time.Now,
	}
}

// Provider returns the catalog's roster for one provider id ("anthropic",
// "openai", "google"), newest-first. An unknown provider is ErrNoDiscovery,
// not an error: the caller falls back to bare launch.
func (c *Catalog) Provider(ctx context.Context, id string) (Roster, error) {
	if c == nil {
		return nil, ErrNoDiscovery
	}
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	r, ok := c.byProv[id]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: models.dev has no provider %q", ErrNoDiscovery, id)
	}
	return r, nil
}

// Providers lists the provider ids the catalog carries, sorted.
func (c *Catalog) Providers(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, ErrNoDiscovery
	}
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.provided...), nil
}

// ensure loads the catalog once per process, degrading through the chain in
// the file header. A load failure is remembered so a box with no network does
// not re-attempt a 4MB fetch on every dispatch.
func (c *Catalog) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.loadErr
	}
	c.loaded = true

	path := c.cachePath()
	raw, age, haveCache := c.readCache(path)
	if haveCache && age <= c.ttl() {
		if err := c.parse(raw); err == nil {
			return nil
		}
		// A corrupt cache is not fatal — fall through and refetch.
		haveCache = false
	}

	fetched, ferr := c.fetch(ctx)
	if ferr == nil {
		if err := c.parse(fetched); err == nil {
			c.writeCache(path, fetched)
			return nil
		} else {
			ferr = err
		}
	}
	// Degrade: a STALE cache beats no catalog at all.
	if haveCache {
		if err := c.parse(raw); err == nil {
			return nil
		}
	}
	c.loadErr = fmt.Errorf("%w: models.dev catalog unavailable: %v", ErrNoDiscovery, ferr)
	return c.loadErr
}

func (c *Catalog) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultCatalogTTL
}

func (c *Catalog) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Catalog) cachePath() string {
	if c.Dir == "" {
		return ""
	}
	return filepath.Join(c.Dir, "models", catalogFile)
}

// readCache returns the cached bytes and their age.
func (c *Catalog) readCache(path string) (raw []byte, age time.Duration, ok bool) {
	if path == "" {
		return nil, 0, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil, 0, false
	}
	return b, c.now().Sub(fi.ModTime()), true
}

// writeCache stores a fresh copy, atomically. A cache we cannot write is not
// an error — discovery still worked this time.
func (c *Catalog) writeCache(path string, raw []byte) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

func (c *Catalog) fetch(ctx context.Context) ([]byte, error) {
	if c.HTTP == nil {
		return nil, fmt.Errorf("no http client")
	}
	url := c.URL
	if url == "" {
		url = CatalogURL
	}
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// --- the models.dev document shape -----------------------------------------
//
// { "<provider>": { "id", "name", "models": { "<model>": { … } } } }

type catalogProvider struct {
	ID     string                       `json:"id"`
	Name   string                       `json:"name"`
	Models map[string]catalogModelEntry `json:"models"`
}

type catalogModelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ReleaseDate string `json:"release_date"`
	LastUpdated string `json:"last_updated"`
	Limit       struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

func (c *Catalog) parse(raw []byte) error {
	var doc map[string]catalogProvider
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse models.dev catalog: %w", err)
	}
	if len(doc) == 0 {
		return fmt.Errorf("models.dev catalog is empty")
	}
	byProv := make(map[string]Roster, len(doc))
	names := make([]string, 0, len(doc))
	for provID, p := range doc {
		id := p.ID
		if id == "" {
			id = provID
		}
		r := make(Roster, 0, len(p.Models))
		for modelID, m := range p.Models {
			mid := m.ID
			if mid == "" {
				mid = modelID
			}
			released := m.ReleaseDate
			if released == "" {
				released = m.LastUpdated
			}
			r = append(r, Model{
				ID: mid, Name: m.Name, Provider: id,
				Context: m.Limit.Context, MaxOutput: m.Limit.Output,
				InputUSD: m.Cost.Input, OutputUSD: m.Cost.Output,
				Released: released,
			})
		}
		byProv[provID] = r.SortNewestFirst()
		if id != provID {
			byProv[id] = byProv[provID]
		}
		names = append(names, provID)
	}
	sort.Strings(names)
	c.byProv, c.provided = byProv, names
	return nil
}
