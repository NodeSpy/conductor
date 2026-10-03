package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

func cfgOneSource(name string, src map[string]any, listen string) map[string]any {
	return map[string]any{"listen": listen, "sources": map[string]any{name: src}}
}

// --- DescribeInstance: target.assigned is knowable from config alone ------

func TestDescribeInstanceStaticRepoIsAssigned(t *testing.T) {
	w := New()
	d, err := w.DescribeInstance(context.Background(), "wh", cfgOneSource("cw", map[string]any{
		"path": "/hooks/cw", "repo": "acme/infra",
	}, ":0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 || d.Events[0].Name != "cw" {
		t.Fatalf("events = %+v", d.Events)
	}
	sem := d.Events[0].Semantics
	if sem == nil || sem.Target == nil {
		t.Fatal("no target semantics declared")
	}
	var assigned bool
	if err := json.Unmarshal(sem.Target.Assigned, &assigned); err != nil || !assigned {
		t.Fatalf("static repo: assigned = %s, want true", sem.Target.Assigned)
	}
}

func TestDescribeInstanceTemplatedRepoIsNotAssigned(t *testing.T) {
	w := New()
	d, err := w.DescribeInstance(context.Background(), "wh", cfgOneSource("cw", map[string]any{
		"path": "/hooks/cw", "repo": "{{.body.repo}}",
	}, ":0"))
	if err != nil {
		t.Fatal(err)
	}
	sem := d.Events[0].Semantics
	var assigned bool
	if err := json.Unmarshal(sem.Target.Assigned, &assigned); err != nil || assigned {
		t.Fatalf("templated repo: assigned = %s, want false", sem.Target.Assigned)
	}
}

func TestDescribeInstanceSyntheticIsAssigned(t *testing.T) {
	w := New()
	d, err := w.DescribeInstance(context.Background(), "wh", cfgOneSource("cw", map[string]any{
		"path": "/hooks/cw",
	}, ":0"))
	if err != nil {
		t.Fatal(err)
	}
	sem := d.Events[0].Semantics
	var assigned bool
	if err := json.Unmarshal(sem.Target.Assigned, &assigned); err != nil || !assigned {
		t.Fatalf("synthetic (no repo): assigned = %s, want true (the operator's own name, not sender-chosen)", sem.Target.Assigned)
	}
}

func TestDescribeInstanceMultipleSources(t *testing.T) {
	w := New()
	d, err := w.DescribeInstance(context.Background(), "wh", map[string]any{
		"listen": ":0",
		"sources": map[string]any{
			"a": map[string]any{"path": "/a", "repo": "acme/a"},
			"b": map[string]any{"path": "/b", "repo": "{{.body.repo}}"},
			"c": map[string]any{"path": "/c"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 3 {
		t.Fatalf("events = %+v, want 3", d.Events)
	}
	want := map[string]bool{"a": true, "b": false, "c": true}
	for _, ev := range d.Events {
		var assigned bool
		_ = json.Unmarshal(ev.Semantics.Target.Assigned, &assigned)
		if assigned != want[ev.Name] {
			t.Errorf("source %q: assigned = %v, want %v", ev.Name, assigned, want[ev.Name])
		}
	}
}

// --- Validate --------------------------------------------------------------

func TestValidateRejectionTable(t *testing.T) {
	w := New()
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr string
	}{
		{"no transport", map[string]any{"sources": map[string]any{"s": map[string]any{}}}, "set `listen` and/or `smee_url`"},
		{"no sources", map[string]any{"listen": ":0"}, "no sources"},
		{"path required with listener", map[string]any{"listen": ":0", "sources": map[string]any{"s": map[string]any{}}}, "required with a listener"},
		{"match required multi-smee", map[string]any{"smee_url": "https://smee.io/x", "sources": map[string]any{
			"a": map[string]any{}, "b": map[string]any{},
		}}, "required to route a smee channel"},
		{"bad template", map[string]any{"listen": ":0", "sources": map[string]any{
			"s": map[string]any{"path": "/x", "title": "{{.broken"},
		}}, "bad template"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := w.Validate(context.Background(), plugin.ValidateRequest{Instance: "wh", Config: c.cfg})
			if err != nil {
				t.Fatalf("Validate returned an error (want problems): %v", err)
			}
			if len(res.Problems) == 0 {
				t.Fatalf("want a problem mentioning %q, got none", c.wantErr)
			}
			found := false
			for _, p := range res.Problems {
				if strings.Contains(p.Message, c.wantErr) {
					found = true
				}
			}
			if !found {
				t.Fatalf("problems = %+v, want one mentioning %q", res.Problems, c.wantErr)
			}
		})
	}

	// Valid.
	ok := map[string]any{"listen": ":0", "sources": map[string]any{"a": map[string]any{"path": "/a"}}}
	res, err := w.Validate(context.Background(), plugin.ValidateRequest{Instance: "wh", Config: ok})
	if err != nil || len(res.Problems) != 0 {
		t.Fatalf("valid config rejected: %v %+v", err, res.Problems)
	}
}

func TestValidateUnknownTriggerSource(t *testing.T) {
	w := New()
	cfg := map[string]any{"listen": ":0", "sources": map[string]any{"a": map[string]any{"path": "/a"}}}
	res, err := w.Validate(context.Background(), plugin.ValidateRequest{
		Instance: "wh", Config: cfg,
		Triggers: []plugin.SourceTrigger{{ID: "0:wh.ghost", Event: "ghost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Message, `unknown webhook source "ghost"`) {
		t.Fatalf("problems = %+v", res.Problems)
	}
}

// --- StartSource: direct listener ------------------------------------------

func collectEvents() (func(any) error, chan plugin.SourceEvent) {
	ch := make(chan plugin.SourceEvent, 8)
	return func(payload any) error {
		ev, ok := payload.(plugin.SourceEvent)
		if !ok {
			b, _ := json.Marshal(payload)
			_ = json.Unmarshal(b, &ev)
		}
		ch <- ev
		return nil
	}, ch
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestStartSourceDeliverMapsBodyToEvent(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()

	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen": addr,
				"sources": map[string]any{
					"cloudwatch": map[string]any{
						"path": "/hooks/cw", "repo": "AcmeCorp/infra",
						"title": "{{.body.detail.alarmName}} -> {{.body.detail.state}}",
						"dedup": "{{.body.detail.alarmName}}-{{.body.time}}",
					},
				},
			},
		}, emit)
	}()
	waitListening(t, addr)

	body := `{"detail":{"alarmName":"cpu-high","state":"ALARM"},"time":"t1"}`
	resp := postTo(t, "http://"+addr+"/hooks/cw", body, nil)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}

	select {
	case ev := <-got:
		if ev.Event != "cloudwatch" || ev.Title != "cpu-high -> ALARM" {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Dedup != "cpu-high-t1" {
			t.Fatalf("dedup = %q", ev.Dedup)
		}
		if ev.Target.Repo != "AcmeCorp/infra" || ev.Target.Owner != "AcmeCorp" {
			t.Fatalf("target = %+v", ev.Target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event emitted")
	}
}

func TestStartSourceSyntheticRepoHasNoCheckout(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen":  addr,
				"sources": map[string]any{"statuspage": map[string]any{"path": "/hooks/sp"}},
			},
		}, emit)
	}()
	waitListening(t, addr)
	postTo(t, "http://"+addr+"/hooks/sp", `{"x":1}`, nil)
	select {
	case ev := <-got:
		if !strings.HasPrefix(ev.Target.Repo, "webhook:") {
			t.Fatalf("expected a synthetic repo, got %q", ev.Target.Repo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event emitted")
	}
}

func TestStartSourceMatchPredicate(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen": addr,
				"sources": map[string]any{
					"only-alarms": map[string]any{"path": "/h", "match": `{{if eq .body.type "alarm"}}true{{end}}`},
				},
			},
		}, emit)
	}()
	waitListening(t, addr)
	postTo(t, "http://"+addr+"/h", `{"type":"info"}`, nil)
	select {
	case ev := <-got:
		t.Fatalf("non-matching delivery should be dropped, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
	postTo(t, "http://"+addr+"/h", `{"type":"alarm"}`, nil)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("matching delivery should fire")
	}
}

func TestStartSourceDedupsRedelivery(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen":  addr,
				"sources": map[string]any{"x": map[string]any{"path": "/h", "dedup": "{{.body.id}}"}},
			},
		}, emit)
	}()
	waitListening(t, addr)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-1"}`, nil)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-1"}`, nil)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery should emit")
	}
	select {
	case ev := <-got:
		t.Fatalf("identical delivery should not re-emit, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// A source with NO `dedup:` template must not drop every delivery after the
// first: before the fix, an empty rendered dedup template made the dedup key
// a per-source constant (name+"\x00"), so the second and every later,
// DIFFERENT delivery was treated as a "duplicate" of the first and silently
// dropped. Two distinct bodies must both fire.
func TestStartSourceNoDedupTemplateBothDeliveriesFire(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen":  addr,
				"sources": map[string]any{"x": map[string]any{"path": "/h"}}, // no dedup:
			},
		}, emit)
	}()
	waitListening(t, addr)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-1"}`, nil)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-2"}`, nil)

	var titles []string
	for i := 0; i < 2; i++ {
		select {
		case ev := <-got:
			titles = append(titles, fmt.Sprint(ev.Context["body"]))
		case <-time.After(5 * time.Second):
			t.Fatalf("delivery %d of 2 never fired (got %d so far)", i+1, len(titles))
		}
	}
	select {
	case ev := <-got:
		t.Fatalf("exactly 2 deliveries were sent, got an unexpected 3rd: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// A source with no `dedup:` template still dedupes an exact retry of the
// SAME delivery (identical body): the fallback key is a content hash, so a
// byte-identical redelivery collapses to the same key as before.
func TestStartSourceNoDedupTemplateStillDedupesIdenticalRetry(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen":  addr,
				"sources": map[string]any{"x": map[string]any{"path": "/h"}},
			},
		}, emit)
	}()
	waitListening(t, addr)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-1"}`, nil)
	postTo(t, "http://"+addr+"/h", `{"id":"evt-1"}`, nil) // byte-identical retry

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery should emit")
	}
	select {
	case ev := <-got:
		t.Fatalf("an identical retry with no dedup: configured should still dedupe via the body hash, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStartSourceVerifiesSignature(t *testing.T) {
	w := New()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit, got := collectEvents()
	secret := "shh"
	go func() {
		_ = w.StartSource(ctx, plugin.StartSourceRequest{
			Instance: "wh",
			Config: map[string]any{
				"listen": addr,
				"sources": map[string]any{
					"signed": map[string]any{"path": "/h", "sign": map[string]any{"header": "X-Sig", "secret": secret}},
				},
			},
		}, emit)
	}()
	waitListening(t, addr)

	body := `{"ok":true}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))

	code := postTo(t, "http://"+addr+"/h", body, map[string]string{"X-Sig": sig})
	if code != http.StatusAccepted {
		t.Fatalf("valid signature: status = %d", code)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("valid signature should emit")
	}

	code = postTo(t, "http://"+addr+"/h", body, map[string]string{"X-Sig": "deadbeef"})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad signature: status = %d", code)
	}
	select {
	case ev := <-got:
		t.Fatalf("bad signature must not emit, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}

func postTo(t *testing.T, url, body string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// --- the `post` verb: SSRF guard -------------------------------------------

func resolveTo(ip string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
}

func TestPostEgressGuardBlocksPrivateAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "10.1.2.3"} {
		w := &Webhook{resolve: resolveTo(ip)}
		_, err := w.Invoke(plugin.InvokeRequest{
			Verb:    "post",
			Options: map[string]any{"url": "https://evil.example.com/x", "json": map[string]any{"a": 1}},
		})
		if err == nil {
			t.Fatalf("post to %s: expected refusal", ip)
		}
	}
}

func TestPostEgressGuardAllowPrivate(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hit = true
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	w := &Webhook{resolve: resolveTo("127.0.0.1")}
	res, err := w.Invoke(plugin.InvokeRequest{
		Verb:       "post",
		Options:    map[string]any{"url": "http://" + host + "/hook", "method": http.MethodPost, "body": "hi"},
		Connection: map[string]any{"allow_private": true},
	})
	if err != nil {
		t.Fatalf("post with allow_private: %v", err)
	}
	if !hit {
		t.Fatal("server was never reached")
	}
	if res.Outputs["status"] != http.StatusOK {
		t.Fatalf("status = %v", res.Outputs["status"])
	}
}

func TestPostEgressGuardAllowHostsByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	w := &Webhook{resolve: resolveTo("127.0.0.1")}
	res, err := w.Invoke(plugin.InvokeRequest{
		Verb:       "post",
		Options:    map[string]any{"url": fmt.Sprintf("http://internal.svc:%s/hook", port), "body": "hi"},
		Connection: map[string]any{"allow_hosts": []any{"internal.svc"}},
	})
	if err != nil {
		t.Fatalf("allow_hosts by name: %v", err)
	}
	if res.Outputs["status"] != http.StatusOK {
		t.Fatalf("status = %v", res.Outputs["status"])
	}
}

func TestInvokeUnknownVerb(t *testing.T) {
	w := New()
	_, err := w.Invoke(plugin.InvokeRequest{Verb: "nope"})
	if err == nil {
		t.Fatal("expected an error for an unknown verb")
	}
	var rpcErr *plugin.Error
	if !asError(err, &rpcErr) || rpcErr.Code != plugin.CodeMethodNotFound {
		t.Fatalf("expected CodeMethodNotFound, got %v", err)
	}
}

func asError(err error, target **plugin.Error) bool {
	if e, ok := err.(*plugin.Error); ok {
		*target = e
		return true
	}
	return false
}
