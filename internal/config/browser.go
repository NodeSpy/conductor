package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// BrowserConfig is conductor's browser for agents (docs/wiki/Browser.md):
// present = on. Every agent conductor launches sandboxed may then drive a
// headless browser through `conductor browser …`; conductor runs it,
// sandboxed, reaching only Allow, with an anonymous profile.
type BrowserConfig struct {
	// Allow are the sites an agent's browser may reach: allowlist patterns
	// as for sandbox.egress ("docs.example.com", "*.example.org",
	// "host:port"; a bare host means :443). sandbox.egress joins it. The
	// whole web ("*") is refused: list the sites.
	Allow []string `yaml:"allow"`
	// Chrome is the browser binary (default: the first Chrome or Chromium
	// installed in the usual places).
	Chrome string `yaml:"chrome,omitempty"`
	// Evaluate is `off` (the default: no agent JavaScript in a page) or
	// `read-only` (an expression runs only if V8 can rule out every side
	// effect — no DOM write, no network, no storage).
	Evaluate string `yaml:"evaluate,omitempty"`
	// IdleTimeout closes a session's browsers unused this long (default 10m).
	IdleTimeout string `yaml:"idle_timeout,omitempty"`
	// MaxBrowsers bounds the browsers running at once (default 4).
	MaxBrowsers int `yaml:"max_browsers,omitempty"`
	// MaxDownloadMB / MaxUploadMB bound one file (default 100 / 25).
	MaxDownloadMB int `yaml:"max_download_mb,omitempty"`
	MaxUploadMB   int `yaml:"max_upload_mb,omitempty"`
	// Logins are the sites an identity may use logged in — the operator's
	// explicit opt-in, per site and per identity. Opting a site in gives
	// that identity's agent YOUR session's power on that site whenever the
	// work is your own request. A saved session is seeded separately
	// (`conductor browser login` / `conductor browser cookies import`).
	Logins []BrowserLogin `yaml:"logins,omitempty"`
}

// BrowserLogin opts one site in for logged-in use.
type BrowserLogin struct {
	// Site is a host ("app.example.com") or a domain and its subdomains
	// ("*.example.com"). It must be reachable through Allow.
	Site string `yaml:"site"`
	// Identities are the agent sandbox identities that may use it: exact
	// ("agent:triage@acme/app", "dir:/home/me/project") or a prefix ending
	// in "*" ("agent:triage@*").
	Identities []string `yaml:"identities"`
	// Also are further hosts the logged-in browser may reach for this site
	// (its asset CDN, its single sign-on): the logged-in browser reaches
	// nothing else.
	Also []string `yaml:"also,omitempty"`
}

// BrowserOn reports whether the browser is configured.
func (c *Config) BrowserOn() bool { return c != nil && c.Browser != nil }

// BrowserIdle is the idle timeout.
func (b *BrowserConfig) BrowserIdle() time.Duration {
	if b != nil && b.IdleTimeout != "" {
		if d, err := time.ParseDuration(b.IdleTimeout); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Minute
}

// BrowserStateDir is where conductor keeps the browser's saved sessions
// and each browser's ephemeral profile: beside the agent identity homes,
// never inside one.
func (c *Config) BrowserStateDir() string {
	return filepath.Join(filepath.Dir(c.Store.StateFile), "browser")
}

func (c *Config) validateBrowser() error {
	b := c.Browser
	if b == nil {
		return nil
	}
	if len(b.Allow) == 0 {
		return fmt.Errorf("config: browser.allow is empty — list the sites an agent's browser may reach")
	}
	for _, a := range b.Allow {
		a = strings.TrimSpace(a)
		if a == "" {
			return fmt.Errorf("config: browser.allow: empty pattern")
		}
		if h, _ := splitHostPortPattern(a); h == "*" {
			return fmt.Errorf("config: browser.allow: %q would open the whole web to an agent's browser — list the sites", a)
		}
	}
	switch b.Evaluate {
	case "", "off", "read-only":
	default:
		return fmt.Errorf("config: browser.evaluate must be off|read-only, got %q", b.Evaluate)
	}
	if b.IdleTimeout != "" {
		if d, err := time.ParseDuration(b.IdleTimeout); err != nil || d <= 0 {
			return fmt.Errorf("config: browser.idle_timeout %q is not a duration", b.IdleTimeout)
		}
	}
	if b.MaxBrowsers < 0 || b.MaxDownloadMB < 0 || b.MaxUploadMB < 0 {
		return fmt.Errorf("config: browser: limits cannot be negative")
	}
	if b.Chrome != "" && strings.Contains(b.Chrome, "/") && !filepath.IsAbs(expandHome(b.Chrome)) {
		return fmt.Errorf("config: browser.chrome %q must be an absolute path or a command name", b.Chrome)
	}
	seen := map[string]bool{}
	for i, l := range b.Logins {
		site := strings.TrimPrefix(l.Site, "*.")
		if site == "" || strings.ContainsAny(site, ":/ *@?#") || strings.HasPrefix(site, ".") || strings.HasSuffix(site, ".") {
			return fmt.Errorf("config: browser.logins[%d].site %q is not a host (app.example.com) or *.domain", i, l.Site)
		}
		if seen[l.Site] {
			return fmt.Errorf("config: browser.logins: %s is listed twice — one entry per site, with every identity it is for", l.Site)
		}
		seen[l.Site] = true
		if len(l.Identities) == 0 {
			return fmt.Errorf("config: browser.logins[%d] (%s) names no identities — a logged-in site is opted in per identity", i, l.Site)
		}
		for _, id := range l.Identities {
			if id == "" || id == "*" {
				return fmt.Errorf("config: browser.logins[%d] (%s): identity %q — name each identity (or a prefix ending in *)", i, l.Site, id)
			}
		}
		for _, a := range l.Also {
			if h, _ := splitHostPortPattern(a); h == "" || h == "*" {
				return fmt.Errorf("config: browser.logins[%d] (%s): also %q is not a host", i, l.Site, a)
			}
		}
		if !browserSiteReachable(l.Site, append(append([]string(nil), b.Allow...), l.Also...)) {
			return fmt.Errorf("config: browser.logins[%d]: %s is not reachable through browser.allow — add it there (the logged-in browser reaches only its sites)", i, l.Site)
		}
	}
	return nil
}

// browserSiteReachable reports whether some allow pattern names the site's
// host (or, for *.domain, the same wildcard or the domain itself).
func browserSiteReachable(site string, allow []string) bool {
	s := strings.ToLower(site)
	base, wild := strings.CutPrefix(s, "*.")
	for _, a := range allow {
		h, _ := splitHostPortPattern(strings.ToLower(strings.TrimSpace(a)))
		switch {
		case h == s:
			return true
		case wild && (h == base || strings.HasSuffix(h, "."+base)):
			return true
		case !wild && strings.HasPrefix(h, "*.") && strings.HasSuffix(s, h[1:]):
			return true
		}
	}
	return false
}

func splitHostPortPattern(p string) (host, port string) {
	if h, pt, err := net.SplitHostPort(p); err == nil {
		return h, pt
	}
	return p, ""
}
