package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/NodeSpy/conductor/internal/repocache"
)

// SandboxCaches is `sandbox.caches:`: per-repository dependency caches for
// sandboxed agents (docs/wiki/Repo-Caches.md). A cache is named and keyed
// by a template over the checkout's files; a launch in a checkout of the
// repository gets the newest matching cache at its paths — an overlay on
// the shared copy (Linux) or a copy-on-write clone (macOS), so the shared
// copy is never written. Conductor alone publishes a cache: a warm step in
// a checkout of the default branch, in the agent sandbox.
//
// Definitions come from, by name, highest first: repos[<owner/name>],
// defaults, the repository's own .conductor.yaml (default branch only), and
// the recipes autodetected in the checkout.
type SandboxCaches struct {
	// Enabled turns caches on (default off: an upgrade never starts warms,
	// disk use or fetches a config did not ask for). They need the agent
	// sandbox.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Dir is the cache store (default <state dir>/repo-caches).
	Dir string `yaml:"dir,omitempty" path:"local"`
	// MaxSize caps the store, least recently used evicted first, never a
	// cache a live run uses (default 10GiB; "off" = no cap).
	MaxSize string `yaml:"max_size,omitempty"`
	// Autodetect adds the recipes whose files the checkout has (default
	// true).
	Autodetect *bool `yaml:"autodetect,omitempty"`
	// RepoFile reads the repository's .conductor.yaml from its default
	// branch (default true).
	RepoFile *bool `yaml:"repo_file,omitempty"`
	// WarmOnMiss warms a repository's caches when a launch misses its key
	// (default true).
	WarmOnMiss *bool `yaml:"warm_on_miss,omitempty"`
	// WarmEvery re-warms every known repository on a schedule ("" = never).
	WarmEvery string `yaml:"warm_every,omitempty"`
	// WarmTimeout bounds one warm command (default 20m).
	WarmTimeout string `yaml:"warm_timeout,omitempty"`
	// Recipes replace bundled recipes by name, or add new ones.
	Recipes []repocache.Recipe `yaml:"recipes,omitempty"`
	// Defaults are caches every repository gets.
	Defaults []repocache.Cache `yaml:"defaults,omitempty"`
	// Repos are caches for one repository (owner/name).
	Repos map[string][]repocache.Cache `yaml:"repos,omitempty"`
}

// CachesOn reports repository caches on (they need the agent sandbox).
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

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// AutodetectOn is autodetect (default true).
func (s *SandboxCaches) AutodetectOn() bool { return s == nil || boolOr(s.Autodetect, true) }

// RepoFileOn is repo_file (default true).
func (s *SandboxCaches) RepoFileOn() bool { return s == nil || boolOr(s.RepoFile, true) }

// WarmOnMissOn is warm_on_miss (default true).
func (s *SandboxCaches) WarmOnMissOn() bool { return s == nil || boolOr(s.WarmOnMiss, true) }

// WarmEveryDur is warm_every (0 = never).
func (s *SandboxCaches) WarmEveryDur() time.Duration {
	if s == nil || s.WarmEvery == "" {
		return 0
	}
	d, _ := time.ParseDuration(s.WarmEvery)
	return d
}

// WarmTimeoutDur is warm_timeout (default 20m).
func (s *SandboxCaches) WarmTimeoutDur() time.Duration {
	if s == nil || s.WarmTimeout == "" {
		return 20 * time.Minute
	}
	d, _ := time.ParseDuration(s.WarmTimeout)
	return d
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
	for k, v := range map[string]string{"warm_every": s.WarmEvery, "warm_timeout": s.WarmTimeout} {
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return fmt.Errorf("config: sandbox.caches.%s %q is not a positive duration", k, v)
		}
	}
	if d := s.WarmEveryDur(); d > 0 && d < 10*time.Minute {
		return fmt.Errorf("config: sandbox.caches.warm_every %s is under 10m", d)
	}
	recipes := repocache.Recipes(s.Recipes)
	for i, r := range s.Recipes {
		if r.Disabled {
			continue
		}
		if r.Recipe != "" {
			return fmt.Errorf("config: sandbox.caches.recipes[%d]: a recipe cannot reference another (recipe: %s)", i, r.Recipe)
		}
		if err := r.Validate(nil); err != nil {
			return fmt.Errorf("config: sandbox.caches.recipes[%d]: %w", i, err)
		}
		if len(r.Detect) == 0 {
			return fmt.Errorf("config: sandbox.caches.recipes[%d] (%s): no detect files", i, r.Name)
		}
	}
	check := func(where string, list []repocache.Cache) error {
		seen := map[string]bool{}
		for i, c := range list {
			defs, warn := repocache.Resolve(repocache.Sources{Repo: []repocache.Cache{c}, Recipes: recipes})
			if len(warn) > 0 {
				return fmt.Errorf("config: %s[%d]: %s", where, i, warn[0])
			}
			name := c.Name
			if name == "" {
				name = c.Recipe
			}
			if len(defs) == 0 && !c.Disabled {
				return fmt.Errorf("config: %s[%d]: no cache defined", where, i)
			}
			if seen[name] {
				return fmt.Errorf("config: %s: cache %q is defined twice", where, name)
			}
			seen[name] = true
		}
		return nil
	}
	if err := check("sandbox.caches.defaults", s.Defaults); err != nil {
		return err
	}
	for repo, list := range s.Repos {
		if err := repocache.CheckRepo(repo); err != nil {
			return fmt.Errorf("config: sandbox.caches.repos: %w", err)
		}
		if err := check("sandbox.caches.repos."+repo, list); err != nil {
			return err
		}
	}
	return nil
}
