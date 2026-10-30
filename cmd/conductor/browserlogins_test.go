package main

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/vaults"
)

const (
	fixturePassword = "Fixture-Typed-Pa55!"
	fixtureSeed     = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXQ"
	fixtureOPPass   = "Fixture-OP-Pa55word!"
	fixtureOPUser   = "fixture-op-user@example.test"
	fixtureOPTOTP   = "583201"
)

const fakeOPGet = `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","title":"Example App","category":"LOGIN","vault":{"name":"Agents"},
 "urls":[{"href":"https://app.example.com/login?next=/"}],
 "fields":[
  {"id":"username","type":"STRING","purpose":"USERNAME","label":"username","value":"` + fixtureOPUser + `","reference":"op://Agents/Example App/username"},
  {"id":"password","type":"CONCEALED","purpose":"PASSWORD","label":"password","value":"` + fixtureOPPass + `","reference":"op://Agents/Example App/password"},
  {"id":"t","type":"OTP","label":"one-time password","value":"otpauth://totp/x?secret=` + fixtureSeed + `","totp":"` + fixtureOPTOTP + `","reference":"op://Agents/Example App/one-time password"}]}`

func loginRig(t *testing.T) (call func(cmd string, q browserLoginReq) controlResponse, m *browser.Manager, audit *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Browser: &config.BrowserConfig{
		Allow: []string{"app.example.com", "login.example.com", "sso.example.com", "localhost:18090"},
		Credentials: []config.BrowserCredential{{Name: "configured", Origin: "https://app.example.com", Identities: []string{"agent:t@o/r"},
			Password: &config.BrowserSecretRef{Vault: "op", Key: "Agents/Example App/password"}}},
	}}
	cfg.Store.StateFile = filepath.Join(dir, "state.json")
	browserCfg.Store(cfg)
	var a []map[string]any
	m = &browser.Manager{StateDir: filepath.Join(dir, "browser"), Store: &browser.LoginStore{Dir: filepath.Join(dir, "browser")},
		Settings: func() browser.Settings { return browserSettings(cfg) },
		Audit:    func(e map[string]any) { a = append(a, e) }}
	browserMgr.Store(m)
	t.Cleanup(func() { browserMgr.Store(nil); browserCfg.Store(nil) })
	vaults.Reset()
	t.Cleanup(vaults.Reset)
	op := &vaults.OnePasswordVault{Exec: func(ctx context.Context, stdin string, env []string, name string, args ...string) (string, error) {
		switch strings.Join(args[:2], " ") {
		case "item list":
			return `[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","title":"Example App","category":"LOGIN","vault":{"name":"Agents"},"urls":[{"href":"https://app.example.com/login"}]}]`, nil
		case "item get":
			return fakeOPGet, nil
		}
		t.Fatalf("op %v: a listing reads no value", args)
		return "", nil
	}}
	vaults.Register("op", "onepassword", op)
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	return func(cmd string, q browserLoginReq) controlResponse {
		return browserControl(c1, controlRequest{Cmd: cmd, Login: &q})
	}, m, &a
}

var allFixtures = []string{fixturePassword, fixtureSeed, fixtureOPPass, fixtureOPUser, fixtureOPTOTP}

func noValue(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, f := range allFixtures {
		if strings.Contains(string(b), f) {
			t.Fatalf("%s carries a value (%s): %.400s", what, f, b)
		}
	}
}

func TestBrowserLoginOpsAddEachMethodNoValueCrosses(t *testing.T) {
	call, m, audit := loginRig(t)
	var responses []controlResponse
	do := func(cmd string, q browserLoginReq) controlResponse {
		r := call(cmd, q)
		responses = append(responses, r)
		return r
	}
	dataOf := func(r controlResponse) map[string]any {
		var v map[string]any
		json.Unmarshal(r.Data, &v)
		return v
	}

	// 1. Typed: the dashboard's username and password (and a TOTP secret).
	r := do("browser-login-add", browserLoginReq{Name: "typed", Method: browser.MethodPassword, Origins: []string{"https://login.example.com"},
		Bindings: []string{"agent:t@o/r", "bot:helper"}, Values: map[string]string{"username": "me@example.com", "password": fixturePassword, "otp": fixtureSeed}})
	if r.Error != "" || dataOf(r)["change"] != "widening" {
		t.Fatalf("typed: %+v", r)
	}
	// 2. From a vault item, metadata only, its fields mapped.
	items := do("browser-vault-items", browserLoginReq{Vault: "op"})
	item := do("browser-vault-item", browserLoginReq{Vault: "op", Item: "aaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if items.Error != "" || item.Error != "" || !strings.Contains(string(item.Data), `{{ vault \"op\" \"Agents/Example App/one-time password?attribute=otp\" }}`) {
		t.Fatalf("listing: %+v %+v", items, string(item.Data))
	}
	r = do("browser-login-add", browserLoginReq{Name: "picked", Vault: "op", Item: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Bindings: []string{"team:ops"},
		Refs: map[string]string{"username": "${env.APP_USER}"}})
	if r.Error != "" {
		t.Fatalf("picked: %+v", r)
	}
	v := dataOf(r)["login"].(map[string]any)
	if v["method"] != browser.MethodVault || v["item"] != "Agents/Example App" || v["origins"].([]any)[0] != "https://app.example.com" {
		t.Fatalf("picked view: %v", v)
	}
	f := v["fields"].(map[string]any)
	if f["password"].(map[string]any)["source"] != `{{ vault "op" "Agents/Example App/password" }}` || f["username"].(map[string]any)["source"] != "${env.APP_USER}" {
		t.Fatalf("mapping and override: %v", f)
	}
	// 3. Sign in yourself: a saved session's login.
	r = do("browser-login-add", browserLoginReq{Name: "mine", Method: browser.MethodSession, Site: "sso.example.com", Bindings: []string{"agent:t@o/r"}})
	if r.Error != "" || !strings.Contains(string(r.Data), "conductor browser seed") {
		t.Fatalf("session: %+v", r)
	}
	m.Logins().Save(browser.Jar{Identity: "agent:t@o/r", Site: "sso.example.com", Cookies: []browser.Cookie{{Name: "sid", Value: "cookie-" + fixturePassword, Domain: "sso.example.com", Expires: 4102444800}}})

	// The inventory: every method, no value; the session's expiry from its cookie.
	inv := do("browser-login-list", browserLoginReq{})
	var list struct{ Logins []loginView }
	json.Unmarshal(inv.Data, &list)
	byName := map[string]loginView{}
	for _, l := range list.Logins {
		byName[l.Name] = l
	}
	if len(list.Logins) != 4 || byName["configured"].Method != browser.MethodConfig || byName["configured"].Editable {
		t.Fatalf("inventory: %+v", list.Logins)
	}
	if s := byName["mine"].Sessions; len(s) != 1 || s[0].Expires == nil || s[0].Expires.Year() != 2100 {
		t.Fatalf("session expiry: %+v", s)
	}
	if byName["typed"].Fields["password"].Source != "(stored, write-only)" {
		t.Fatalf("typed: %+v", byName["typed"].Fields)
	}

	// Set, replace, check, unset: still nothing comes back.
	do("browser-login-set", browserLoginReq{Name: "typed", Field: "password", Values: map[string]string{"password": "2-" + fixturePassword}})
	do("browser-login-check", browserLoginReq{Name: "typed"})
	do("browser-login-usage", browserLoginReq{Name: "typed"})
	do("browser-login-unset", browserLoginReq{Name: "typed", Field: "otp"})
	for i, resp := range responses {
		noValue(t, "response "+string(rune('0'+i)), resp)
	}
	noValue(t, "the audit", *audit)
}

// Widening — a new login, a new binding, a new origin — is classified as
// such (the dashboard's passkey gate), dry runs change nothing, and only
// removals are narrowing.
func TestBrowserLoginBindClassifies(t *testing.T) {
	call, m, _ := loginRig(t)
	change := func(r controlResponse) string {
		var v map[string]any
		json.Unmarshal(r.Data, &v)
		if r.Error != "" {
			return "error: " + r.Error
		}
		return v["change"].(string)
	}
	add := browserLoginReq{Name: "typed", Method: browser.MethodPassword, Origins: []string{"https://login.example.com"},
		Bindings: []string{"agent:t@o/r"}, Values: map[string]string{"password": fixturePassword}}
	dry := add
	dry.DryRun = true
	if c := change(call("browser-login-add", dry)); c != "widening" {
		t.Fatalf("dry add: %s", c)
	}
	if _, ok, _ := m.Store.Get("typed"); ok {
		t.Fatal("a dry run added the login")
	}
	if c := change(call("browser-login-add", add)); c != "widening" {
		t.Fatalf("add: %s", c)
	}
	for _, tc := range []struct {
		q    browserLoginReq
		want string
	}{
		{browserLoginReq{Name: "typed", Bind: []string{"bot:helper"}, DryRun: true}, "widening"},
		{browserLoginReq{Name: "typed", Bind: []string{"bot:helper"}}, "widening"},
		{browserLoginReq{Name: "typed", Bind: []string{"bot:helper"}}, "neutral"},
		{browserLoginReq{Name: "typed", AddOrigins: []string{"https://sso.example.com"}}, "widening"},
		{browserLoginReq{Name: "typed", Unbind: []string{"bot:helper"}, RemoveOrigins: []string{"https://sso.example.com"}}, "narrowing"},
		{browserLoginReq{Name: "typed", Bind: []string{"team:ops"}, Unbind: []string{"agent:t@o/r"}}, "widening"},
		{browserLoginReq{Name: "typed", AddOrigins: []string{"https://evil.example.org"}}, "error: browser: https://evil.example.org is not reachable"},
		{browserLoginReq{Name: "typed", Bind: []string{"*"}}, "error: browser: binding"},
		{browserLoginReq{Name: "configured", Bind: []string{"bot:helper"}}, "error: browser: configured is defined in the config"},
	} {
		if c := change(call("browser-login-bind", tc.q)); !strings.HasPrefix(c, tc.want) {
			t.Errorf("%+v: %s, want %s", tc.q, c, tc.want)
		}
	}
	if r, _, _ := m.Store.Get("typed"); strings.Join(r.Bindings, ",") != "team:ops" || len(r.Origins) != 1 {
		t.Fatalf("after: %+v", r)
	}
	if c := change(call("browser-login-set", browserLoginReq{Name: "typed", Field: "password", Refs: map[string]string{"password": "${env.X}"}})); c != "neutral" {
		t.Fatalf("set: %s", c)
	}
	if c := change(call("browser-login-remove", browserLoginReq{Name: "typed", DryRun: true})); c != "narrowing" {
		t.Fatalf("dry remove: %s", c)
	}
	if _, ok, _ := m.Store.Get("typed"); !ok {
		t.Fatal("a dry remove removed")
	}
	if c := change(call("browser-login-remove", browserLoginReq{Name: "typed"})); c != "narrowing" {
		t.Fatalf("remove: %s", c)
	}
}

func TestBrowserLoginAddRefuses(t *testing.T) {
	call, _, _ := loginRig(t)
	for _, tc := range []struct {
		q    browserLoginReq
		want string
	}{
		{browserLoginReq{Name: "configured", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}, Values: map[string]string{"password": "p"}}, "defined in the config"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://elsewhere.example.org"}, Bindings: []string{"x"}, Values: map[string]string{"password": "p"}}, "not reachable"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}}, "nothing to fill"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}, Refs: map[string]string{"password": "${settings.pw}"}}, "plain config"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}, Refs: map[string]string{"password": `{{ vault "nope" "k" }}`}}, "no vault named"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}, Refs: map[string]string{"password": "hunter2"}}, "not a reference"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Bindings: []string{"x"},
			Refs: map[string]string{"password": "${env.P}"}, Values: map[string]string{"password": "p"}}, "not both"},
		{browserLoginReq{Name: "a", Method: "password", Origins: []string{"https://app.example.com"}, Values: map[string]string{"password": "p"}}, "bound to nobody"},
		{browserLoginReq{Name: "a", Method: "session", Site: "evil.example.org", Bindings: []string{"x"}}, "not reachable"},
		{browserLoginReq{Name: "a", Method: "session", Site: "*.example.org", Bindings: []string{"x"}}, "not reachable"},
	} {
		r := call("browser-login-add", tc.q)
		if !strings.Contains(r.Error, tc.want) {
			t.Errorf("%+v: %q, want %q", tc.q, r.Error, tc.want)
		}
	}
}

// A session login's site is a host, reachable on any listed port (found
// live: localhost listed as localhost:18090 was refused as https://localhost).
func TestBrowserLoginSessionSiteOnAnyPort(t *testing.T) {
	call, _, _ := loginRig(t)
	for _, site := range []string{"localhost", "sso.example.com"} {
		if r := call("browser-login-add", browserLoginReq{Name: "s-" + strings.Split(site, ".")[0], Method: browser.MethodSession, Site: site, Bindings: []string{"x"}}); r.Error != "" {
			t.Errorf("%s: %s", site, r.Error)
		}
	}
}

func TestOriginOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://App.Example.com/login?next=/": "https://app.example.com",
		"http://localhost:3000/":               "http://localhost:3000",
		"https://user@app.example.com/":        "",
		"ftp://x.example.com/":                 "",
		"not a url":                            "",
	} {
		if got := originOf(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestJarExpiry(t *testing.T) {
	if e := jarExpiry(browser.Jar{Cookies: []browser.Cookie{{Expires: 4102444800}, {Expires: 0}}}); e.Expires != nil || !strings.Contains(e.Note, "unknown") {
		t.Fatalf("a session cookie: %+v", e)
	}
	if e := jarExpiry(browser.Jar{Cookies: []browser.Cookie{{Expires: 4102444800}, {Expires: 4000000000}}}); e.Expires == nil || e.Expires.Unix() != 4000000000 {
		t.Fatalf("earliest: %+v", e)
	}
	if e := jarExpiry(browser.Jar{Cookies: []browser.Cookie{{Expires: 1000}}}); !strings.Contains(e.Note, "expired") {
		t.Fatalf("expired: %+v", e)
	}
}

func TestCredFlags(t *testing.T) {
	pos, multi, bools := credFlags([]string{"app", "--origin", "https://a.example.com", "--origin=https://b.example.com,https://c.example.com", "--bind", "x", "--session", "--dry-run"})
	if len(pos) != 1 || len(list(multi, "origin")) != 3 || one(multi, "bind") != "x" || !bools["session"] || !bools["dry-run"] {
		t.Fatalf("%v %v %v", pos, multi, bools)
	}
}

func TestClassifyReach(t *testing.T) {
	base := browser.LoginRecord{Bindings: []string{"a"}, Origins: []string{"https://x.example.com"}, Site: "s.example.com", Also: []string{"cdn.example.com"}}
	for name, tc := range map[string]struct {
		mod  func(*browser.LoginRecord)
		want string
	}{
		"same":           {func(r *browser.LoginRecord) {}, changeNeutral},
		"new binding":    {func(r *browser.LoginRecord) { r.Bindings = []string{"a", "bot:b"} }, changeWidening},
		"new origin":     {func(r *browser.LoginRecord) { r.Origins = append(r.Origins, "https://y.example.com") }, changeWidening},
		"new also":       {func(r *browser.LoginRecord) { r.Also = append(r.Also, "sso.example.com") }, changeWidening},
		"other site":     {func(r *browser.LoginRecord) { r.Site = "t.example.com" }, changeWidening},
		"fewer bindings": {func(r *browser.LoginRecord) { r.Bindings = nil }, changeNarrowing},
		"fewer origins":  {func(r *browser.LoginRecord) { r.Origins = nil }, changeNarrowing},
		"swap":           {func(r *browser.LoginRecord) { r.Bindings = []string{"c"} }, changeWidening},
	} {
		after := base
		after.Bindings = append([]string(nil), base.Bindings...)
		after.Origins = append([]string(nil), base.Origins...)
		after.Also = append([]string(nil), base.Also...)
		tc.mod(&after)
		if got := classifyReach(base, after); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

// Revoking a session login drops its saved sessions (every identity's, that
// site's only); the next action finds none.
func TestBrowserLoginRemoveDropsSessions(t *testing.T) {
	call, m, _ := loginRig(t)
	if r := call("browser-login-add", browserLoginReq{Name: "mine", Method: browser.MethodSession, Site: "sso.example.com", Bindings: []string{"agent:t@o/r", "agent:u@o/r"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	for _, j := range []browser.Jar{{Identity: "agent:t@o/r", Site: "sso.example.com"}, {Identity: "agent:u@o/r", Site: "sso.example.com"}, {Identity: "agent:t@o/r", Site: "app.example.com"}} {
		j.Cookies = []browser.Cookie{{Name: "sid", Value: "v", Domain: j.Site}}
		if err := m.Logins().Save(j); err != nil {
			t.Fatal(err)
		}
	}
	r := call("browser-login-remove", browserLoginReq{Name: "mine"})
	if r.Error != "" || !strings.Contains(string(r.Data), `"sessions_dropped":2`) {
		t.Fatalf("remove: %+v %s", r, r.Data)
	}
	jars, _ := m.Logins().List()
	if len(jars) != 1 || jars[0].Site != "app.example.com" {
		t.Fatalf("left: %+v", jars)
	}
}
