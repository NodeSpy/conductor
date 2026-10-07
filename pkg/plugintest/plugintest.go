// Package plugintest is the vendor-neutral CONFORMANCE harness for source
// plugins (docs/design/plugin-contract.md §1.12): a table of cases — a
// connection, triggers, a fake upstream, deliveries and polls — each with the
// events it must produce, played against a plugin over the real wire.
//
// The harness knows nothing about any vendor. A suite supplies what is
// vendor-shaped: the fake upstream (an http.Handler the connection points
// at), how a delivery is signed, and the probe that says the plugin's
// listener is up. A plugin repository runs its suite against its released
// binary (BinaryStarter) and, during development, against its handler in
// process (HandlerStarter) — the same cases, the same assertions.
package plugintest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/pkg/plugin"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

// Env is what one case run is wired to.
type Env struct {
	// Upstream is the fake upstream's base URL (point the connection at it).
	Upstream string
	// Listen is the address the plugin's webhook listener must bind.
	Listen string
	// Path is the webhook path.
	Path string
}

// Trigger is one configured trigger, in the operator's terms.
type Trigger struct {
	On      string // the event name (`on: <conn>.<On>`)
	Name    string
	Filter  any // the `filter:` surface form (string, map, list), or nil
	Options map[string]any
}

// Delivery is one inbound HTTP request to the plugin's listener.
type Delivery struct {
	Headers map[string]string
	Body    string
}

// Step is one thing a case does: deliver a request, or poll.
type Step struct {
	Deliver *Delivery
	// Poll sends plugin.poll {mode: now}.
	Poll bool
	// Wait pauses before the next step (for a plugin's own cadence).
	Wait time.Duration
}

// Want is one event the case must produce.
type Want struct {
	Event   string
	Trigger string // the trigger NAME it must be routed to ("" = unrouted)
	Target  string // target key ("" = any)
	CatchUp bool
	// Context facts it must carry (compared as strings, so 1 == 1.0).
	Context map[string]any
	// Len: context lists' required lengths.
	Len map[string]int
	// Absent: facts it must NOT carry.
	Absent []string
}

// Got is one event a plugin produced, as compared.
type Got struct {
	Event    string
	Trigger  string // routed trigger name, "" when unrouted
	Target   string // target key (Target.Key, else the legacy Repo#Number)
	CatchUp  bool
	Assigned bool
	Context  map[string]any
}

// Case is one conformance case.
type Case struct {
	Name       string
	Connection func(Env) map[string]any
	Triggers   []Trigger
	// Upstream serves the fake upstream for this case (nil: a 404 for
	// every request).
	Upstream func(t *testing.T) http.Handler
	Steps    []Step
	Want     []Want
	// After, when set, runs once the events are compared (assert on the
	// fake upstream's state: what the plugin wrote).
	After func(t *testing.T)
}

// Suite is the vendor-shaped half of a table.
type Suite struct {
	// Instance is the instance name start_source carries (default "src").
	Instance string
	// Sign returns the headers that authenticate body (nil: none).
	Sign func(body []byte) map[string]string
	// Probe is delivered until it is answered 2xx, before any step (nil:
	// wait for the listen port to accept connections).
	Probe *Delivery
}

// Driver is one running plugin.
type Driver interface {
	Poll(t *testing.T)
	Events() []Got
	Close()
}

// Starter starts the plugin for one case and returns once start_source is
// acknowledged.
type Starter func(t *testing.T, s Suite, c Case, env Env) Driver

// Run plays every case.
func Run(t *testing.T, s Suite, cases []Case, start Starter) {
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) { RunCase(t, s, c, start) })
	}
}

// RunCase plays one case and compares what fired with c.Want.
func RunCase(t *testing.T, s Suite, c Case, start Starter) {
	var up http.Handler = http.NotFoundHandler()
	if c.Upstream != nil {
		up = c.Upstream(t)
	}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	env := Env{Upstream: srv.URL, Listen: FreeAddr(t), Path: "/webhook"}
	d := start(t, s, c, env)
	defer d.Close()
	waitReady(t, s, env)
	for _, st := range c.Steps {
		switch {
		case st.Deliver != nil:
			deliver(t, s, env, *st.Deliver)
		case st.Poll:
			d.Poll(t)
		}
		if st.Wait > 0 {
			time.Sleep(st.Wait)
		}
	}
	got := waitFor(d, len(c.Want))
	for _, p := range Diff(c.Want, got) {
		t.Error(p)
	}
	if c.After != nil {
		c.After(t)
	}
}

func waitFor(d Driver, want int) []Got {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(d.Events()) < want {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond) // grace for an extra, unwanted event
	return d.Events()
}

// Diff reports how got differs from want: each Want consumes one matching
// Got; every Got nothing wanted is reported too. Empty means they agree.
func Diff(want []Want, got []Got) []string {
	var problems []string
	used := make([]bool, len(got))
	for _, w := range want {
		found := -1
		for i, g := range got {
			if !used[i] && matches(w, g) {
				found = i
				break
			}
		}
		if found < 0 {
			problems = append(problems, fmt.Sprintf("missing %s (trigger %q) target=%q catch_up=%v context⊇%v len=%v absent=%v\n  got: %s",
				w.Event, w.Trigger, w.Target, w.CatchUp, w.Context, w.Len, w.Absent, describe(got)))
			continue
		}
		used[found] = true
	}
	for i, g := range got {
		if !used[i] {
			problems = append(problems, fmt.Sprintf("unexpected %s (trigger %q) target=%q catch_up=%v", g.Event, g.Trigger, g.Target, g.CatchUp))
		}
	}
	return problems
}

func matches(w Want, g Got) bool {
	if w.Event != g.Event || w.Trigger != g.Trigger || w.CatchUp != g.CatchUp || (w.Target != "" && w.Target != g.Target) {
		return false
	}
	for k, v := range w.Context {
		if fmt.Sprint(g.Context[k]) != fmt.Sprint(v) {
			return false
		}
	}
	for k, n := range w.Len {
		l, ok := g.Context[k].([]any)
		if !ok || len(l) != n {
			return false
		}
	}
	for _, k := range w.Absent {
		if _, ok := g.Context[k]; ok {
			return false
		}
	}
	return true
}

func describe(got []Got) string {
	parts := make([]string, 0, len(got))
	for _, g := range got {
		parts = append(parts, fmt.Sprintf("%s(%q %s catch_up=%v)", g.Event, g.Trigger, g.Target, g.CatchUp))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// --- transport --------------------------------------------------------------

func deliver(t *testing.T, s Suite, env Env, d Delivery) {
	t.Helper()
	resp, err := post(s, env, d)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if resp/100 != 2 {
		t.Fatalf("deliver: HTTP %d", resp)
	}
}

func post(s Suite, env Env, d Delivery) (int, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://"+env.Listen+env.Path, strings.NewReader(d.Body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	if s.Sign != nil {
		for k, v := range s.Sign([]byte(d.Body)) {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func waitReady(t *testing.T, s Suite, env Env) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s.Probe != nil {
			if code, err := post(s, env, *s.Probe); err == nil && code/100 == 2 {
				return
			}
		} else if c, err := net.DialTimeout("tcp", env.Listen, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the plugin's listener on %s never came up", env.Listen)
}

// FreeAddr is a free loopback address.
func FreeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// WireTriggers is a case's triggers as start_source carries them, and the
// id → name map events are routed back by.
func WireTriggers(t *testing.T, instance string, ts []Trigger) ([]plugin.SourceTrigger, map[string]string) {
	t.Helper()
	out := make([]plugin.SourceTrigger, 0, len(ts))
	names := map[string]string{}
	for i, tr := range ts {
		id := fmt.Sprintf("%d:%s.%s", i, instance, tr.On)
		st := plugin.SourceTrigger{ID: id, Name: tr.Name, Event: tr.On, Options: tr.Options}
		if tr.Filter != nil {
			f, err := sourcekit.ParseFilter(tr.Filter)
			if err != nil {
				t.Fatalf("trigger %s filter: %v", tr.Name, err)
			}
			b, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			st.Filter = b
		}
		out = append(out, st)
		names[id] = tr.Name
	}
	return out, names
}

// --- drivers ----------------------------------------------------------------

// BinaryStarter drives a plugin BINARY over stdin/stdout, as a minimal host
// does: describe (declarations checked), start_source with the case's
// connection and triggers, collect plugin.event, poll on request.
func BinaryStarter(bin string) Starter {
	return func(t *testing.T, s Suite, c Case, env Env) Driver {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, bin)
		cmd.Stderr = os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		w := newWire(t, s, stdin, stdout, func() { cancel(); _ = cmd.Wait() })
		w.start(t, c, env)
		return w
	}
}

// HandlerStarter drives a Handler in process, over a pipe, through the same
// wire protocol as BinaryStarter.
func HandlerStarter(h plugin.Handler) Starter {
	return func(t *testing.T, s Suite, c Case, env Env) Driver {
		t.Helper()
		toPlugin, hostW := io.Pipe()
		hostR, fromPlugin := io.Pipe()
		done := make(chan struct{})
		go func() { _ = plugin.ServeConn(toPlugin, fromPlugin, h); close(done) }()
		w := newWire(t, s, hostW, hostR, func() {
			_ = hostW.Close()
			<-done
		})
		w.start(t, c, env)
		return w
	}
}

type wire struct {
	s     Suite
	mu    sync.Mutex
	in    io.Writer
	next  int
	resp  map[string]chan json.RawMessage
	errs  map[string]string
	got   []Got
	names map[string]string
	stop  func()
}

func newWire(t *testing.T, s Suite, in io.Writer, out io.Reader, stop func()) *wire {
	if s.Instance == "" {
		s.Instance = "src"
	}
	w := &wire{s: s, in: in, resp: map[string]chan json.RawMessage{}, errs: map[string]string{}, stop: stop}
	go w.read(out)
	return w
}

func (w *wire) start(t *testing.T, c Case, env Env) {
	t.Helper()
	var raw json.RawMessage
	host := plugin.DescribeRequest{Host: &plugin.HostInfo{Version: "plugintest", Semantics: plugin.KnownSemantics()}}
	if err := w.call(plugin.MethodDescribe, host, &raw); err != nil {
		t.Fatalf("describe: %v", err)
	}
	var d plugin.Decl
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("describe: %v", err)
	}
	if p := append(plugin.CheckSemantics(raw), plugin.ValidateSemantics(d)...); len(p) > 0 {
		t.Fatalf("declarations refused: %v", p)
	}
	trigs, names := WireTriggers(t, w.s.Instance, c.Triggers)
	w.names = names
	var conn map[string]any
	if c.Connection != nil {
		conn = c.Connection(env)
	}
	req := plugin.StartSourceRequest{Instance: w.s.Instance, Config: conn, Triggers: trigs}
	if err := w.call(plugin.MethodStartSource, req, nil); err != nil {
		t.Fatalf("start_source: %v", err)
	}
}

func (w *wire) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
			Result json.RawMessage  `json:"result"`
			Error  *plugin.Error    `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == plugin.MethodEvent:
			var ev plugin.SourceEvent
			if json.Unmarshal(m.Params, &ev) == nil {
				w.record(ev)
			}
		case m.Method != "" && m.ID != nil:
			// A host callback (host.state): this minimal host keeps none.
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": plugin.HostStateResult{OK: true}})
			w.mu.Lock()
			_, _ = w.in.Write(append(b, '\n'))
			w.mu.Unlock()
		case m.ID != nil:
			id := strings.Trim(string(*m.ID), `"`)
			w.mu.Lock()
			ch := w.resp[id]
			if m.Error != nil {
				w.errs[id] = m.Error.Message
			}
			w.mu.Unlock()
			if ch != nil {
				ch <- m.Result
			}
		}
	}
}

func (w *wire) record(ev plugin.SourceEvent) {
	key := ev.Target.Key
	if key == "" && ev.Target.Repo != "" {
		key = ev.Target.Repo
		if ev.Target.Number != 0 {
			key += "#" + strconv.Itoa(ev.Target.Number)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, Got{Event: ev.Event, Trigger: w.names[ev.Trigger], Target: key, CatchUp: ev.CatchUp,
		Assigned: ev.Target.Assigned || ev.TargetTrusted, Context: ev.Context})
}

func (w *wire) call(method string, params, result any) error {
	w.mu.Lock()
	w.next++
	id := strconv.Itoa(w.next)
	ch := make(chan json.RawMessage, 1)
	w.resp[id] = ch
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": w.next, "method": method, "params": params})
	_, err := w.in.Write(append(b, '\n'))
	w.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case res := <-ch:
		w.mu.Lock()
		e := w.errs[id]
		w.mu.Unlock()
		if e != "" {
			return fmt.Errorf("%s: %s", method, e)
		}
		if result != nil && len(res) > 0 {
			return json.Unmarshal(res, result)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("%s: no response", method)
	}
}

func (w *wire) Poll(t *testing.T) {
	t.Helper()
	var res plugin.PollResult
	if err := w.call(plugin.MethodPoll, plugin.PollRequest{Instance: w.s.Instance, Mode: plugin.PollNow}, &res); err != nil {
		t.Fatalf("poll: %v", err)
	}
	for _, ev := range res.Events {
		w.record(ev)
	}
}

func (w *wire) Events() []Got {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Got(nil), w.got...)
}

func (w *wire) Close() { w.stop() }
