package browser

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/browser/cdp"
)

// A hermetic, in-process Chrome DevTools Protocol endpoint for the browser
// dispatch tests. It speaks the real pipe transport cdp.Conn uses (NUL-
// terminated JSON over an io.Pipe pair), records every command conductor
// issues, and answers each with a canned, correctly-shaped result — so a
// verb can be driven all the way through Manager.Do and asserted by the
// protocol calls it makes, with no real Chromium.
//
// This is the test rig the browser verb coverage lacked: everything else in
// the package either tests pure functions or gates on CONDUCTOR_REQUIRE_BROWSER
// and a real browser.
type fakeCDP struct {
	mu    sync.Mutex
	calls []fakeCall

	// targetURL is what Target.getTargetInfo reports; successive entries are
	// returned on successive calls (the last one sticks), so a test can model
	// a page that navigates (await-approval's "the page moved on").
	targetURL []string
	urlN      int

	// screenshot is the raw PNG Page.captureScreenshot hands back (base64 on
	// the wire).
	screenshot []byte

	// navHistoryIndex / navHistoryIDs drive Page.getNavigationHistory.
	navHistoryIndex int
	navHistoryIDs   []int

	// evalValue is the JSON value Runtime.evaluate returns (settle polls
	// document.readyState; "complete" lets it return at once).
	evalValue json.RawMessage

	onBrowserClose func()
}

type fakeCall struct {
	Method  string
	Session string
	Params  json.RawMessage
}

// newFakeCDP wires a cdp.Conn to an in-process fake over two pipes and starts
// serving. close tears the pipes and the server goroutine down.
func newFakeCDP() (f *fakeCDP, conn *cdp.Conn, closeFn func()) {
	f = &fakeCDP{
		targetURL:       []string{"https://example.com/"},
		screenshot:      []byte("\x89PNG\r\n\x1a\nFAKE-SCREENSHOT"),
		navHistoryIndex: 1,
		navHistoryIDs:   []int{10, 11, 12},
		evalValue:       json.RawMessage(`"complete"`),
	}
	cmdR, cmdW := io.Pipe() // conductor writes commands to cmdW; fake reads cmdR
	repR, repW := io.Pipe() // fake writes replies to repW; conductor reads repR
	conn = cdp.New(cmdW, repR)
	done := make(chan struct{})
	go f.serve(cmdR, repW, done)
	var once sync.Once
	closeFn = func() {
		once.Do(func() {
			cmdR.Close()
			repW.Close()
			conn.Close()
			<-done
		})
	}
	return f, conn, closeFn
}

func (f *fakeCDP) serve(r io.ReadCloser, w io.WriteCloser, done chan struct{}) {
	defer close(done)
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		b, err := br.ReadBytes(0)
		if err != nil {
			return
		}
		var m struct {
			ID        int64           `json:"id"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b[:len(b)-1], &m) != nil {
			continue
		}
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{Method: m.Method, Session: m.SessionID, Params: m.Params})
		hook := f.onBrowserClose
		f.mu.Unlock()
		result := f.result(m.Method)
		reply := map[string]any{"id": m.ID, "result": result}
		out, _ := json.Marshal(reply)
		if _, err := w.Write(append(out, 0)); err != nil {
			return
		}
		if m.Method == "Browser.close" && hook != nil {
			hook()
		}
	}
}

// result is the canned reply for a method. Unknown methods get {} — enough
// for every command whose return value conductor discards.
func (f *fakeCDP) result(method string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "Target.getTargets":
		return map[string]any{"targetInfos": []map[string]any{
			{"targetId": "T1", "type": "page", "url": f.targetURL[0], "title": "Example"},
		}}
	case "Target.attachToTarget":
		return map[string]any{"sessionId": "S1"}
	case "Target.createTarget":
		return map[string]any{"targetId": "Tnew"}
	case "Target.getTargetInfo":
		u := f.targetURL[f.urlN]
		if f.urlN < len(f.targetURL)-1 {
			f.urlN++
		}
		return map[string]any{"targetInfo": map[string]any{"targetId": "T1", "type": "page", "url": u, "title": "Example"}}
	case "Page.getNavigationHistory":
		entries := make([]map[string]any, len(f.navHistoryIDs))
		for i, id := range f.navHistoryIDs {
			entries[i] = map[string]any{"id": id}
		}
		return map[string]any{"currentIndex": f.navHistoryIndex, "entries": entries}
	case "Page.captureScreenshot":
		return map[string]any{"data": base64.StdEncoding.EncodeToString(f.screenshot)}
	case "Runtime.evaluate":
		return map[string]any{"result": map[string]any{"value": f.evalValue}}
	default:
		return map[string]any{}
	}
}

// methodCalls returns the recorded calls for one CDP method, in order.
func (f *fakeCDP) methodCalls(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// called reports whether a CDP method was issued at all.
func (f *fakeCDP) called(method string) bool { return len(f.methodCalls(method)) > 0 }

// firstParams decodes the params of the first call to a method into a map.
func (f *fakeCDP) firstParams(t *testing.T, method string) map[string]any {
	t.Helper()
	cs := f.methodCalls(method)
	if len(cs) == 0 {
		t.Fatalf("no %s call was issued", method)
	}
	var m map[string]any
	if err := json.Unmarshal(cs[0].Params, &m); err != nil {
		t.Fatalf("%s params: %v", method, err)
	}
	return m
}

// fakeRig is a Manager wired to fake browsers, plus accessors for the fake a
// launch produced and the audit entries it recorded.
type fakeRig struct {
	m       *Manager
	mu      sync.Mutex
	fakes   []*fakeCDP
	audits  []map[string]any
	closers []func()
	// nextURLs, when set, becomes the Target.getTargetInfo URL sequence of the
	// next browser a launch produces (models a page that navigates).
	nextURLs []string
}

// setNextURLs makes the next launched browser report these URLs in order from
// Target.getTargetInfo (the last one sticks).
func (r *fakeRig) setNextURLs(urls []string) {
	r.mu.Lock()
	r.nextURLs = urls
	r.mu.Unlock()
}

// newFakeRig builds a Manager whose Launch returns a fake-backed browser.
func newFakeRig(t *testing.T, st Settings) *fakeRig {
	t.Helper()
	if st.Chrome == "" {
		chrome := filepath.Join(t.TempDir(), "chrome")
		if err := os.WriteFile(chrome, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		st.Chrome = chrome
	}
	r := &fakeRig{}
	r.m = &Manager{
		Settings: func() Settings { return st },
		StateDir: t.TempDir(),
		Audit: func(e map[string]any) {
			r.mu.Lock()
			r.audits = append(r.audits, e)
			r.mu.Unlock()
		},
		Launch: func(LaunchSpec) (*Process, error) {
			f, conn, closeFn := newFakeCDP()
			done := make(chan struct{})
			var once sync.Once
			f.onBrowserClose = func() { once.Do(func() { close(done) }) }
			p := &Process{Conn: conn, done: done, kill: func() { once.Do(func() { close(done) }) }}
			r.mu.Lock()
			if r.nextURLs != nil {
				f.targetURL = r.nextURLs
				r.nextURLs = nil
			}
			r.fakes = append(r.fakes, f)
			r.closers = append(r.closers, closeFn)
			r.mu.Unlock()
			return p, nil
		},
	}
	t.Cleanup(func() {
		r.m.Close()
		r.mu.Lock()
		cs := append([]func(){}, r.closers...)
		r.mu.Unlock()
		for _, c := range cs {
			c()
		}
	})
	return r
}

// fake is the browser the Nth (0-based) launch produced.
func (r *fakeRig) fake(n int) *fakeCDP {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakes[n]
}

func (r *fakeRig) launches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fakes)
}

func (r *fakeRig) auditEntries() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any{}, r.audits...)
}

// newFakeInstance builds a single Instance backed by the fake CDP endpoint,
// for tests that drive an Instance method (waitFor) directly.
func newFakeInstance(t *testing.T) (*Instance, *fakeCDP) {
	t.Helper()
	f, conn, closeFn := newFakeCDP()
	in, err := newInstance(context.Background(), &Process{Conn: conn, done: make(chan struct{})}, t.TempDir(), nil)
	if err != nil {
		closeFn()
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	return in, f
}
