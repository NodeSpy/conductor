package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// approveFixture is a real auth Manager behind an httptest server (tagged as the
// localhost listener), with operator "ops" holding a fresh ed25519 key.
func approveFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, key, _ := approveFixtureWith(t, nil)
	return srv, key
}

// approveFixtureWith is approveFixture with a DescribeStepup hook, returning the
// Manager too (to mint a session for a step-up).
func approveFixtureWith(t *testing.T, describe func(string, auth.Purpose, string) string) (*httptest.Server, string, *auth.Manager) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	st, err := auth.OpenStore(filepath.Join(dir, "auth"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := auth.NewManager(auth.Deps{Store: st, DescribeStepup: describe,
		Operators: func() map[string]string { return map[string]string{"ops": strings.TrimSpace(string(pub))} }})
	if err != nil {
		t.Fatal(err)
	}
	mux := m.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, auth.WithReqInfo(r, auth.ListenerLocalhost, "", ""))
	}))
	t.Cleanup(srv.Close)
	return srv, key, m
}

func post(t *testing.T, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestApproveLoginCLI(t *testing.T) {
	srv, key := approveFixture(t)
	_, begun := post(t, srv.URL+"/api/v1/auth/ssh/begin", map[string]string{"operator": "ops"})
	code, _ := begun["login_code"].(string)
	id, _ := begun["challenge_id"].(string)
	if code == "" || id == "" {
		t.Fatalf("begin: %v", begun)
	}

	// The wrong operator, and a URL that isn't the challenge's origin (a relay),
	// are refused before anything is signed.
	if err := dashboardApproveLogin(code, map[string]string{"url": srv.URL, "operator": "someone", "key": key, "yes": "true"}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatal("approve-login must refuse a request for another operator")
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A look-alike that forwards to the real dashboard.
		req, _ := http.NewRequest(r.Method, srv.URL+r.URL.Path, r.Body)
		req.Header = r.Header
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer relay.Close()
	if err := dashboardApproveLogin(code, map[string]string{"url": relay.URL, "operator": "ops", "key": key, "yes": "true"}); err == nil || !strings.Contains(err.Error(), "not "+relay.URL) {
		t.Fatalf("approve-login must refuse a challenge whose origin isn't --url: %v", err)
	}

	if err := dashboardApproveLogin(code, map[string]string{"url": srv.URL, "operator": "ops", "key": key, "yes": "true"}); err != nil {
		t.Fatalf("approve-login: %v", err)
	}
	resp, polled := post(t, srv.URL+"/api/v1/auth/ssh/poll", map[string]string{"challenge_id": id})
	if resp.StatusCode != 200 || polled["operator"] != "ops" || polled["method"] != "ssh" {
		t.Fatalf("poll after approve: %d %v", resp.StatusCode, polled)
	}
	var cookie bool
	for _, c := range resp.Cookies() {
		cookie = cookie || c.Name == auth.SessionCookie
	}
	if !cookie {
		t.Fatal("the polling page must receive the session cookie")
	}
	if err := dashboardApproveLogin(code, map[string]string{"url": srv.URL, "operator": "ops", "key": key, "yes": "true"}); err == nil {
		t.Fatal("a login code is single-use")
	}
}

func TestDashboardSignOnlyChallenges(t *testing.T) {
	if err := dashboardSign(string(auth.EnrollPayload("ops", time.Now())), map[string]string{"key": "/nonexistent"}); err == nil {
		t.Fatal("sign must refuse anything that isn't a dashboard login/step-up challenge")
	}
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	ferr := fn()
	w.Close()
	os.Stderr = old
	return string(<-done), ferr
}

// A relay can't be told apart from the operator's own browser, so approve-login
// shows everything it knows before asking: where the request came from, the
// device it claims to be, when it started, and that a code someone sent you
// signs them in.
func TestApproveLoginShowsTheRequest(t *testing.T) {
	srv, key := approveFixture(t)
	b, _ := json.Marshal(map[string]string{"operator": "ops"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/ssh/begin", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit Chrome/141 Safari")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var begun auth.SSHChallengeView
	_ = json.NewDecoder(resp.Body).Decode(&begun)
	resp.Body.Close()

	out, err := captureStderr(t, func() error {
		return dashboardApproveLogin(begun.Code, map[string]string{"url": srv.URL, "operator": "ops", "key": key, "yes": "true"})
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	for _, want := range []string{"Sign-in request for operator ops", "from:", "127.0.0.1", "device:", "Chrome", "started:", "If someone sent you this code"} {
		if !strings.Contains(out, want) {
			t.Errorf("approve-login output lacks %q:\n%s", want, out)
		}
	}
}

// For a step-up it also shows the action: the purpose, the dashboard's summary
// of it and the start of the digest the waiting page shows.
func TestApproveLoginShowsTheStepupAction(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	srv, key, m := approveFixtureWith(t, func(_ string, p auth.Purpose, d string) string {
		if p == auth.PurposeApprove && d == digest {
			return "approve bot b1's proposal p1: box.run rm -rf /srv/data"
		}
		return ""
	})
	sr := auth.WithReqInfo(httptest.NewRequest("POST", "/", nil), auth.ListenerLocalhost, "", "")
	ck, sess, err := m.CreateSessionFor(sr, "ops", auth.MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"purpose": string(auth.PurposeApprove), "digest": digest})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/stepup/ssh/begin", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, sess.CSRF)
	req.AddCookie(ck)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var begun auth.SSHChallengeView
	_ = json.NewDecoder(resp.Body).Decode(&begun)
	resp.Body.Close()
	if begun.Code == "" {
		t.Fatalf("step-up begin: %d", resp.StatusCode)
	}
	out, err := captureStderr(t, func() error {
		return dashboardApproveLogin(begun.Code, map[string]string{"url": srv.URL, "operator": "ops", "key": key, "yes": "true"})
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	for _, want := range []string{"Confirmation request for operator ops", "purpose:", "approve", "action:", "rm -rf /srv/data", "digest:", digest[:12]} {
		if !strings.Contains(out, want) {
			t.Errorf("approve-login output lacks %q:\n%s", want, out)
		}
	}
}

func TestWhenAgo(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{now, "(now)"},
		{now.Add(-45 * time.Second), "(45s ago)"},
		{now.Add(-200 * time.Second), "(3m 20s ago)"},
		{now.Add(5 * time.Minute), "(in 5m)"},
		{now.Add(-125 * time.Minute), "(2h 5m ago)"},
	} {
		if got := whenAgo(c.at.Format(time.RFC3339), now); !strings.HasSuffix(got, c.want) {
			t.Errorf("whenAgo(%v) = %q, want suffix %q", c.at, got, c.want)
		}
	}
}
