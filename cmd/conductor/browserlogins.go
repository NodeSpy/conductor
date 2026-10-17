package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// The logins the operator adds at runtime — the backend of the dashboard's
// three ways to add a login, over the control socket (same uid), each also
// a `conductor browser credential …` command:
//
//	browser-login-add      a login: typed values (write-only), references, or a saved session
//	browser-login-set      set or replace one field (a typed value, or any reference)
//	browser-login-unset    remove one field
//	browser-login-bind     add/remove bindings (identity, bot:, team:) and origins
//	browser-login-remove   revoke: the login and every value it stored
//	browser-login-list     the inventory: method, origins, bindings, last used/test, expiry — no value
//	browser-login-usage    who used a login, when — no value
//	browser-login-check    each field resolves? (never the value; a plain-env secret warned)
//	browser-login-test     sign in, sandboxed: ok | failed (why) | needs-you — no value, no screenshot
//	browser-vault-items    a vault's items for the picker — metadata only, never a value
//	browser-vault-item     one item's fields and their sign-in mapping — metadata only
//
// Every change that widens who may use a login or where (a new login, a
// binding, an origin, a site) answers change: "widening" — the dashboard
// asks for a passkey for those — and dry_run reports it without changing
// anything. No answer ever carries a value.

// browserLoginOps are the commands this file answers.
var browserLoginOps = map[string]bool{
	"browser-login-add": true, "browser-login-set": true, "browser-login-unset": true, "browser-login-bind": true,
	"browser-login-remove": true, "browser-login-list": true, "browser-login-usage": true, "browser-login-check": true,
	"browser-login-test": true, "browser-vault-items": true, "browser-vault-item": true,
}

// browserLoginReq is a login command's body.
type browserLoginReq struct {
	Name     string                 `json:"name,omitempty"`
	Method   string                 `json:"method,omitempty"`
	Origins  []string               `json:"origins,omitempty"`
	Bindings []string               `json:"bindings,omitempty"`
	LoginURL string                 `json:"login_url,omitempty"`
	Approval bool                   `json:"approval,omitempty"`
	Site     string                 `json:"site,omitempty"`
	Also     []string               `json:"also,omitempty"`
	Check    *browser.SignedInCheck `json:"check,omitempty"`
	// Values are typed values by field (stored write-only); Refs are
	// references by field (any value source). A field is one or the other.
	Values map[string]string `json:"values,omitempty"`
	Refs   map[string]string `json:"refs,omitempty"`
	// Field names the field set or unset.
	Field string `json:"field,omitempty"`
	// Vault / Item / Query: a vault item to pick from (its fields mapped
	// automatically; Refs override), or to list.
	Vault string `json:"vault,omitempty"`
	Item  string `json:"item,omitempty"`
	Query string `json:"query,omitempty"`
	// Bind / Unbind / AddOrigins / RemoveOrigins change a login's reach.
	Bind          []string `json:"bind,omitempty"`
	Unbind        []string `json:"unbind,omitempty"`
	AddOrigins    []string `json:"add_origins,omitempty"`
	RemoveOrigins []string `json:"remove_origins,omitempty"`
	// DryRun: report the change's classification, change nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Via is who asks (cli, dashboard), for the login's provenance.
	Via   string `json:"via,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// Change classifications.
const (
	changeWidening  = "widening"
	changeNarrowing = "narrowing"
	changeNeutral   = "neutral"
)

// classifyReach compares who and where before and after: anything new is
// widening, only removals narrowing.
func classifyReach(before, after browser.LoginRecord) string {
	added, removed := false, false
	diff := func(a, b []string) {
		for _, x := range b {
			if !contains(a, x) {
				added = true
			}
		}
		for _, x := range a {
			if !contains(b, x) {
				removed = true
			}
		}
	}
	diff(before.Bindings, after.Bindings)
	diff(before.Origins, after.Origins)
	diff(before.Also, after.Also)
	if before.Site != after.Site {
		added = true
	}
	switch {
	case added:
		return changeWidening
	case removed:
		return changeNarrowing
	}
	return changeNeutral
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// loginView is a record as the inventory shows it: no value, only sources.
type loginView struct {
	Name     string                 `json:"name"`
	Method   string                 `json:"method"`
	Origins  []string               `json:"origins,omitempty"`
	Site     string                 `json:"site,omitempty"`
	Bindings []string               `json:"bindings"`
	LoginURL string                 `json:"login_url,omitempty"`
	Fields   map[string]fieldView   `json:"fields,omitempty"`
	Item     string                 `json:"item,omitempty"`
	Added    *time.Time             `json:"added,omitempty"`
	Via      string                 `json:"via,omitempty"`
	LastUsed *time.Time             `json:"last_used,omitempty"`
	LastTest *browser.TestResult    `json:"last_test,omitempty"`
	Sessions []sessionExpiry        `json:"sessions,omitempty"`
	Editable bool                   `json:"editable"`
	Check    *browser.SignedInCheck `json:"check,omitempty"`
	Approval bool                   `json:"approval,omitempty"`
	Extra    map[string]any         `json:"extra,omitempty"`
}

type fieldView struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// sessionExpiry is a saved session's expiry, where its cookies tell it.
type sessionExpiry struct {
	Identity string     `json:"identity"`
	Saved    time.Time  `json:"saved"`
	Expires  *time.Time `json:"expires,omitempty"`
	Note     string     `json:"note,omitempty"`
}

// jarExpiry reads a saved session's expiry from its cookies: the earliest
// persistent cookie's; unknown when one is a session cookie (it lasts as
// long as the site lets it — the cookie doesn't say).
func jarExpiry(j browser.Jar) sessionExpiry {
	e := sessionExpiry{Identity: j.Identity, Saved: j.Saved}
	var earliest float64
	for _, c := range j.Cookies {
		if c.Expires <= 0 {
			e.Note = "unknown: the site keeps the sign-in in a session cookie (no expiry) — test it, or sign in again when it lapses"
			return e
		}
		if earliest == 0 || c.Expires < earliest {
			earliest = c.Expires
		}
	}
	if earliest > 0 {
		t := time.Unix(int64(earliest), 0).UTC()
		e.Expires = &t
		if t.Before(time.Now()) {
			e.Note = "expired: sign in again (conductor browser seed)"
		}
	}
	return e
}

func viewOfRecord(r browser.LoginRecord, jars []browser.Jar) loginView {
	v := loginView{Name: r.Name, Method: r.Method, Origins: r.Origins, Site: r.Site, Bindings: r.Bindings, LoginURL: r.LoginURL,
		Item: r.Item, Via: r.Via, LastUsed: r.LastUsed, LastTest: r.LastTest, Editable: true, Check: r.Check, Approval: r.Approval}
	added := r.Added
	v.Added = &added
	for f, fs := range r.Fields {
		if v.Fields == nil {
			v.Fields = map[string]fieldView{}
		}
		src := fs.Ref
		if fs.Kind == "stored" {
			src = "(stored, write-only)"
		}
		v.Fields[f] = fieldView{Kind: fs.Kind, Source: src}
	}
	if r.Method == browser.MethodSession {
		for _, j := range jars {
			if j.Site == r.Site {
				v.Sessions = append(v.Sessions, jarExpiry(j))
			}
		}
	}
	return v
}

func viewOfCredential(c browser.Credential) loginView {
	v := loginView{Name: c.Name, Method: browser.MethodConfig, Origins: c.AllOrigins(), Bindings: c.Identities, LoginURL: c.LoginURL, Approval: c.Approval}
	for _, f := range []struct {
		name string
		ref  *browser.SecretRef
	}{{"username", c.Username}, {"password", c.Password}, {"otp", c.OTP}, {"passkey", c.Passkey}} {
		if f.ref == nil {
			continue
		}
		if v.Fields == nil {
			v.Fields = map[string]fieldView{}
		}
		v.Fields[f.name] = fieldView{Kind: f.ref.Kind(), Source: f.ref.Source()}
	}
	if c.Email != nil {
		v.Extra = map[string]any{"email_code_mailbox": c.Email.Mailbox}
	}
	return v
}

// checkFieldRef validates one reference a login names: a parseable source,
// and a vault that exists.
func checkFieldRef(field, ref string) error {
	src, err := secrets.ParseSource(ref)
	if err != nil {
		return fmt.Errorf("browser: %s: %w", field, err)
	}
	if !src.IsRef() {
		return fmt.Errorf("browser: %s: not a reference — a typed value goes in as a value, stored write-only", field)
	}
	if src.Vault != "" && vaults.Type(src.Vault) == "" {
		return fmt.Errorf("browser: %s: no vault named %q (vaults:)", field, src.Vault)
	}
	return nil
}

// browserLoginControl answers the login commands.
func browserLoginControl(cfg *config.Config, m *browser.Manager, req controlRequest) controlResponse {
	if m.Store == nil {
		return controlResponse{Error: "browser: no login store on this daemon"}
	}
	st := browserEffective(cfg, m)
	q := req.Login
	if q == nil {
		q = &browserLoginReq{}
	}
	data := func(v any) controlResponse {
		b, _ := json.Marshal(v)
		return controlResponse{OK: true, Data: b}
	}
	fail := func(err error) controlResponse { return controlResponse{Error: err.Error()} }
	audit := func(e map[string]any) {
		if m.Audit != nil {
			e["event"], e["by"] = "browser", "operator"
			if q.Via != "" {
				e["via"] = q.Via
			}
			m.Audit(e)
		}
	}
	inConfig := func(name string) bool {
		if cfg.Browser == nil {
			return false
		}
		for _, c := range cfg.Browser.Credentials {
			if c.Name == name {
				return true
			}
		}
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	switch req.Cmd {
	case "browser-vault-items":
		items, err := vaults.ListItems(ctx, q.Vault, q.Query)
		if err != nil {
			return fail(err)
		}
		return data(map[string]any{"vault": q.Vault, "items": items})
	case "browser-vault-item":
		it, err := vaults.ItemDetail(ctx, q.Vault, q.Item)
		if err != nil {
			return fail(err)
		}
		return data(it)

	case "browser-login-list":
		jars, _ := m.Logins().List()
		var full []browser.Jar
		for _, ji := range jars {
			if j, ok, _ := m.Logins().Load(ji.Identity, ji.Site); ok {
				full = append(full, j)
			}
		}
		recs, err := m.Store.List()
		if err != nil {
			return fail(err)
		}
		var out []loginView
		stored := map[string]bool{}
		for _, r := range recs {
			stored[r.Name] = true
			out = append(out, viewOfRecord(r, full))
		}
		for _, c := range st.Credentials {
			if c.Method == browser.MethodConfig && !stored[c.Name] {
				out = append(out, viewOfCredential(c))
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return data(map[string]any{"logins": out})

	case "browser-login-usage":
		limit := q.Limit
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		return data(map[string]any{"usage": m.Store.UsageOf(q.Name, limit)})

	case "browser-login-check":
		res, err := m.CheckSources(ctx, q.Name)
		if err != nil {
			return fail(err)
		}
		ok := true
		for _, f := range res {
			ok = ok && f.OK
		}
		audit(map[string]any{"action": "credential-check", "credential": q.Name, "outcome": map[bool]string{true: "ok", false: "error"}[ok]})
		return data(map[string]any{"name": q.Name, "ok": ok, "fields": res})

	case "browser-login-test":
		res := m.TestLogin(ctx, q.Name)
		return data(res)

	case "browser-login-add":
		if inConfig(q.Name) {
			return fail(fmt.Errorf("browser: %s is defined in the config (browser.credentials) — pick another name", q.Name))
		}
		if _, ok, _ := m.Store.Get(q.Name); ok {
			return fail(fmt.Errorf("browser: a login named %s exists — set its fields, or remove it first", q.Name))
		}
		rec := browser.LoginRecord{Name: q.Name, Method: q.Method, Origins: q.Origins, Bindings: q.Bindings, LoginURL: q.LoginURL,
			Approval: q.Approval, Site: q.Site, Also: q.Also, Check: q.Check, AddedBy: currentUser(), Via: q.Via}
		refs := map[string]string{}
		if q.Vault != "" && q.Item != "" {
			it, err := vaults.ItemDetail(ctx, q.Vault, q.Item)
			if err != nil {
				return fail(err)
			}
			for f, r := range it.Mapping {
				refs[f] = r
			}
			rec.Item = strings.TrimPrefix(it.Vault+"/"+it.Title, "/")
			if len(rec.Origins) == 0 {
				for _, u := range it.URLs {
					if o := originOf(u); o != "" && !contains(rec.Origins, o) {
						rec.Origins = append(rec.Origins, o)
					}
				}
			}
			if rec.Method == "" {
				rec.Method = browser.MethodVault
			}
		}
		for f, r := range q.Refs {
			refs[f] = r
		}
		for f := range q.Values {
			if _, both := q.Refs[f]; both {
				return fail(fmt.Errorf("browser: %s is a typed value or a reference, not both", f))
			}
			delete(refs, f)
		}
		for f, r := range refs {
			if err := checkFieldRef(f, r); err != nil {
				return fail(err)
			}
		}
		if rec.Method == browser.MethodPassword || rec.Method == browser.MethodVault {
			if err := browser.OriginsReachable(st, rec.Origins); err != nil {
				return fail(err)
			}
			if len(refs)+len(q.Values) == 0 {
				return fail(fmt.Errorf("browser: login %s has nothing to fill — give a username and password (typed or references)", q.Name))
			}
		}
		if rec.Method == browser.MethodSession {
			if err := browser.OriginsReachable(st, []string{"https://" + strings.TrimPrefix(rec.Site, "*.")}); err != nil {
				return fail(err)
			}
		}
		change := classifyReach(browser.LoginRecord{}, rec)
		if q.DryRun {
			return data(map[string]any{"change": change, "dry_run": true})
		}
		if err := m.Store.Put(rec); err != nil {
			return fail(err)
		}
		undo := func() { _, _ = m.Store.Remove(rec.Name) }
		for f, r := range refs {
			if err := m.Store.SetRef(rec.Name, f, r); err != nil {
				undo()
				return fail(err)
			}
		}
		for f, v := range q.Values {
			if err := m.Store.SetValue(rec.Name, f, v); err != nil {
				undo()
				return fail(err)
			}
		}
		saved, _, _ := m.Store.Get(rec.Name)
		audit(map[string]any{"action": "credential-add", "credential": rec.Name, "method": rec.Method, "origins": rec.Origins,
			"site": rec.Site, "bindings": rec.Bindings, "fields": fieldKinds(saved), "change": change, "outcome": "ok"})
		resp := map[string]any{"login": viewOfRecord(saved, nil), "change": change}
		if rec.Method == browser.MethodSession {
			resp["next"] = fmt.Sprintf("sign in yourself: conductor browser seed --identity <one of %s> --site %s", strings.Join(rec.Bindings, ", "), rec.Site)
		}
		return data(resp)

	case "browser-login-set", "browser-login-unset":
		if inConfig(q.Name) {
			return fail(fmt.Errorf("browser: %s is defined in the config — change it there", q.Name))
		}
		if _, ok, _ := m.Store.Get(q.Name); !ok {
			return fail(fmt.Errorf("browser: no login %q", q.Name))
		}
		if q.DryRun {
			return data(map[string]any{"change": changeNeutral, "dry_run": true})
		}
		var err error
		switch {
		case req.Cmd == "browser-login-unset":
			err = m.Store.ClearField(q.Name, q.Field)
		case q.Values[q.Field] != "":
			err = m.Store.SetValue(q.Name, q.Field, q.Values[q.Field])
		case q.Refs[q.Field] != "":
			if err = checkFieldRef(q.Field, q.Refs[q.Field]); err == nil {
				err = m.Store.SetRef(q.Name, q.Field, q.Refs[q.Field])
			}
		case q.LoginURL != "" || q.Check != nil:
			_, err = m.Store.Update(q.Name, func(r *browser.LoginRecord) error {
				if q.LoginURL != "" {
					r.LoginURL = q.LoginURL
				}
				if q.Check != nil {
					r.Check = q.Check
				}
				return nil
			})
		default:
			err = fmt.Errorf("browser: set %s: give a value or a reference for it", q.Field)
		}
		if err != nil {
			return fail(err)
		}
		saved, _, _ := m.Store.Get(q.Name)
		audit(map[string]any{"action": strings.TrimPrefix(req.Cmd, "browser-login-") + "-field", "credential": q.Name, "field": q.Field,
			"fields": fieldKinds(saved), "change": changeNeutral, "outcome": "ok"})
		return data(map[string]any{"login": viewOfRecord(saved, nil), "change": changeNeutral})

	case "browser-login-bind":
		if inConfig(q.Name) {
			return fail(fmt.Errorf("browser: %s is defined in the config — change its identities there", q.Name))
		}
		before, ok, err := m.Store.Get(q.Name)
		if err != nil || !ok {
			return fail(fmt.Errorf("browser: no login %q", q.Name))
		}
		after := before
		after.Bindings = edit(before.Bindings, q.Bind, q.Unbind)
		after.Origins = edit(before.Origins, q.AddOrigins, q.RemoveOrigins)
		for _, b := range q.Bind {
			if err := browser.CheckBinding(b); err != nil {
				return fail(err)
			}
		}
		if err := browser.OriginsReachable(st, q.AddOrigins); err != nil {
			return fail(err)
		}
		change := classifyReach(before, after)
		if q.DryRun {
			return data(map[string]any{"change": change, "dry_run": true})
		}
		saved, err := m.Store.Update(q.Name, func(r *browser.LoginRecord) error {
			r.Bindings, r.Origins = after.Bindings, after.Origins
			return nil
		})
		if err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": "credential-bind", "credential": q.Name, "bindings": saved.Bindings, "origins": saved.Origins, "change": change, "outcome": "ok"})
		return data(map[string]any{"login": viewOfRecord(saved, nil), "change": change})

	case "browser-login-remove":
		if inConfig(q.Name) {
			return fail(fmt.Errorf("browser: %s is defined in the config — remove it there", q.Name))
		}
		rec, ok, _ := m.Store.Get(q.Name)
		if !ok {
			return fail(fmt.Errorf("browser: no login %q", q.Name))
		}
		if q.DryRun {
			return data(map[string]any{"change": changeNarrowing, "dry_run": true})
		}
		if _, err := m.Store.Remove(q.Name); err != nil {
			return fail(err)
		}
		dropped := 0
		if rec.Method == browser.MethodSession {
			jars, _ := m.Logins().List()
			for _, j := range jars {
				if j.Site == rec.Site {
					if ok, _ := m.Logins().Delete(j.Identity, j.Site); ok {
						dropped++
					}
				}
			}
		}
		audit(map[string]any{"action": "credential-remove", "credential": q.Name, "method": rec.Method, "sessions_dropped": dropped, "change": changeNarrowing, "outcome": "ok"})
		return data(map[string]any{"removed": q.Name, "change": changeNarrowing, "sessions_dropped": dropped})
	}
	return controlResponse{Error: "browser: unknown command " + req.Cmd}
}

// edit is xs plus add, minus remove (order kept, no duplicates).
func edit(xs, add, remove []string) []string {
	var out []string
	for _, x := range append(append([]string(nil), xs...), add...) {
		if !contains(remove, x) && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// fieldKinds is a record's fields and their kinds, for the audit.
func fieldKinds(r browser.LoginRecord) map[string]string {
	out := map[string]string{}
	for f, fs := range r.Fields {
		out[f] = fs.Kind
	}
	return out
}

// originOf is a website URL's exact origin ("" when it has none).
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return fmt.Sprint(os.Getuid())
}

const browserCredentialUsage = `usage: conductor browser credential <command>

Add a login (one of three ways):
  add NAME --origin URL --bind WHO [--login-url U] [--approval]      a username and password you type
      [--username VALUE | --username-ref REF]
      [--password-stdin | --password-ref REF]
      [--otp-stdin | --otp-ref REF]                                   a TOTP secret or otpauth:// URI
  add NAME --vault V --item ID --bind WHO [--origin URL]             from a vault item (fields mapped)
      [--username-ref REF] [--password-ref REF] [--otp-ref REF]      (override the mapping)
  add NAME --session --site SITE --bind WHO [--also HOST]            sign in yourself, then: browser seed
      [--check-url PREFIX] [--check-text TEXT] [--dry-run]

  REF is any value source: {{ vault "<vault>" "<key>" }}, {{secret "<vault>/<key>"}}, ${env.NAME}.
  WHO is an identity (or a prefix ending in *), bot:<name> or team:<name>.
  --origin, --bind, --also repeat (or take a comma-separated list). A typed
  value is read from stdin (no echo on a terminal) and stored write-only:
  nothing reads it back.

Then:
  ls                                     the inventory (no values)
  usage [NAME] [--limit N]               who used it, when
  check NAME                             does every field resolve? (never the value)
  test NAME                              sign in, sandboxed: ok | failed | needs-you
  set NAME FIELD (--stdin | --ref REF)   replace one field (username, password, otp)
  unset NAME FIELD
  bind NAME [--bind WHO] [--unbind WHO] [--add-origin URL] [--remove-origin URL] [--dry-run]
  rm NAME [--dry-run]                    revoke: the login and every value it stored

Pick from a vault (metadata only, never a value):
  conductor browser vault-items VAULT [--query Q]
  conductor browser vault-item VAULT ITEM
`

// credFlags parses a credential command's flags: repeatable, comma lists.
func credFlags(args []string) (pos []string, multi map[string][]string, bools map[string]bool) {
	multi, bools = map[string][]string{}, map[string]bool{}
	boolean := map[string]bool{"approval": true, "session": true, "dry-run": true, "stdin": true, "password-stdin": true, "otp-stdin": true, "json": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || len(a) == 2 {
			pos = append(pos, a)
			continue
		}
		k := strings.TrimPrefix(a, "--")
		if key, v, ok := strings.Cut(k, "="); ok {
			multi[key] = append(multi[key], v)
			continue
		}
		if boolean[k] {
			bools[k] = true
			continue
		}
		if i+1 < len(args) {
			i++
			multi[k] = append(multi[k], args[i])
		}
	}
	return pos, multi, bools
}

// list is a repeatable flag's values, comma lists split.
func list(multi map[string][]string, k string) []string {
	var out []string
	for _, v := range multi[k] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func one(multi map[string][]string, k string) string {
	if v := multi[k]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

// readSecret reads one typed value from stdin: on a terminal, prompted with
// no echo; otherwise one line.
func readSecret(in *bufio.Reader, prompt string) (string, error) {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprint(os.Stderr, prompt+": ")
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		if off.Run() == nil {
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = os.Stdin
				_ = on.Run()
				fmt.Fprintln(os.Stderr)
			}()
		}
	}
	line, err := in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("reading the %s from stdin: %v", prompt, err)
	}
	v := strings.TrimRight(line, "\r\n")
	if v == "" {
		return "", fmt.Errorf("an empty %s", prompt)
	}
	return v, nil
}

// browserCredentialCmd is `conductor browser credential …`.
func browserCredentialCmd(rest []string) error {
	if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" {
		fmt.Fprint(os.Stderr, browserCredentialUsage)
		return nil
	}
	sub := rest[0]
	pos, multi, bools := credFlags(rest[1:])
	sock := map[string]any{}
	for _, k := range []string{"config", "socket"} {
		if v := one(multi, k); v != "" {
			sock[k] = v
		}
	}
	call := func(cmd string, q browserLoginReq) (json.RawMessage, error) {
		q.Via = "cli"
		resp, err := browserCtl(sock, controlRequest{Cmd: cmd, Login: &q})
		if err != nil {
			return nil, err
		}
		return resp.Data, nil
	}
	show := func(raw json.RawMessage, err error) error {
		if err != nil {
			return err
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		return printJSON(v)
	}
	name := func() (string, error) {
		if len(pos) < 1 {
			return "", fmt.Errorf("name the login (conductor browser credential %s NAME …)", sub)
		}
		return pos[0], nil
	}
	stdin := bufio.NewReader(os.Stdin)
	switch sub {
	case "ls", "list":
		return show(call("browser-login-list", browserLoginReq{}))
	case "usage":
		q := browserLoginReq{}
		if len(pos) > 0 {
			q.Name = pos[0]
		}
		fmt.Sscan(one(multi, "limit"), &q.Limit)
		return show(call("browser-login-usage", q))
	case "check", "test", "rm", "remove":
		n, err := name()
		if err != nil {
			return err
		}
		cmd := map[string]string{"check": "browser-login-check", "test": "browser-login-test", "rm": "browser-login-remove", "remove": "browser-login-remove"}[sub]
		return show(call(cmd, browserLoginReq{Name: n, DryRun: bools["dry-run"]}))
	case "add":
		n, err := name()
		if err != nil {
			return err
		}
		q := browserLoginReq{Name: n, Origins: list(multi, "origin"), Bindings: list(multi, "bind"), LoginURL: one(multi, "login-url"),
			Approval: bools["approval"], Site: one(multi, "site"), Also: list(multi, "also"), Vault: one(multi, "vault"), Item: one(multi, "item"),
			DryRun: bools["dry-run"], Refs: map[string]string{}, Values: map[string]string{}}
		if u, t := one(multi, "check-url"), one(multi, "check-text"); u != "" || t != "" {
			q.Check = &browser.SignedInCheck{URL: u, Text: t}
		}
		switch {
		case bools["session"]:
			q.Method = browser.MethodSession
		case q.Vault != "":
			q.Method = browser.MethodVault
		default:
			q.Method = browser.MethodPassword
		}
		for _, f := range []string{"username", "password", "otp"} {
			if r := one(multi, f+"-ref"); r != "" {
				q.Refs[f] = r
			}
		}
		if u := one(multi, "username"); u != "" {
			q.Values["username"] = u
		}
		for _, f := range []string{"password", "otp"} {
			if bools[f+"-stdin"] {
				v, err := readSecret(stdin, f)
				if err != nil {
					return err
				}
				q.Values[f] = v
			}
		}
		if q.Method == browser.MethodPassword && len(q.Refs) == 0 && q.Values["password"] == "" && !q.DryRun {
			v, err := readSecret(stdin, "password")
			if err != nil {
				return err
			}
			q.Values["password"] = v
		}
		if len(q.Values) > 0 && q.Method == browser.MethodVault {
			q.Method = browser.MethodPassword
		}
		return show(call("browser-login-add", q))
	case "set", "unset":
		n, err := name()
		if err != nil {
			return err
		}
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor browser credential %s NAME FIELD …", sub)
		}
		q := browserLoginReq{Name: n, Field: pos[1], DryRun: bools["dry-run"]}
		if sub == "set" {
			switch {
			case one(multi, "ref") != "":
				q.Refs = map[string]string{q.Field: one(multi, "ref")}
			case bools["stdin"]:
				v, err := readSecret(stdin, q.Field)
				if err != nil {
					return err
				}
				q.Values = map[string]string{q.Field: v}
			default:
				return fmt.Errorf("set %s: --stdin (a typed value, stored write-only) or --ref REF", q.Field)
			}
			return show(call("browser-login-set", q))
		}
		return show(call("browser-login-unset", q))
	case "bind":
		n, err := name()
		if err != nil {
			return err
		}
		return show(call("browser-login-bind", browserLoginReq{Name: n, Bind: list(multi, "bind"), Unbind: list(multi, "unbind"),
			AddOrigins: list(multi, "add-origin"), RemoveOrigins: list(multi, "remove-origin"), DryRun: bools["dry-run"]}))
	}
	return fmt.Errorf("unknown credential command %q (conductor browser credential --help)", sub)
}

// browserVaultCmd is `conductor browser vault-items|vault-item`.
func browserVaultCmd(cmd string, rest []string) error {
	pos, multi, _ := credFlags(rest)
	sock := map[string]any{}
	for _, k := range []string{"config", "socket"} {
		if v := one(multi, k); v != "" {
			sock[k] = v
		}
	}
	q := browserLoginReq{Query: one(multi, "query"), Via: "cli"}
	if len(pos) > 0 {
		q.Vault = pos[0]
	}
	op := "browser-vault-items"
	if cmd == "vault-item" {
		if len(pos) != 2 {
			return fmt.Errorf("usage: conductor browser vault-item VAULT ITEM")
		}
		q.Item, op = pos[1], "browser-vault-item"
	} else if len(pos) != 1 {
		return fmt.Errorf("usage: conductor browser vault-items VAULT [--query Q]")
	}
	resp, err := browserCtl(sock, controlRequest{Cmd: op, Login: &q})
	if err != nil {
		return err
	}
	var v any
	_ = json.Unmarshal(resp.Data, &v)
	return printJSON(v)
}
