package models

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// Model-unsupported classification + the fallback memory behind it.
//
// A provider can refuse a model at RUN time for reasons no roster shows in
// advance: the API gates a new model behind a minimum client version (the
// claude-code "400 … does not support this model; version X or newer is
// required" that broke every review judge when a `claude-opus-*` fleet glob
// started resolving to a freshly released model), a model is deprecated or
// removed, or a plan doesn't include it. Conductor's answer is the FLEET: mark
// the refused (runtime, model) pair here and re-resolve — the resolver skips
// marked pairs (Resolver.Excluded), so the dispatch walks down to the newest
// model that actually runs.
//
// Entries expire after UnsupportedTTL rather than being keyed to tool
// versions: after the operator updates the client, conductor automatically
// re-tries the newest model within hours; if nothing changed, one cheap failed
// probe re-marks it. Self-healing in both directions, no version plumbing.

// UnsupportedTTL is how long a model-unsupported mark holds before conductor
// re-tries the model (picks up client/provider fixes automatically).
const UnsupportedTTL = 6 * time.Hour

// unsupportedSignatures are the run-error shapes that mean "this model will
// not run here" (as opposed to a transient failure or a bad reply). Matched
// case-insensitively against the run's error/output text.
var unsupportedSignatures = []string{
	"does not support this model",
	"model is not supported",
	"unknown model",
	"model not found",
	"no such model",
	"invalid model",
	"model has been deprecated",
	"model is deprecated",
}

// UnsupportedSignature reports whether a run's error/output text is a
// model-won't-run-here refusal.
func UnsupportedSignature(text string) bool {
	lo := strings.ToLower(text)
	for _, sig := range unsupportedSignatures {
		if strings.Contains(lo, sig) {
			return true
		}
	}
	return false
}

// UnsupportedCache remembers (runtime, model) pairs a provider refused, with a
// TTL. Persisted (best-effort, atomic) so a restart doesn't re-probe every
// refused model at once. An empty path keeps it in-memory (tests).
type UnsupportedCache struct {
	mu      sync.Mutex
	entries map[string]time.Time // "runtime|model" -> marked-at
	path    string
	now     func() time.Time
}

// NewUnsupportedCache loads (or creates) the cache at path.
func NewUnsupportedCache(path string) *UnsupportedCache {
	c := &UnsupportedCache{entries: map[string]time.Time{}, path: path, now: time.Now}
	c.load()
	return c
}

func unsupportedKey(runtime, model string) string { return runtime + "|" + model }

// Mark records that the model was refused on the runtime.
func (c *UnsupportedCache) Mark(runtime, model string) {
	if c == nil || model == "" {
		return
	}
	c.mu.Lock()
	c.entries[unsupportedKey(runtime, model)] = c.now()
	c.save()
	c.mu.Unlock()
}

// Has reports whether the pair is currently marked unsupported (within TTL).
// A mark recorded with an empty runtime applies to every runtime.
func (c *UnsupportedCache) Has(runtime, model string) bool {
	if c == nil || model == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range []string{unsupportedKey(runtime, model), unsupportedKey("", model)} {
		if at, ok := c.entries[k]; ok {
			if c.now().Sub(at) < UnsupportedTTL {
				return true
			}
			delete(c.entries, k)
		}
	}
	return false
}

func (c *UnsupportedCache) load() {
	if c.path == "" {
		return
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var rec map[string]time.Time
	if json.Unmarshal(b, &rec) != nil {
		return
	}
	for k, at := range rec {
		if time.Since(at) < UnsupportedTTL {
			c.entries[k] = at
		}
	}
}

// save persists the entries (best-effort, atomic). Caller holds c.mu.
func (c *UnsupportedCache) save() {
	if c.path == "" {
		return
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}
