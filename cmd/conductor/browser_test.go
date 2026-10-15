package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestBrowserCallerBinding: a token's browser session is its sandbox —
// bound at mint, else its dispatch's registered workspace — and a token
// with neither gets no browser.
func TestBrowserCallerBinding(t *testing.T) {
	c, err := browserCaller("tok", skill.Identity{Agent: "a", Requester: "operator",
		Sandbox: &skill.SandboxBinding{Identity: "dir:/w", Workspace: "/w", Egress: []string{"x.example"}, Deny: []string{"/w/ro"}}})
	if err != nil || c.Identity != "dir:/w" || c.Workspace != "/w" || c.Requester != "operator" || c.Key != browser.SessionKey("tok") || c.Deny[0] != "/w/ro" {
		t.Fatalf("bound: %+v %v", c, err)
	}
	if c2, _ := browserCaller("tok2", skill.Identity{Sandbox: &skill.SandboxBinding{Identity: "i", Workspace: "/w"}}); c2.Key == c.Key {
		t.Fatal("each token is its own browser session")
	}
	ws := t.TempDir()
	defer agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "agent:x@o/r", Dispatch: "d-1", ReadOnly: []string{ws + "/ro"}, Hide: []string{ws + "/h"}})()
	c, err = browserCaller("tok", skill.Identity{Dispatch: "d-1", Requester: "event"})
	if err != nil || c.Identity != "agent:x@o/r" || c.Requester != "event" || len(c.Deny) != 2 {
		t.Fatalf("by dispatch: %+v %v", c, err)
	}
	if _, err := browserCaller("tok", skill.Identity{Dispatch: "d-unknown"}); err == nil {
		t.Fatal("an unregistered dispatch got a browser")
	}
	if _, err := browserCaller("tok", skill.Identity{Agent: "sandbox:scratch"}); err == nil {
		t.Fatal("a token with no sandbox got a browser")
	}
	// The requester hook (members replace it) decides, not the token alone.
	old := requesterFor
	requesterFor = func(skill.Identity) string { return "user" }
	defer func() { requesterFor = old }()
	if c, _ := browserCaller("t", skill.Identity{Requester: "operator", Sandbox: &skill.SandboxBinding{Identity: "i", Workspace: "/w"}}); c.Requester != "user" {
		t.Fatalf("requester hook: %q", c.Requester)
	}
}

func TestSplitBrowserArgs(t *testing.T) {
	pos, f := splitBrowserArgs([]string{"e3", "hello", "world", "--submit", "--ref", "e4", "--dy=200", "--", "--x"})
	if !reflect.DeepEqual(pos, []string{"e3", "hello", "world", "--x"}) || f["submit"] != true || f["ref"] != "e4" || f["dy"] != "200" {
		t.Fatalf("%v %v", pos, f)
	}
}

// TestBrowserControl: the operator's commands save only an opted-in
// site's cookies for an opted-in identity, and only a passkey for the
// credential's own site.
func TestBrowserControl(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Browser: &config.BrowserConfig{
		Allow:  []string{"app.example.com"},
		Logins: []config.BrowserLogin{{Site: "app.example.com", Identities: []string{"agent:t@*"}}},
		Credentials: []config.BrowserCredential{{Name: "app", Origin: "https://app.example.com", Identities: []string{"agent:t@o/r"},
			Passkey: &config.BrowserSecretRef{Vault: "v", Key: "pk"}}},
	}}
	cfg.Store.StateFile = filepath.Join(dir, "state.json")
	browserCfg.Store(cfg)
	var audit []map[string]any
	m := &browser.Manager{StateDir: filepath.Join(dir, "browser"), Audit: func(e map[string]any) { audit = append(audit, e) }}
	browserMgr.Store(m)
	defer browserMgr.Store(nil)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	call := func(req controlRequest) controlResponse { return browserControl(a, req) }

	cookies := []browser.Cookie{{Name: "sid", Value: "s1", Domain: "app.example.com"}, {Name: "x", Value: "other", Domain: ".evil.example"}}
	if r := call(controlRequest{Cmd: "browser-cookies-save", Identity: "agent:other@o/r", Site: "app.example.com", Cookies: cookies}); r.Error == "" {
		t.Fatal("saved a session for an identity the site is not opted in for")
	}
	if r := call(controlRequest{Cmd: "browser-cookies-save", Identity: "agent:t@o/r", Site: "evil.example", Cookies: cookies}); r.Error == "" {
		t.Fatal("saved a session for a site not opted in")
	}
	r := call(controlRequest{Cmd: "browser-cookies-save", Identity: "agent:t@o/r", Site: "app.example.com", Cookies: cookies})
	if r.Error != "" || !strings.Contains(r.Msg, "saved 1 cookie") {
		t.Fatalf("save: %+v", r)
	}
	j, ok, _ := m.Logins().Load("agent:t@o/r", "app.example.com")
	if !ok || len(j.Cookies) != 1 || j.Cookies[0].Name != "sid" {
		t.Fatalf("only the site's cookie is kept: %+v", j)
	}
	r = call(controlRequest{Cmd: "browser-logins"})
	if strings.Contains(string(r.Data), "s1") {
		t.Fatal("logins listed a cookie value")
	}
	if r := call(controlRequest{Cmd: "browser-passkey-save", Credential: "app", Passkey: &browser.Passkey{RPID: "evil.example", CredentialID: "c", PrivateKey: "k"}}); !strings.Contains(r.Error, "not app.example.com") {
		t.Fatalf("a passkey for another site: %+v", r)
	}
	if r := call(controlRequest{Cmd: "browser-logout", Identity: "agent:t@o/r", Site: "app.example.com"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, ok, _ := m.Logins().Load("agent:t@o/r", "app.example.com"); ok {
		t.Fatal("logout left the session")
	}
	for _, e := range audit {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "s1") {
			t.Fatalf("audit holds a cookie value: %s", b)
		}
	}
	_ = time.Now
}
