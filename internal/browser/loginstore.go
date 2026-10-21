package browser

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// The login store: sign-ins the operator adds at runtime (the dashboard,
// `conductor browser credential …`), beside the config's
// browser.credentials. Three methods, as the dashboard offers them:
//
//   - password: a username and password (and optionally a TOTP secret or
//     otpauth:// URI) the operator types. Typed values go into the store's
//     own encrypted vault and are WRITE-ONLY: set, replace, remove; nothing
//     reads one back but the fill itself.
//   - vault: references into the operator's vaults (1Password's items
//     picked from a metadata-only listing, pass, HashiCorp, conductor's
//     vault), or ${env.NAME}; each field independently.
//   - session: the operator signs in themself (`conductor browser seed`);
//     the saved session is the login, its expiry recorded where the
//     cookies tell it.
//
// A record holds no value: its fields are sources (a canonical reference,
// or "stored" for a typed value). The store lives in <state>/browser:
//
//	credentials.json         the records (0600)
//	credential-values/       the write-only vault (vault.json + its key, 0700/0600)
//	credential-usage.jsonl   who used which login, when (never a value)

// Methods.
const (
	MethodPassword = "password"
	MethodVault    = "vault"
	MethodSession  = "session"
	// MethodConfig is a browser.credentials entry (read-only here).
	MethodConfig = "config"
)

// Fields a login fills.
var loginFields = []string{"username", "password", "otp"}

// FieldSource is where one field's value comes from — never the value.
type FieldSource struct {
	// Kind is stored (a typed value, write-only), env, vault, or secret.
	Kind string `json:"kind"`
	// Ref is the canonical reference (empty for stored).
	Ref string    `json:"ref,omitempty"`
	Set time.Time `json:"set"`
}

// SignedInCheck is how a test sign-in knows it worked (both optional; by
// default: no sign-in field is left on the page).
type SignedInCheck struct {
	// URL: the page's URL starts with this.
	URL string `json:"url,omitempty"`
	// Text: the page's text contains this.
	Text string `json:"text,omitempty"`
}

// TestResult is a test sign-in's outcome: ok, failed (with why), or
// needs-you (an approval on the operator's phone). Never a value, never a
// screenshot.
type TestResult struct {
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	Steps  []string  `json:"steps,omitempty"`
	At     time.Time `json:"at"`
}

// Test outcomes.
const (
	TestOK       = "ok"
	TestFailed   = "failed"
	TestNeedsYou = "needs-you"
)

// LoginRecord is one stored login.
type LoginRecord struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	// Origins are the exact scheme://host[:port][/path] a fill may happen
	// on (password, vault).
	Origins []string `json:"origins,omitempty"`
	// Bindings are who may use it: agent sandbox identities (exact, or a
	// prefix ending in "*"), bot:<name>, team:<name>.
	Bindings []string               `json:"bindings"`
	LoginURL string                 `json:"login_url,omitempty"`
	Fields   map[string]FieldSource `json:"fields,omitempty"`
	Approval bool                   `json:"approval,omitempty"`
	// Site / Also (session): the logged-in site and the further hosts its
	// browser may reach.
	Site string   `json:"site,omitempty"`
	Also []string `json:"also,omitempty"`
	// Check is how a test sign-in knows it worked.
	Check *SignedInCheck `json:"check,omitempty"`
	// Provenance: when, by whom, through what it was added.
	Added   time.Time `json:"added"`
	AddedBy string    `json:"added_by,omitempty"`
	Via     string    `json:"via,omitempty"`
	Updated time.Time `json:"updated"`
	// Item is the vault item it was picked from (vault method): its vault
	// and title, for the inventory.
	Item     string      `json:"item,omitempty"`
	LastUsed *time.Time  `json:"last_used,omitempty"`
	LastTest *TestResult `json:"last_test,omitempty"`
}

// Usage is one use of a login: who, for what, when — never a value.
type Usage struct {
	Login     string    `json:"login"`
	Identity  string    `json:"identity"`
	Requester string    `json:"requester,omitempty"`
	Dispatch  string    `json:"dispatch,omitempty"`
	Action    string    `json:"action"`
	Outcome   string    `json:"outcome"`
	Origin    string    `json:"origin,omitempty"`
	At        time.Time `json:"at"`
}

// LoginStore is the store at Dir (<state>/browser).
type LoginStore struct {
	Dir string

	mu    sync.Mutex
	vault *secrets.Vault
}

func (s *LoginStore) recordsPath() string { return filepath.Join(s.Dir, "credentials.json") }
func (s *LoginStore) usagePath() string   { return filepath.Join(s.Dir, "credential-usage.jsonl") }
func (s *LoginStore) valuesDir() string   { return filepath.Join(s.Dir, "credential-values") }

// valueKey is where one typed value lives in the store's vault.
func valueKey(name, field string) string { return name + "/" + field }

// loadLocked reads every record.
func (s *LoginStore) loadLocked() (map[string]*LoginRecord, error) {
	out := map[string]*LoginRecord{}
	b, err := os.ReadFile(s.recordsPath())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []*LoginRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("browser: %s: %w", s.recordsPath(), err)
	}
	for _, r := range recs {
		out[r.Name] = r
	}
	return out, nil
}

func (s *LoginStore) saveLocked(recs map[string]*LoginRecord) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	list := make([]*LoginRecord, 0, len(recs))
	for _, r := range recs {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.recordsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.recordsPath())
}

// List returns every record, by name.
func (s *LoginStore) List() ([]LoginRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]LoginRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one record.
func (s *LoginStore) Get(name string) (LoginRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return LoginRecord{}, false, err
	}
	r, ok := recs[name]
	if !ok {
		return LoginRecord{}, false, nil
	}
	return *r, true, nil
}

// CheckLoginName refuses a name an agent couldn't ask for.
func CheckLoginName(name string) error {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, " /\t\r\n\"'\\") {
		return fmt.Errorf("browser: a login's name is 1–64 characters, no spaces, slashes or quotes")
	}
	return nil
}

// CheckBinding refuses a binding that names nobody, or everybody.
func CheckBinding(b string) error {
	b = strings.TrimSpace(b)
	switch {
	case b == "" || b == "*" || strings.TrimSuffix(b, "*") == "":
		return fmt.Errorf("browser: binding %q — name an identity (or a prefix ending in *), bot:<name> or team:<name>", b)
	case strings.HasPrefix(b, "bot:") || strings.HasPrefix(b, "team:"):
		_, n, _ := strings.Cut(b, ":")
		if n == "" || strings.ContainsAny(n, "* /") {
			return fmt.Errorf("browser: binding %q — a bot or team is named exactly", b)
		}
	}
	return nil
}

// checkRecord validates a record before it is saved.
func checkRecord(r *LoginRecord) error {
	if err := CheckLoginName(r.Name); err != nil {
		return err
	}
	if len(r.Bindings) == 0 {
		return fmt.Errorf("browser: login %s is bound to nobody — a login is shared with named identities, bots or teams only", r.Name)
	}
	for _, b := range r.Bindings {
		if err := CheckBinding(b); err != nil {
			return err
		}
	}
	switch r.Method {
	case MethodPassword, MethodVault:
		if len(r.Origins) == 0 {
			return fmt.Errorf("browser: login %s names no origin — the exact scheme://host[:port] it signs in to", r.Name)
		}
		for _, o := range r.Origins {
			if err := CheckOrigin(o); err != nil {
				return err
			}
			if u, _ := url.Parse(o); strings.Contains(u.Hostname(), "*") {
				return fmt.Errorf("browser: login %s: origin %q has a wildcard — origins are exact", r.Name, o)
			}
		}
		if r.LoginURL != "" {
			lu, err := url.Parse(r.LoginURL)
			ok := false
			for _, o := range r.Origins {
				ou, _ := url.Parse(o)
				if err == nil && lu.Scheme == ou.Scheme && strings.EqualFold(lu.Host, ou.Host) {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("browser: login %s: login_url %q is on none of its origins", r.Name, r.LoginURL)
			}
		}
		for f := range r.Fields {
			if !isLoginField(f) {
				return fmt.Errorf("browser: login %s: no field %q (username, password, otp)", r.Name, f)
			}
		}
	case MethodSession:
		if err := CheckSite(r.Site); err != nil {
			return err
		}
		if len(r.Fields) > 0 || len(r.Origins) > 0 {
			return fmt.Errorf("browser: login %s signs in with a saved session — it has no fields or origins", r.Name)
		}
	default:
		return fmt.Errorf("browser: login %s: method %q is password, vault or session", r.Name, r.Method)
	}
	return nil
}

func isLoginField(f string) bool {
	for _, x := range loginFields {
		if x == f {
			return true
		}
	}
	return false
}

// Put adds or replaces a record (its values are set separately). The
// fields' sources are kept as given; Added survives a replace.
func (s *LoginStore) Put(r LoginRecord) error {
	if err := checkRecord(&r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if old, ok := recs[r.Name]; ok {
		r.Added, r.AddedBy, r.Via = old.Added, old.AddedBy, old.Via
		r.LastUsed, r.LastTest = old.LastUsed, old.LastTest
	} else if r.Added.IsZero() {
		r.Added = now
	}
	r.Updated = now
	recs[r.Name] = &r
	return s.saveLocked(recs)
}

// Update changes one record in place.
func (s *LoginStore) Update(name string, fn func(*LoginRecord) error) (LoginRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return LoginRecord{}, err
	}
	r, ok := recs[name]
	if !ok {
		return LoginRecord{}, fmt.Errorf("browser: no login %q", name)
	}
	cp := *r
	if err := fn(&cp); err != nil {
		return LoginRecord{}, err
	}
	if err := checkRecord(&cp); err != nil {
		return LoginRecord{}, err
	}
	cp.Updated = time.Now().UTC()
	recs[name] = &cp
	return cp, s.saveLocked(recs)
}

// Remove revokes a login: the record and every value it stored go. A
// later use is refused (it is no longer in the settings).
func (s *LoginStore) Remove(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	if _, ok := recs[name]; !ok {
		return false, nil
	}
	delete(recs, name)
	if err := s.saveLocked(recs); err != nil {
		return false, err
	}
	v, err := s.openValuesLocked()
	if err != nil {
		return true, err
	}
	changed := false
	for _, f := range loginFields {
		if v.Delete(valueKey(name, f)) {
			changed = true
		}
	}
	if changed {
		return true, v.Save()
	}
	return true, nil
}

// SetValue stores a typed value for one field, write-only: there is no
// way to read it back but filling it. The field's source becomes stored.
func (s *LoginStore) SetValue(name, field, value string) error {
	if !isLoginField(field) {
		return fmt.Errorf("browser: no field %q (username, password, otp)", field)
	}
	if value == "" {
		return fmt.Errorf("browser: an empty %s — remove the field instead", field)
	}
	if field == "otp" {
		if _, err := OTPCode(value, time.Now()); err != nil {
			return fmt.Errorf("browser: the one-time code's secret is neither an otpauth:// URI nor a base32 seed")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	r, ok := recs[name]
	if !ok {
		return fmt.Errorf("browser: no login %q", name)
	}
	if r.Method == MethodSession {
		return fmt.Errorf("browser: login %s signs in with a saved session — it has no fields", name)
	}
	v, err := s.openValuesLocked()
	if err != nil {
		return err
	}
	if err := v.Set(valueKey(name, field), value); err != nil {
		return err
	}
	if err := v.Save(); err != nil {
		return err
	}
	if r.Fields == nil {
		r.Fields = map[string]FieldSource{}
	}
	now := time.Now().UTC()
	r.Fields[field] = FieldSource{Kind: "stored", Set: now}
	r.Updated = now
	return s.saveLocked(recs)
}

// SetRef points one field at a reference (any value source but a typed
// value, which goes through SetValue). A value the field stored before is
// deleted.
func (s *LoginStore) SetRef(name, field, ref string) error {
	if !isLoginField(field) {
		return fmt.Errorf("browser: no field %q (username, password, otp)", field)
	}
	src, err := secrets.ParseSource(ref)
	if err != nil {
		return fmt.Errorf("browser: %s: %w", field, err)
	}
	if !src.IsRef() {
		return fmt.Errorf("browser: %s: not a reference — a typed value is stored write-only (set the value instead)", field)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	r, ok := recs[name]
	if !ok {
		return fmt.Errorf("browser: no login %q", name)
	}
	if r.Method == MethodSession {
		return fmt.Errorf("browser: login %s signs in with a saved session — it has no fields", name)
	}
	if err := s.dropValueLocked(name, field); err != nil {
		return err
	}
	if r.Fields == nil {
		r.Fields = map[string]FieldSource{}
	}
	now := time.Now().UTC()
	r.Fields[field] = FieldSource{Kind: string(src.Kind), Ref: src.String(), Set: now}
	r.Updated = now
	return s.saveLocked(recs)
}

// ClearField removes one field (and its stored value).
func (s *LoginStore) ClearField(name, field string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	r, ok := recs[name]
	if !ok {
		return fmt.Errorf("browser: no login %q", name)
	}
	if err := s.dropValueLocked(name, field); err != nil {
		return err
	}
	delete(r.Fields, field)
	r.Updated = time.Now().UTC()
	return s.saveLocked(recs)
}

func (s *LoginStore) dropValueLocked(name, field string) error {
	v, err := s.openValuesLocked()
	if err != nil {
		return err
	}
	if v.Delete(valueKey(name, field)) {
		return v.Save()
	}
	return nil
}

// HasValue reports whether a typed value is stored (never what it is).
func (s *LoginStore) HasValue(name, field string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.openValuesLocked()
	if err != nil {
		return false
	}
	_, err = v.Get(valueKey(name, field))
	return err == nil
}

// value reads a stored value for a fill — the one read path, unexported:
// only the fill (and a resolve check, which returns nothing of it) calls it.
func (s *LoginStore) value(name, field string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.openValuesLocked()
	if err != nil {
		return "", err
	}
	val, err := v.Get(valueKey(name, field))
	if err != nil {
		return "", fmt.Errorf("browser: login %s has no stored %s", name, field)
	}
	return val, nil
}

// openValuesLocked opens the store's vault: conductor's own vault format
// (one sealed blob, names and count ciphertext), keyed by a random key in
// its own 0600 file — never the operator's vault key chain, and not a
// vaults: entry, so no `conductor vault … get`, template or verb names it.
func (s *LoginStore) openValuesLocked() (*secrets.Vault, error) {
	if s.vault != nil {
		return s.vault, nil
	}
	dir := s.valuesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "vault.key")
	key := func() ([]byte, error) {
		b, err := os.ReadFile(keyPath)
		if os.IsNotExist(err) {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return nil, err
			}
			k := base64.StdEncoding.EncodeToString(raw)
			f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, err
			}
			if _, err := f.WriteString(k + "\n"); err != nil {
				f.Close()
				return nil, err
			}
			if err := f.Close(); err != nil {
				return nil, err
			}
			return []byte(k), nil
		}
		if err != nil {
			return nil, err
		}
		return []byte(strings.TrimSpace(string(b))), nil
	}
	v, err := secrets.OpenVault(filepath.Join(dir, "vault.json"), key)
	if err != nil {
		return nil, fmt.Errorf("browser: the login store's vault: %w", err)
	}
	s.vault = v
	return v, nil
}

// maxUsage bounds the usage history kept.
const maxUsage = 5000

// RecordUse appends one use and stamps the login's last use.
func (s *LoginStore) RecordUse(u Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(u)
	f, err := os.OpenFile(s.usagePath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	s.trimUsageLocked()
	if u.Outcome != "ok" {
		return
	}
	recs, err := s.loadLocked()
	if err != nil {
		return
	}
	if r, ok := recs[u.Login]; ok {
		at := u.At
		r.LastUsed = &at
		_ = s.saveLocked(recs)
	}
}

func (s *LoginStore) trimUsageLocked() {
	st, err := os.Stat(s.usagePath())
	if err != nil || st.Size() < 2<<20 {
		return
	}
	lines := s.usageLinesLocked()
	if len(lines) <= maxUsage {
		return
	}
	lines = lines[len(lines)-maxUsage:]
	tmp := s.usagePath() + ".tmp"
	if os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600) == nil {
		_ = os.Rename(tmp, s.usagePath())
	}
}

func (s *LoginStore) usageLinesLocked() []string {
	f, err := os.Open(s.usagePath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// UsageOf returns a login's uses, newest first, at most limit ("" = every
// login's).
func (s *LoginStore) UsageOf(name string, limit int) []Usage {
	s.mu.Lock()
	lines := s.usageLinesLocked()
	s.mu.Unlock()
	var out []Usage
	for i := len(lines) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		var u Usage
		if json.Unmarshal([]byte(lines[i]), &u) != nil {
			continue
		}
		if name == "" || u.Login == name {
			out = append(out, u)
		}
	}
	return out
}

// SetTest records a login's last test.
func (s *LoginStore) SetTest(name string, t TestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return
	}
	if r, ok := recs[name]; ok {
		r.LastTest = &t
		_ = s.saveLocked(recs)
	}
}

// WithStore adds the store's logins to st: a password or vault login is a
// credential, a session login a login site. A name (or site) the config
// already has stays the config's.
func WithStore(st Settings, recs []LoginRecord) Settings {
	have := map[string]bool{}
	for _, c := range st.Credentials {
		have[c.Name] = true
	}
	sites := map[string]bool{}
	for _, l := range st.Logins {
		sites[l.Site] = true
	}
	st.Credentials = append([]Credential(nil), st.Credentials...)
	st.Logins = append([]LoginSiteConfig(nil), st.Logins...)
	for _, r := range recs {
		switch r.Method {
		case MethodSession:
			if sites[r.Site] {
				continue
			}
			sites[r.Site] = true
			st.Logins = append(st.Logins, LoginSiteConfig{LoginSite: LoginSite{Site: r.Site, Identities: r.Bindings}, Also: r.Also})
		case MethodPassword, MethodVault:
			if have[r.Name] || len(r.Origins) == 0 {
				continue
			}
			have[r.Name] = true
			cr := Credential{Name: r.Name, Origin: r.Origins[0], Origins: r.Origins, Identities: r.Bindings,
				LoginURL: r.LoginURL, Approval: r.Approval, Method: r.Method}
			for field, fs := range r.Fields {
				var ref *SecretRef
				if fs.Kind == "stored" {
					ref = &SecretRef{Stored: valueKey(r.Name, field)}
				} else if src, err := secrets.ParseSource(fs.Ref); err == nil && src.IsRef() {
					ref = RefFromSource(src)
				} else {
					continue // an unreadable source fills nothing
				}
				switch field {
				case "username":
					cr.Username = ref
				case "password":
					cr.Password = ref
				case "otp":
					cr.OTP = ref
				}
			}
			st.Credentials = append(st.Credentials, cr)
		}
	}
	return st
}
