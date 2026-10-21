package browser

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TestIdentity is the identity a test sign-in runs as: the operator's own,
// bound to nothing but the one credential under test.
const TestIdentity = "operator:test-login"

// TestLogin signs in with a credential in a fresh, sandboxed browser — the
// agents' own pipeline (origin, field and allowlist checks, fresh reads,
// scrubbing), narrowed to that one credential and its hosts — and says how
// it went: ok, failed (why), or needs-you (an approval waits on the
// operator's phone). It returns no value and no screenshot, keeps no
// cookie, and closes the browser.
func (m *Manager) TestLogin(ctx context.Context, name string) TestResult {
	res := m.testLogin(ctx, name)
	res.At = time.Now().UTC()
	if m.Store != nil {
		m.Store.SetTest(name, res)
	}
	m.audit(map[string]any{"event": "browser", "action": "test-login", "credential": name, "by": "operator",
		"outcome": res.Status, "reason": res.Reason})
	return res
}

func (m *Manager) testLogin(ctx context.Context, name string) TestResult {
	st := m.settings()
	var cred *Credential
	for i := range st.Credentials {
		if st.Credentials[i].Name == name {
			cred = &st.Credentials[i]
		}
	}
	if cred == nil {
		return TestResult{Status: TestFailed, Reason: fmt.Sprintf("no credential %q", name)}
	}
	tst, start, err := testSettings(st, *cred)
	if err != nil {
		return TestResult{Status: TestFailed, Reason: err.Error()}
	}

	var rb [8]byte
	_, _ = rand.Read(rb[:])
	ws := filepath.Join(m.StateDir, "test-login", hex.EncodeToString(rb[:]))
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return TestResult{Status: TestFailed, Reason: err.Error()}
	}
	defer os.RemoveAll(ws)
	c := Principal{Key: "test-login:" + hex.EncodeToString(rb[:]), Identity: TestIdentity, Workspace: ws, Requester: RequesterOperator, Agent: "test-login"}
	defer m.dropSession(c.Key)

	if _, err := m.doWith(ctx, c, tst, "open", map[string]any{"url": start}); err != nil {
		return TestResult{Status: TestFailed, Reason: "the sign-in page: " + reason(err)}
	}
	out, err := m.doWith(ctx, c, tst, "login", map[string]any{"credential": name})
	var steps []string
	if out != nil {
		if s, ok := out["steps"].([]string); ok {
			steps = s
		}
	}
	if err != nil {
		return TestResult{Status: TestFailed, Reason: reason(err), Steps: steps}
	}
	if a, _ := out["approval"].(string); strings.HasPrefix(a, "still waiting") {
		return TestResult{Status: TestNeedsYou, Reason: "the site waits for your approval (on your phone); approve it and test again", Steps: steps}
	}
	var check *SignedInCheck
	if m.Store != nil {
		if r, ok, _ := m.Store.Get(name); ok {
			check = r.Check
		}
	}
	ok, why := m.signedIn(ctx, c, check, start)
	if !ok {
		return TestResult{Status: TestFailed, Reason: why, Steps: steps}
	}
	return TestResult{Status: TestOK, Steps: steps}
}

// testSettings is the browser an agent would get for this one credential,
// and where a sign-in starts: the allowlist's entries for its hosts and
// nothing else, the credential bound to the test identity alone, no login
// site, no evaluate.
func testSettings(st Settings, cred Credential) (Settings, string, error) {
	var hosts []string
	start := cred.LoginURL
	for _, so := range cred.AllOrigins() {
		o, err := parseOrigin(so)
		if err != nil {
			return Settings{}, "", err
		}
		hosts = appendSite(hosts, o.host)
		if start == "" {
			start = o.String()
		}
	}
	if len(hosts) == 0 {
		return Settings{}, "", fmt.Errorf("credential %q has no origin", cred.Name)
	}
	tst := st
	cred.Identities = []string{TestIdentity}
	tst.Credentials, tst.Logins, tst.Evaluate = []Credential{cred}, nil, nil
	tst.Allow = loginAllow(st.Allow, hosts, st)
	return tst, start, nil
}

// reason is an error as a test result says it: refusals plainly.
func reason(err error) string {
	return strings.TrimPrefix(strings.TrimPrefix(err.Error(), "browser: refused: "), "browser: ")
}

// dropSession closes a session's browsers and forgets it.
func (m *Manager) dropSession(key string) {
	m.mu.Lock()
	s := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		m.closeSession(s)
		s.mu.Unlock()
	}
}

// signedIn judges the page after a sign-in: the check's URL prefix and
// text when given; by default, that the page left the sign-in page (where
// the test started) and no longer asks for a password or a one-time code.
func (m *Manager) signedIn(ctx context.Context, c Principal, check *SignedInCheck, loginURL string) (bool, string) {
	m.mu.Lock()
	s := m.sessions[c.Key]
	m.mu.Unlock()
	if s == nil {
		return false, "the browser is gone"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.active
	if in == nil {
		return false, "no browser"
	}
	t, err := in.current(ctx)
	if err != nil {
		return false, reason(err)
	}
	u, _ := in.pageInfo(ctx, t)
	u = redactURL(in.scrub(u))
	if check != nil && check.URL != "" && !strings.HasPrefix(u, check.URL) {
		return false, fmt.Sprintf("after signing in the page is %s, not %s…", u, check.URL)
	}
	if check != nil && check.Text != "" {
		text, _, err := in.snapshot(ctx, t, 200000)
		if err != nil || !strings.Contains(in.scrub(text), check.Text) {
			return false, fmt.Sprintf("after signing in the page (%s) does not show %q", u, check.Text)
		}
	}
	if check != nil && (check.URL != "" || check.Text != "") {
		return true, ""
	}
	if in.onSubmitted(u) {
		return false, fmt.Sprintf("the site answered on the page the sign-in was submitted from (%s) — a wrong password or code?", u)
	}
	if lu, err := url.Parse(loginURL); err == nil && loginURL != "" {
		if pu, err := url.Parse(u); err == nil && pu.Host == lu.Host && strings.TrimSuffix(pu.Path, "/") == strings.TrimSuffix(lu.Path, "/") {
			return false, fmt.Sprintf("the site stayed on its sign-in page (%s) — a wrong username or password?", u)
		}
	}
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return false, reason(err)
	}
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return false, reason(err)
	}
	raw, err := in.callIn(ctx, t, w, findFields)
	if err != nil {
		return false, reason(err)
	}
	var found struct{ Password, OTP bool }
	_ = json.Unmarshal(raw, &found)
	switch {
	case found.Password:
		return false, fmt.Sprintf("the site still asks for a password (%s) — a wrong username or password?", u)
	case found.OTP:
		return false, fmt.Sprintf("the site still asks for a one-time code (%s) — a wrong code, or one this login cannot give?", u)
	}
	return true, ""
}

// FieldCheck is one field's resolve check: its source, and whether it
// resolves — never the value.
type FieldCheck struct {
	Field   string `json:"field"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// CheckSources resolves every field of a credential, fresh, as a fill would
// — and reports for each only whether it resolved (and a one-time code's
// secret is usable), never what it resolved to. A secret field read from
// an environment variable is warned about: the environment is plain text
// (and conductor.env, where it usually comes from, is a plain file).
func (m *Manager) CheckSources(ctx context.Context, name string) ([]FieldCheck, error) {
	st := m.settings()
	var cred *Credential
	for i := range st.Credentials {
		if st.Credentials[i].Name == name {
			cred = &st.Credentials[i]
		}
	}
	if cred == nil {
		return nil, fmt.Errorf("browser: no credential %q", name)
	}
	var out []FieldCheck
	check := func(field string, ref *SecretRef, secret bool) {
		if ref == nil {
			return
		}
		fc := FieldCheck{Field: field, Source: ref.Source(), Kind: ref.Kind()}
		v, err := m.resolveRef(ctx, ref)
		switch {
		case err != nil:
			fc.Error = reason(err)
		case v == "":
			fc.Error = "it resolved to nothing"
		case field == "otp":
			if _, oerr := OTPCode(v, time.Now()); oerr != nil {
				fc.Error = "it resolved, but is neither a one-time code, an otpauth:// URI nor a base32 seed"
			} else {
				fc.OK = true
			}
		default:
			fc.OK = true
		}
		if secret && ref.Env != "" {
			fc.Warning = m.plainEnvWarning(ref.Env)
		}
		out = append(out, fc)
	}
	check("username", cred.Username, false)
	check("password", cred.Password, true)
	check("otp", cred.OTP, true)
	if cred.Email != nil {
		if mb, ok := st.Mailboxes[cred.Email.Mailbox]; ok {
			check("mailbox username", mb.Username, false)
			check("mailbox password", mb.Password, true)
		} else {
			out = append(out, FieldCheck{Field: "email code", Error: fmt.Sprintf("no mailbox %q", cred.Email.Mailbox)})
		}
	}
	if cred.Passkey != nil {
		fc := FieldCheck{Field: "passkey", Source: cred.Passkey.Source(), Kind: cred.Passkey.Kind()}
		raw, err := m.resolveRef(ctx, cred.Passkey)
		var pk Passkey
		switch {
		case err != nil:
			fc.Error = reason(err)
		case json.Unmarshal([]byte(raw), &pk) != nil || pk.PrivateKey == "":
			fc.Error = "nothing registered yet (conductor browser passkey register " + name + ")"
		default:
			fc.OK = true
		}
		out = append(out, fc)
	}
	return out, nil
}

// plainEnvWarning is the warning for a secret read from the environment.
func (m *Manager) plainEnvWarning(name string) string {
	w := fmt.Sprintf("${env.%s} is a plain-text environment variable: anything that can read the daemon's environment can read it — prefer a vault reference or a stored value", name)
	if m.EnvFile != "" && envFileDeclares(m.EnvFile, name) {
		w += fmt.Sprintf(" (it is set in %s, a plain file)", m.EnvFile)
	}
	return w
}

// envFileDeclares reports whether an env file sets name (never its value).
func envFileDeclares(path, name string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export "))
		if k, _, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == name {
			return true
		}
	}
	return false
}

// OriginsReachable refuses origins the browser's allowlist cannot reach
// (the same rule the config's credentials follow).
func OriginsReachable(st Settings, origins []string) error {
	for _, s := range origins {
		o, err := parseOrigin(s)
		if err != nil {
			return err
		}
		if err := checkURL(o.scheme+"://"+net.JoinHostPort(o.host, o.port)+"/", st.Allow); err != nil {
			return fmt.Errorf("browser: %s is not reachable through browser.allow — add it there first", s)
		}
	}
	return nil
}

// SiteReachable refuses a logged-in site no allowlist entry names (the
// config's rule for browser.logins: a site is a host, on any listed port;
// *.domain needs the same wildcard, or the domain itself).
func SiteReachable(st Settings, site string, also []string) error {
	s := strings.ToLower(site)
	base, wild := strings.CutPrefix(s, "*.")
	for _, a := range append(append([]string(nil), st.Allow...), also...) {
		h, _ := splitHost(strings.ToLower(strings.TrimSpace(a)))
		switch {
		case h == s:
			return nil
		case wild && (h == base || strings.HasSuffix(h, "."+base)):
			return nil
		case !wild && strings.HasPrefix(h, "*.") && strings.HasSuffix(s, h[1:]):
			return nil
		}
	}
	return fmt.Errorf("browser: %s is not reachable through browser.allow — add it there first", site)
}
