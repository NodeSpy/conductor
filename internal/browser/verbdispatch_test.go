package browser

import (
	"context"
	"testing"
	"time"
)

// These tests drive each agent verb through Manager.Do against the fake CDP
// endpoint and assert the protocol calls (and the dispatch-level behaviour)
// it produces. They close the gap the claims audit flagged: the verbs type,
// press, scroll, back, forward, reload, tabs, status and close, plus the
// operator peek/sessions and the await-approval verb path, had no test
// exercising the dispatch path — only env-gated real-browser coverage.

func fakeAgent(t *testing.T) Principal {
	t.Helper()
	return Principal{Key: "k1", Identity: "agent:dev@acme/app", Workspace: t.TempDir(), Requester: RequesterOperator, Agent: "dev"}
}

func browseSettings() Settings { return Settings{Allow: []string{"example.com"}} }

// doOrFatal runs a verb and fails on error.
func doOrFatal(t *testing.T, m *Manager, c Principal, action string, args map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := m.Do(ctx, c, action, args)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return out
}

// TestVerbTypeInsertsText: the `type` verb reaches the page as Input.insertText
// carrying exactly the agent's text (not a fill/select) — proving the dispatch
// routes "type" to typeText.
func TestVerbTypeInsertsText(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "type", map[string]any{"text": "hello world"})

	calls := r.fake(0).methodCalls("Input.insertText")
	if len(calls) != 1 {
		t.Fatalf("type should issue exactly one Input.insertText, got %d", len(calls))
	}
	p := r.fake(0).firstParams(t, "Input.insertText")
	if p["text"] != "hello world" {
		t.Fatalf("typed text: got %v", p["text"])
	}
}

// TestVerbPressDispatchesKey: `press` sends a keyDown+keyUp for the named key.
func TestVerbPressDispatchesKey(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "press", map[string]any{"key": "Enter"})

	calls := r.fake(0).methodCalls("Input.dispatchKeyEvent")
	if len(calls) != 2 {
		t.Fatalf("press should dispatch keyDown+keyUp, got %d key events", len(calls))
	}
	down := r.fake(0).firstParams(t, "Input.dispatchKeyEvent")
	if down["type"] != "keyDown" || down["key"] != "Enter" {
		t.Fatalf("first key event: got type=%v key=%v", down["type"], down["key"])
	}
}

// TestVerbScrollWheelsThePage: `scroll` with no ref wheels the viewport by dy.
func TestVerbScrollWheelsThePage(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "scroll", map[string]any{"dy": 600.0})

	calls := r.fake(0).methodCalls("Input.dispatchMouseEvent")
	if len(calls) == 0 {
		t.Fatal("scroll issued no mouse event")
	}
	p := r.fake(0).firstParams(t, "Input.dispatchMouseEvent")
	if p["type"] != "mouseWheel" {
		t.Fatalf("scroll should wheel, got type=%v", p["type"])
	}
	if dy, _ := p["deltaY"].(float64); dy != 600 {
		t.Fatalf("scroll deltaY: got %v", p["deltaY"])
	}
}

// TestVerbBackForwardNavigateHistory: `back` goes one entry earlier, `forward`
// one later — the exact history entry id each resolves to.
func TestVerbBackForwardNavigateHistory(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want float64
	}{
		{"back", 10},    // currentIndex 1 - 1 -> entries[0].id = 10
		{"forward", 12}, // currentIndex 1 + 1 -> entries[2].id = 12
	} {
		r := newFakeRig(t, browseSettings())
		c := fakeAgent(t)
		doOrFatal(t, r.m, c, tc.verb, nil)

		if !r.fake(0).called("Page.getNavigationHistory") {
			t.Fatalf("%s did not read the navigation history", tc.verb)
		}
		p := r.fake(0).firstParams(t, "Page.navigateToHistoryEntry")
		if id, _ := p["entryId"].(float64); id != tc.want {
			t.Fatalf("%s navigated to entry %v, want %v", tc.verb, p["entryId"], tc.want)
		}
	}
}

// TestVerbBackRefusesPastTheStart: at the first history entry, `back` has
// nowhere to go and refuses without navigating.
func TestVerbBackRefusesPastTheStart(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	// A browser is created lazily; prime it, then rewrite the history fixture
	// to sit at the first entry so there is no earlier page.
	doOrFatal(t, r.m, c, "reload", nil)
	f := r.fake(0)
	f.mu.Lock()
	f.navHistoryIndex, f.navHistoryIDs = 0, []int{10, 11}
	f.mu.Unlock()

	ctx := context.Background()
	if _, err := r.m.Do(ctx, c, "back", nil); err == nil {
		t.Fatal("back from the first entry should error")
	}
	if f.called("Page.navigateToHistoryEntry") {
		t.Fatal("a refused back must not navigate")
	}
}

// TestVerbReload: `reload` reloads the page.
func TestVerbReload(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "reload", nil)
	if !r.fake(0).called("Page.reload") {
		t.Fatal("reload did not issue Page.reload")
	}
}

// TestVerbTabsListsTabs: `tabs` returns the tab list in its reply.
func TestVerbTabsListsTabs(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	out := doOrFatal(t, r.m, c, "tabs", nil)
	tabs, ok := out["tabs"].([]TabInfo)
	if !ok || len(tabs) == 0 {
		t.Fatalf("tabs reply: got %#v", out["tabs"])
	}
	if tabs[0].Index != 1 {
		t.Fatalf("first tab index: got %d", tabs[0].Index)
	}
}

// TestVerbStatusNeedsNoBrowser: `status` reports the identity, allowlist and
// planned isolation without ever starting a browser.
func TestVerbStatusNeedsNoBrowser(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	out := doOrFatal(t, r.m, c, "status", nil)

	if r.launches() != 0 {
		t.Fatalf("status started %d browser(s); it must start none", r.launches())
	}
	if out["identity"] != c.Identity {
		t.Fatalf("status identity: got %v", out["identity"])
	}
	if out["isolation"] != "namespace" {
		t.Fatalf("status isolation on linux: got %v", out["isolation"])
	}
	allow, _ := out["allow"].([]string)
	if len(allow) != 1 || allow[0] != "example.com" {
		t.Fatalf("status allow: got %v", out["allow"])
	}
}

// TestVerbCloseTearsTheBrowserDown: `close` ends the session's browser (sends
// Browser.close) and the session no longer appears to the operator.
func TestVerbCloseTearsTheBrowserDown(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "reload", nil) // start a browser
	if len(r.m.Sessions()) != 1 {
		t.Fatalf("expected one live session, got %d", len(r.m.Sessions()))
	}
	out := doOrFatal(t, r.m, c, "close", nil)
	if out["closed"] != true {
		t.Fatalf("close reply: got %#v", out)
	}
	if !r.fake(0).called("Browser.close") {
		t.Fatal("close did not send Browser.close to the browser")
	}
	if len(r.m.Sessions()) != 0 {
		t.Fatalf("closed session still listed: %d", len(r.m.Sessions()))
	}
}

// TestOperatorSessions: once an agent has a browser, the operator's `sessions`
// lists it with its identity and requester.
func TestOperatorSessions(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	if len(r.m.Sessions()) != 0 {
		t.Fatal("no session should exist before any action")
	}
	doOrFatal(t, r.m, c, "reload", nil)

	ss := r.m.Sessions()
	if len(ss) != 1 {
		t.Fatalf("sessions: got %d", len(ss))
	}
	if ss[0].Identity != c.Identity || ss[0].Requester != RequesterOperator {
		t.Fatalf("session info: %+v", ss[0])
	}
	if ss[0].ID == "" {
		t.Fatal("session has no id")
	}
}

// TestOperatorPeek: the operator's `peek` screenshots a session's active tab,
// returns the PNG and the redacted URL, and audits itself as an operator peek.
func TestOperatorPeek(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	c := fakeAgent(t)
	doOrFatal(t, r.m, c, "reload", nil)
	id := r.m.Sessions()[0].ID

	ctx := context.Background()
	png, url, err := r.m.Peek(ctx, id)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if string(png) != string(r.fake(0).screenshot) {
		t.Fatalf("peek returned %d bytes, not the captured screenshot", len(png))
	}
	if url == "" {
		t.Fatal("peek returned no url")
	}
	var peeked bool
	for _, e := range r.auditEntries() {
		if e["action"] == "peek" {
			peeked = true
			if e["by"] != "operator" {
				t.Fatalf("peek audit not marked operator: %+v", e)
			}
		}
	}
	if !peeked {
		t.Fatal("peek was not audited")
	}
}

// TestPeekUnknownSession: peeking a session that does not exist is a clear
// error, not a panic.
func TestPeekUnknownSession(t *testing.T) {
	r := newFakeRig(t, browseSettings())
	if _, _, err := r.m.Peek(context.Background(), "nope"); err == nil {
		t.Fatal("peeking an unknown session should error")
	}
}

// TestVerbAwaitApproval: the await-approval verb notifies the operator and
// reports the sign-in as approved once the page moves on from where it started.
func TestVerbAwaitApproval(t *testing.T) {
	st := browseSettings()
	st.Credentials = []Credential{{
		Name: "acme", Origin: "https://example.com", Identities: []string{"agent:*"}, Approval: true,
	}}
	r := newFakeRig(t, st)
	var notified string
	r.m.Notify = func(msg string) { notified = msg }

	c := fakeAgent(t)
	// Model a page that navigates: first URL is the sign-in page, then it
	// "moves on" — awaitApproval reads this as the operator having approved.
	r.setNextURLs([]string{"https://example.com/signin", "https://example.com/home"})

	out := doOrFatal(t, r.m, c, "await-approval", map[string]any{"credential": "acme", "timeout": 3.0})
	approval, _ := out["approval"].(string)
	if approval == "" {
		t.Fatalf("await-approval gave no verdict: %#v", out)
	}
	if approval != "approved (the page moved on)" {
		t.Fatalf("await-approval verdict: %q", approval)
	}
	if notified == "" {
		t.Fatal("await-approval did not notify the operator")
	}
}
