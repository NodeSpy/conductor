package plugin

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// sourceExt is a ConnectorABI source implementing every extension handler.
type sourceExt struct {
	funcHandler
	started chan StartSourceRequest
}

func (s *sourceExt) StartSource(_ context.Context, req StartSourceRequest, emit func(any) error) error {
	s.started <- req
	return emit(SourceEvent{Event: "e", Instance: req.Instance, Trigger: req.Triggers[0].ID, CatchUp: true})
}
func (s *sourceExt) Nudge(r NudgeRequest) (NudgeResult, error) {
	return NudgeResult{Nudged: r.Instance == "gh"}, nil
}
func (s *sourceExt) Force(r ForceRequest) (ForceResult, error) {
	return ForceResult{Events: []SourceEvent{{Event: r.Kind, Target: Target{Repo: r.Repo, Number: r.Number}}}}, nil
}
func (s *sourceExt) AppToken(r AppTokenRequest) (AppTokenResult, error) {
	if r.InstallationID == 0 {
		return AppTokenResult{}, Errorf(CodeInvalidParams, "no installation")
	}
	return AppTokenResult{Token: "tok"}, nil
}

func serveLines(t *testing.T, h Handler, lines ...string) map[string]wireMessage {
	t.Helper()
	pr, pw := io.Pipe()
	var out strings.Builder
	done := make(chan error, 1)
	go func() { done <- serve(pr, &lockedWriter{w: &out}, h) }()
	for _, l := range lines {
		if _, err := pw.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := map[string]wireMessage{}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m wireMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		key := m.Method
		if m.ID != nil {
			key = string(*m.ID)
		}
		byID[key] = m
	}
	return byID
}

// The three extension methods reach their handlers with their params decoded,
// and a handler's own *Error crosses as itself.
func TestServeSourceExtensionMethods(t *testing.T) {
	h := &sourceExt{started: make(chan StartSourceRequest, 1)}
	got := serveLines(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"plugin.nudge","params":{"instance":"gh"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.force","params":{"instance":"gh","kind":"merge_conflict","repo":"o/r","number":7}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.app_token","params":{"instance":"gh","installation_id":42}}`,
		`{"jsonrpc":"2.0","id":4,"method":"plugin.app_token","params":{"instance":"gh"}}`,
	)
	var n NudgeResult
	if err := json.Unmarshal(got["1"].Result, &n); err != nil || !n.Nudged {
		t.Fatalf("nudge: %s %v", got["1"].Result, err)
	}
	var f ForceResult
	if err := json.Unmarshal(got["2"].Result, &f); err != nil || len(f.Events) != 1 ||
		f.Events[0].Target.Repo != "o/r" || f.Events[0].Target.Number != 7 || f.Events[0].Event != "merge_conflict" {
		t.Fatalf("force: %s %v", got["2"].Result, err)
	}
	var a AppTokenResult
	if err := json.Unmarshal(got["3"].Result, &a); err != nil || a.Token != "tok" {
		t.Fatalf("app_token: %s %v", got["3"].Result, err)
	}
	if e := got["4"].Error; e == nil || e.Code != CodeInvalidParams {
		t.Fatalf("a handler's *Error must cross as itself, got %+v", e)
	}
}

// A plugin that implements none of the extension answers method-not-found —
// which the daemon reads as "not supported" — and the old surface is unchanged.
func TestServeSourceExtensionNotImplemented(t *testing.T) {
	h := ConnectorFunc(func() Decl { return Decl{Type: "old"} },
		func(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil })
	got := serveLines(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"plugin.nudge","params":{"instance":"x"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.force","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.app_token"}`,
	)
	for _, id := range []string{"1", "2", "3"} {
		if e := got[id].Error; e == nil || e.Code != CodeMethodNotFound {
			t.Errorf("request %s: want method-not-found, got %+v", id, e)
		}
	}
}

// start_source carries the triggers to a ConnectorABI plugin, and the routed
// event fields ride the existing plugin.event notification.
func TestStartSourceCarriesTriggersAndRoutedEvents(t *testing.T) {
	h := &sourceExt{started: make(chan StartSourceRequest, 1)}
	pr, pw := io.Pipe()
	var out lockedWriter
	var sb strings.Builder
	out.w = &sb
	done := make(chan error, 1)
	go func() { done <- serve(pr, &out, h) }()
	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"plugin.start_source","params":{"instance":"gh",` +
		`"triggers":[{"id":"0:gh.e","name":"t","event":"e","options":{"k":1},"filter":{"op":"match","key":"repo","val":["a/b"]}}]}}` + "\n"))
	req := <-h.started
	if len(req.Triggers) != 1 || req.Triggers[0].ID != "0:gh.e" || req.Triggers[0].Event != "e" ||
		!strings.Contains(string(req.Triggers[0].Filter), `"op":"match"`) {
		t.Fatalf("triggers did not arrive intact: %+v", req.Triggers)
	}
	// Let the emit land before closing the stream.
	for i := 0; i < 1000 && !strings.Contains(out.String(), `"method":"plugin.event"`); i++ {
		waitBriefly()
	}
	_ = pw.Close()
	<-done
	var ev SourceEvent
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m wireMessage
		if json.Unmarshal([]byte(line), &m) == nil && m.Method == MethodEvent {
			_ = json.Unmarshal(m.Params, &ev)
		}
	}
	if ev.Trigger != "0:gh.e" || ev.Instance != "gh" || !ev.CatchUp {
		t.Fatalf("routed event fields lost: %+v (stream %s)", ev, out.String())
	}
}

// An old-style event decodes into SourceEvent with every extension field zero,
// so the daemon treats it exactly as before.
func TestLegacySourceEventDecodes(t *testing.T) {
	var ev SourceEvent
	raw := `{"event":"new_comment","title":"x","target":{"Repo":"o/r","Number":3},"context":{"a":1},"dedup":"d"}`
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Target.Repo != "o/r" || ev.Target.Number != 3 || ev.Trigger != "" || ev.CatchUp || ev.TargetTrusted || ev.Instance != "" {
		t.Fatalf("legacy event decoded wrong: %+v", ev)
	}
}

// lockedWriter is a strings.Builder safe for serve's concurrent writers and a
// concurrent reader.
type lockedWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

func waitBriefly() { time.Sleep(2 * time.Millisecond) }
