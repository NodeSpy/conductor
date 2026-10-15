//go:build linux || darwin

package browser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// These run a real Chromium in the real sandbox against a local site. They
// skip where either is missing — unless CONDUCTOR_REQUIRE_BROWSER=1 (CI's
// browser legs), where they fail.

var (
	conductorOnce sync.Once
	conductorBin  string
	conductorErr  error
)

func requireBrowser(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short")
	}
	need := os.Getenv("CONDUCTOR_REQUIRE_BROWSER") == "1"
	skip := func(why string) {
		if need {
			t.Fatalf("browser tests required: %s", why)
		}
		t.Skip(why)
	}
	if why := agentsandbox.Probe(); why != "" {
		skip("no sandbox here: " + why)
	}
	chrome, err := FindChrome(os.Getenv("CONDUCTOR_TEST_CHROME"))
	if err != nil {
		skip(err.Error())
	}
	return chrome
}

// selfExe builds conductor once (the Linux in-sandbox forwarder).
func selfExe(t *testing.T) func() (string, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return func() (string, error) { return "/usr/bin/true", nil }
	}
	conductorOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cbt")
		if err != nil {
			conductorErr = err
			return
		}
		conductorBin = filepath.Join(dir, "conductor")
		out, err := exec.Command("go", "build", "-o", conductorBin, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput()
		if err != nil {
			conductorErr = err
			conductorBin = string(out)
		}
	})
	if conductorErr != nil {
		t.Fatalf("build conductor: %v\n%s", conductorErr, conductorBin)
	}
	return func() (string, error) { return conductorBin, nil }
}

type rig struct {
	t     *testing.T
	m     *Manager
	site  *browsertest.Site
	url   string
	host  string
	ws    string
	audit *auditLog
	st    *Settings
}

type auditLog struct {
	mu sync.Mutex
	es []map[string]any
}

func (a *auditLog) add(e map[string]any) {
	a.mu.Lock()
	a.es = append(a.es, e)
	a.mu.Unlock()
}

func (a *auditLog) find(action string) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []map[string]any
	for _, e := range a.es {
		if e["action"] == action {
			out = append(out, e)
		}
	}
	return out
}

func newRig(t *testing.T) *rig {
	t.Helper()
	chrome := requireBrowser(t)
	site := browsertest.New()
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	state := t.TempDir()
	if r, err := filepath.EvalSymlinks(state); err == nil {
		state = r
	}
	ws := filepath.Join(state, "ws")
	os.MkdirAll(ws, 0o755)
	st := &Settings{Chrome: chrome, Allow: []string{host}, Evaluate: EvaluateOff, MaxDownload: 1 << 20, MaxUpload: 1 << 20, MaxBrowsers: 6}
	a := &auditLog{}
	m := &Manager{
		Settings: func() Settings { return *st },
		StateDir: filepath.Join(state, "browser"),
		SelfExe:  selfExe(t),
		Audit:    a.add,
		Log:      t.Logf,
	}
	t.Cleanup(m.Close)
	return &rig{t: t, m: m, site: site, url: srv.URL, host: host, ws: ws, audit: a, st: st}
}

func (r *rig) caller(key, requester string) Principal {
	return Principal{Key: key, Identity: "agent:test@acme/app", Workspace: r.ws, Requester: requester, Agent: "test"}
}

func (r *rig) do(c Principal, action string, args map[string]any) map[string]any {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := r.m.Do(ctx, c, action, args)
	if err != nil {
		r.t.Fatalf("%s %v: %v", action, args, err)
	}
	return out
}

func (r *rig) fail(c Principal, action string, args map[string]any) error {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := r.m.Do(ctx, c, action, args)
	if err == nil {
		r.t.Fatalf("%s %v: want an error", action, args)
	}
	return err
}

// refOf finds the ref of the first snapshot line containing all of want.
func refOf(t *testing.T, snap string, want ...string) string {
	t.Helper()
	for _, line := range strings.Split(snap, "\n") {
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if i := strings.Index(line, "[ref="); ok && i >= 0 {
			rest := line[i+5:]
			return rest[:strings.IndexAny(rest, "],")]
		}
	}
	t.Fatalf("no ref for %v in:\n%s", want, snap)
	return ""
}

// TestBrowserMultiStepTask: open, read, fill a form with a select and an
// upload from the workspace, submit, download a file, screenshot — every
// file through the workspace.
func TestBrowserMultiStepTask(t *testing.T) {
	r := newRig(t)
	c := r.caller("k1", RequesterEvent)
	os.WriteFile(filepath.Join(r.ws, "notes.txt"), []byte("hello from the workspace"), 0o644)

	r.do(c, "open", map[string]any{"url": r.url + "/form"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill", map[string]any{"ref": refOf(t, snap, "textbox", "Name"), "text": "Ada"})
	if got := r.do(c, "select", map[string]any{"ref": refOf(t, snap, "Color"), "value": "Blue"})["selected"]; got != "Blue" {
		t.Fatalf("select: %v", got)
	}
	r.do(c, "upload", map[string]any{"ref": refOf(t, snap, "Attachment"), "paths": []any{"notes.txt"}})
	out := r.do(c, "click", map[string]any{"ref": refOf(t, snap, "button", "Submit")})
	if out["title"] != "Thanks" {
		t.Fatalf("after submit: %v", out)
	}
	subs, _ := r.site.Snapshot()
	if len(subs) != 1 || subs[0].Name != "Ada" || subs[0].Color != "blue" || subs[0].FileName != "notes.txt" || subs[0].FileHead != "hello from the workspace" {
		t.Fatalf("the site got %+v", subs)
	}

	// A download lands in the workspace, by conductor.
	r.do(c, "open", map[string]any{"url": r.url + "/"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "click", map[string]any{"ref": refOf(t, snap, "link", "Download report")})
	var got string
	for i := 0; i < 50 && got == ""; i++ {
		for _, d := range r.do(c, "downloads", nil)["downloads"].([]Download) {
			if d.Path != "" {
				got = d.Path
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != ".browser/downloads/report.csv" {
		t.Fatalf("download path %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(r.ws, got)); err != nil || string(b) != browsertest.ReportCSV {
		t.Fatalf("downloaded file: %q %v", b, err)
	}
	if es := r.audit.find("download"); len(es) != 1 || es[0]["path"] != got {
		t.Fatalf("download audit: %v", es)
	}

	shot := r.do(c, "screenshot", map[string]any{"path": "shots/home.png"})
	if shot["path"] != "shots/home.png" {
		t.Fatalf("screenshot: %v", shot)
	}
	if b, err := os.ReadFile(filepath.Join(r.ws, "shots/home.png")); err != nil || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Fatalf("screenshot file: %v", err)
	}
	// Every action audited, with the identity and the page.
	for _, e := range r.audit.find("click") {
		if e["identity"] != "agent:test@acme/app" || e["logged_in"] != false || e["ref"] == nil || e["url"] == nil {
			t.Fatalf("click audit: %v", e)
		}
	}
	if es := r.audit.find("fill"); len(es) != 1 || es[0]["chars"] != 3 {
		t.Fatalf("fill audit (length only, never the text): %v", es)
	}
}

// TestBrowserAllowlistEnforcedByTheProxy: a URL off the allowlist is
// refused up front, and a redirect off it — which the browser follows —
// dies at the proxy.
func TestBrowserAllowlistEnforcedByTheProxy(t *testing.T) {
	r := newRig(t)
	c := r.caller("k1", RequesterOperator)
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte("<title>LEAK</title>"))
	}))
	defer other.Close()

	err := r.fail(c, "open", map[string]any{"url": other.URL + "/"})
	if !strings.Contains(err.Error(), "not in this browser's allowlist") {
		t.Fatalf("refusal: %v", err)
	}
	err = r.fail(c, "open", map[string]any{"url": "file:///etc/passwd"})
	if !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("file: %v", err)
	}
	// The redirect: the pre-check passes (the site is allowed); the proxy
	// refuses where it leads.
	err = r.fail(c, "open", map[string]any{"url": r.url + "/out?to=" + other.URL + "/"})
	if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("redirect off the allowlist: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the non-allowlisted server was reached")
	}
	denied := false
	for _, e := range r.audit.find("egress") {
		if strings.Contains(e["target"].(string), strings.TrimPrefix(other.URL, "http://")) {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("the proxy's refusal was not audited: %v", r.audit.find("egress"))
	}
	if es := r.audit.find("open"); len(es) < 2 || es[0]["outcome"] != "refused" {
		t.Fatalf("open audit: %v", es)
	}
}

// TestBrowserLoginGatedByProvenance: the same identity, opted in with a
// saved session: the operator's request acts logged in, an event's gets
// the anonymous profile and cannot act.
func TestBrowserLoginGatedByProvenance(t *testing.T) {
	r := newRig(t)
	h, port, _ := net.SplitHostPort(r.host)
	// Another allowed host (the same site under another name): the anonymous
	// browser reaches it, the logged-in one must not.
	r.st.Allow = append(r.st.Allow, "localhost:"+port)
	r.st.Logins = []LoginSiteConfig{{LoginSite: LoginSite{Site: h, Identities: []string{"agent:test@*"}}}}
	if err := r.m.Logins().Save(Jar{Identity: "agent:test@acme/app", Site: h, Source: "import",
		Cookies: []Cookie{{Name: "sid", Value: r.site.Session(), Domain: h, Path: "/", HTTPOnly: true}, {Name: "x", Value: "other-site", Domain: "evil.test"}}}); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := r.m.Logins().Load("agent:test@acme/app", h); len(j.Cookies) != 1 {
		t.Fatalf("only the site's cookies are kept: %+v", j.Cookies)
	}

	ev := r.caller("event-session", RequesterEvent)
	out := r.do(ev, "open", map[string]any{"url": r.url + "/account"})
	if out["profile"] != "anonymous" || out["logged_in_note"] == nil {
		t.Fatalf("event: %v", out)
	}
	if snap := r.do(ev, "snapshot", nil)["snapshot"].(string); !strings.Contains(snap, "Not signed in") || strings.Contains(snap, "Place order") {
		t.Fatalf("an event-requested session saw:\n%s", snap)
	}

	op := r.caller("operator-session", RequesterOperator)
	out = r.do(op, "open", map[string]any{"url": r.url + "/account"})
	if !strings.HasPrefix(out["profile"].(string), "the logged-in browser") {
		t.Fatalf("operator: %v", out)
	}
	snap := r.do(op, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(snap, "Signed in as "+browsertest.User) {
		t.Fatalf("the operator's session is not signed in:\n%s", snap)
	}
	r.do(op, "click", map[string]any{"ref": refOf(t, snap, "button", "Place order")})
	if _, acts := r.site.Snapshot(); len(acts) != 1 {
		t.Fatalf("the operator's logged-in order: %v", acts)
	}
	for _, e := range r.audit.find("click") {
		if e["logged_in"] != true || e["requester"] != RequesterOperator {
			t.Fatalf("logged-in click audit: %v", e)
		}
	}
	// The anonymous session's own cookie jar never got the session.
	if snap := r.do(ev, "snapshot", nil)["snapshot"].(string); strings.Contains(snap, "Signed in") {
		t.Fatal("the event session became signed in")
	}
	// Routing: another allowlisted host goes to the anonymous browser — and a
	// redirect from the logged-in site to it dies at the logged-in browser's
	// proxy (it reaches only its sites).
	out = r.do(op, "open", map[string]any{"url": "http://localhost:" + port + "/account"})
	if out["profile"] != "anonymous" {
		t.Fatalf("another host in the logged-in browser: %v", out)
	}
	if err := r.fail(op, "open", map[string]any{"url": r.url + "/out?to=http://localhost:" + port + "/account"}); !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("the logged-in browser followed a redirect off its sites: %v", err)
	}
	// Provenance is read at every action: the same session, now an event's,
	// loses the logged-in browser.
	op.Requester = RequesterEvent
	// A non-navigating action first: it must not reach the logged-in browser.
	if snap := r.do(op, "snapshot", nil)["snapshot"].(string); strings.Contains(snap, "Signed in as") || strings.Contains(snap, "Place order") {
		t.Fatalf("after provenance changed, an action still ran logged in:\n%s", snap)
	}
	out = r.do(op, "open", map[string]any{"url": r.url + "/account"})
	if out["profile"] != "anonymous" {
		t.Fatalf("after provenance changed: %v", out)
	}
}

// TestBrowserEvaluateGated: off by default; read-only refuses writes.
func TestBrowserEvaluateGated(t *testing.T) {
	r := newRig(t)
	c := r.caller("k1", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.url + "/"})
	err := r.fail(c, "evaluate", map[string]any{"expr": "document.title"})
	if !strings.Contains(err.Error(), "evaluate is off") {
		t.Fatalf("off: %v", err)
	}
	r.st.Evaluate = EvaluateReadOnly
	if v := r.do(c, "evaluate", map[string]any{"expr": "document.title"})["value"]; v != `"Test Site"` {
		t.Fatalf("read: %v", v)
	}
	for _, expr := range []string{
		`document.title = "pwned"`,
		`document.body.innerHTML = ""`,
		`fetch("/order", {method: "POST"})`,
		`document.querySelector("a").click()`,
		`localStorage.setItem("a", "b")`,
	} {
		err := r.fail(c, "evaluate", map[string]any{"expr": expr})
		if !strings.Contains(err.Error(), "side effect") {
			t.Fatalf("%s: %v", expr, err)
		}
	}
	if v := r.do(c, "evaluate", map[string]any{"expr": "document.title"})["value"]; v != `"Test Site"` {
		t.Fatalf("the page changed: %v", v)
	}
	if es := r.audit.find("evaluate"); len(es) < 6 || es[0]["outcome"] != "refused" {
		t.Fatalf("evaluate audit: %v", es)
	}
}

// TestBrowserFilesStayInTheWorkspace: uploads only from it, writes only
// into it, never through a planted symlink, never into .git.
func TestBrowserFilesStayInTheWorkspace(t *testing.T) {
	r := newRig(t)
	c := r.caller("k1", RequesterOperator)
	outside := filepath.Join(filepath.Dir(r.ws), "secret.txt")
	os.WriteFile(outside, []byte("DECOY-SECRET"), 0o600)
	os.Symlink(outside, filepath.Join(r.ws, "link.txt"))
	os.Symlink(filepath.Dir(r.ws), filepath.Join(r.ws, "up"))
	os.MkdirAll(filepath.Join(r.ws, ".git"), 0o755)

	r.do(c, "open", map[string]any{"url": r.url + "/form"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	file := refOf(t, snap, "Attachment")
	for _, p := range []string{"../secret.txt", outside, "link.txt", "up/secret.txt", ".git/config"} {
		err := r.fail(c, "upload", map[string]any{"ref": file, "paths": []any{p}})
		if !strings.Contains(err.Error(), "upload") {
			t.Fatalf("upload %s: %v", p, err)
		}
	}
	for _, p := range []string{"../x.png", "/tmp/x.png", "up/x.png", ".git/x.png", "x.txt"} {
		r.fail(c, "screenshot", map[string]any{"path": p})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.ws), "x.png")); err == nil {
		t.Fatal("a screenshot was written outside the workspace")
	}
	// The workspace's carve-outs (read-only, hidden) are closed too.
	os.MkdirAll(filepath.Join(r.ws, "ro"), 0o755)
	os.WriteFile(filepath.Join(r.ws, "ro", "f.txt"), []byte("x"), 0o644)
	c.Deny = []string{filepath.Join(r.ws, "ro")}
	if err := r.fail(c, "upload", map[string]any{"ref": file, "paths": []any{"ro/f.txt"}}); !strings.Contains(err.Error(), "not open") {
		t.Fatalf("upload from a carve-out: %v", err)
	}
	if err := r.fail(c, "screenshot", map[string]any{"path": "ro/x.png"}); !strings.Contains(err.Error(), "not open") {
		t.Fatalf("screenshot into a carve-out: %v", err)
	}
	c.Deny = nil
	// A taken name is never overwritten.
	os.WriteFile(filepath.Join(r.ws, "a.png"), []byte("mine"), 0o644)
	if got := r.do(c, "screenshot", map[string]any{"path": "a.png"})["path"]; got != "a-1.png" {
		t.Fatalf("screenshot over an existing file: %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(r.ws, "a.png")); string(b) != "mine" {
		t.Fatal("an existing file was overwritten")
	}
}

// TestBrowserSeesNothingOfTheHost: the browser's own view (driven straight
// over the protocol, past every conductor check) has no operator files.
func TestBrowserSeesNothingOfTheHost(t *testing.T) {
	r := newRig(t)
	c := r.caller("k1", RequesterOperator)
	decoy := filepath.Join(os.Getenv("HOME"), ".config", "google-chrome", "Default")
	os.MkdirAll(decoy, 0o700)
	os.WriteFile(filepath.Join(decoy, "Cookies"), []byte("DECOY-OPERATOR-COOKIES"), 0o600)
	r.do(c, "open", map[string]any{"url": r.url + "/"})
	s, _ := r.m.session(c)
	in := s.anon
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tb, err := in.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(decoy, "Cookies"), r.ws, "/etc/hostname"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		err := in.navigate(ctx, tb, "file://"+p)
		if p == "/etc/hostname" && runtime.GOOS == "linux" {
			// The base system is read-only visible, like every sandbox's.
			continue
		}
		if err == nil {
			v, _ := in.eval(ctx, tb, "document.body ? document.body.innerText : ''")
			if strings.Contains(string(v), "DECOY") || p == r.ws {
				t.Fatalf("the browser read %s: %s", p, v)
			}
		}
	}
}
