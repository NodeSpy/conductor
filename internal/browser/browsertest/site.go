// Package browsertest is a small local website for exercising conductor's
// browser: a login with a TEST account, a form with a file upload, a
// download, and a page that reports whether the visitor is signed in. It
// serves only fixed content; nothing it holds is a real credential.
package browsertest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"sync"
)

// The test account.
const (
	User     = "test-user"
	Password = "test-password-not-secret"
)

// ReportCSV is the downloadable file's content.
const ReportCSV = "id,item,qty\n1,widget,3\n2,gadget,5\n"

// Site is the test website.
type Site struct {
	mu       sync.Mutex
	sessions map[string]bool
	// Submissions are the form posts received, in order.
	Submissions []Submission
	// Actions are the signed-in actions taken ("order") — what a logged-in
	// session can do that an anonymous one cannot.
	Actions []string
	// TOTPSeed (base32) turns on a one-time-code step after the password;
	// NeedApproval an approval "on the phone" after that (Approve).
	TOTPSeed     string
	NeedApproval bool
	// Codes are the one-time codes the site accepted (a test checks none
	// reached the agent).
	Codes []string

	pending    map[string]*pending
	challenges map[string]bool
	passkeys   map[string][]byte // credential id (base64url) → SPKI public key
	signIns    []string          // how each session was made: password | passkey
}

// Submission is one form post.
type Submission struct {
	Name, Color, FileName string
	FileSize              int
	FileHead              string
	SignedIn              bool
}

// New makes the site.
func New() *Site {
	return &Site{sessions: map[string]bool{}, pending: map[string]*pending{}, challenges: map[string]bool{}, passkeys: map[string][]byte{}}
}

// Session mints a signed-in session id (what a login sets as the "sid"
// cookie) — for seeding a saved session without driving the login form.
func (s *Site) Session() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[id] = true
	s.mu.Unlock()
	return id
}

func (s *Site) signedIn(r *http.Request) bool {
	c, err := r.Cookie("sid")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[c.Value]
}

// Snapshot returns copies of what the site recorded.
func (s *Site) Snapshot() ([]Submission, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Submission(nil), s.Submissions...), append([]string(nil), s.Actions...)
}

func page(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title></head><body><main>%s</main></body></html>`, html.EscapeString(title), body)
}

// ServeHTTP serves the site.
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		page(w, "Test Site", `<h1>Test Site</h1><nav><a href="/login">Sign in</a> <a href="/account">Account</a> <a href="/form">Form</a> <a href="/files/report.csv">Download report</a></nav>`)
	case "/login":
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("username") == User && r.Form.Get("password") == Password {
				s.passwordOK(w, r)
				return
			}
			page(w, "Sign in", `<h1>Sign in</h1><p role="alert">Wrong username or password</p>`)
			return
		}
		page(w, "Sign in", `<h1>Sign in</h1><form method="post" action="/login">
<label>Username <input name="username" autocomplete="username"></label>
<label>Password <input name="password" type="password" autocomplete="current-password"></label>
<button type="submit">Sign in</button></form>`)
	case "/account":
		if s.signedIn(r) {
			page(w, "Account", `<h1>Account</h1><p>Signed in as `+User+`</p><form method="post" action="/order"><button type="submit">Place order</button></form>`)
			return
		}
		page(w, "Account", `<h1>Account</h1><p>Not signed in</p><a href="/login">Sign in</a>`)
	case "/order":
		if r.Method != http.MethodPost {
			http.Error(w, "post only", http.StatusMethodNotAllowed)
			return
		}
		if !s.signedIn(r) {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.Actions = append(s.Actions, "order")
		s.mu.Unlock()
		page(w, "Ordered", `<h1>Order placed</h1>`)
	case "/form":
		if r.Method == http.MethodPost {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sub := Submission{Name: r.FormValue("name"), Color: r.FormValue("color"), SignedIn: s.signedIn(r)}
			if f, h, err := r.FormFile("attachment"); err == nil {
				b, _ := io.ReadAll(f)
				f.Close()
				sub.FileName, sub.FileSize = h.Filename, len(b)
				if len(b) > 64 {
					b = b[:64]
				}
				sub.FileHead = string(b)
			}
			s.mu.Lock()
			s.Submissions = append(s.Submissions, sub)
			s.mu.Unlock()
			page(w, "Thanks", fmt.Sprintf(`<h1>Thanks</h1><p>Received %s, color %s, file %s (%d bytes)</p>`,
				html.EscapeString(sub.Name), html.EscapeString(sub.Color), html.EscapeString(sub.FileName), sub.FileSize))
			return
		}
		page(w, "Form", `<h1>Form</h1><form method="post" action="/form" enctype="multipart/form-data">
<label>Name <input name="name"></label>
<label>Color <select name="color"><option value="">Pick one</option><option value="red">Red</option><option value="blue">Blue</option></select></label>
<label>Attachment <input type="file" name="attachment"></label>
<button type="submit">Submit</button></form>`)
	case "/files/report.csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
		io.WriteString(w, ReportCSV)
	case "/out":
		// A redirect off the allowlist: the browser follows it, the proxy
		// refuses it.
		to := r.URL.Query().Get("to")
		http.Redirect(w, r, to, http.StatusFound)
	case "/codefield":
		// A code field whose value the page echoes as text.
		page(w, "Code", `<h1>Code</h1><label>Code <input name="otp" autocomplete="one-time-code" oninput="document.getElementById('echo').textContent = 'You typed ' + this.value"></label><p id="echo"></p>`)
	case "/login/otp", "/login/approve", "/login/approve/status", "/passkey/register", "/passkey/login", "/passkey/challenge", "/framed":
		s.auth(w, r)
	case "/evil":
		// A page that tries to make the visitor's browser act elsewhere.
		page(w, "Evil", `<h1>Evil</h1><img src="http://127.0.0.2:9/x.png"><a href="http://example.invalid/">away</a>`)
	default:
		http.NotFound(w, r)
	}
}
