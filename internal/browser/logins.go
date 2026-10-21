package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
)

// Requester classes: who asked for the work a session does, least trusted
// first. They mirror the member gate's (a third-party user, an event with no
// human, the member's own initiative, an operator). Only an operator's own
// request may act logged in.
const (
	RequesterUser     = "user"
	RequesterEvent    = "event"
	RequesterMember   = "member"
	RequesterOperator = "operator"
)

// Principal is who a browser action is for: what the agent's conductor token
// stands for, resolved by the daemon — never asserted by the agent.
type Principal struct {
	// Key is the session the browser belongs to (a hash of the token: each
	// launch's token gets its own browser).
	Key string
	// Identity is the agent sandbox identity (agentsandbox): logged-in sites
	// are opted in for it.
	Identity string
	// Workspace is where screenshots and downloads land and uploads come
	// from; nothing outside it.
	Workspace string
	// Egress is the identity's own allowlist beyond sandbox.egress (its
	// registry entry's), joined to browser.allow for its browser.
	Egress []string
	// Deny are paths inside the workspace the browser's files may not touch
	// (the workspace's read-only and hidden carve-outs).
	Deny []string
	// Requester is who asked for the work ("" = unknown: least trust).
	Requester string
	// Agent / Repo / Dispatch label the audit.
	Agent, Repo, Dispatch string
	// Groups are the bots and teams the session acts for ("bot:<name>",
	// "team:<name>"): a login bound to one of them is bound to it.
	Groups []string
	// BotLogins / BotCredentials narrow a bot session to a subset of the
	// logins (sites) and sign-in credentials (names) its identity and groups
	// would otherwise reach (bots.<name>.browser). nil means no narrowing (a
	// plain agent, or a bot with no browser block); a non-nil list — even
	// empty — caps the session to exactly its entries. It never grants.
	BotLogins, BotCredentials []string
	// Bot / BotRole / CaseID are a bot session's binding (a purchase goes
	// through the bot's gate).
	Bot, BotRole, CaseID string
}

// Provenance decides whether a caller may use its logged-in session on a
// site: ok, or why not. It runs at every action — a member's home session
// whose open cases change gets the answer for now, not for when its
// browser started.
type Provenance func(c Principal, site string) (ok bool, why string)

// OperatorOnly is the default provenance rule: a logged-in site is used
// only for the operator's own request. An event, a third party, a member's
// own initiative, or a session whose requester is unknown gets the
// anonymous profile.
func OperatorOnly(c Principal, _ string) (bool, string) {
	if c.Requester == RequesterOperator {
		return true, ""
	}
	who := c.Requester
	if who == "" {
		who = "an unknown requester"
	}
	return false, fmt.Sprintf("this session's work was requested by %s, not the operator — a logged-in site is used only for the operator's own requests", who)
}

// LoginSite is one site an operator opted in for logged-in use, and the
// identities it is opted in for.
type LoginSite struct {
	// Site is a host ("app.example.com") or a domain with its subdomains
	// ("*.example.com"): only cookies for it are imported, kept, and put in
	// a browser.
	Site string
	// Identities are the agent sandbox identities that may use it: exact, or
	// a prefix ending in "*".
	Identities []string
}

// For reports whether the site is opted in for identity.
func (l LoginSite) For(identity string) bool {
	for _, p := range l.Identities {
		if p == identity {
			return true
		}
		if pre, ok := strings.CutSuffix(p, "*"); ok && pre != "" && strings.HasPrefix(identity, pre) {
			return true
		}
	}
	return false
}

// ForPrincipal reports whether the site is opted in for the caller: its
// identity, or a bot or team it acts for (an exact bot:/team: binding), and —
// for a real login site — within the bot's own browser.logins narrowing.
func (l LoginSite) ForPrincipal(p Principal) bool {
	if !l.grantedTo(p) {
		return false
	}
	// A bot that narrows bots.<name>.browser.logins may use only the sites it
	// kept, even when a team: binding would otherwise reach a broader one — the
	// narrowing never grants, it only caps. Site is "" for the Credential /
	// wallet wrappers that reuse this method; their narrowing is by name.
	if l.Site != "" && p.BotLogins != nil && !siteWithin(p.BotLogins, l.Site) {
		return false
	}
	return true
}

// grantedTo is the identity/group binding check, without any bot narrowing.
func (l LoginSite) grantedTo(p Principal) bool {
	if l.For(p.Identity) {
		return true
	}
	for _, b := range l.Identities {
		if !strings.HasPrefix(b, "bot:") && !strings.HasPrefix(b, "team:") {
			continue
		}
		for _, g := range p.Groups {
			if g == b {
				return true
			}
		}
	}
	return false
}

// siteWithin reports whether a login site is covered by a narrowing list: an
// exact entry, or a "*.domain" entry covering a host (or narrower pattern)
// under it — the same coverage config uses when it validates the subset.
func siteWithin(list []string, site string) bool {
	for _, s := range list {
		if s == site {
			return true
		}
		if dom, ok := strings.CutPrefix(s, "*."); ok {
			if h := strings.TrimPrefix(site, "*."); strings.HasSuffix(h, "."+dom) {
				return true
			}
		}
	}
	return false
}

// CheckSite refuses a site that is not a plain host or *.domain.
func CheckSite(site string) error {
	s := strings.TrimPrefix(site, "*.")
	if s == "" || strings.ContainsAny(s, ":/ *@?#") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return fmt.Errorf("browser: login site %q is not a host (app.example.com) or *.domain", site)
	}
	return nil
}

// siteCovers reports whether a cookie for domain belongs to site.
func siteCovers(site, domain string) bool {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	s := strings.ToLower(site)
	if base, ok := strings.CutPrefix(s, "*."); ok {
		return d == base || strings.HasSuffix(d, "."+base)
	}
	return d != "" && d == s
}

// hostCovered reports whether a request to host belongs to site.
func hostCovered(site, host string) bool { return siteCovers(site, host) }

// Cookie is a browser cookie (CDP Network.CookieParam).
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"` // seconds since the epoch; 0 = a session cookie
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// FilterCookies keeps only site's unexpired cookies — the rest of an import
// or a browser's jar never reaches the store.
func FilterCookies(site string, cs []Cookie, now time.Time) []Cookie {
	var out []Cookie
	for _, c := range cs {
		if c.Name == "" || !siteCovers(site, c.Domain) {
			continue
		}
		if c.Expires < 0 {
			c.Expires = 0
		}
		if c.Expires > 0 && c.Expires < float64(now.Unix()) {
			continue
		}
		switch c.SameSite {
		case "", "Strict", "Lax", "None":
		default:
			c.SameSite = ""
		}
		if c.Path == "" {
			c.Path = "/"
		}
		out = append(out, c)
	}
	return out
}

// Jar is one identity's saved session for one site.
type Jar struct {
	Identity string    `json:"identity"`
	Site     string    `json:"site"`
	Saved    time.Time `json:"saved"`
	// Source is how it was seeded: import, login (the operator's headed
	// takeover), or session (written back from a logged-in browser).
	Source  string   `json:"source"`
	Cookies []Cookie `json:"cookies"`
}

// JarInfo describes a jar without its values.
type JarInfo struct {
	Identity string    `json:"identity"`
	Site     string    `json:"site"`
	Saved    time.Time `json:"saved"`
	Source   string    `json:"source"`
	Cookies  int       `json:"cookies"`
}

// Logins is the store of saved sessions: <state>/browser/logins, mode 0700,
// each jar 0600. It is conductor's: no agent's sandbox shows it, and only a
// logged-in browser gets the cookies in it.
type Logins struct{ Dir string }

func (l Logins) path(identity, site string) string {
	return filepath.Join(l.Dir, agentsandbox.IdentityKey(identity), agentsandbox.IdentityKey(site)+".json")
}

// Load reads a jar; ok=false when there is none.
func (l Logins) Load(identity, site string) (Jar, bool, error) {
	b, err := os.ReadFile(l.path(identity, site))
	if os.IsNotExist(err) {
		return Jar{}, false, nil
	}
	if err != nil {
		return Jar{}, false, err
	}
	var j Jar
	if err := json.Unmarshal(b, &j); err != nil {
		return Jar{}, false, fmt.Errorf("browser: saved session %s: %w", l.path(identity, site), err)
	}
	if j.Identity != identity || j.Site != site {
		// Two names hashing to one key is not this identity's jar.
		return Jar{}, false, nil
	}
	j.Cookies = FilterCookies(site, j.Cookies, time.Now())
	return j, true, nil
}

// Save writes a jar holding only the site's cookies, replacing any old one
// by rename.
func (l Logins) Save(j Jar) error {
	if err := CheckSite(j.Site); err != nil {
		return err
	}
	if j.Identity == "" {
		return fmt.Errorf("browser: a saved session needs an identity")
	}
	j.Cookies = FilterCookies(j.Site, j.Cookies, time.Now())
	if j.Saved.IsZero() {
		j.Saved = time.Now().UTC()
	}
	p := l.path(j.Identity, j.Site)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(l.Dir, 0o700)
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".jar-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Delete removes a jar (logging the identity out of the site).
func (l Logins) Delete(identity, site string) (bool, error) {
	err := os.Remove(l.path(identity, site))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// List describes every saved jar.
func (l Logins) List() ([]JarInfo, error) {
	var out []JarInfo
	err := filepath.WalkDir(l.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var j Jar
		if json.Unmarshal(b, &j) != nil {
			return nil
		}
		out = append(out, JarInfo{Identity: j.Identity, Site: j.Site, Saved: j.Saved, Source: j.Source, Cookies: len(FilterCookies(j.Site, j.Cookies, time.Now()))})
		return nil
	})
	sort.Slice(out, func(i, k int) bool {
		if out[i].Identity != out[k].Identity {
			return out[i].Identity < out[k].Identity
		}
		return out[i].Site < out[k].Site
	})
	return out, err
}

// ParseCookies reads an operator's cookie export: a JSON array of cookies
// (CDP/Playwright/browser-extension shape: name, value, domain, path,
// expires|expirationDate, httpOnly, secure, sameSite) or a Netscape
// cookies.txt.
func ParseCookies(b []byte) ([]Cookie, error) {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
		var raw []map[string]any
		if strings.HasPrefix(s, "{") {
			// {"cookies": [...]} (a Playwright storage state).
			var wrap struct {
				Cookies []map[string]any `json:"cookies"`
			}
			if err := json.Unmarshal(b, &wrap); err != nil {
				return nil, fmt.Errorf("browser: cookies: %w", err)
			}
			raw = wrap.Cookies
		} else if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("browser: cookies: %w", err)
		}
		var out []Cookie
		for _, m := range raw {
			c := Cookie{}
			c.Name, _ = m["name"].(string)
			c.Value, _ = m["value"].(string)
			c.Domain, _ = m["domain"].(string)
			c.Path, _ = m["path"].(string)
			c.HTTPOnly, _ = m["httpOnly"].(bool)
			c.Secure, _ = m["secure"].(bool)
			if v, ok := m["expires"].(float64); ok {
				c.Expires = v
			} else if v, ok := m["expirationDate"].(float64); ok {
				c.Expires = v
			}
			if v, ok := m["sameSite"].(string); ok {
				switch strings.ToLower(v) {
				case "strict":
					c.SameSite = "Strict"
				case "lax":
					c.SameSite = "Lax"
				case "none", "no_restriction":
					c.SameSite = "None"
				}
			}
			out = append(out, c)
		}
		return out, nil
	}
	var out []Cookie
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		httpOnly := false
		if rest, ok := strings.CutPrefix(line, "#HttpOnly_"); ok {
			line, httpOnly = rest, true
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			return nil, fmt.Errorf("browser: cookies.txt: a line without 7 tab-separated fields")
		}
		var exp float64
		fmt.Sscan(f[4], &exp)
		out = append(out, Cookie{Domain: f[0], Path: f[2], Secure: strings.EqualFold(f[3], "TRUE"), Expires: exp, Name: f[5], Value: f[6], HTTPOnly: httpOnly})
	}
	return out, nil
}

// SiteCovers reports whether a host (or a cookie's domain) belongs to site.
func SiteCovers(site, host string) bool { return siteCovers(site, host) }
