// Package browser is conductor's browser for agents: a headless Chromium
// conductor launches, sandboxed, per agent session, and drives over the
// DevTools protocol on a pipe. The agent never holds the browser — it asks
// conductor (`conductor browser …` over its skill socket, with its token),
// and conductor decides, acts, and audits:
//
//   - The browser's only network is conductor's egress proxy with an
//     allowlist: the operator's browser.allow (plus sandbox.egress and the
//     identity's own). Navigation elsewhere fails at the proxy.
//   - Its profile is ephemeral and anonymous: no cookies, no saved logins.
//     A logged-in site is the operator's explicit opt-in per identity and
//     per site; its saved session goes only into a separate browser that
//     reaches only that site, and only when the session's provenance allows
//     (the operator's own request) — decided at every action.
//   - Files cross only through the workspace, by conductor: downloads land
//     there, uploads come from there, screenshots are written there.
//   - evaluate is off, or read-only by V8's side-effect check.
//
// See docs/design/agent-browser.md.
package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Evaluate modes (what status reports).
const (
	EvaluateOff      = "off"
	EvaluateReadOnly = "read-only"
)

// Settings are the operator's browser: config resolved.
type Settings struct {
	// Chrome is the browser binary ("" = find one).
	Chrome string
	// Allow is the browsing allowlist (browser.allow + sandbox.egress).
	Allow []string
	// Logins are the logged-in sites and the identities each is opted in for.
	Logins []LoginSiteConfig
	// Evaluate are the identities opted in to read-only evaluate (exact, or
	// a prefix ending in "*"); none by default.
	Evaluate []string
	// IdleTimeout closes a session's browsers unused this long.
	IdleTimeout time.Duration
	// MaxBrowsers bounds the browsers running at once.
	MaxBrowsers int
	// MaxDownload / MaxUpload bound one file, in bytes.
	MaxDownload, MaxUpload int64
	// Credentials are the sign-ins the operator shares (by vault reference).
	Credentials []Credential
	// Mailboxes are where emailed codes are read, by name.
	Mailboxes map[string]Mailbox
	// Engine / Image run every browser in a container (Chromium's own
	// sandbox on, inside it); nil runs it in the host's sandbox.
	Engine *ContainerEngine
	Image  string
	// IsolationNote is logged with every browser started without a
	// container on macOS (why, and what that means).
	IsolationNote string
	// Unavailable refuses every launch, with why (a container still being
	// prepared, or required and impossible).
	Unavailable string
	// Profiles are personal details, Cards payment cards (wallet.go), by
	// name; PurchaseTTL how long an approved purchase may be filled.
	Profiles    map[string]Profile
	Cards       map[string]Card
	PurchaseTTL time.Duration
	// BotPurchases / TeamPurchases are bots' and teams' purchase caps, and
	// BotTeam each bot's team (limits.go).
	BotPurchases  map[string]*config.PurchaseLimits
	TeamPurchases map[string]*config.PurchaseLimits
	BotTeam       map[string]string
}

// LoginSiteConfig is one opted-in site: who may use it, and what further
// hosts its logged-in browser may reach (its asset CDN, its SSO).
type LoginSiteConfig struct {
	LoginSite
	Also []string
}

// Manager runs every agent session's browsers.
type Manager struct {
	Settings func() Settings
	// StateDir is conductor's browser state: logins/ (saved sessions) and
	// run/ (each browser's ephemeral profile).
	StateDir string
	SelfExe  func() (string, error)
	RealHome string
	// Audit records every action (store.Audit).
	Audit func(map[string]any)
	// Provenance decides logged-in use (nil = OperatorOnly).
	Provenance Provenance
	Log        func(string, ...any)
	// Launch starts a browser (nil = the sandboxed Launch; tests replace it).
	Launch func(LaunchSpec) (*Process, error)
	// Secrets reads one credential value fresh from a vault (never cached,
	// tainted for the logs' redaction); PutSecret writes one (a passkey's
	// sign count). Notify tells the operator something needs them now (a
	// sign-in to approve on their phone).
	Secrets   func(ctx context.Context, vault, key string) (string, error)
	PutSecret func(ctx context.Context, vault, key, value string) error
	Notify    func(msg string)
	// LookupEnv reads a ${env.NAME} source (nil = os.LookupEnv); Taint
	// marks a value read outside the vaults sensitive (the logs' redaction).
	LookupEnv func(string) (string, bool)
	Taint     func(string)
	// ApprovalWait bounds a sign-in's wait for the operator's approval
	// (0 = 90s).
	ApprovalWait time.Duration
	// EnvFile is conductor.env (a plain file), named in a resolve check's
	// warning when a secret comes from a variable it sets.
	EnvFile string
	// Purchases is the purchase ledger (purchases.go); nil: no card can be
	// used. Paused reports "pause all automation" (no payment while it
	// holds). BotPurchase hands a bot's purchase to its gate (its
	// commit_money tier and spend cap): the bot proposal's id, or
	// unattended (allowed now under the bot's opt-in caps).
	Purchases   *Purchases
	Paused      func() bool
	BotPurchase func(ctx context.Context, c Principal, p Purchase) (proposal string, unattended bool, err error)
	// Budgets reads a ticket's spend_limit (nil: no ticket layer).
	Budgets CaseBudgets
	// OnPurchase tells the operator a purchase waits on them (Needs you).
	OnPurchase func(p Purchase)
	// Store is the logins the operator added at runtime (nil = none): they
	// join the config's credentials and login sites.
	Store *LoginStore

	// usedMail are the messages whose codes were filled: never twice.
	mailMu   sync.Mutex
	usedMail map[string]bool

	mu       sync.Mutex
	sessions map[string]*session
	nextID   int
	closed   bool
}

type session struct {
	id string
	mu sync.Mutex // one action at a time

	anon, login *Instance
	active      *Instance
	sites       []string // the sites the login browser holds
	last        time.Time
	saved       time.Time
	caller      Principal
}

// settings are the operator's settings with the login store's logins.
func (m *Manager) settings() Settings {
	st := m.Settings()
	if m.Store == nil {
		return st
	}
	recs, err := m.Store.List()
	if err != nil {
		m.logf("browser: the login store: %v", err)
		return st
	}
	st = WithStore(st, recs)
	ps, cs, frozen, err := m.Store.Wallet()
	if err != nil {
		m.logf("browser: the wallet: %v", err)
		// Unreadable: no stored profile or card, and every card frozen.
		frozen = map[string]time.Time{}
		for name := range st.Cards {
			frozen[name] = time.Now()
		}
	}
	return WithWallet(st, ps, cs, frozen)
}

// Effective is the settings every browser action sees: the config's and the
// login store's.
func (m *Manager) Effective() Settings { return m.settings() }

// recordUse keeps one use of a login in the store's history (never a value).
func (m *Manager) recordUse(c Principal, name, action, outcome, origin string) {
	if m.Store == nil {
		return
	}
	m.Store.RecordUse(Usage{Login: name, Identity: c.Identity, Requester: c.Requester, Dispatch: c.Dispatch,
		Action: action, Outcome: outcome, Origin: origin, At: time.Now().UTC()})
}

// Logins is the saved-session store.
func (m *Manager) Logins() Logins { return Logins{Dir: filepath.Join(m.StateDir, "logins")} }

func (m *Manager) logf(format string, a ...any) {
	if m.Log != nil {
		m.Log(format, a...)
	}
}

func (m *Manager) audit(e map[string]any) {
	if m.Audit != nil {
		e["event"] = "browser"
		m.Audit(e)
	}
}

// scrubEntry redacts filled secret values from the audit fields that can
// carry text reflected from the page (url, target) or an error reason, so a
// page-echoed or path-embedded secret never reaches the audit sink. It must
// run before the entry is written (m.audit), not after — scrubbing a map the
// sink has already consumed does nothing.
func scrubEntry(inst *Instance, e map[string]any) {
	if inst == nil {
		return
	}
	for _, k := range []string{"url", "target", "reason"} {
		if v, ok := e[k].(string); ok {
			e[k] = inst.scrub(v)
		}
	}
}

// SessionKey is the browser session a token stands for.
func SessionKey(token string) string {
	h := sha256.Sum256([]byte("browser:" + token))
	return hex.EncodeToString(h[:16])
}

// Actions are the agent's verbs.
var Actions = []string{
	"open", "snapshot", "source", "click", "fill", "type", "select", "press", "scroll", "wait",
	"screenshot", "evaluate", "back", "forward", "reload", "tabs", "tab-new", "tab-select",
	"tab-close", "downloads", "upload", "status", "close",
	"login", "fill-credential", "await-approval",
	"profiles", "fill-profile", "fill-form", "cards", "purchase", "purchase-status", "pay",
}

func knownAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// Do runs one action for a caller.
func (m *Manager) Do(ctx context.Context, c Principal, action string, args map[string]any) (map[string]any, error) {
	if !knownAction(action) {
		return nil, fmt.Errorf("browser: unknown action %q (%s)", action, strings.Join(Actions, ", "))
	}
	if c.Workspace == "" || c.Identity == "" || c.Key == "" {
		return nil, fmt.Errorf("browser: conductor's browser is for sandboxed agent launches — this session has no sandbox identity")
	}
	return m.doWith(ctx, c, m.settings(), action, args)
}

// doWith runs one action with these settings (Do's are the operator's;
// a test sign-in narrows them to one credential).
func (m *Manager) doWith(ctx context.Context, c Principal, st Settings, action string, args map[string]any) (map[string]any, error) {
	s, err := m.session(c)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caller = c
	s.last = time.Now()

	granted, why := m.grant(c, st)
	if len(s.sites) > 0 && !sameSites(granted, s.sites) {
		// Provenance (or the operator's opt-in) changed: the logged-in
		// browser goes now, its session saved first.
		m.closeLogin(s)
	}
	entry := map[string]any{"action": action}
	res, err := m.act(ctx, s, c, st, granted, action, args, entry)
	inst := s.active
	entry["identity"], entry["agent"], entry["repo"] = c.Identity, c.Agent, c.Repo
	if c.Dispatch != "" {
		entry["dispatch"] = c.Dispatch
	}
	entry["requester"] = c.Requester
	if inst != nil {
		entry["isolation"] = inst.proc.Isolation()
	}
	entry["logged_in"] = inst != nil && inst == s.login
	if inst != nil && inst == s.login {
		entry["sites"] = s.sites
	}
	switch {
	case errors.Is(err, errRefused):
		entry["outcome"], entry["reason"] = "refused", strings.TrimPrefix(err.Error(), "browser: refused: ")
	case err != nil:
		entry["outcome"], entry["reason"] = "error", clip(err.Error(), 300)
	default:
		entry["outcome"] = "ok"
	}
	// Scrub filled secret values out of the fields that can carry text
	// reflected from the page (the URL, the action target) or an error
	// string, BEFORE the entry reaches the audit sink — not after.
	scrubEntry(inst, entry)
	m.audit(entry)
	if name, _ := entry["credential"].(string); name != "" && (action == "login" || action == "fill-credential") {
		o, _ := entry["origin"].(string)
		m.recordUse(c, name, action, entry["outcome"].(string), o)
	}
	if inst != nil && inst == s.login && time.Since(s.saved) > 30*time.Second {
		m.saveBack(s)
	}
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = map[string]any{}
	}
	res["profile"] = profileName(s, granted)
	if len(granted) == 0 && why != "" {
		res["logged_in_note"] = why
	}
	if inst != nil {
		if d := inst.takeDialogs(); len(d) > 0 {
			res["dialogs"] = d
		}
		// Nothing filled from a credential leaves in a reply.
		inst.scrubAny(res)
	}
	return res, nil
}

var errRefused = errors.New("browser: refused")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errRefused, fmt.Sprintf(format, a...))
}

func profileName(s *session, granted []string) string {
	switch {
	case s.active != nil && s.active == s.login:
		return "the logged-in browser (" + strings.Join(s.sites, ", ") + ")"
	case s.active == nil && len(granted) > 0:
		// Nothing open yet: the first page decides.
		return "none open yet (a page on " + strings.Join(granted, ", ") + " opens in the logged-in browser, any other in the anonymous one)"
	}
	return "anonymous"
}

// grant is the logged-in sites this caller may use now: opted in for its
// identity, a saved session present, and its provenance allowing. why says
// why none were granted when one was opted in.
func (m *Manager) grant(c Principal, st Settings) (sites []string, why string) {
	prov := m.Provenance
	if prov == nil {
		prov = OperatorOnly
	}
	for _, l := range st.Logins {
		if !l.ForPrincipal(c) {
			continue
		}
		if _, ok, err := m.Logins().Load(c.Identity, l.Site); err != nil || !ok {
			if why == "" {
				why = fmt.Sprintf("%s is opted in for this identity but has no saved session (conductor browser seed)", l.Site)
			}
			continue
		}
		if ok, w := prov(c, l.Site); !ok {
			why = w
			continue
		}
		sites = appendSite(sites, l.Site)
	}
	// A credential's host is a logged-in site too, while the credential is
	// usable: whatever conductor signs the agent in to lives in the
	// separate, narrowed browser — never beside an untrusted page.
	for _, cr := range st.Credentials {
		if !cr.ForPrincipal(c) {
			continue
		}
		for _, so := range cr.AllOrigins() {
			o, err := parseOrigin(so)
			if err != nil {
				continue
			}
			if ok, w := prov(c, o.host); !ok {
				why = w
				continue
			}
			sites = appendSite(sites, o.host)
		}
	}
	sort.Strings(sites)
	if len(sites) > 0 {
		why = ""
	}
	return sites, why
}

func appendSite(sites []string, s string) []string {
	for _, x := range sites {
		if x == s {
			return sites
		}
	}
	return append(sites, s)
}

func sameSites(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// session finds or makes the caller's session.
func (m *Manager) session(c Principal) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("browser: conductor is shutting down")
	}
	if m.sessions == nil {
		m.sessions = map[string]*session{}
	}
	s := m.sessions[c.Key]
	if s == nil {
		m.nextID++
		s = &session{id: fmt.Sprintf("b%d", m.nextID), caller: c}
		m.sessions[c.Key] = s
	}
	return s, nil
}

// setSlot stores a session's browser-instance pointer (s.anon / s.login)
// under m.mu. The owning session already holds its own s.mu here; taking
// m.mu as well puts every write of these pointers on the same lock that
// running() reads them under, so a session's start/stop can't race another
// session's MaxBrowsers count. (The owner never holds m.mu at these sites,
// so there is no re-entrancy; lock order stays s.mu → m.mu.)
func (m *Manager) setSlot(slot **Instance, in *Instance) {
	m.mu.Lock()
	*slot = in
	m.mu.Unlock()
}

// running counts live browsers.
func (m *Manager) running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		for _, in := range []*Instance{s.anon, s.login} {
			if in != nil && !isDone(in) {
				n++
			}
		}
	}
	return n
}

func isDone(in *Instance) bool {
	select {
	case <-in.Done():
		return true
	default:
		return false
	}
}

// allowFor is the allowlist of a session's anonymous browser.
func allowFor(c Principal, st Settings) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{st.Allow, c.Egress} {
		for _, a := range list {
			if a = strings.TrimSpace(a); a != "" && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// loginAllow is the allowlist of a logged-in browser: only the entries of
// the browsing allowlist that reach its sites, and each site's also: — an
// untrusted page never shares a browser with the operator's session.
func loginAllow(allow []string, sites []string, st Settings) []string {
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, site := range sites {
		for _, a := range allow {
			h, _ := splitHost(a)
			if h == "*" {
				continue
			}
			if hostCovered(site, h) || (strings.HasPrefix(site, "*.") && h == site) {
				add(a)
			}
		}
		for _, l := range st.Logins {
			if l.Site == site {
				for _, a := range l.Also {
					add(a)
				}
			}
		}
	}
	return out
}

func splitHost(pat string) (host, port string) {
	if h, p, err := net.SplitHostPort(pat); err == nil {
		return strings.ToLower(h), p
	}
	return strings.ToLower(pat), ""
}

// instance returns the session's browser of one kind, starting it if need be.
func (m *Manager) instance(ctx context.Context, s *session, c Principal, st Settings, sites []string) (*Instance, error) {
	slot := &s.anon
	allow := allowFor(c, st)
	if len(sites) > 0 {
		slot = &s.login
		allow = loginAllow(allow, sites, st)
	}
	if *slot != nil && !isDone(*slot) {
		return *slot, nil
	}
	if *slot != nil {
		(*slot).Close()
		m.setSlot(slot, nil)
	}
	if len(allow) == 0 {
		if len(sites) > 0 {
			return nil, refuse("no browser.allow entry reaches %s", strings.Join(sites, ", "))
		}
		return nil, refuse("no site is allowed (browser.allow is empty)")
	}
	if max := st.MaxBrowsers; max > 0 && m.running() >= max {
		return nil, fmt.Errorf("browser: %d browsers are running (browser.max_browsers); try again when one closes", max)
	}
	if st.Unavailable != "" {
		return nil, fmt.Errorf("browser: %s", st.Unavailable)
	}
	var chrome string
	var err error
	if st.Engine == nil {
		if chrome, err = FindChrome(st.Chrome); err != nil {
			return nil, err
		}
	}
	dir, err := RunDir(m.StateDir, s.id)
	if err != nil {
		return nil, err
	}
	// Each refused target is audited once per browser (a page or Chrome
	// retrying cannot flood the audit); the log counts the rest.
	var refusedMu sync.Mutex
	refused := map[string]int{}
	pm := sandbox.NewProxyManager(func(_, hostport string) {
		refusedMu.Lock()
		refused[hostport]++
		n := refused[hostport]
		refusedMu.Unlock()
		if n == 1 {
			m.audit(map[string]any{"action": "egress", "outcome": "refused", "identity": c.Identity, "agent": c.Agent,
				"target": hostport, "logged_in": len(sites) > 0, "reason": "not in the browser's allowlist"})
		}
	})
	launch := m.Launch
	if launch == nil {
		launch = Launch
	}
	ring := &Ring{}
	p, err := launch(LaunchSpec{Chrome: chrome, Dir: dir, Allow: allow, SelfExe: m.SelfExe, Proxy: pm, RealHome: m.RealHome, Stderr: ring,
		Engine: st.Engine, Image: st.Image})
	if err != nil {
		pm.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	go func() { <-p.Done(); pm.Close() }()
	in, err := newInstance(ctx, p, dir, sites)
	if err != nil {
		p.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	// A download belongs to the session that started this browser: its
	// workspace, fixed now (the session's own fields move under its lock).
	loggedIn := len(sites) > 0
	in.onDownload = func(d *Download) { m.landDownload(in, c, loggedIn, d) }
	if len(sites) > 0 {
		var cookies []Cookie
		for _, site := range sites {
			j, ok, err := m.Logins().Load(c.Identity, site)
			if err == nil && ok {
				cookies = append(cookies, j.Cookies...)
			}
		}
		if err := in.setCookies(ctx, cookies); err != nil {
			in.Close()
			return nil, fmt.Errorf("browser: put the saved session in the browser: %w", err)
		}
		s.sites = sites
		s.saved = time.Now()
	}
	m.setSlot(slot, in)
	m.logf("browser %s: started a %s browser for %s (%d allowed host pattern(s); isolation: %s)", s.id, map[bool]string{true: "logged-in", false: "anonymous"}[len(sites) > 0], c.Identity, len(allow), p.Isolation())
	if p.Isolation() == "seatbelt" && st.IsolationNote != "" {
		m.logf("browser %s: WARNING: %s", s.id, st.IsolationNote)
	}
	return in, nil
}

// pick is the browser an action goes to: a navigation to a granted site's
// host goes to the logged-in browser, any other to the anonymous one; every
// other action to the browser the session last navigated.
func (m *Manager) pick(ctx context.Context, s *session, c Principal, st Settings, granted []string, target string) (*Instance, error) {
	if target != "" {
		host := ""
		if u, err := url.Parse(target); err == nil {
			host = u.Hostname()
		}
		var sites []string
		for _, site := range granted {
			if host != "" && hostCovered(site, host) {
				sites = granted
				break
			}
		}
		in, err := m.instance(ctx, s, c, st, sites)
		if err != nil {
			return nil, err
		}
		s.active = in
		return in, nil
	}
	if s.active != nil && !isDone(s.active) && (s.active == s.anon || s.active == s.login) {
		return s.active, nil
	}
	in, err := m.instance(ctx, s, c, st, nil)
	if err != nil {
		return nil, err
	}
	s.active = in
	return in, nil
}

// closeLogin saves and closes a session's logged-in browser.
func (m *Manager) closeLogin(s *session) {
	if s.login == nil {
		return
	}
	m.saveBack(s)
	if s.active == s.login {
		s.active = s.anon
	}
	s.login.Close()
	m.setSlot(&s.login, nil)
	s.sites = nil
}

// saveBack writes a logged-in browser's refreshed session into its jars:
// only the cookies the saved session already had (rotated values, new
// expiry) — an agent can neither add a cookie to the operator's session nor
// clear one from it.
func (m *Manager) saveBack(s *session) {
	in := s.login
	if in == nil || isDone(in) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, err := in.cookies(ctx)
	if err != nil {
		return
	}
	for _, site := range s.sites {
		j, ok, err := m.Logins().Load(s.caller.Identity, site)
		if err != nil || !ok {
			continue
		}
		next, changed := refreshCookies(j.Cookies, FilterCookies(site, live, time.Now()))
		if changed {
			j.Cookies, j.Saved, j.Source = next, time.Now().UTC(), "session"
			if err := m.Logins().Save(j); err != nil {
				m.logf("browser %s: save the session for %s: %v", s.id, site, err)
			}
		}
	}
	s.saved = time.Now()
}

// refreshCookies updates saved cookies from the browser's: same name,
// domain and path — nothing added, nothing removed.
func refreshCookies(saved, live []Cookie) ([]Cookie, bool) {
	key := func(c Cookie) string { return c.Name + "\x00" + strings.TrimPrefix(c.Domain, ".") + "\x00" + c.Path }
	byKey := map[string]Cookie{}
	for _, c := range live {
		byKey[key(c)] = c
	}
	out := make([]Cookie, len(saved))
	changed := false
	for i, c := range saved {
		out[i] = c
		if l, ok := byKey[key(c)]; ok && (l.Value != c.Value || l.Expires != c.Expires) {
			out[i].Value, out[i].Expires = l.Value, l.Expires
			changed = true
		}
	}
	return out, changed
}

// landDownload moves a completed download from the browser's transfer
// directory into the workspace's .browser/downloads, by conductor.
func (m *Manager) landDownload(in *Instance, c Principal, loggedIn bool, d *Download) {
	st := m.settings()
	entry := map[string]any{"action": "download", "identity": c.Identity, "agent": c.Agent, "repo": c.Repo,
		"url": d.URL, "logged_in": loggedIn, "requester": c.Requester}
	// The download URL and any error reason can carry a credential a page
	// reflected into the link's path (M3 scrubs doWith's entries; this path
	// built its own), so scrub every entry here too before it reaches the log.
	audit := func() {
		scrubEntry(in, entry)
		m.audit(entry)
	}
	// A completed download is handled on the browser's event goroutine, off
	// any request: the transfer bounds itself with its own deadline.
	src, err := in.hostDownload(context.Background(), d.GUID, st.MaxDownload)
	if err != nil {
		in.setDownload(d, func(d *Download) { d.State, d.Err = "refused", err.Error() })
		entry["outcome"], entry["reason"] = "refused", err.Error()
		audit()
		return
	}
	defer os.Remove(src)
	if err := refuseSecretFile(src, in.secretValues()); err != nil {
		in.setDownload(d, func(d *Download) { d.State, d.Err = "refused", err.Error() })
		entry["outcome"], entry["reason"] = "refused", err.Error()
		audit()
		return
	}
	f, err := os.Open(src)
	if err == nil {
		defer f.Close()
		var rel string
		rel, err = writeWorkspace(c.Workspace, filepath.Join(OutDir, "downloads", safeName(d.Name)), c.deny(), f, st.MaxDownload)
		if err == nil {
			in.setDownload(d, func(d *Download) { d.Path = rel })
			entry["path"], entry["outcome"] = rel, "ok"
		}
	}
	if err != nil {
		in.setDownload(d, func(d *Download) { d.State, d.Err = "refused", err.Error() })
		entry["outcome"], entry["reason"] = "refused", err.Error()
	}
	audit()
}

// act performs one action.
func (m *Manager) act(ctx context.Context, s *session, c Principal, st Settings, granted []string, action string, args map[string]any, entry map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	num := func(k string, def float64) float64 {
		switch v := args[k].(type) {
		case float64:
			return v
		case string:
			var f float64
			if _, err := fmt.Sscan(v, &f); err == nil {
				return f
			}
		}
		return def
	}
	ref := str("ref")
	if ref != "" {
		entry["ref"] = ref
	}
	switch action {
	case "status":
		out := map[string]any{"session": s.id, "identity": c.Identity, "requester": c.Requester, "evaluate": evalMode(st, c)}
		var opted []string
		for _, l := range st.Logins {
			if l.ForPrincipal(c) {
				opted = append(opted, l.Site)
			}
		}
		out["login_sites_opted_in"] = opted
		out["login_sites_granted"] = granted
		// What a browser runs in, and evaluate as it stands for the current
		// one — before any browser is started too.
		out["isolation"] = plannedIsolation(st, runtime.GOOS)
		if s.active != nil {
			out["isolation"] = s.active.proc.Isolation()
			if s.active.filled() {
				out["evaluate"] = EvaluateOff + " (a credential was filled in this browser)"
			}
		}
		out["allow"] = allowFor(c, st)
		return out, nil
	case "close":
		m.closeSession(s)
		return map[string]any{"closed": true}, nil
	case "profiles", "cards", "purchase", "purchase-status":
		return m.walletInfo(ctx, c, st, action, args, entry)
	}

	var target string
	if action == "open" || action == "tab-new" {
		target = str("url")
		if action == "open" && target == "" {
			return nil, fmt.Errorf("open needs a URL")
		}
		if target != "" {
			entry["target"] = redactURL(target)
			allow := allowFor(c, st)
			if err := checkURL(target, allow); err != nil {
				return nil, err
			}
		}
	}
	route := target
	if action == "login" && route == "" && (s.active == nil || s.active != s.login || isDone(s.active)) {
		// A sign-in starts in the separate, narrowed browser: routed by the
		// credential's own sign-in page, never in whatever browser is open
		// (found live: a login with no page open ran in the anonymous one).
		for _, cr := range st.Credentials {
			if cr.Name == str("credential") {
				route = cr.LoginURL
				if route == "" && len(cr.AllOrigins()) > 0 {
					route = cr.AllOrigins()[0]
				}
			}
		}
	}
	in, err := m.pick(ctx, s, c, st, granted, route)
	if err != nil {
		return nil, err
	}
	if in == s.login {
		// The logged-in browser reaches only its sites.
		if target != "" {
			if err := checkURL(target, loginAllow(allowFor(c, st), s.sites, st)); err != nil {
				return nil, err
			}
		}
	}
	var t *tab
	if action == "tab-new" {
		u := target
		if u == "" {
			u = "about:blank"
		}
		if t, err = in.newTab(ctx, u); err != nil {
			return nil, err
		}
		in.settle(ctx, t, 15*time.Second)
	} else if action == "tab-select" || action == "tab-close" {
		n := int(num("tab", 0))
		if t, err = in.tabAt(n); err != nil {
			return nil, err
		}
		if action == "tab-close" {
			if err := in.conn().Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.id}, nil); err != nil {
				return nil, err
			}
			in.mu.Lock()
			in.removeTabLocked(t.id)
			in.mu.Unlock()
			if t, err = in.current(ctx); err != nil {
				return nil, err
			}
		} else {
			in.mu.Lock()
			in.active = t.id
			in.mu.Unlock()
			if err := in.attach(ctx, t); err != nil {
				return nil, err
			}
			_ = in.conn().Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t.id}, nil)
		}
	} else if t, err = in.current(ctx); err != nil {
		return nil, err
	}
	before := len(in.order)
	out := map[string]any{}
	switch action {
	case "open":
		if err := in.navigate(ctx, t, target); err != nil {
			return nil, err
		}
	case "tab-new", "tab-select", "tab-close":
	case "snapshot":
		max := int(num("max", 25000))
		if max <= 0 || max > 200000 {
			max = 25000
		}
		text, n, err := in.snapshot(ctx, t, max)
		if err != nil {
			return nil, err
		}
		out["snapshot"], out["refs"] = text, n
	case "source":
		max := int(num("max", 25000))
		if max <= 0 || max > 200000 {
			max = 25000
		}
		text, truncated, err := in.source(ctx, t, max)
		if err != nil {
			return nil, err
		}
		out["source"], out["bytes"] = text, len(text)
		if truncated {
			out["truncated"] = true
		}
	case "click":
		if err := in.click(ctx, t, ref); err != nil {
			return nil, err
		}
	case "fill", "type":
		text := str("text")
		entry["chars"] = len(text)
		if action == "fill" {
			err = in.fill(ctx, t, ref, text)
		} else {
			err = in.typeText(ctx, t, ref, text)
		}
		if err != nil {
			return nil, err
		}
		if args["submit"] == true {
			if err := in.key(ctx, t, "Enter"); err != nil {
				return nil, err
			}
			in.settle(ctx, t, 5*time.Second)
		}
	case "select":
		got, err := in.selectOption(ctx, t, ref, str("value"))
		if err != nil {
			return nil, err
		}
		out["selected"] = got
	case "press":
		if ref != "" {
			if _, err := in.focus(ctx, t, ref); err != nil {
				return nil, err
			}
		}
		k := str("key")
		entry["key"] = k
		if err := in.key(ctx, t, k); err != nil {
			return nil, err
		}
		in.settle(ctx, t, 3*time.Second)
	case "scroll":
		if err := in.scroll(ctx, t, ref, num("dy", 600)); err != nil {
			return nil, err
		}
	case "wait":
		d := time.Duration(num("timeout", 10) * float64(time.Second))
		if d <= 0 || d > 60*time.Second {
			d = 60 * time.Second
		}
		if err := in.waitFor(ctx, t, str("text"), str("gone"), str("url"), d); err != nil {
			return nil, err
		}
	case "screenshot":
		png, err := in.screenshot(ctx, t, args["full"] == true)
		if err != nil {
			return nil, err
		}
		if png, err = in.maskSecrets(ctx, t, png, args["full"] == true); err != nil {
			return nil, err
		}
		rel := str("path")
		if rel == "" {
			rel = filepath.Join(OutDir, "screenshots", "screenshot-"+time.Now().UTC().Format("20060102-150405")+".png")
		}
		if !strings.EqualFold(filepath.Ext(rel), ".png") {
			return nil, refuse("a screenshot is written as .png")
		}
		got, err := writeWorkspace(c.Workspace, rel, c.deny(), strings.NewReader(string(png)), 50<<20)
		if err != nil {
			return nil, refuse("screenshot path: %v", err)
		}
		out["path"], out["bytes"] = got, len(png)
		entry["path"] = got
	case "evaluate":
		entry["evaluate_opt_in"] = evalMode(st, c) == EvaluateReadOnly
		if evalMode(st, c) != EvaluateReadOnly {
			return nil, refuse("evaluate is off for this identity (browser.evaluate does not list it) — read the page with snapshot")
		}
		if in.filled() {
			// Even a side-effect-free expression can read a filled field
			// and transform it past any scrub.
			return nil, refuse("evaluate is off in a browser where a credential was filled")
		}
		expr := str("expr")
		entry["chars"] = len(expr)
		v, err := in.evaluate(ctx, t, expr)
		if err != nil {
			if strings.HasPrefix(err.Error(), "refused:") {
				return nil, refuse("%s", strings.TrimPrefix(err.Error(), "refused: "))
			}
			return nil, err
		}
		if len(v) > 20000 {
			v = append(v[:20000:20000], []byte("…")...)
		}
		out["value"] = string(v)
	case "back", "forward":
		delta := -1
		if action == "forward" {
			delta = 1
		}
		if err := in.history(ctx, t, delta); err != nil {
			return nil, err
		}
	case "reload":
		if err := in.reload(ctx, t); err != nil {
			return nil, err
		}
	case "login", "fill-credential":
		name := str("credential")
		entry["credential"] = name
		var so string
		if fr, err := in.mainFrame(ctx, t); err == nil {
			so = fr.SecurityOrigin
		}
		cred, o, err := m.credentialFor(c, st, name, so)
		if err != nil {
			return nil, err
		}
		entry["origin"] = o.String()
		// A credential fills only in the separate browser narrowed to its
		// hosts, never beside an untrusted page.
		notLogin := refuse("a credential is filled only in the separate, logged-in browser — open its sign-in page first (conductor browser open %s), or login --credential %s from no page", o.browserOrigin(), name)
		if action == "login" && in != s.login {
			return nil, notLogin
		}
		allow := allowFor(c, st)
		if in == s.login {
			allow = loginAllow(allow, s.sites, st)
		}
		if action == "login" {
			res, err := m.login(ctx, s, in, t, c, st, cred, o, allow, args["passkey"] == true, ref, entry)
			if err != nil {
				return nil, err
			}
			for k, v := range res {
				out[k] = v
			}
			entry["steps"] = res["steps"]
			break
		}
		field := str("field")
		entry["field"] = field
		fr, err := m.checkPage(ctx, in, t, o, allow)
		if err != nil {
			return nil, err
		}
		if in != s.login {
			return nil, notLogin
		}
		id, err := in.node(t, ref)
		if err != nil {
			return nil, err
		}
		// A code mailed for this step: recent, never one filled before.
		if err := m.fillField(ctx, in, t, fr, o, id, cred, field, time.Now().Add(-5*time.Minute)); err != nil {
			return nil, err
		}
		out["filled"] = field
	case "fill-profile":
		name, field := str("profile"), str("field")
		entry["profile"], entry["field"] = name, field
		allow := allowFor(c, st)
		if in == s.login {
			allow = loginAllow(allow, s.sites, st)
		}
		id, err := in.node(t, ref)
		if err != nil {
			return nil, err
		}
		if err := m.fillProfile(ctx, in, t, c, st, name, field, id, allow, entry); err != nil {
			return nil, err
		}
		out["filled"] = field
	case "fill-form":
		name := str("profile")
		entry["profile"] = name
		only := map[string]bool{}
		for _, f := range strings.Split(str("fields"), ",") {
			if f = strings.TrimSpace(f); f != "" {
				only[f] = true
			}
		}
		allow := allowFor(c, st)
		if in == s.login {
			allow = loginAllow(allow, s.sites, st)
		}
		res, err := m.fillForm(ctx, in, t, c, st, name, only, allow, entry)
		if err != nil {
			return nil, err
		}
		for k, v := range res {
			out[k] = v
		}
	case "pay":
		allow := allowFor(c, st)
		if in == s.login {
			allow = loginAllow(allow, s.sites, st)
		}
		res, err := m.pay(ctx, s, in, t, c, st, args, allow, entry)
		if err != nil {
			return nil, err
		}
		for k, v := range res {
			out[k] = v
		}
	case "await-approval":
		name := str("credential")
		entry["credential"] = name
		cred, _, err := m.credentialFor(c, st, name, "")
		if err != nil {
			return nil, err
		}
		d := time.Duration(num("timeout", 90) * float64(time.Second))
		if d <= 0 || d > 100*time.Second {
			d = 100 * time.Second
		}
		out["approval"] = m.awaitApproval(ctx, in, t, c, cred, d, entry)
	case "tabs":
	case "downloads":
		out["downloads"] = in.downloadList()
	case "upload":
		paths, _ := args["paths"].([]any)
		if len(paths) == 0 {
			if p := str("path"); p != "" {
				paths = []any{p}
			}
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("upload needs a workspace file")
		}
		upDir, err := os.MkdirTemp(filepath.Join(in.dir, "xfer"), "up-")
		if err != nil {
			return nil, err
		}
		var files, rels []string
		for i, p := range paths {
			rel, _ := p.(string)
			sub := filepath.Join(upDir, fmt.Sprint(i))
			if err := os.Mkdir(sub, 0o700); err != nil {
				return nil, err
			}
			dst := filepath.Join(sub, safeName(filepath.Base(rel)))
			if _, err := readWorkspace(c.Workspace, rel, c.deny(), st.MaxUpload, dst); err != nil {
				return nil, refuse("upload %s: %v", rel, err)
			}
			bp, err := in.browserPath(ctx, dst)
			if err != nil {
				return nil, err
			}
			files, rels = append(files, bp), append(rels, rel)
		}
		entry["files"] = rels
		if err := in.setFiles(ctx, t, ref, files); err != nil {
			return nil, err
		}
		out["uploaded"] = rels
	}
	u, title := in.pageInfo(ctx, t)
	entry["url"] = redactURL(u)
	out["url"], out["title"] = redactURL(u), clip(title, 200)
	if action == "tabs" || len(in.order) != before || action == "tab-new" || action == "tab-close" {
		tabs, err := in.tabList(ctx)
		if err == nil {
			out["tabs"] = tabs
		}
		if len(in.order) > before && action != "tab-new" {
			out["note"] = "a new tab opened — conductor browser tabs / tab-select N"
		}
	}
	return out, nil
}

// deny are the workspace paths the browser's files may not touch.
func (c Principal) deny() []string { return c.Deny }

// plannedIsolation is what the next browser runs in.
func plannedIsolation(st Settings, goos string) string {
	switch {
	case st.Unavailable != "":
		return "none: " + st.Unavailable
	case st.Engine != nil:
		return "container"
	case goos == "darwin":
		return "seatbelt"
	default:
		return "namespace"
	}
}

// evalMode is the caller's evaluate: read-only only for an identity the
// operator opted in.
func evalMode(st Settings, c Principal) string {
	if (LoginSite{Identities: st.Evaluate}).For(c.Identity) {
		return EvaluateReadOnly
	}
	return EvaluateOff
}

// checkURL refuses a navigation the allowlist would refuse anyway (the
// proxy is the boundary; this is the clear error) and any scheme but
// http(s) and about:blank.
func checkURL(raw string, allow []string) error {
	if raw == "about:blank" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return refuse("%q is not an http(s) URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return refuse("%s: URLs are http or https", u.Scheme)
	}
	if u.Host == "" {
		return refuse("%q is not an http(s) URL", raw)
	}
	port := u.Port()
	switch u.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return refuse("%s: URLs are http or https", u.Scheme)
	}
	hp := net.JoinHostPort(u.Hostname(), port)
	if !sandbox.EgressAllowed(allow, hp) {
		return refuse("%s is not in this browser's allowlist (browser.allow)", hp)
	}
	return nil
}

// redactURL drops a URL's query, fragment and userinfo — the audit's (and
// the agent's tab list's) view: the place, not its parameters.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return clip(raw, 200)
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return clip(u.String(), 300)
}

// closeSession ends a session's browsers.
func (m *Manager) closeSession(s *session) {
	m.closeLogin(s)
	if s.anon != nil {
		s.anon.Close()
		m.setSlot(&s.anon, nil)
	}
	s.active = nil
}

// Reap closes sessions idle past the timeout. The daemon runs it on a ticker.
func (m *Manager) Reap() {
	st := m.settings()
	idle := st.IdleTimeout
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	m.mu.Lock()
	var stale []*session
	for k, s := range m.sessions {
		if s.mu.TryLock() {
			if time.Since(s.last) > idle {
				stale = append(stale, s)
				delete(m.sessions, k)
			}
			s.mu.Unlock()
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.mu.Lock()
		m.closeSession(s)
		s.mu.Unlock()
		m.logf("browser %s: closed (idle)", s.id)
	}
}

// ReapLoop runs Reap until ctx ends, then closes every browser.
func (m *Manager) ReapLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Close()
			return
		case <-t.C:
			m.Reap()
		}
	}
}

// Close ends every browser (daemon shutdown).
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	ss := m.sessions
	m.sessions = nil
	m.mu.Unlock()
	for _, s := range ss {
		s.mu.Lock()
		m.closeSession(s)
		s.mu.Unlock()
	}
}

// SweepRun removes ephemeral profiles a previous daemon left behind.
func (m *Manager) SweepRun() {
	_ = os.RemoveAll(filepath.Join(m.StateDir, "run"))
}

// SessionInfo describes a live session for the operator.
type SessionInfo struct {
	ID        string    `json:"id"`
	Identity  string    `json:"identity"`
	Agent     string    `json:"agent,omitempty"`
	Requester string    `json:"requester,omitempty"`
	Profile   string    `json:"profile"`
	LastUsed  time.Time `json:"last_used"`
}

// Sessions lists the live sessions (the operator's `conductor browser sessions`).
func (m *Manager) Sessions() []SessionInfo {
	m.mu.Lock()
	ss := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	var out []SessionInfo
	for _, s := range ss {
		s.mu.Lock()
		if s.anon != nil || s.login != nil {
			out = append(out, SessionInfo{ID: s.id, Identity: s.caller.Identity, Agent: s.caller.Agent, Requester: s.caller.Requester, Profile: profileName(s, nil), LastUsed: s.last})
		}
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Peek screenshots a session's active tab for the operator (watching an
// agent's browser). It is audited like an action.
func (m *Manager) Peek(ctx context.Context, id string) ([]byte, string, error) {
	m.mu.Lock()
	var s *session
	for _, x := range m.sessions {
		if x.id == id {
			s = x
		}
	}
	m.mu.Unlock()
	if s == nil {
		return nil, "", fmt.Errorf("browser: no session %q (conductor browser sessions)", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.active
	if in == nil || isDone(in) {
		return nil, "", fmt.Errorf("browser: session %s has no running browser", id)
	}
	t, err := in.current(ctx)
	if err != nil {
		return nil, "", err
	}
	png, err := in.screenshot(ctx, t, false)
	u, _ := in.pageInfo(ctx, t)
	m.audit(map[string]any{"action": "peek", "identity": s.caller.Identity, "url": redactURL(u), "outcome": "ok", "by": "operator"})
	return png, redactURL(u), err
}

// minFileSubstringLen is the shortest filled value the download guard matches
// as a substring. A 2–3 char value (a CVC, a 2-letter region or expiry part)
// collides with almost any file — a price, a date, an order number — so
// refusing a whole file on an incidental hit breaks ordinary downloads. For a
// value that short the guard refuses only a file that IS the value (the page
// offered it back verbatim), never one that merely contains it. Longer values
// stay a substring match: a password echoed anywhere in a file is still caught.
const minFileSubstringLen = 4

// refuseSecretFile refuses a downloaded file holding a value filled from a
// credential (a page offering the password back as a file).
func refuseSecretFile(path string, secrets []string) error {
	if len(secrets) == 0 {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(b)
	trimmed := strings.TrimSpace(content)
	for _, v := range secrets {
		switch {
		case v == "":
			continue
		case len(v) >= minFileSubstringLen:
			if strings.Contains(content, v) {
				return fmt.Errorf("the file contains a value filled from a credential — not written to the workspace")
			}
		case trimmed == v:
			return fmt.Errorf("the file is a value filled from a credential — not written to the workspace")
		}
	}
	return nil
}

// LoginAllow is the allowlist of a browser logged in to sites.
func LoginAllow(st Settings, sites []string) []string { return loginAllow(st.Allow, sites, st) }

// RunDir makes a browser's private directory (its profile, transfer dir,
// HOME). On Linux it lives in conductor's state; on macOS in a fresh 0700
// directory of /private/tmp, so that no component leading to it — each of
// which the browser's Seatbelt profile must let it stat — is the
// operator's home.
func RunDir(stateDir, prefix string) (string, error) {
	return runDir(stateDir, prefix, runtime.GOOS)
}

func runDir(stateDir, prefix, goos string) (string, error) {
	if goos == "darwin" {
		return os.MkdirTemp("/private/tmp", "cbr-"+prefix+"-")
	}
	root := filepath.Join(stateDir, "run")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix+"-")
}
