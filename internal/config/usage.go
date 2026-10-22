package config

import (
	"fmt"
	"net/url"
)

// USAGE REPORTING (docs/wiki/Configuration.md#usage): what conductor shows
// of model usage, and where plan-window or utilization data comes from.
// Display only — every budget cap (runs, turns, tokens, dollars) enforces in
// every mode, `off` included.
//
//	usage:
//	  mode: off              # off (default) | subscription | api
//	  sources:               # optional: who reports plan windows / utilization
//	    - { name: proxy, type: http, url: "http://127.0.0.1:8080/usage" }
type UsageConfig struct {
	// Mode: off (the default: reports and notifications carry no usage or
	// cost figures), subscription (units conductor observed — runs, turns,
	// tokens — per runtime and model, no dollars), api (the same, with
	// dollar figures).
	Mode string `yaml:"mode,omitempty"`
	// Sources report plan-window / utilization figures conductor cannot
	// observe itself. Optional; without one, conductor shows only what it
	// observed.
	Sources []UsageSource `yaml:"sources,omitempty"`
}

// Usage modes.
const (
	UsageOff          = "off"
	UsageSubscription = "subscription"
	UsageAPI          = "api"
)

// UsageMode is the configured usage.mode (default off).
func (c *Config) UsageMode() string {
	if c.Usage == nil || c.Usage.Mode == "" {
		return UsageOff
	}
	return c.Usage.Mode
}

// UsageSource is one pluggable usage source. Type picks the implementation
// (internal/cost's source registry); "http" is built in: a GET of a JSON
// document in conductor's usage-report shape, from anything that reports its
// own usage (a proxy, a gateway) — no vendor is assumed.
type UsageSource struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Timeout Duration          `yaml:"timeout,omitempty"`
}

// UsageSourceTypes is the set of source types this build knows.
var UsageSourceTypes = map[string]bool{"http": true}

func (c *Config) validateUsage() error {
	u := c.Usage
	if u == nil {
		return nil
	}
	switch u.Mode {
	case "", UsageOff, UsageSubscription, UsageAPI:
	default:
		return fmt.Errorf("config: usage.mode: want off, subscription or api, got %q", u.Mode)
	}
	seen := map[string]bool{}
	for i, s := range u.Sources {
		where := fmt.Sprintf("config: usage.sources[%d]", i)
		if !validBotName(s.Name) {
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, - or _", where, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("%s: name %q is used twice", where, s.Name)
		}
		seen[s.Name] = true
		if !UsageSourceTypes[s.Type] {
			return fmt.Errorf("%s (%s): unknown type %q (this build knows: http)", where, s.Name, s.Type)
		}
		if s.Type == "http" {
			p, err := url.Parse(s.URL)
			if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
				return fmt.Errorf("%s (%s): url must be an http(s) URL, got %q", where, s.Name, s.URL)
			}
		}
		if s.Timeout < 0 {
			return fmt.Errorf("%s (%s): timeout must not be negative", where, s.Name)
		}
	}
	return nil
}
