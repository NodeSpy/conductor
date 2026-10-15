package browser

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCheckURL(t *testing.T) {
	allow := []string{"docs.example.com", "127.0.0.1:8080", "*.example.org"}
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://docs.example.com/x?y=1", true},
		{"http://docs.example.com/", false}, // a bare host is :443 only
		{"https://docs.example.com:8443/", false},
		{"http://127.0.0.1:8080/", true},
		{"http://127.0.0.1:8081/", false},
		{"http://localhost:8080/", false},
		{"https://a.example.org/", true},
		{"https://example.org/", false},
		{"https://docs.example.com.evil.test/", false},
		{"https://evil.test/docs.example.com", false},
		{"about:blank", true},
		{"about:config", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,hi", false},
		{"chrome://settings", false},
		{"view-source:https://docs.example.com/", false},
		{"ftp://docs.example.com/", false},
		{"https:///nohost", false},
		{"::bad", false},
	} {
		err := checkURL(tc.url, allow)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.url, err)
		}
		if err != nil && !errors.Is(err, errRefused) {
			t.Errorf("%s: a refusal must be errRefused (audited as refused): %v", tc.url, err)
		}
	}
}

func TestLoginAllowOnlyTheSites(t *testing.T) {
	st := Settings{Logins: []LoginSiteConfig{
		{LoginSite: LoginSite{Site: "app.example.com"}, Also: []string{"cdn.example.net"}},
		{LoginSite: LoginSite{Site: "*.corp.test"}},
	}}
	allow := []string{"app.example.com", "docs.example.com", "*", "127.0.0.1:8080", "x.corp.test", "*.corp.test", "evil.test"}
	got := loginAllow(allow, []string{"app.example.com"}, st)
	if !reflect.DeepEqual(got, []string{"app.example.com", "cdn.example.net"}) {
		t.Fatalf("app: %v", got)
	}
	got = loginAllow(allow, []string{"*.corp.test"}, st)
	if !reflect.DeepEqual(got, []string{"x.corp.test", "*.corp.test"}) {
		t.Fatalf("corp: %v", got)
	}
	if got := loginAllow(allow, nil, st); len(got) != 0 {
		t.Fatalf("no sites, no allowlist: %v", got)
	}
	if got := loginAllow([]string{"127.0.0.1:8080"}, []string{"127.0.0.1"}, Settings{}); !reflect.DeepEqual(got, []string{"127.0.0.1:8080"}) {
		t.Fatalf("a host with a port: %v", got)
	}
}

func TestAllowFor(t *testing.T) {
	got := allowFor(Principal{Egress: []string{"a.test", " ", "b.test"}}, Settings{Allow: []string{"b.test", "c.test"}})
	if !reflect.DeepEqual(got, []string{"b.test", "c.test", "a.test"}) {
		t.Fatal(got)
	}
}

func TestSiteCovers(t *testing.T) {
	for _, tc := range []struct {
		site, domain string
		ok           bool
	}{
		{"app.example.com", "app.example.com", true},
		{"app.example.com", ".app.example.com", true},
		{"app.example.com", "APP.example.com", true},
		{"app.example.com", "example.com", false},
		{"app.example.com", ".example.com", false},
		{"app.example.com", "x.app.example.com", false},
		{"app.example.com", "evilapp.example.com", false},
		{"app.example.com", "", false},
		{"*.example.com", "example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "evilexample.com", false},
		{"*.example.com", "example.com.evil", false},
	} {
		if siteCovers(tc.site, tc.domain) != tc.ok {
			t.Errorf("%s / %s", tc.site, tc.domain)
		}
	}
}

func TestFilterCookies(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := []Cookie{
		{Name: "sid", Value: "1", Domain: "app.example.com"},
		{Name: "old", Value: "2", Domain: "app.example.com", Expires: float64(now.Unix() - 10)},
		{Name: "live", Value: "3", Domain: ".app.example.com", Expires: float64(now.Unix() + 10), SameSite: "Weird"},
		{Name: "", Value: "4", Domain: "app.example.com"},
		{Name: "other", Value: "5", Domain: "evil.test"},
		{Name: "sess", Value: "6", Domain: "app.example.com", Expires: -1},
	}
	got := FilterCookies("app.example.com", in, now)
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
		if c.Path != "/" || c.Expires < 0 || c.SameSite == "Weird" {
			t.Fatalf("normalized: %+v", c)
		}
	}
	if !reflect.DeepEqual(names, []string{"sid", "live", "sess"}) {
		t.Fatal(names)
	}
}

func TestRefreshCookiesUpdatesOnly(t *testing.T) {
	saved := []Cookie{{Name: "sid", Value: "old", Domain: "app.example.com", Path: "/"}, {Name: "keep", Value: "k", Domain: "app.example.com", Path: "/"}}
	live := []Cookie{{Name: "sid", Value: "new", Domain: ".app.example.com", Path: "/", Expires: 5}, {Name: "planted", Value: "x", Domain: "app.example.com", Path: "/"}}
	got, changed := refreshCookies(saved, live)
	if !changed || len(got) != 2 || got[0].Value != "new" || got[0].Expires != 5 || got[1].Value != "k" {
		t.Fatalf("%+v %v", got, changed)
	}
	if _, changed := refreshCookies(saved, nil); changed {
		t.Fatal("nothing live is no change (a cleared cookie is kept)")
	}
	if _, changed := refreshCookies(saved, saved); changed {
		t.Fatal("same values are no change")
	}
}

func TestLoginsStore(t *testing.T) {
	l := Logins{Dir: filepath.Join(t.TempDir(), "logins")}
	if err := l.Save(Jar{Identity: "", Site: "a.test"}); err == nil {
		t.Fatal("a jar with no identity")
	}
	if err := l.Save(Jar{Identity: "i", Site: "a.test:443"}); err == nil {
		t.Fatal("a site with a port")
	}
	if err := l.Save(Jar{Identity: "i", Site: "a.test", Cookies: []Cookie{{Name: "s", Value: "v", Domain: "a.test"}, {Name: "x", Value: "y", Domain: "b.test"}}}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(l.path("i", "a.test"))
	di, _ := os.Stat(l.Dir)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode(), di.Mode())
	}
	j, ok, err := l.Load("i", "a.test")
	if err != nil || !ok || len(j.Cookies) != 1 {
		t.Fatalf("%+v %v %v", j, ok, err)
	}
	if _, ok, _ := l.Load("j", "a.test"); ok {
		t.Fatal("another identity's jar")
	}
	list, _ := l.List()
	if len(list) != 1 || list[0].Cookies != 1 || list[0].Identity != "i" {
		t.Fatal(list)
	}
	if ok, _ := l.Delete("i", "a.test"); !ok {
		t.Fatal("delete")
	}
	if ok, _ := l.Delete("i", "a.test"); ok {
		t.Fatal("delete twice")
	}
}

func TestLoginSiteFor(t *testing.T) {
	l := LoginSite{Identities: []string{"dir:/a", "agent:t@*", "*"}}
	for id, ok := range map[string]bool{"dir:/a": true, "dir:/a/b": false, "agent:t@x/y": true, "agent:tt@x": false, "anything": false, "": false} {
		if l.For(id) != ok {
			t.Errorf("%q", id)
		}
	}
}

func TestOperatorOnly(t *testing.T) {
	for r, ok := range map[string]bool{RequesterOperator: true, RequesterEvent: false, RequesterUser: false, RequesterMember: false, "": false, "Operator": false} {
		got, why := OperatorOnly(Principal{Requester: r}, "s")
		if got != ok || (!ok && why == "") {
			t.Errorf("%q: %v %q", r, got, why)
		}
	}
}

func TestCheckRel(t *testing.T) {
	ws := "/w/ws"
	for _, tc := range []struct {
		rel, want string
	}{
		{"a.png", "a.png"},
		{"d/a.png", "d/a.png"},
		{"/w/ws/d/a.png", "d/a.png"},
		{"./a.png", "a.png"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"../a", ""},
		{"d/../../a", ""},
		{"/w/other/a", ""},
		{"/w/ws", ""},
		{"/w/wsx/a", ""},
		{".git/x", ""},
		{"d/.git/hooks/x", ""},
		{"ro/x", ""},
		{"ro", ""},
		{"rox/a", "rox/a"},
		{"a\x00b", ""},
	} {
		got, err := checkRel(ws, tc.rel, []string{"/w/ws/ro"})
		if (tc.want == "") != (err != nil) || got != tc.want {
			t.Errorf("%q: %q %v", tc.rel, got, err)
		}
	}
}

func TestWorkspaceIO(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(root, "secret"), []byte("S"), 0o600)
	os.Symlink(filepath.Join(root, "secret"), filepath.Join(ws, "link"))
	os.Symlink(root, filepath.Join(ws, "up"))
	os.WriteFile(filepath.Join(ws, "big"), make([]byte, 100), 0o600)
	os.WriteFile(filepath.Join(ws, "ok"), []byte("fine"), 0o600)
	os.Mkdir(filepath.Join(ws, "dir"), 0o755)
	dst := filepath.Join(root, "dst")
	for _, rel := range []string{"link", "up/secret", "big", "dir", "../secret", "missing"} {
		if _, err := readWorkspace(ws, rel, nil, 50, dst); err == nil {
			t.Errorf("read %s", rel)
		}
		os.Remove(dst)
	}
	if n, err := readWorkspace(ws, "ok", nil, 50, dst); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	for _, rel := range []string{"up/x", "../x"} {
		if _, err := writeWorkspace(ws, rel, nil, strings.NewReader("x"), 10); err == nil {
			t.Errorf("write %s", rel)
		}
	}
	// A planted symlink's name is taken: the write goes beside it, never
	// through it.
	if got, err := writeWorkspace(ws, "link", nil, strings.NewReader("x"), 10); err != nil || got != "link-1" {
		t.Fatal(got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "secret")); string(b) != "S" {
		t.Fatal("written through a symlink")
	}
	if _, err := writeWorkspace(ws, "n/big", nil, strings.NewReader(strings.Repeat("x", 11)), 10); err == nil {
		t.Fatal("over the limit")
	}
	if _, err := os.Stat(filepath.Join(ws, "n/big")); err == nil {
		t.Fatal("an over-limit write left its file")
	}
	got, err := writeWorkspace(ws, "n/ok", nil, strings.NewReader("1"), 10)
	got2, _ := writeWorkspace(ws, "n/ok", nil, strings.NewReader("2"), 10)
	if err != nil || got != "n/ok" || got2 != "n/ok-1" {
		t.Fatal(got, got2, err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "n/ok")); string(b) != "1" {
		t.Fatal("overwritten")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.csv": "report.csv", "../../etc/passwd": "passwd", "..": "download", ".bashrc": "bashrc",
		"a\x00b\nc": "abc", "x\\..\\y.txt": "y.txt", "a$b|c.txt": "a_b_c.txt", "": "download",
		"\u202eexe.txt": "_exe.txt",
	} {
		if got := safeName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if got := safeName(strings.Repeat("a", 300) + ".pdf"); len(got) != 120 || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long: %q", got)
	}
}

func TestOTPCode(t *testing.T) {
	// RFC 6238 test vectors (SHA1, 8 digits).
	seed := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for ts, want := range map[int64]string{59: "94287082", 1111111109: "07081804", 1234567890: "89005924", 2000000000: "69279037"} {
		got, err := OTPCode("otpauth://totp/x?secret="+seed+"&digits=8", time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("%d: %q %v", ts, got, err)
		}
	}
	if got, _ := OTPCode("otpauth://totp/x?secret="+seed+"&digits=8&algorithm=SHA256", time.Unix(59, 0)); got != "32247374" {
		t.Errorf("sha256 vector: %s", got)
	}
	if got, _ := OTPCode(" 123456 ", time.Now()); got != "123456" {
		t.Error("a code passes through")
	}
	if got, _ := OTPCode(strings.ToLower(seed), time.Unix(59, 0)); got != "287082" {
		t.Errorf("a bare seed: %s", got)
	}
	for _, bad := range []string{"", "not base32 !", "otpauth://hotp/x?secret=" + seed, "otpauth://totp/x?secret=" + seed + "&digits=9",
		"otpauth://totp/x?secret=" + seed + "&algorithm=MD5", "otpauth://totp/x?secret=" + seed + "&period=0", "otpauth://totp/x?secret=" + seed + "&period=601"} {
		if _, err := OTPCode(bad, time.Now()); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// An error never carries the seed.
	if _, err := OTPCode("otpauth://totp/x?secret="+seed+"&digits=9", time.Now()); strings.Contains(err.Error(), seed) {
		t.Fatal("the error shows the seed")
	}
}

func TestOriginCheck(t *testing.T) {
	o, err := parseOrigin("https://app.example.com/login")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		so, page string
		ok       bool
	}{
		{"https://app.example.com", "https://app.example.com/login", true},
		{"https://app.example.com", "https://app.example.com/login/2fa", true},
		{"https://app.example.com:443", "https://app.example.com/login", true},
		{"https://app.example.com", "https://app.example.com/loginx", false},
		{"https://app.example.com", "https://app.example.com/", false},
		{"http://app.example.com", "http://app.example.com/login", false},
		{"https://app.example.com:8443", "https://app.example.com:8443/login", false},
		{"https://app.example.com.evil.test", "https://app.example.com.evil.test/login", false},
		{"https://evil-app.example.com", "https://evil-app.example.com/login", false},
		{"https://example.com", "https://example.com/login", false},
		{"null", "about:blank", false},
		{"", "", false},
	} {
		if err := o.check(tc.so, tc.page); (err == nil) != tc.ok {
			t.Errorf("%s %s: %v", tc.so, tc.page, err)
		}
	}
	root, _ := parseOrigin("http://localhost:8090")
	if root.check("http://localhost:8090", "http://localhost:8090/anything") != nil || root.check("http://127.0.0.1:8090", "http://127.0.0.1:8090/") == nil {
		t.Fatal("root path / lookalike")
	}
	for _, bad := range []string{"app.example.com", "ftp://a.test", "https://u:p@a.test", "https://a.test/?q", "https://a.test/#f", "https://"} {
		if CheckOrigin(bad) == nil {
			t.Errorf("origin %q accepted", bad)
		}
	}
}

func TestRPFits(t *testing.T) {
	for _, tc := range []struct {
		rp, host string
		ok       bool
	}{
		{"example.com", "example.com", true}, {"example.com", "app.example.com", true},
		{"example.com", "evilexample.com", false}, {"app.example.com", "example.com", false},
		{"", "example.com", false}, {"localhost", "localhost", true}, {"EXAMPLE.com", "app.example.COM", true},
	} {
		if rpFits(tc.rp, tc.host) != tc.ok {
			t.Errorf("%s %s", tc.rp, tc.host)
		}
	}
}

func TestScrub(t *testing.T) {
	in := &Instance{}
	if in.scrub("hunter22") != "hunter22" || in.filled() {
		t.Fatal("nothing filled, nothing scrubbed")
	}
	in.remember(7, "hunter22")
	in.remember(8, "")
	if !in.filled() {
		t.Fatal("filled")
	}
	res := map[string]any{"snapshot": "pw hunter22!", "list": []any{"x hunter22"}, "tabs": []TabInfo{{Title: "hunter22"}},
		"downloads": []Download{{Name: "hunter22.txt"}}, "s": []string{"hunter22"}, "n": 3}
	in.scrubAny(res)
	for _, s := range []string{res["snapshot"].(string), res["list"].([]any)[0].(string), res["tabs"].([]TabInfo)[0].Title, res["downloads"].([]Download)[0].Name, res["s"].([]string)[0]} {
		if strings.Contains(s, "hunter22") || !strings.Contains(s, Redacted) {
			t.Errorf("scrub: %q", s)
		}
	}
	f := filepath.Join(t.TempDir(), "d")
	os.WriteFile(f, []byte("xx hunter22 yy"), 0o600)
	if refuseSecretFile(f, in.secretValues()) == nil {
		t.Fatal("a download holding a filled value")
	}
	os.WriteFile(f, []byte("clean"), 0o600)
	if refuseSecretFile(f, in.secretValues()) != nil || refuseSecretFile(f, nil) != nil {
		t.Fatal("a clean download")
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@a.test/x?token=abc#frag": "https://a.test/x",
		"about:blank":                         "about:blank",
		"no scheme":                           "no scheme",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestParseCookies(t *testing.T) {
	js := `[{"name":"a","value":"1","domain":".x.test","path":"/","expirationDate":5,"httpOnly":true,"secure":true,"sameSite":"no_restriction"}]`
	cs, err := ParseCookies([]byte(js))
	if err != nil || len(cs) != 1 || cs[0].Expires != 5 || cs[0].SameSite != "None" || !cs[0].HTTPOnly {
		t.Fatalf("%+v %v", cs, err)
	}
	cs, err = ParseCookies([]byte(`{"cookies":[{"name":"b","value":"2","domain":"x.test","expires":7,"sameSite":"Lax"}]}`))
	if err != nil || len(cs) != 1 || cs[0].SameSite != "Lax" || cs[0].Expires != 7 {
		t.Fatalf("%+v %v", cs, err)
	}
	txt := "# Netscape\n#HttpOnly_.x.test\tTRUE\t/\tTRUE\t9\tsid\tv\n.y.test\tTRUE\t/\tFALSE\t0\tz\tw\n"
	cs, err = ParseCookies([]byte(txt))
	if err != nil || len(cs) != 2 || !cs[0].HTTPOnly || !cs[0].Secure || cs[1].Secure || cs[0].Expires != 9 {
		t.Fatalf("%+v %v", cs, err)
	}
	if _, err := ParseCookies([]byte("a\tb\n")); err == nil {
		t.Fatal("bad cookies.txt")
	}
}

func TestGrantNeedsOptInJarAndProvenance(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	st := Settings{Logins: []LoginSiteConfig{{LoginSite: LoginSite{Site: "a.test", Identities: []string{"id"}}}, {LoginSite: LoginSite{Site: "b.test", Identities: []string{"other"}}}}}
	op := Principal{Identity: "id", Requester: RequesterOperator}
	if s, why := m.grant(op, st); len(s) != 0 || !strings.Contains(why, "no saved session") {
		t.Fatalf("no jar: %v %q", s, why)
	}
	m.Logins().Save(Jar{Identity: "id", Site: "a.test", Cookies: []Cookie{{Name: "s", Value: "v", Domain: "a.test"}}})
	m.Logins().Save(Jar{Identity: "id", Site: "b.test", Cookies: []Cookie{{Name: "s", Value: "v", Domain: "b.test"}}})
	if s, why := m.grant(op, st); !reflect.DeepEqual(s, []string{"a.test"}) || why != "" {
		t.Fatalf("operator: %v %q (b.test is not opted in for this identity)", s, why)
	}
	ev := op
	ev.Requester = RequesterEvent
	if s, why := m.grant(ev, st); len(s) != 0 || !strings.Contains(why, "not the operator") {
		t.Fatalf("event: %v %q", s, why)
	}
	m.Provenance = func(Principal, string) (bool, string) { return false, "member gate says no" }
	if s, why := m.grant(op, st); len(s) != 0 || why != "member gate says no" {
		t.Fatalf("provenance hook: %v %q", s, why)
	}
}

func TestCredentialFor(t *testing.T) {
	m := &Manager{}
	st := Settings{Credentials: []Credential{{Name: "c", Origin: "https://a.test", Identities: []string{"id"}}, {Name: "bad", Origin: "a.test", Identities: []string{"id"}}}}
	if _, _, err := m.credentialFor(Principal{Identity: "id", Requester: RequesterOperator}, st, "c"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p    Principal
		name string
	}{
		{Principal{Identity: "other", Requester: RequesterOperator}, "c"},
		{Principal{Identity: "id", Requester: RequesterEvent}, "c"},
		{Principal{Identity: "id", Requester: ""}, "c"},
		{Principal{Identity: "id", Requester: RequesterOperator}, "nope"},
		{Principal{Identity: "id", Requester: RequesterOperator}, "bad"},
	} {
		if _, _, err := m.credentialFor(tc.p, st, tc.name); err == nil {
			t.Errorf("%+v %s", tc.p, tc.name)
		}
	}
}

func TestParseKey(t *testing.T) {
	k, mods, err := parseKey("Control+Shift+a")
	if err != nil || k.key != "a" || mods != 10 {
		t.Fatal(k, mods, err)
	}
	for _, bad := range []string{"", "Hyper+a", "NoSuchKey", "F13"} {
		if _, _, err := parseKey(bad); err == nil {
			t.Errorf("%q", bad)
		}
	}
	if k, _, _ := parseKey("F5"); k.vk != 116 {
		t.Error("F5")
	}
}

func TestFindChrome(t *testing.T) {
	look := func(n string) (string, error) {
		if n == "chromium" {
			return "/usr/bin/chromium", nil
		}
		return "", os.ErrNotExist
	}
	if _, err := findChrome("", "linux", look); err != nil {
		t.Fatal(err)
	}
	if _, err := findChrome("", "plan9", look); err == nil {
		t.Fatal("nowhere to look")
	}
	if _, err := findChrome("/nonexistent/chrome", "linux", look); err == nil {
		t.Fatal("a configured binary that is not there")
	}
	if installDir("/usr/bin/chromium", "linux") != "" || installDir("/opt/google/chrome/chrome", "linux") != "" {
		t.Fatal("the base system already shows /usr and /opt")
	}
	if got := installDir("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "darwin"); got != "/Applications/Google Chrome.app" {
		t.Fatal(got)
	}
}

func TestChromeArgs(t *testing.T) {
	a := strings.Join(ChromeArgs("/d", "127.0.0.1:1", false), " ")
	for _, want := range []string{"--remote-debugging-pipe", "--proxy-server=http://127.0.0.1:1", "--proxy-bypass-list=<-loopback>", "--headless=new", "--user-data-dir=/d/profile", "--disable-quic", "disable_non_proxied_udp", "--password-store=basic"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(a, "remote-debugging-port") || strings.Contains(a, "no-sandbox") {
		t.Fatal("a debugging port, or chrome's own sandbox off")
	}
	if strings.Contains(strings.Join(ChromeArgs("/d", "x", true), " "), "--headless") {
		t.Fatal("headed")
	}
}

func TestBrowserSpecHasNoNetwork(t *testing.T) {
	sp := browserSpec([]string{"a.test"})
	if !sp.Deny || !sp.EnforcedEgress() || sp.Mode != "namespace" || sp.Privileged || len(sp.Egress) != 1 {
		t.Fatalf("%+v", sp)
	}
}

func TestPlatformArgs(t *testing.T) {
	if len(platformArgs("linux")) != 0 {
		t.Fatal("Chromium's own sandbox stays on on Linux")
	}
	if a := platformArgs("darwin"); len(a) != 1 || a[0] != "--no-sandbox" {
		t.Fatal(a)
	}
}
