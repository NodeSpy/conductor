package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
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
	// Credentials are sign-ins conductor performs FOR an agent, from your
	// vaults: the agent names one, conductor fills it on the bound origin
	// and the agent never sees a value. Nothing is shared by default.
	Credentials []BrowserCredential `yaml:"credentials,omitempty"`
}

// BrowserCredential binds one sign-in to identities and an origin.
type BrowserCredential struct {
	// Name is what an agent asks for (`conductor browser login --credential`).
	Name string `yaml:"name"`
	// Origin is the exact scheme://host[:port] — and optional path prefix —
	// a fill may happen on. A lookalike host, an embedded frame, or a page a
	// redirect lands on elsewhere is refused.
	Origin string `yaml:"origin"`
	// Identities are the agent sandbox identities that may use it (exact,
	// or a prefix ending in "*"); only for your own requests.
	Identities []string `yaml:"identities"`
	// LoginURL is where `login` starts when the page is elsewhere (on the
	// origin).
	LoginURL string `yaml:"login_url,omitempty"`
	// Username / Password / OTP / Passkey name where each value lives:
	// {vault: <a vaults: entry>, key: <its key>} — never a value. OTP holds
	// a code (1Password: "<item>/one-time password?attribute=otp"), an
	// otpauth:// URI, or a base32 seed; conductor computes the code. Passkey
	// is where `conductor browser passkey register` keeps the credential's
	// dedicated passkey (a writable vault).
	Username *BrowserSecretRef `yaml:"username,omitempty"`
	Password *BrowserSecretRef `yaml:"password,omitempty"`
	OTP      *BrowserSecretRef `yaml:"otp,omitempty"`
	Passkey  *BrowserSecretRef `yaml:"passkey,omitempty"`
	// Approval: the site asks for an approval on your phone after sign-in;
	// conductor notifies you (a needs_input event) and the agent waits.
	Approval bool `yaml:"approval,omitempty"`
}

// BrowserSecretRef names a value in one of the operator's vaults.
type BrowserSecretRef struct {
	Vault string `yaml:"vault"`
	Key   string `yaml:"key"`
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
	names := map[string]bool{}
	for i, cr := range b.Credentials {
		where := fmt.Sprintf("config: browser.credentials[%d] (%s)", i, cr.Name)
		if cr.Name == "" || strings.ContainsAny(cr.Name, " /\t") {
			return fmt.Errorf("config: browser.credentials[%d]: a name (no spaces or slashes) is required", i)
		}
		if names[cr.Name] {
			return fmt.Errorf("config: browser.credentials: %s is defined twice", cr.Name)
		}
		names[cr.Name] = true
		u, err := url.Parse(cr.Origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || strings.Contains(u.Hostname(), "*") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s: origin %q must be exactly scheme://host[:port][/path] (no wildcard)", where, cr.Origin)
		}
		if len(cr.Identities) == 0 {
			return fmt.Errorf("%s names no identities — a credential is shared with named identities only", where)
		}
		for _, id := range cr.Identities {
			if id == "" || id == "*" {
				return fmt.Errorf("%s: identity %q — name each identity (or a prefix ending in *)", where, id)
			}
		}
		if cr.Passkey != nil && (len(cr.Identities) != 1 || strings.HasSuffix(cr.Identities[0], "*")) {
			return fmt.Errorf("%s: a passkey is dedicated to one identity and one site — name exactly one identity (no prefix)", where)
		}
		refs := 0
		for _, r := range []*BrowserSecretRef{cr.Username, cr.Password, cr.OTP, cr.Passkey} {
			if r == nil {
				continue
			}
			refs++
			if r.Vault == "" || r.Key == "" {
				return fmt.Errorf("%s: every value is {vault, key} — a reference to your vaults, never the value", where)
			}
			if _, ok := c.Vaults[r.Vault]; !ok {
				return fmt.Errorf("%s: no vault named %q (vaults:)", where, r.Vault)
			}
		}
		if refs == 0 {
			return fmt.Errorf("%s has nothing to fill", where)
		}
		if cr.LoginURL != "" {
			lu, err := url.Parse(cr.LoginURL)
			if err != nil || lu.Scheme != u.Scheme || !strings.EqualFold(lu.Host, u.Host) {
				return fmt.Errorf("%s: login_url %q is not on the origin %s", where, cr.LoginURL, cr.Origin)
			}
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
		}
		if !browserHostAllowed(b.Allow, u.Hostname(), port) {
			return fmt.Errorf("%s: %s is not reachable through browser.allow", where, cr.Origin)
		}
	}
	return nil
}

// browserHostAllowed reports whether some allow pattern reaches host:port
// (the proxy's own rule: a bare host means :443).
func browserHostAllowed(allow []string, host, port string) bool {
	for _, a := range allow {
		h, p := splitHostPortPattern(strings.ToLower(strings.TrimSpace(a)))
		if p == "" {
			p = "443"
		}
		if p != "*" && p != port {
			continue
		}
		if ok, _ := path.Match(h, strings.ToLower(host)); ok {
			return true
		}
	}
	return false
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
