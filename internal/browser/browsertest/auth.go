package browsertest

import (
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The sign-in steps after the password: a one-time code (TOTP), an approval
// "on the phone", and passkeys — a minimal WebAuthn relying party that
// verifies real ES256 assertions, so a passkey sign-in is proven, not
// pretended.

type pending struct {
	otpDone  bool
	approved bool
}

func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Code is the site's current TOTP code (for a test that needs to know it).
func Code(seed string, at time.Time) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(seed))
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1000000)
}

// Approve approves every sign-in waiting on the phone.
func (s *Site) Approve() {
	s.mu.Lock()
	for _, p := range s.pending {
		p.approved = true
	}
	s.mu.Unlock()
}

// SignIns are how each session was made, in order.
func (s *Site) SignIns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signIns...)
}

// Passkeys counts registered passkeys.
func (s *Site) Passkeys() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.passkeys)
}

func (s *Site) signIn(w http.ResponseWriter, how string) {
	id := s.Session()
	s.mu.Lock()
	s.signIns = append(s.signIns, how)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// passwordOK continues a sign-in whose password was right.
func (s *Site) passwordOK(w http.ResponseWriter, r *http.Request) {
	if s.TOTPSeed == "" && !s.NeedApproval {
		s.signIn(w, "password")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	id := token()
	s.mu.Lock()
	s.pending[id] = &pending{otpDone: s.TOTPSeed == ""}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "pending", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	if s.TOTPSeed != "" {
		http.Redirect(w, r, "/login/otp", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login/approve", http.StatusSeeOther)
}

func (s *Site) pendingOf(r *http.Request) (string, *pending) {
	c, err := r.Cookie("pending")
	if err != nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.Value, s.pending[c.Value]
}

func origin(r *http.Request) string { return "http://" + r.Host }

func rpID(r *http.Request) string {
	h := r.Host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h
}

func b64u(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

const webauthnJS = `<script>
const b64u = b => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const fromB64u = s => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), c => c.charCodeAt(0));
async function challenge() { return (await (await fetch("/passkey/challenge")).json()).challenge; }
async function register() {
  const ch = await challenge();
  const cred = await navigator.credentials.create({publicKey: {
    challenge: fromB64u(ch), rp: {id: location.hostname, name: "Test Site"},
    user: {id: new TextEncoder().encode("test-user-id"), name: "test-user", displayName: "Test User"},
    pubKeyCredParams: [{type: "public-key", alg: -7}],
    authenticatorSelection: {residentKey: "required", userVerification: "preferred"}, attestation: "none"}});
  const r = await fetch("/passkey/register", {method: "POST", body: JSON.stringify({
    id: b64u(cred.rawId), publicKey: b64u(cred.response.getPublicKey()), clientDataJSON: b64u(cred.response.clientDataJSON)})});
  document.getElementById("status").textContent = r.ok ? "Passkey added" : "Passkey failed: " + await r.text();
}
async function signin() {
  const ch = await challenge();
  const a = await navigator.credentials.get({publicKey: {challenge: fromB64u(ch), rpId: location.hostname, userVerification: "preferred"}});
  const r = await fetch("/passkey/login", {method: "POST", body: JSON.stringify({id: b64u(a.rawId),
    authenticatorData: b64u(a.response.authenticatorData), clientDataJSON: b64u(a.response.clientDataJSON), signature: b64u(a.response.signature)})});
  if (r.ok) location.href = "/account"; else document.getElementById("status").textContent = "Passkey sign-in failed: " + await r.text();
}
</script>`

func (s *Site) auth(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login/otp":
		id, p := s.pendingOf(r)
		if p == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			code := r.Form.Get("otp")
			now := time.Now()
			if code == Code(s.TOTPSeed, now) || code == Code(s.TOTPSeed, now.Add(-30*time.Second)) {
				s.mu.Lock()
				p.otpDone = true
				s.Codes = append(s.Codes, code)
				approval := s.NeedApproval
				s.mu.Unlock()
				if approval {
					http.Redirect(w, r, "/login/approve", http.StatusSeeOther)
					return
				}
				s.mu.Lock()
				delete(s.pending, id)
				s.mu.Unlock()
				s.signIn(w, "password+otp")
				http.Redirect(w, r, "/account", http.StatusSeeOther)
				return
			}
			page(w, "Verification", `<h1>Verification</h1><p role="alert">Wrong code</p>`)
			return
		}
		page(w, "Verification", `<h1>Verification</h1><form method="post" action="/login/otp">
<label>Code <input name="otp" autocomplete="one-time-code" inputmode="numeric"></label>
<button type="submit">Verify</button></form>`)
	case "/login/approve":
		id, p := s.pendingOf(r)
		if p == nil || !p.otpDone {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		s.mu.Lock()
		ok := p.approved
		if ok {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		if ok {
			s.signIn(w, "approved")
			http.Redirect(w, r, "/account", http.StatusSeeOther)
			return
		}
		page(w, "Approve", `<h1>Approve the sign-in on your phone</h1><script>
setInterval(async () => { const r = await (await fetch("/login/approve/status")).json(); if (r.approved) location.reload(); }, 300);
</script>`)
	case "/login/approve/status":
		_, p := s.pendingOf(r)
		s.mu.Lock()
		ok := p != nil && p.approved
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"approved": %v}`, ok)
	case "/passkey/challenge":
		ch := base64.RawURLEncoding.EncodeToString([]byte(token()))
		s.mu.Lock()
		s.challenges[ch] = true
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"challenge": %q}`, ch)
	case "/passkey/register":
		if r.Method == http.MethodPost {
			if !s.signedIn(r) {
				http.Error(w, "sign in first", http.StatusUnauthorized)
				return
			}
			var body struct{ ID, PublicKey, ClientDataJSON string }
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			if err := s.clientData(body.ClientDataJSON, "webauthn.create", origin(r)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			spki, err := b64u(body.PublicKey)
			if err != nil {
				http.Error(w, "bad key", http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.passkeys[body.ID] = spki
			s.mu.Unlock()
			w.Write([]byte("ok"))
			return
		}
		if !s.signedIn(r) {
			page(w, "Passkeys", `<h1>Passkeys</h1><p>Not signed in</p>`)
			return
		}
		auto := ""
		if r.URL.Query().Get("auto") == "1" {
			auto = `<script>register();</script>`
		}
		page(w, "Passkeys", `<h1>Passkeys</h1><button onclick="register()">Add a passkey</button><p id="status"></p>`+webauthnJS+auto)
	case "/passkey/login":
		if r.Method == http.MethodPost {
			var body struct{ ID, AuthenticatorData, ClientDataJSON, Signature string }
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			if err := s.verifyAssertion(body.ID, body.AuthenticatorData, body.ClientDataJSON, body.Signature, origin(r), rpID(r)); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			s.signIn(w, "passkey")
			w.Write([]byte("ok"))
			return
		}
		page(w, "Passkey sign-in", `<h1>Passkey sign-in</h1><button onclick="signin()">Sign in with a passkey</button><p id="status"></p>`+webauthnJS)
	case "/framed":
		// A page embedding another page's sign-in form in a frame.
		src := r.URL.Query().Get("src")
		page(w, "Framed", `<h1>Framed</h1><iframe title="embedded" width="600" height="300" src="`+strings.ReplaceAll(src, `"`, "")+`"></iframe>`)
	}
}

func (s *Site) clientData(b64 string, typ, wantOrigin string) error {
	raw, err := b64u(b64)
	if err != nil {
		return fmt.Errorf("bad client data")
	}
	var cd struct{ Type, Challenge, Origin string }
	if json.Unmarshal(raw, &cd) != nil {
		return fmt.Errorf("bad client data")
	}
	s.mu.Lock()
	ok := s.challenges[cd.Challenge]
	delete(s.challenges, cd.Challenge)
	s.mu.Unlock()
	if cd.Type != typ || !ok || cd.Origin != wantOrigin {
		return fmt.Errorf("client data mismatch (type %s, origin %s)", cd.Type, cd.Origin)
	}
	return nil
}

func (s *Site) verifyAssertion(id, authData, clientData, sig, wantOrigin, rp string) error {
	s.mu.Lock()
	spki := s.passkeys[id]
	s.mu.Unlock()
	if spki == nil {
		return fmt.Errorf("unknown passkey")
	}
	if err := s.clientData(clientData, "webauthn.get", wantOrigin); err != nil {
		return err
	}
	ad, err1 := b64u(authData)
	cd, err2 := b64u(clientData)
	sg, err3 := b64u(sig)
	if err1 != nil || err2 != nil || err3 != nil || len(ad) < 37 {
		return fmt.Errorf("bad assertion")
	}
	rpHash := sha256.Sum256([]byte(rp))
	if !hmac.Equal(ad[:32], rpHash[:]) || ad[32]&1 == 0 {
		return fmt.Errorf("assertion is for another relying party, or without presence")
	}
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("not an EC key")
	}
	cdHash := sha256.Sum256(cd)
	msg := sha256.Sum256(append(append([]byte(nil), ad...), cdHash[:]...))
	if !ecdsa.VerifyASN1(ec, msg[:], sg) {
		return fmt.Errorf("bad signature")
	}
	return nil
}
