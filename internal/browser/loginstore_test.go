package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	typedPassword = "Typed-Pa55word-Fixture!"
	typedSeed     = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXQ"
)

func newStore(t *testing.T) *LoginStore {
	t.Helper()
	s := &LoginStore{Dir: filepath.Join(t.TempDir(), "browser")}
	if err := s.Put(LoginRecord{Name: "app", Method: MethodPassword, Origins: []string{"https://app.example.com"}, Bindings: []string{"agent:t@o/r"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

// everything returns every exported face of the store, marshalled, after
// values are set: none may carry one.
func everything(t *testing.T, s *LoginStore) []string {
	t.Helper()
	var out []string
	add := func(v any) {
		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}
	l, _ := s.List()
	add(l)
	r, _, _ := s.Get("app")
	add(r)
	add(s.UsageOf("", 0))
	add(WithStore(Settings{}, l))
	filepath.Walk(s.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out = append(out, string(b))
		}
		return nil
	})
	return out
}

// Write-only: a typed value goes in and is filled, and nothing else reads
// it — not a listing, a record, the history, the merged settings, nor the
// files on disk (the values sit sealed in the store's own vault).
func TestLoginStoreValuesAreWriteOnly(t *testing.T) {
	s := newStore(t)
	if err := s.SetValue("app", "password", typedPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue("app", "otp", "otpauth://totp/x?secret="+typedSeed); err != nil {
		t.Fatal(err)
	}
	s.RecordUse(Usage{Login: "app", Identity: "agent:t@o/r", Action: "login", Outcome: "ok", At: time.Now()})
	for _, h := range everything(t, s) {
		for _, secret := range []string{typedPassword, typedSeed} {
			if strings.Contains(h, secret) {
				t.Fatalf("a typed value is readable: %.200s", h)
			}
		}
	}
	if !s.HasValue("app", "password") || s.HasValue("app", "username") {
		t.Fatal("HasValue")
	}
	r, _, _ := s.Get("app")
	if r.Fields["password"].Kind != "stored" || r.Fields["password"].Ref != "" {
		t.Fatalf("the record names a stored value, not the value: %+v", r.Fields)
	}
	// The one read path is the fill's.
	if v, err := s.value("app", "password"); err != nil || v != typedPassword {
		t.Fatalf("the fill reads it: %q %v", v, err)
	}
	// Replace, then remove.
	if err := s.SetValue("app", "password", "second-"+typedPassword); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.value("app", "password"); v != "second-"+typedPassword {
		t.Fatal("replace")
	}
	if err := s.ClearField("app", "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.value("app", "password"); err == nil {
		t.Fatal("a removed value still fills")
	}
	// A reference in its place drops a stored value too.
	s.SetValue("app", "password", typedPassword)
	if err := s.SetRef("app", "password", `{{ vault "op" "Agents/App/password" }}`); err != nil {
		t.Fatal(err)
	}
	if s.HasValue("app", "password") {
		t.Fatal("a reference left the stored value behind")
	}
}

// No exported method of the store hands out a string it did not get as
// metadata: the read path stays unexported.
func TestLoginStoreHasNoExportedReader(t *testing.T) {
	typ := reflect.TypeOf(&LoginStore{})
	allowed := map[string]bool{"List": true, "Get": true, "Put": true, "Update": true, "Remove": true, "SetValue": true, "SetRef": true,
		"ClearField": true, "HasValue": true, "RecordUse": true, "UsageOf": true, "SetTest": true}
	for i := 0; i < typ.NumMethod(); i++ {
		if n := typ.Method(i).Name; !allowed[n] {
			t.Errorf("LoginStore.%s is new — is it a way to read a value back?", n)
		}
	}
}

// Revoking removes the record and its values: the merged settings no
// longer have it, and its values are gone from the vault.
func TestLoginStoreRemoveRevokes(t *testing.T) {
	s := newStore(t)
	s.SetValue("app", "password", typedPassword)
	l, _ := s.List()
	if st := WithStore(Settings{}, l); len(st.Credentials) != 1 || st.Credentials[0].Password.Stored != "app/password" {
		t.Fatalf("merged: %+v", st.Credentials)
	}
	if ok, err := s.Remove("app"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	l, _ = s.List()
	if st := WithStore(Settings{}, l); len(st.Credentials) != 0 {
		t.Fatal("a revoked login is still a credential")
	}
	if _, err := s.value("app", "password"); err == nil {
		t.Fatal("a revoked login's value survived")
	}
	// The same name again starts empty.
	s.Put(LoginRecord{Name: "app", Method: MethodPassword, Origins: []string{"https://app.example.com"}, Bindings: []string{"x"}})
	if s.HasValue("app", "password") {
		t.Fatal("a re-added login inherited the revoked one's value")
	}
}

func TestLoginRecordChecks(t *testing.T) {
	good := LoginRecord{Name: "app", Method: MethodPassword, Origins: []string{"https://app.example.com"}, Bindings: []string{"agent:t@o/r"}}
	for name, mod := range map[string]func(*LoginRecord){
		"no bindings":       func(r *LoginRecord) { r.Bindings = nil },
		"everyone":          func(r *LoginRecord) { r.Bindings = []string{"*"} },
		"bot prefix":        func(r *LoginRecord) { r.Bindings = []string{"bot:*"} },
		"empty team":        func(r *LoginRecord) { r.Bindings = []string{"team:"} },
		"no origin":         func(r *LoginRecord) { r.Origins = nil },
		"wildcard origin":   func(r *LoginRecord) { r.Origins = []string{"https://*.example.com"} },
		"origin with query": func(r *LoginRecord) { r.Origins = []string{"https://app.example.com/?a=b"} },
		"login url off":     func(r *LoginRecord) { r.LoginURL = "https://evil.example.com/login" },
		"bad name":          func(r *LoginRecord) { r.Name = "a b" },
		"slash name":        func(r *LoginRecord) { r.Name = "a/b" },
		"bad method":        func(r *LoginRecord) { r.Method = "magic" },
		"unknown field":     func(r *LoginRecord) { r.Fields = map[string]FieldSource{"pin": {Kind: "env"}} },
		"session no site":   func(r *LoginRecord) { r.Method, r.Origins = MethodSession, nil },
		"session fields": func(r *LoginRecord) {
			r.Method, r.Origins, r.Site, r.Fields = MethodSession, nil, "app.example.com", map[string]FieldSource{"password": {Kind: "stored"}}
		},
	} {
		r := good
		mod(&r)
		if err := checkRecord(&r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := checkRecord(&good); err != nil {
		t.Fatal(err)
	}
	ok := LoginRecord{Name: "s", Method: MethodSession, Site: "*.example.com", Bindings: []string{"team:ops"}}
	if err := checkRecord(&ok); err != nil {
		t.Fatal(err)
	}
}

func TestLoginStoreSetRefRefusesValuesAndSettings(t *testing.T) {
	s := newStore(t)
	for _, ref := range []string{"hunter2", "${settings.pw}", `{{secret "nokey"}}`, "op://Agents/App/password"} {
		if err := s.SetRef("app", "password", ref); err == nil {
			t.Errorf("SetRef(%q) accepted", ref)
		}
	}
	if err := s.SetValue("app", "otp", "not a seed!"); err == nil {
		t.Fatal("an unusable OTP secret was stored")
	}
	if err := s.SetValue("nope", "password", "x"); err == nil {
		t.Fatal("a value for no login")
	}
	if err := s.SetRef("app", "password", "${env.APP_PW}"); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.Get("app")
	if f := r.Fields["password"]; f.Kind != "env" || f.Ref != "${env.APP_PW}" {
		t.Fatalf("%+v", f)
	}
}

// Each method merges into the browser's settings bound exactly as stored:
// a password/vault login is a credential on its origins for its bindings,
// a session login a login site.
func TestWithStoreBindsEachMethod(t *testing.T) {
	recs := []LoginRecord{
		{Name: "typed", Method: MethodPassword, Origins: []string{"https://a.example.com", "https://login.example.com"}, Bindings: []string{"bot:helper"},
			Fields: map[string]FieldSource{"password": {Kind: "stored"}, "username": {Kind: "env", Ref: "${env.U}"}}},
		{Name: "picked", Method: MethodVault, Origins: []string{"https://b.example.com"}, Bindings: []string{"agent:x@o/r"},
			Fields: map[string]FieldSource{"password": {Kind: "vault", Ref: `{{ vault "op" "V/I/password" }}`}, "otp": {Kind: "secret", Ref: `{{secret "op/V/I/otp"}}`}}},
		{Name: "mine", Method: MethodSession, Site: "c.example.com", Bindings: []string{"team:ops"}, Also: []string{"cdn.example.net"}},
		{Name: "cfg", Method: MethodPassword, Origins: []string{"https://evil.example.com"}, Bindings: []string{"*x"}},
	}
	st := WithStore(Settings{Credentials: []Credential{{Name: "cfg", Origin: "https://cfg.example.com"}}}, recs)
	byName := map[string]Credential{}
	for _, c := range st.Credentials {
		byName[c.Name] = c
	}
	if c := byName["cfg"]; c.Origin != "https://cfg.example.com" || len(st.Credentials) != 3 {
		t.Fatalf("a stored login shadowed the config's: %+v", st.Credentials)
	}
	ty := byName["typed"]
	if ty.Password.Stored != "typed/password" || ty.Username.Env != "U" || len(ty.AllOrigins()) != 2 {
		t.Fatalf("typed: %+v", ty)
	}
	pk := byName["picked"]
	if pk.Password.Vault != "op" || pk.Password.Key != "V/I/password" || !pk.OTP.Named || pk.OTP.Key != "V/I/otp" {
		t.Fatalf("picked: %+v %+v", pk.Password, pk.OTP)
	}
	if !ty.ForPrincipal(Principal{Identity: "agent:z@o/r", Groups: []string{"bot:helper"}}) || ty.ForPrincipal(Principal{Identity: "agent:z@o/r"}) ||
		ty.ForPrincipal(Principal{Identity: "bot:helperx", Groups: []string{"bot:helper2"}}) {
		t.Fatal("bot binding")
	}
	if !pk.ForPrincipal(Principal{Identity: "agent:x@o/r"}) || pk.ForPrincipal(Principal{Identity: "agent:x@o/r2"}) {
		t.Fatal("identity binding")
	}
	if len(st.Logins) != 1 || st.Logins[0].Site != "c.example.com" || !st.Logins[0].ForPrincipal(Principal{Identity: "a", Groups: []string{"team:ops"}}) ||
		st.Logins[0].ForPrincipal(Principal{Identity: "agent:a@o/r", Groups: []string{"team:dev"}}) {
		t.Fatalf("session: %+v", st.Logins)
	}
}

// A page on one of several origins picks that origin; elsewhere the login
// URL's, else the first — and the fill's own check then refuses a page on
// none of them.
func TestPickOrigin(t *testing.T) {
	cr := Credential{Name: "c", Origins: []string{"https://a.example.com", "https://login.example.com:8443"}, LoginURL: "https://login.example.com:8443/in"}
	for so, want := range map[string]string{
		"https://a.example.com":          "https://a.example.com:443",
		"https://login.example.com:8443": "https://login.example.com:8443",
		"https://login.example.com":      "https://login.example.com:8443",
		"https://evil.example.com":       "https://login.example.com:8443",
		"":                               "https://login.example.com:8443",
	} {
		o, err := pickOrigin(cr, so)
		if err != nil || o.String() != want {
			t.Errorf("%q: %s %v", so, o.String(), err)
		}
		if so == "https://evil.example.com" && o.check(so, so+"/") == nil {
			t.Error("a page on no origin passed the check")
		}
	}
	cr.LoginURL = ""
	if o, _ := pickOrigin(cr, "https://evil.example.com"); o.host != "a.example.com" {
		t.Fatal("first origin")
	}
}

// Resolution reads each source kind at fill time, outside the sandbox, and
// taints what it read; a missing variable is an error, not an empty fill.
func TestResolveRefPerSource(t *testing.T) {
	s := newStore(t)
	s.SetValue("app", "password", typedPassword)
	var tainted []string
	m := &Manager{Store: s,
		LookupEnv: func(k string) (string, bool) {
			if k == "APP_USER" {
				return "env-user@example.com", true
			}
			return "", false
		},
		Taint:   func(v string) { tainted = append(tainted, v) },
		Secrets: func(ctx context.Context, vault, key string) (string, error) { return vault + ":" + key, nil },
	}
	ctx := context.Background()
	for _, tc := range []struct {
		ref  *SecretRef
		want string
	}{
		{&SecretRef{Env: "APP_USER"}, "env-user@example.com"},
		{&SecretRef{Stored: "app/password"}, typedPassword},
		{&SecretRef{Vault: "op", Key: "V/I/f"}, "op:V/I/f"},
		{&SecretRef{Vault: "house", Key: "k", Named: true}, "house:k"},
		{&SecretRef{Literal: "agent@example.com"}, "agent@example.com"},
	} {
		v, err := m.secret(ctx, tc.ref)
		if err != nil || v != tc.want {
			t.Errorf("%s: %q %v", tc.ref.Source(), v, err)
		}
	}
	if len(tainted) != 2 || tainted[0] != "env-user@example.com" || tainted[1] != typedPassword {
		t.Fatalf("env and stored reads are tainted: %v", tainted)
	}
	if _, err := m.secret(ctx, &SecretRef{Env: "MISSING"}); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("unset env: %v", err)
	}
	if _, err := m.secret(ctx, &SecretRef{Stored: "app/otp"}); err == nil {
		t.Fatal("an unset stored field filled")
	}
	if (&SecretRef{Stored: "app/password"}).Source() != "(stored, write-only)" || (&SecretRef{Literal: "x"}).Source() != "(typed value)" {
		t.Fatal("a source names a value")
	}
}

// The resolve check says ok or why per field — never the value — and warns
// a secret read from a plain-text environment variable (naming
// conductor.env when it sets it).
func TestCheckSourcesNeverReturnsAValue(t *testing.T) {
	s := newStore(t)
	s.SetValue("app", "password", typedPassword)
	s.SetRef("app", "otp", "${env.APP_OTP}")
	s.SetRef("app", "username", "${env.APP_USER}")
	envFile := filepath.Join(t.TempDir(), "conductor.env")
	os.WriteFile(envFile, []byte("# x\nexport APP_OTP="+typedSeed+"\nOTHER=1\n"), 0o600)
	m := &Manager{Store: s, EnvFile: envFile, Settings: func() Settings { return Settings{} },
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "APP_OTP":
				return typedSeed, true
			case "APP_USER":
				return "u@example.com", true
			}
			return "", false
		}}
	res, err := m.CheckSources(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for _, v := range []string{typedPassword, typedSeed, "u@example.com"} {
		if strings.Contains(string(b), v) {
			t.Fatalf("the check returned a value: %s", b)
		}
	}
	got := map[string]FieldCheck{}
	for _, f := range res {
		got[f.Field] = f
	}
	if !got["password"].OK || got["password"].Kind != "stored" || got["password"].Warning != "" {
		t.Fatalf("password: %+v", got["password"])
	}
	if !got["otp"].OK || !strings.Contains(got["otp"].Warning, "plain-text") || !strings.Contains(got["otp"].Warning, envFile) {
		t.Fatalf("otp: %+v", got["otp"])
	}
	if got["username"].Warning != "" {
		t.Fatal("a username from the environment is no secret: no warning")
	}
	s.SetRef("app", "otp", "${env.NOPE}")
	res, _ = m.CheckSources(context.Background(), "app")
	for _, f := range res {
		if f.Field == "otp" && (f.OK || !strings.Contains(f.Error, "not set")) {
			t.Fatalf("an unset variable: %+v", f)
		}
	}
	m.LookupEnv = func(k string) (string, bool) { return "not-a-seed!", true }
	res, _ = m.CheckSources(context.Background(), "app")
	for _, f := range res {
		if f.Field == "otp" && (f.OK || strings.Contains(f.Error, "not-a-seed")) {
			t.Fatalf("an unusable seed: %+v", f)
		}
	}
	if _, err := m.CheckSources(context.Background(), "nope"); err == nil {
		t.Fatal("no such credential")
	}
}

func TestUsageHistory(t *testing.T) {
	s := newStore(t)
	for i, id := range []string{"a", "b", "a"} {
		s.RecordUse(Usage{Login: "app", Identity: id, Dispatch: "d" + string(rune('0'+i)), Action: "login", Outcome: "ok", At: time.Now()})
	}
	s.RecordUse(Usage{Login: "other", Identity: "c", Outcome: "refused", At: time.Now()})
	u := s.UsageOf("app", 2)
	if len(u) != 2 || u[0].Dispatch != "d2" || u[1].Identity != "b" {
		t.Fatalf("newest first, limited: %+v", u)
	}
	if r, _, _ := s.Get("app"); r.LastUsed == nil {
		t.Fatal("last used")
	}
	if len(s.UsageOf("", 0)) != 4 {
		t.Fatal("all")
	}
}

// A test sign-in's browser is narrowed to the credential: the allowlist's
// entries for its hosts only, bound to the test identity alone, no login
// site, no evaluate; it starts at the login URL, else the first origin.
func TestTestSettingsNarrow(t *testing.T) {
	st := Settings{Allow: []string{"app.example.com", "other.example.com", "*.cdn.example"},
		Logins:      []LoginSiteConfig{{LoginSite: LoginSite{Site: "other.example.com", Identities: []string{"x"}}}},
		Evaluate:    []string{"*x"},
		Credentials: []Credential{{Name: "a"}, {Name: "b"}}}
	cred := Credential{Name: "app", Origins: []string{"https://app.example.com/in"}, Identities: []string{"agent:t@o/r"}}
	tst, start, err := testSettings(st, cred)
	if err != nil {
		t.Fatal(err)
	}
	if start != "https://app.example.com:443/in" {
		t.Fatalf("start: %s", start)
	}
	if strings.Join(tst.Allow, ",") != "app.example.com" || tst.Logins != nil || tst.Evaluate != nil {
		t.Fatalf("narrowed: %+v", tst)
	}
	if len(tst.Credentials) != 1 || strings.Join(tst.Credentials[0].Identities, ",") != TestIdentity {
		t.Fatalf("credential: %+v", tst.Credentials)
	}
	if len(st.Credentials) != 2 || st.Credentials[0].Name != "a" {
		t.Fatal("the operator's settings changed")
	}
	cred.LoginURL = "https://app.example.com/login"
	if _, start, _ := testSettings(st, cred); start != cred.LoginURL {
		t.Fatalf("login url: %s", start)
	}
	if _, _, err := testSettings(st, Credential{Name: "none"}); err == nil {
		t.Fatal("no origin")
	}
}
