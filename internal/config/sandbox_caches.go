package config

import (
	"fmt"
	"path/filepath"
)

// SandboxCaches is `sandbox.caches:`: step caches for sandboxed agents
// (docs/wiki/Step-Caches.md). A step names its cache with a key template
// (its `cache:` block, or its workflow's); a launch of the step is shown
// the key's newest published copy, and a successful run publishes what it
// filled — the directories under the home that runs of the key keep filling
// (learned), never agent state or credentials.
type SandboxCaches struct {
	// Enabled turns caches on (default off: an upgrade never uses disk a
	// config did not ask for). They need the agent sandbox.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Dir is the cache store (default <state dir>/repo-caches).
	Dir string `yaml:"dir,omitempty" path:"local"`
	// MaxSize caps the store, superseded and least recently used copies
	// evicted first, never one a live run uses (default 10GiB; "off" = no
	// cap).
	MaxSize string `yaml:"max_size,omitempty"`
	// Learn is when a directory becomes one of a key's cached paths.
	Learn *CacheLearn `yaml:"learn,omitempty"`

	// Removed with the recipe caches (removed.go): accepted, ignored,
	// warned about. A cache is a step's now, keyed by its own `cache:`.
	Autodetect  RemovedField `yaml:"autodetect,omitempty"`
	RepoFile    RemovedField `yaml:"repo_file,omitempty"`
	WarmOnMiss  RemovedField `yaml:"warm_on_miss,omitempty"`
	WarmEvery   RemovedField `yaml:"warm_every,omitempty"`
	WarmTimeout RemovedField `yaml:"warm_timeout,omitempty"`
	Recipes     RemovedField `yaml:"recipes,omitempty"`
	Defaults    RemovedField `yaml:"defaults,omitempty"`
	Repos       RemovedField `yaml:"repos,omitempty"`
}

// CacheLearn is `sandbox.caches.learn:`: a directory under the home becomes
// one of a key's cached paths once min_runs successful runs of the key each
// wrote at least min_size there.
type CacheLearn struct {
	MinSize string `yaml:"min_size,omitempty"`
	MinRuns int    `yaml:"min_runs,omitempty"`
}

// Learning defaults (keycache.DefaultMinSize / DefaultMinRuns).
const (
	DefaultCacheLearnMinSize = 50 << 20
	DefaultCacheLearnMinRuns = 2
)

// CachesOn reports step caches on (they need the agent sandbox).
func (c *Config) CachesOn() bool {
	if !c.AgentSandboxOn() {
		return false
	}
	s := c.Sandbox
	return s != nil && s.Caches != nil && s.Caches.Enabled != nil && *s.Caches.Enabled
}

// CachesDir is the cache store.
func (c *Config) CachesDir() string {
	if c.Sandbox != nil && c.Sandbox.Caches != nil && c.Sandbox.Caches.Dir != "" {
		return c.Sandbox.Caches.Dir
	}
	return filepath.Join(filepath.Dir(c.Store.StateFile), "repo-caches")
}

// DefaultCachesMax is the store's default size cap.
const DefaultCachesMax = 10 << 30

// MaxSizeBytes is the store cap (0 = none).
func (s *SandboxCaches) MaxSizeBytes() int64 {
	if s == nil || s.MaxSize == "" {
		return DefaultCachesMax
	}
	n, _ := ParseByteSize(s.MaxSize)
	if n < 0 {
		return 0
	}
	return n
}

// LearnMinSize is learn.min_size in bytes.
func (s *SandboxCaches) LearnMinSize() int64 {
	if s == nil || s.Learn == nil || s.Learn.MinSize == "" {
		return DefaultCacheLearnMinSize
	}
	n, _ := ParseByteSize(s.Learn.MinSize)
	if n <= 0 {
		return DefaultCacheLearnMinSize
	}
	return n
}

// LearnMinRuns is learn.min_runs.
func (s *SandboxCaches) LearnMinRuns() int {
	if s == nil || s.Learn == nil || s.Learn.MinRuns <= 0 {
		return DefaultCacheLearnMinRuns
	}
	return s.Learn.MinRuns
}

func (s *SandboxCaches) validate() error {
	if s == nil {
		return nil
	}
	if s.Dir != "" && !filepath.IsAbs(s.Dir) {
		return fmt.Errorf("config: sandbox.caches.dir %q must be an absolute path", s.Dir)
	}
	if _, err := ParseByteSize(s.MaxSize); err != nil {
		return fmt.Errorf("config: sandbox.caches.max_size: %w", err)
	}
	if l := s.Learn; l != nil {
		if l.MinSize != "" {
			if n, err := ParseByteSize(l.MinSize); err != nil || n <= 0 {
				return fmt.Errorf("config: sandbox.caches.learn.min_size %q: want a size such as 50MiB", l.MinSize)
			}
		}
		if l.MinRuns < 0 {
			return fmt.Errorf("config: sandbox.caches.learn.min_runs must be at least 1")
		}
	}
	return nil
}

// CachesRemoved is the warning for a recipe-cache field still under
// sandbox.caches.
const CachesRemoved = "recipe caches were removed; this field is ignored and can be deleted (a step names its own cache with `cache: { key: … }`, and what it caches is learned — see Step-Caches)"

// removedCacheFields are the sandbox.caches keys the recipe caches had.
var removedCacheFields = []string{"autodetect", "repo_file", "warm_on_miss", "warm_every", "warm_timeout", "recipes", "defaults", "repos"}
