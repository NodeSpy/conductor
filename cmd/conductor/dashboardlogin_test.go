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
	m, err := auth.NewManager(auth.Deps{Store: st, Operators: func() map[string]string { return map[string]string{"ops": strings.TrimSpace(string(pub))} }})
	if err != nil {
		t.Fatal(err)
	}
	mux := m.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, auth.WithReqInfo(r, auth.ListenerLocalhost, "", ""))
	}))
	t.Cleanup(srv.Close)
	return srv, key
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
