//go:build linux || darwin

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// The test account's secrets: they must never reach a reply, the audit, the
// workspace or a screenshot.
const (
	testSeed = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
)

type fakeVault struct {
	mu      sync.Mutex
	m       map[string]string
	fetches map[string]int
}

func (v *fakeVault) read(_ context.Context, vault, key string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if vault != "testvault" {
		return "", fmt.Errorf("no vault %q", vault)
	}
	v.fetches[key]++
	val, ok := v.m[key]
	if !ok {
		return "", fmt.Errorf("no key %q", key)
	}
	return val, nil
}

func (v *fakeVault) write(_ context.Context, vault, key, value string) error {
	v.mu.Lock()
	v.m[key] = value
	v.mu.Unlock()
	return nil
}

func (v *fakeVault) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, c := range v.fetches {
		n += c
	}
	return n
}

type credRig struct {
	*rig
	vault  *fakeVault
	bound  string // http://localhost:PORT — the credential's origin
	alike  string // http://127.0.0.1:PORT — the same server, another origin
	shared []string
}

func newCredRig(t *testing.T) *credRig {
	r := newRig(t)
	_, port, _ := strings.Cut(r.host, ":")
	r.site.TOTPSeed = testSeed
	r.st.Allow = []string{"localhost:" + port, "127.0.0.1:" + port}
	v := &fakeVault{m: map[string]string{
		"site/username": browsertest.User, "site/password": browsertest.Password, "site/otp": "otpauth://totp/Test?secret=" + testSeed,
	}, fetches: map[string]int{}}
	ref := func(k string) *SecretRef { return &SecretRef{Vault: "testvault", Key: k} }
	r.st.Credentials = []Credential{{
		Name: "site", Origin: "http://localhost:" + port, Identities: []string{"agent:test@acme/app"},
		Username: ref("site/username"), Password: ref("site/password"), OTP: ref("site/otp"), Passkey: ref("site/passkey"),
	}}
	r.m.Secrets, r.m.PutSecret = v.read, v.write
	return &credRig{rig: r, vault: v, bound: "http://localhost:" + port, alike: "http://127.0.0.1:" + port}
}

// out records every reply, for the leak check.
func (r *credRig) do(c Principal, action string, args map[string]any) map[string]any {
	r.t.Helper()
	out := r.rig.do(c, action, args)
	b, _ := json.Marshal(out)
	r.shared = append(r.shared, string(b))
	return out
}

// noLeak checks that no secret (password, seed, any code the site
// accepted) is in any reply, audit entry, or workspace file.
func (r *credRig) noLeak() {
	r.t.Helper()
	_, _ = r.site.Snapshot()
	secrets := []string{browsertest.Password, testSeed}
	r.site.Snapshot()
	secrets = append(secrets, r.site.Codes...)
	var hay []string
	hay = append(hay, r.shared...)
	r.audit.mu.Lock()
	for _, e := range r.audit.es {
		b, _ := json.Marshal(e)
		hay = append(hay, string(b))
	}
	r.audit.mu.Unlock()
	filepath.Walk(r.ws, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			hay = append(hay, string(b))
		}
		return nil
	})
	for _, s := range secrets {
		for _, h := range hay {
			if s != "" && strings.Contains(h, s) {
				r.t.Fatalf("a secret leaked into: %.300s", h)
			}
		}
	}
}

func (r *credRig) refused(c Principal, action string, args map[string]any, want string) {
	r.t.Helper()
	before := r.vault.count()
	err := r.fail(c, action, args)
	if !strings.Contains(err.Error(), want) {
		r.t.Fatalf("%s %v: want %q, got %v", action, args, want, err)
	}
	if r.vault.count() != before {
		r.t.Fatalf("%s: a refused fill fetched a secret", action)
	}
}

// TestCredentialLoginPasswordAndTOTP: conductor signs the agent in —
// username, password, then the one-time code — one fresh fetch per fill,
// and nothing of it reaches the agent or the audit.
func TestCredentialLoginPasswordAndTOTP(t *testing.T) {
	r := newCredRig(t)
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	out := r.do(c, "login", map[string]any{"credential": "site"})
	if fmt.Sprint(out["steps"]) != "[username password submitted one-time code submitted]" {
		t.Fatalf("steps: %v", out)
	}
	// From a page with nothing to fill, login starts at the credential's
	// sign-in page.
	r.st.Credentials[0].LoginURL = r.bound + "/login"
	r.do(c, "open", map[string]any{"url": r.bound + "/"})
	if out := r.do(c, "login", map[string]any{"credential": "site"}); fmt.Sprint(out["steps"]) != "[opened the sign-in page username password submitted one-time code submitted]" {
		t.Fatalf("steps from another page: %v", out)
	}
	r.st.Credentials[0].LoginURL = ""
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(snap, "Signed in as") {
		t.Fatalf("not signed in:\n%s", snap)
	}
	if got := r.site.SignIns(); len(got) != 2 || got[0] != "password+otp" {
		t.Fatalf("sign-ins: %v", got)
	}
	for k, n := range r.vault.fetches {
		if n != 2 {
			t.Fatalf("%s fetched %d times in two sign-ins (one fill, one fetch)", k, n)
		}
	}
	es := r.audit.find("login")
	if len(es) != 2 || es[0]["credential"] != "site" || es[0]["origin"] != r.bound+"" || es[0]["outcome"] != "ok" {
		t.Fatalf("login audit: %v", es)
	}
	r.do(c, "screenshot", map[string]any{"path": "after-login.png"})
	// A page offering the filled password back as a download: refused.
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")})
	r.rig.do(c, "open", map[string]any{"url": r.bound + "/files/echo?v=" + browsertest.Password})
	var state string
	for i := 0; i < 50 && state == ""; i++ {
		for _, d := range r.do(c, "downloads", nil)["downloads"].([]Download) {
			if d.Name == "echo.txt" && d.State != "inProgress" {
				state = d.State
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state != "refused" {
		t.Fatalf("a download holding the password: %q", state)
	}
	r.noLeak()
}

// TestCredentialOriginBinding: a fill happens only on the exact bound
// origin's own document — never a lookalike host, a redirect's landing, an
// embedded frame, or outside the bound path — and a refused fill fetches
// nothing.
func TestCredentialOriginBinding(t *testing.T) {
	r := newCredRig(t)
	c := r.caller("op", RequesterOperator)

	r.do(c, "open", map[string]any{"url": r.alike + "/login"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")}, "origin")
	r.refused(c, "login", map[string]any{"credential": "site"}, "origin")

	// A redirect from the bound origin to the lookalike: the credential's
	// browser reaches exactly its origin's host (localhost, not 127.0.0.1),
	// so the redirect dies at its proxy.
	if err := r.fail(c, "open", map[string]any{"url": r.bound + "/out?to=" + r.alike + "/login"}); !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("a redirect off the credential's host: %v", err)
	}
	// The bound origin's pages live in the separate browser, the lookalike
	// in the anonymous one.
	if p := r.do(c, "open", map[string]any{"url": r.bound + "/"})["profile"].(string); !strings.Contains(p, "logged-in") {
		t.Fatalf("the credential's origin: %s", p)
	}
	if p := r.do(c, "open", map[string]any{"url": r.alike + "/"})["profile"]; p != "anonymous" {
		t.Fatalf("the lookalike: %v", p)
	}

	// Frames: the bound page embedding a sign-in form — same origin or not.
	for _, src := range []string{r.bound + "/login", r.alike + "/login"} {
		r.do(c, "open", map[string]any{"url": r.bound + "/framed?src=" + src})
		time.Sleep(500 * time.Millisecond)
		snap = r.do(c, "snapshot", nil)["snapshot"].(string)
		if strings.Contains(snap, "Password") && strings.Contains(snap, "[ref=") {
			for _, line := range strings.Split(snap, "\n") {
				if strings.Contains(line, "Password") && strings.Contains(line, "[ref=") {
					r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, line, "Password")}, "own document")
				}
			}
		}
		r.refused(c, "login", map[string]any{"credential": "site"}, "no sign-in field")
	}

	// Past the snapshot (which never shows a frame's fields): a node of a
	// same-origin frame's document, named straight to the fill — refused by
	// the main frame's world, before any fetch.
	r.do(c, "open", map[string]any{"url": r.bound + "/framed?src=" + r.bound + "/login"})
	time.Sleep(500 * time.Millisecond)
	s, _ := r.m.session(c)
	in := s.active
	ctx := context.Background()
	tb, _ := in.current(ctx)
	var doc struct {
		Root json.RawMessage `json:"root"`
	}
	if err := in.conn().Call(ctx, tb.session, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true}, &doc); err != nil {
		t.Fatal(err)
	}
	pw := findPasswordNode(doc.Root)
	if pw == 0 {
		t.Fatal("no password field in the frame")
	}
	cr, o, _ := r.m.credentialFor(c, *r.st, "site")
	fr, err := r.m.checkPage(ctx, in, tb, o, r.st.Allow)
	if err != nil {
		t.Fatal(err)
	}
	before := r.vault.count()
	if err := r.m.fillField(ctx, in, tb, fr, o, pw, cr, "password", time.Now()); err == nil || !strings.Contains(err.Error(), "own document") {
		t.Fatalf("a frame's field: %v", err)
	}
	if r.vault.count() != before {
		t.Fatal("a frame's field fetched the secret")
	}

	// The wrong field for the value.
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Username")}, "not a password field")
	r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "username", "ref": refOf(t, snap, "textbox", "Password")}, "only the password")
	r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "otp", "ref": refOf(t, snap, "button", "Sign in")}, "not an input")

	// A path-bound credential, off its path.
	r.st.Credentials[0].Origin = r.bound + "/account"
	r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")}, "path")
	r.noLeak()
}

// TestCredentialProvenanceAndIdentity: only the bound identity, only for
// the operator's own request.
func TestCredentialProvenanceAndIdentity(t *testing.T) {
	r := newCredRig(t)
	for _, c := range []Principal{r.caller("ev", RequesterEvent), r.caller("unk", ""), r.caller("mem", RequesterMember), r.caller("usr", RequesterUser)} {
		r.do(c, "open", map[string]any{"url": r.bound + "/login"})
		snap := r.do(c, "snapshot", nil)["snapshot"].(string)
		r.refused(c, "login", map[string]any{"credential": "site"}, "not the operator")
		r.refused(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")}, "not the operator")
	}
	other := r.caller("op2", RequesterOperator)
	other.Identity = "agent:other@acme/app"
	r.do(other, "open", map[string]any{"url": r.bound + "/login"})
	r.refused(other, "login", map[string]any{"credential": "site"}, "not bound to this identity")
	r.refused(r.caller("op", RequesterOperator), "login", map[string]any{"credential": "nope"}, "no credential")
	if r.vault.count() != 0 {
		t.Fatal("a refused credential was fetched")
	}
	if len(r.site.SignIns()) != 0 {
		t.Fatal("someone signed in")
	}
}

// TestCredentialRedaction: after a fill, the value is gone from snapshots
// (the field and the page's echo of it), a wait cannot test for it,
// evaluate is off, and a screenshot masks both boxes.
func TestCredentialRedaction(t *testing.T) {
	r := newCredRig(t)
	r.st.Evaluate = []string{"agent:test@*"}
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/codefield"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "otp", "ref": refOf(t, snap, "textbox", "Code")})
	s, _ := r.m.session(c)
	in := s.active
	secret := in.secretValues()[0]
	if len(secret) != 6 {
		t.Fatalf("filled %q", secret)
	}
	r.site.Codes = append(r.site.Codes, secret) // for noLeak
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(snap, "You typed "+Redacted) || strings.Contains(snap, secret) {
		t.Fatalf("snapshot after the fill:\n%s", snap)
	}
	// The page really has it (the fill worked) — the agent just cannot see it.
	ctx := context.Background()
	tb, _ := in.current(ctx)
	if v, _ := in.eval(ctx, tb, `document.getElementById("echo").textContent`); !strings.Contains(string(v), secret) {
		t.Fatalf("the page did not get the code: %s", v)
	}
	if err := r.fail(c, "wait", map[string]any{"text": secret[:3], "timeout": 1.0}); !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("wait as an oracle: %v", err)
	}
	if err := r.fail(c, "evaluate", map[string]any{"expr": "document.title"}); !strings.Contains(err.Error(), "credential was filled") {
		t.Fatalf("evaluate after a fill: %v", err)
	}
	if st := r.do(c, "status", nil); !strings.HasPrefix(fmt.Sprint(st["evaluate"]), "off") {
		t.Fatalf("status after a fill says evaluate is %v", st["evaluate"])
	}
	shot := r.do(c, "screenshot", map[string]any{"path": "code.png"})
	b, _ := os.ReadFile(filepath.Join(r.ws, shot["path"].(string)))
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := in.world(ctx, tb, mustFrame(t, in, tb).ID)
	raw, _ := in.callIn(ctx, tb, w, secretRectsJS, []string{secret}, false)
	var rects struct {
		Rects [][4]float64 `json:"rects"`
		Width float64      `json:"width"`
	}
	json.Unmarshal(raw, &rects)
	if len(rects.Rects) < 2 {
		t.Fatalf("want the field and the echo boxed: %v", rects)
	}
	scale := float64(img.Bounds().Dx()) / rects.Width
	for _, rc := range rects.Rects {
		x, y := int((rc[0]+rc[2]/2)*scale), int((rc[1]+rc[3]/2)*scale)
		if cr, cg, cb, _ := img.At(x, y).RGBA(); cr != 0 || cg != 0 || cb != 0 {
			t.Fatalf("the screenshot shows the box at %v", rc)
		}
	}
	r.noLeak()
}

func mustFrame(t *testing.T, in *Instance, tb *tab) frame {
	f, err := in.mainFrame(context.Background(), tb)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestCredentialApprovalPush: a sign-in that waits on the operator's phone
// tells the operator, and continues once they approve.
func TestCredentialApprovalPush(t *testing.T) {
	r := newCredRig(t)
	r.site.TOTPSeed = ""
	r.site.NeedApproval = true
	var notified []string
	r.m.Notify = func(msg string) {
		notified = append(notified, msg)
		go func() { time.Sleep(700 * time.Millisecond); r.site.Approve() }()
	}
	r.st.Credentials[0].Approval = true
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	out := r.do(c, "login", map[string]any{"credential": "site"})
	if !strings.HasPrefix(fmt.Sprint(out["approval"]), "approved") || len(notified) != 1 {
		t.Fatalf("approval: %v (notified %v)", out, notified)
	}
	if got := r.site.SignIns(); len(got) != 1 || got[0] != "approved" {
		t.Fatalf("sign-ins: %v", got)
	}
	r.noLeak()
}

// TestCredentialPasskey: the operator registers a dedicated passkey (a
// takeover; here the site creates it on load), conductor keeps it in the
// vault, and the agent signs in with it — on the bound origin only.
func TestCredentialPasskey(t *testing.T) {
	r := newCredRig(t)
	r.site.TOTPSeed = ""
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, _ := CredentialHost(r.bound)
	dir := t.TempDir()
	k, err := StartTakeover(ctx, LaunchSpec{Chrome: r.st.Chrome, Dir: dir, Allow: r.st.Allow, SelfExe: r.m.SelfExe, Stderr: &Ring{}},
		[]Cookie{{Name: "sid", Value: r.site.Session(), Domain: host, Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := k.ArmPasskey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(ctx, r.bound+"/passkey/register?auto=1"); err != nil {
		t.Fatal(err)
	}
	pk, err := k.TakePasskey(ctx, auth, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The site records it once its page has posted it.
	for i := 0; i < 50 && r.site.Passkeys() == 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	k.Close()
	if r.site.Passkeys() != 1 || pk.RPID != "localhost" || pk.PrivateKey == "" {
		t.Fatalf("registration: %d passkeys, rp %q", r.site.Passkeys(), pk.RPID)
	}
	b, _ := json.Marshal(pk)
	r.vault.write(ctx, "testvault", "site/passkey", string(b))

	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.alike + "/passkey/login"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.refused(c, "login", map[string]any{"credential": "site", "passkey": true, "ref": refOf(t, snap, "button", "passkey")}, "origin")

	r.do(c, "open", map[string]any{"url": r.bound + "/passkey/login"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	out := r.do(c, "login", map[string]any{"credential": "site", "passkey": true, "ref": refOf(t, snap, "button", "passkey")})
	if fmt.Sprint(out["steps"]) != "[passkey]" {
		t.Fatalf("passkey login: %v", out)
	}
	if got := r.site.SignIns(); len(got) != 1 || got[0] != "passkey" {
		t.Fatalf("sign-ins: %v", got)
	}
	if snap := r.do(c, "snapshot", nil)["snapshot"].(string); !strings.Contains(snap, "Signed in as") {
		t.Fatalf("after the passkey:\n%s", snap)
	}
	for _, h := range r.shared {
		if strings.Contains(h, pk.PrivateKey) {
			t.Fatal("the passkey's private key reached a reply")
		}
	}
	// The authenticator went with the sign-in: the site's button alone
	// signs no one in.
	r.do(c, "open", map[string]any{"url": r.bound + "/passkey/login"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "click", map[string]any{"ref": refOf(t, snap, "button", "passkey")})
	time.Sleep(time.Second)
	if got := r.site.SignIns(); len(got) != 1 {
		t.Fatalf("the virtual authenticator outlived its sign-in: %v", got)
	}
	// A passkey for another relying party is refused by conductor, not
	// only by WebAuthn.
	r.st.Credentials[0].Origin = r.alike
	r.do(c, "open", map[string]any{"url": r.alike + "/passkey/login"})
	// (The stored passkey is read to learn its RP — after every binding
	// check — and never leaves conductor.)
	if err := r.fail(c, "login", map[string]any{"credential": "site", "passkey": true}); !strings.Contains(err.Error(), "the passkey is for localhost") {
		t.Fatalf("a passkey for another RP: %v", err)
	}
	r.st.Credentials[0].Origin = r.bound
	ev := r.caller("ev", RequesterEvent)
	r.do(ev, "open", map[string]any{"url": r.bound + "/passkey/login"})
	r.refused(ev, "login", map[string]any{"credential": "site", "passkey": true}, "not the operator")
}

// findPasswordNode finds a password input's backend id in a pierced DOM.
func findPasswordNode(raw json.RawMessage) int64 {
	var n struct {
		BackendNodeID   int64             `json:"backendNodeId"`
		NodeName        string            `json:"nodeName"`
		Attributes      []string          `json:"attributes"`
		Children        []json.RawMessage `json:"children"`
		ContentDocument json.RawMessage   `json:"contentDocument"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return 0
	}
	if n.NodeName == "INPUT" {
		for i := 0; i+1 < len(n.Attributes); i += 2 {
			if n.Attributes[i] == "type" && n.Attributes[i+1] == "password" {
				return n.BackendNodeID
			}
		}
	}
	kids := n.Children
	if len(n.ContentDocument) > 0 {
		kids = append(kids, n.ContentDocument)
	}
	for _, k := range kids {
		if id := findPasswordNode(k); id != 0 {
			return id
		}
	}
	return 0
}

// TestCredentialEmailCode: a site that mails a one-time code — conductor
// reads only the matching message from the opted-in mailbox, fills the code,
// and nothing of the mailbox, message or code reaches the agent.
func TestCredentialEmailCode(t *testing.T) {
	r := newCredRig(t)
	r.site.TOTPSeed = ""
	mb := &browsertest.Mailbox{User: "agent-inbox@example.test", Pass: "imap-app-password-not-secret"}
	if err := mb.Serve("127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	// Someone else's mail, and an older code, in the same mailbox.
	mb.Deliver("Bank <alerts@bank.example>", "Your sign-in code", "Your code is 999999", time.Now())
	mb.Deliver("Test Site <no-reply@testsite.example>", "Your sign-in code", "Your sign-in code is 111111", time.Now().Add(-time.Hour))
	r.site.EmailCodes = mb
	r.vault.m["mail/user"], r.vault.m["mail/pass"] = mb.User, mb.Pass
	r.st.Mailboxes = map[string]Mailbox{"inbox": {Name: "inbox", Addr: mb.Addr(), PlainLoopback: true,
		Username: &SecretRef{Vault: "testvault", Key: "mail/user"}, Password: &SecretRef{Vault: "testvault", Key: "mail/pass"}}}
	r.st.Credentials[0].OTP = nil
	r.st.Credentials[0].Email = &EmailCode{Mailbox: "inbox", From: "no-reply@testsite.example", Subject: "sign-in code",
		Code: regexp.MustCompile(`code is ([0-9]{6})`), Wait: 20 * time.Second}
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	out := r.do(c, "login", map[string]any{"credential": "site"})
	if fmt.Sprint(out["steps"]) != "[username password submitted one-time code submitted]" {
		t.Fatalf("steps: %v", out)
	}
	if got := r.site.SignIns(); len(got) != 1 || got[0] != "password+email-code" {
		t.Fatalf("sign-ins: %v", got)
	}
	if snap := r.do(c, "snapshot", nil)["snapshot"].(string); !strings.Contains(snap, "Signed in as") {
		t.Fatalf("not signed in:\n%s", snap)
	}
	bodies := 0
	for _, cmd := range mb.Log() {
		if strings.Contains(cmd, "BODY.PEEK[])") {
			bodies++
		}
		if strings.HasPrefix(strings.Fields(cmd + " x x")[1], "SELECT") {
			t.Fatal("the mailbox was opened read-write")
		}
	}
	if bodies != 1 {
		t.Fatalf("message bodies read: %d (only the match)", bodies)
	}
	if r.vault.fetches["mail/user"] != 1 || r.vault.fetches["mail/pass"] != 1 {
		t.Fatalf("the mailbox login is fetched fresh, once: %v", r.vault.fetches)
	}
	// Nothing of the mailbox reaches a reply or the audit: the code, the
	// app password, the other mail.
	for _, h := range r.shared {
		for _, s := range append(r.site.Codes, mb.Pass, "999999", "Bank") {
			if strings.Contains(h, s) {
				t.Fatalf("%q reached a reply: %.200s", s, h)
			}
		}
	}
	es := r.audit.find("login")
	if len(es) != 1 || es[0]["outcome"] != "ok" {
		t.Fatalf("audit: %v", es)
	}
	r.noLeak()
	// A second sign-in waits for a new message: the used one is never
	// filled again (a site that does not send one fails, rather than reuse).
	r.site.EmailCodes = nil
	r.site.TOTPSeed = ""
	r.st.Credentials[0].Email.Wait = 2 * time.Second
	ev := r.caller("op2", RequesterOperator)
	r.do(ev, "open", map[string]any{"url": r.bound + "/codefield"})
	snap := r.do(ev, "snapshot", nil)["snapshot"].(string)
	if err := r.fail(ev, "fill-credential", map[string]any{"credential": "site", "field": "otp", "ref": refOf(t, snap, "textbox", "Code")}); !strings.Contains(err.Error(), "no message") {
		t.Fatalf("the used message again: %v", err)
	}
}
