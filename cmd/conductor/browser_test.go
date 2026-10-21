package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestBrowserCallerBotNarrowing (C1): a bot session's Principal carries its
// bots.<name>.browser narrowing, so the enforcement at ForPrincipal binds at
// runtime — not just a team binding that would reach the whole team's set.
func TestBrowserCallerBotNarrowing(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{
		"intern": {Team: "platform", Browser: &config.BotBrowser{Logins: []string{"status.example.com"}, Credentials: []string{"app"}}},
		"lead":   {Team: "platform"}, // no browser block: no narrowing
	}}
	browserCfg.Store(cfg)
	defer browserCfg.Store(nil)

	c, err := browserCaller("tok", skill.Identity{Bot: "intern", BotRole: "member",
		Sandbox: &skill.SandboxBinding{Identity: "agent:intern@o/r", Workspace: "/w"}})
	if err != nil {
		t.Fatalf("intern: %v", err)
	}
	if !reflect.DeepEqual(c.BotCredentials, []string{"app"}) || !reflect.DeepEqual(c.BotLogins, []string{"status.example.com"}) {
		t.Fatalf("narrowing not carried onto the Principal: logins=%v creds=%v", c.BotLogins, c.BotCredentials)
	}
	sensitive := browser.Credential{Name: "sensitive", Identities: []string{"team:platform"}}
	if sensitive.ForPrincipal(c) {
		t.Fatal("C1: at runtime the narrowed bot reached a team credential it was denied")
	}

	// A bot with no browser block gets no narrowing (nil → whole team set).
	lead, err := browserCaller("tok", skill.Identity{Bot: "lead",
		Sandbox: &skill.SandboxBinding{Identity: "agent:lead@o/r", Workspace: "/w"}})
	if err != nil {
		t.Fatalf("lead: %v", err)
	}
	if lead.BotCredentials != nil || lead.BotLogins != nil {
		t.Fatalf("an un-narrowed bot must carry nil narrowing: logins=%v creds=%v", lead.BotLogins, lead.BotCredentials)
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

func TestCountHumanUsers(t *testing.T) {
	out := "_spotlight 89\nroot 0\ndaemon 1\nnobody -2\ndaniel 501\nguest 201\nalice 502\n_mbsetupuser 248\n"
	if n := countHumanUsers(out); n != 2 {
		t.Fatalf("%d", n)
	}
	if countHumanUsers("daniel 501\n") != 1 || countHumanUsers("") != 0 {
		t.Fatal("one / none")
	}
}

// TestBrowserIsoDecision: Linux stays on its namespaces unless asked; a
// required container that cannot be had refuses every launch; macOS without
// one warns plainly.
func TestBrowserIsoDecision(t *testing.T) {
	defer browserIsoState.Store(nil)
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	fallbackIso("on", "linux", "no container engine", logf)
	if st := applyIso(browser.Settings{}); !strings.Contains(st.Unavailable, "container: on") || st.Engine != nil {
		t.Fatalf("required and impossible: %+v", st)
	}
	fallbackIso("auto", "darwin", "no container engine", logf)
	st := applyIso(browser.Settings{})
	if st.Unavailable != "" || !strings.Contains(st.IsolationNote, "single-user Mac only") || !strings.Contains(st.IsolationNote, "without its own sandbox") {
		t.Fatalf("macOS fallback: %+v", st)
	}
	if !strings.Contains(logs[len(logs)-1], "WARNING") {
		t.Fatalf("not logged as a warning: %v", logs)
	}
	if seatbeltNote("linux", "x") != "" {
		t.Fatal("Linux has no Seatbelt note")
	}
}

// TestBrowserIsoBoot: macOS uses the container whenever an engine is present,
// refusing (never weaker) until it is proven; Linux stays native under auto;
// a failed proof falls back with the warning (auto) or refuses (on).
func TestBrowserIsoBoot(t *testing.T) {
	defer browserIsoState.Store(nil)
	logf := func(string, ...any) {}
	eng := &browser.ContainerEngine{Bin: "/x/docker", Kind: "docker"}
	found := func() (*browser.ContainerEngine, error) { return eng, nil }
	none := func() (*browser.ContainerEngine, error) { return nil, errors.New("no container engine") }
	gate := make(chan struct{})
	ok := func(context.Context, *browser.ContainerEngine, func(string, ...any)) (string, error) {
		<-gate
		return "conductor-browser:t", nil
	}
	bad := func(context.Context, *browser.ContainerEngine, func(string, ...any)) (string, error) {
		return "", errors.New("chrome://sandbox: not adequately sandboxed")
	}
	wait := func(cond func(browser.Settings) bool) browser.Settings {
		for i := 0; i < 200; i++ {
			if st := applyIso(browser.Settings{}); cond(st) {
				return st
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("never: %+v", applyIso(browser.Settings{}))
		return browser.Settings{}
	}

	isoDecide(context.Background(), &config.BrowserConfig{}, "darwin", found, ok, logf)
	if st := applyIso(browser.Settings{}); !strings.Contains(st.Unavailable, "being prepared") || st.Engine != nil {
		t.Fatalf("macOS with an engine, before the proof: %+v", st)
	}
	close(gate)
	if st := wait(func(s browser.Settings) bool { return s.Engine != nil }); st.Unavailable != "" || st.Image != "conductor-browser:t" || st.IsolationNote != "" {
		t.Fatalf("macOS, proven: %+v", st)
	}

	isoDecide(context.Background(), &config.BrowserConfig{}, "linux", found, ok, logf)
	if st := applyIso(browser.Settings{}); st.Engine != nil || st.Unavailable != "" {
		t.Fatalf("Linux auto: %+v", st)
	}
	isoDecide(context.Background(), &config.BrowserConfig{Container: "off"}, "darwin", found, ok, logf)
	if st := applyIso(browser.Settings{}); st.Engine != nil || !strings.Contains(st.IsolationNote, "single-user Mac only") {
		t.Fatalf("macOS off: %+v", st)
	}
	isoDecide(context.Background(), &config.BrowserConfig{}, "darwin", none, ok, logf)
	if st := applyIso(browser.Settings{}); st.Engine != nil || st.Unavailable != "" || !strings.Contains(st.IsolationNote, "single-user Mac only") {
		t.Fatalf("macOS without an engine: %+v", st)
	}
	isoDecide(context.Background(), &config.BrowserConfig{Container: "on"}, "linux", none, ok, logf)
	if st := applyIso(browser.Settings{}); !strings.Contains(st.Unavailable, "container: on") {
		t.Fatalf("on, no engine: %+v", st)
	}
	isoDecide(context.Background(), &config.BrowserConfig{}, "darwin", found, bad, logf)
	if st := wait(func(s browser.Settings) bool { return s.IsolationNote != "" }); st.Engine != nil || st.Unavailable != "" || !strings.Contains(st.IsolationNote, "could not sandbox Chromium") {
		t.Fatalf("auto, proof failed: %+v", st)
	}
	isoDecide(context.Background(), &config.BrowserConfig{Container: "on"}, "darwin", found, bad, logf)
	if st := wait(func(s browser.Settings) bool { return strings.Contains(s.Unavailable, "container: on") }); st.Engine != nil {
		t.Fatalf("on, proof failed: %+v", st)
	}
}
