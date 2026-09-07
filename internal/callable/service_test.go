package callable

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// fakeRuns is an in-memory stand-in for the §20 history store.
type fakeRuns struct {
	mu   sync.Mutex
	recs map[string]store.RunHistory
}

func (f *fakeRuns) put(rec store.RunHistory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[rec.ID] = rec
}

func (f *fakeRuns) read(id string) (store.RunHistory, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	return rec, ok
}

// harness wires a Service over fakes and records audit + captured callbacks.
type harness struct {
	svc      *Service
	runs     *fakeRuns
	invoked  []string // history ids passed to Invoke, in order
	audits   []map[string]any
	callback chan []byte
	mu       sync.Mutex
	// completeWith, if set, is the terminal record Invoke synthesizes (async,
	// after a short delay) so wait/callback/poll paths observe completion.
	completeWith func(histID string) store.RunHistory
	delay        time.Duration
}

func newHarness(t *testing.T, cfg config.CallableConfig, callable map[string]bool) *harness {
	t.Helper()
	h := &harness{
		runs:     &fakeRuns{recs: map[string]store.RunHistory{}},
		callback: make(chan []byte, 4),
	}
	h.svc = New(Deps{
		Cfg:      cfg,
		Callable: func(name string) bool { return callable[name] },
		Invoke: func(ctx context.Context, name string, input map[string]any, histID string) error {
			h.mu.Lock()
			h.invoked = append(h.invoked, histID)
			cw := h.completeWith
			d := h.delay
			h.mu.Unlock()
			if cw != nil {
				finish := func() { h.runs.put(cw(histID)) }
				if d > 0 {
					time.AfterFunc(d, finish)
				} else {
					finish()
				}
			}
			return nil
		},
		ReadRun: h.runs.read,
		Audit: func(m map[string]any) {
			h.mu.Lock()
			h.audits = append(h.audits, m)
			h.mu.Unlock()
		},
		HTTPPost: func(ctx context.Context, url string, body []byte) error {
			h.callback <- body
			return nil
		},
		Log: func(string, ...any) {},
	})
	return h
}

func okRun(histID string) store.RunHistory {
	return store.RunHistory{
		ID:     histID,
		Status: "ok",
		Steps: []store.StepRecord{
			{ID: "compute", Status: "ok", Outputs: map[string]any{"answer": float64(42)}},
		},
	}
}

func bearerCfg() config.CallableConfig {
	return config.CallableConfig{
		Listen: ":0",
		Tokens: []config.CallableToken{
			{Name: "n8n", Bearer: "s3cret", Workflows: []string{"triage"}},
			{Name: "other", Bearer: "othertok", Workflows: []string{"triage"}},
		},
	}
}

func invokeReq(name, bearer, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/invoke/"+name, strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func TestInvokeAsyncAccepted(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	w := httptest.NewRecorder()
	h.svc.handleInvoke(w, invokeReq("triage", "s3cret", `{"input":{"pr":7}}`))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "accepted" {
		t.Fatalf("status field = %v, want accepted", resp["status"])
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" || !strings.HasPrefix(runID, "r") {
		t.Fatalf("run_id = %q, want a non-empty r-prefixed id", runID)
	}
	if len(h.invoked) != 1 || h.invoked[0] != runID {
		t.Fatalf("Invoke got histIDs %v, want [%s] — run_id must be threaded as the history id", h.invoked, runID)
	}
	// Audited with caller identity + run id (observable effect; must fail if gutted).
	var found bool
	for _, a := range h.audits {
		if a["event"] == "callable_invoke" && a["caller"] == "n8n" && a["run_id"] == runID && a["workflow"] == "triage" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no callable_invoke audit with caller=n8n run_id=%s; audits=%v", runID, h.audits)
	}
}

func TestInvokeAuthRefusal(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	cases := []struct{ name, bearer string }{
		{"no token", ""},
		{"wrong token", "nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.svc.handleInvoke(w, invokeReq("triage", c.bearer, `{}`))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
	if len(h.invoked) != 0 {
		t.Fatalf("Invoke ran %d times on refused auth, want 0", len(h.invoked))
	}
}

func TestInvokeScopeAndCallableRefusal(t *testing.T) {
	cfg := bearerCfg()
	// "deploy" exists and is callable, but the n8n token is NOT scoped for it.
	h := newHarness(t, cfg, map[string]bool{"triage": true, "deploy": true})

	// Out of token scope → 403, no dispatch.
	w := httptest.NewRecorder()
	h.svc.handleInvoke(w, invokeReq("deploy", "s3cret", `{}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope status = %d, want 403", w.Code)
	}

	// In scope by token but the trigger did NOT opt in (not callable) → 403.
	h2 := newHarness(t, cfg, map[string]bool{}) // nothing callable
	w2 := httptest.NewRecorder()
	h2.svc.handleInvoke(w2, invokeReq("triage", "s3cret", `{}`))
	if w2.Code != http.StatusForbidden {
		t.Fatalf("not-callable status = %d, want 403", w2.Code)
	}

	if len(h.invoked)+len(h2.invoked) != 0 {
		t.Fatal("a denied invoke reached the dispatch machinery")
	}
}

func TestInvokeWaitSync(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	h.completeWith = okRun // Invoke completes immediately

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/invoke/triage?wait=true", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer s3cret")
	h.svc.handleInvoke(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("wait status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok (terminal result inline)", resp["status"])
	}
	outputs, _ := resp["outputs"].(map[string]any)
	step, _ := outputs["compute"].(map[string]any)
	if step["answer"] != float64(42) {
		t.Fatalf("outputs = %v, want structured per-step outputs (answer=42)", outputs)
	}
}

func TestInvokeWaitTimesOutTo202(t *testing.T) {
	cfg := bearerCfg()
	cfg.WaitTimeout = config.Duration(200 * time.Millisecond)
	h := newHarness(t, cfg, map[string]bool{"triage": true})
	// No completion — the run never reaches terminal within the wait window.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/invoke/triage?wait=1", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer s3cret")
	h.svc.handleInvoke(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("timed-out wait status = %d, want 202 (poll with run_id)", w.Code)
	}
}

func TestInvokeCallback(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	h.completeWith = okRun
	h.delay = 30 * time.Millisecond

	w := httptest.NewRecorder()
	h.svc.handleInvoke(w, invokeReq("triage", "s3cret", `{"callback_url":"http://cb.example/hook"}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("callback invoke status = %d, want 202", w.Code)
	}

	select {
	case body := <-h.callback:
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatal(err)
		}
		if resp["status"] != "ok" {
			t.Fatalf("callback body status = %v, want ok", resp["status"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback was never delivered")
	}
}

func TestGetRunPollAndIsolation(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})

	// Invoke to mint + own a run_id under the n8n token.
	w := httptest.NewRecorder()
	h.svc.handleInvoke(w, invokeReq("triage", "s3cret", `{}`))
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	runID := resp["run_id"].(string)

	get := func(id, bearer string) *httptest.ResponseRecorder {
		rw := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/runs/"+id, nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		h.svc.handleRun(rw, r)
		return rw
	}

	// Issued but not yet on disk → accepted, not 404.
	if rw := get(runID, "s3cret"); rw.Code != http.StatusOK {
		t.Fatalf("poll-before-write status = %d, want 200 accepted", rw.Code)
	} else {
		var g map[string]any
		json.Unmarshal(rw.Body.Bytes(), &g)
		if g["status"] != "accepted" {
			t.Fatalf("status = %v, want accepted", g["status"])
		}
	}

	// Now the run finishes; poll returns the structured terminal result.
	h.runs.put(okRun(runID))
	if rw := get(runID, "s3cret"); rw.Code != http.StatusOK {
		t.Fatalf("poll status = %d, want 200", rw.Code)
	} else {
		var g map[string]any
		json.Unmarshal(rw.Body.Bytes(), &g)
		if g["status"] != "ok" {
			t.Fatalf("status = %v, want ok", g["status"])
		}
	}

	// A different token may not read a run it did not invoke.
	if rw := get(runID, "othertok"); rw.Code != http.StatusForbidden {
		t.Fatalf("cross-token read status = %d, want 403", rw.Code)
	}

	// An unauthenticated read is refused.
	if rw := get(runID, ""); rw.Code != http.StatusUnauthorized {
		t.Fatalf("unauth read status = %d, want 401", rw.Code)
	}

	// A never-issued id (authenticated) → 404.
	if rw := get("rdoesnotexist", "s3cret"); rw.Code != http.StatusNotFound {
		t.Fatalf("unknown-run status = %d, want 404", rw.Code)
	}
}

// TestFailedRunErrorIsGeneric proves a failed run's raw internal error (which
// can carry local paths/hostnames) is NOT surfaced to the external caller —
// only a generic "workflow failed" plus the operator-named failed_step (#36
// §13 review, item 6).
func TestFailedRunErrorIsGeneric(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	h.completeWith = func(id string) store.RunHistory {
		return store.RunHistory{
			ID:         id,
			Status:     "failed",
			Error:      "dial tcp 10.1.2.3:5432: connect: connection refused (/var/secrets/db.pem)",
			FailedStep: "query",
			Steps:      []store.StepRecord{{ID: "query", Status: "failed"}},
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/invoke/triage?wait=true", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer s3cret")
	h.svc.handleInvoke(w, r)

	body := w.Body.String()
	if strings.Contains(body, "10.1.2.3") || strings.Contains(body, "/var/secrets") || strings.Contains(body, "connection refused") {
		t.Fatalf("raw internal error leaked to external caller: %s", body)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "failed" {
		t.Fatalf("status = %v, want failed", resp["status"])
	}
	if resp["error"] != "workflow failed" {
		t.Fatalf("error = %v, want generic \"workflow failed\"", resp["error"])
	}
	if resp["failed_step"] != "query" {
		t.Fatalf("failed_step = %v, want query (operator-named, safe to expose)", resp["failed_step"])
	}
}

// TestGetRunFailsClosedOnUnknownOwner proves a run whose issuing token this
// process does not know — a record on disk but never issued here (the
// post-restart or ring-evicted case) — is REFUSED, not readable by any
// authenticated token. The old code failed OPEN, which combined with
// enumerable ids let a caller read a victim's run. (#36 §13 review, item 3a.)
func TestGetRunFailsClosedOnUnknownOwner(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	h.runs.put(okRun("rvictim-not-issued")) // on disk, never issued by this Service

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/runs/rvictim-not-issued", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	h.svc.handleRun(rw, r)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("unknown-owner read status = %d, want 404 (fail closed); body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "answer") {
		t.Fatalf("run outputs leaked for an unknown-owner run: %s", rw.Body.String())
	}
}

// TestEvictionDeniesRatherThanDiscloses proves a bounded-map eviction (a caller
// bursting invokes to push a victim's ownership entry out of the ring) can no
// longer be weaponized into a cross-token read: once evicted the run is
// unknown-owner and fails closed, not readable by the evicting token. (#36 §13
// review, item 3c.)
func TestEvictionDeniesRatherThanDiscloses(t *testing.T) {
	h := newHarness(t, bearerCfg(), map[string]bool{"triage": true})
	h.svc.issued = newIssuedSet(4) // tiny ring so eviction is cheap to trigger

	h.svc.issued.add("rvictim", "other") // the victim token owns a run…
	h.runs.put(okRun("rvictim"))         // …which lands on disk
	for i := 0; i < 6; i++ {             // attacker bursts, evicting rvictim
		h.svc.issued.add("rburst"+strconv.Itoa(i), "n8n")
	}
	if _, known := h.svc.issued.owner("rvictim"); known {
		t.Fatal("precondition: rvictim should have been evicted by the burst")
	}

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/runs/rvictim", nil)
	r.Header.Set("Authorization", "Bearer s3cret") // n8n, the evictor
	h.svc.handleRun(rw, r)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("evicted-run read status = %d, want 404 (deny, not disclose); body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "answer") {
		t.Fatalf("victim run outputs leaked after eviction: %s", rw.Body.String())
	}
}

// TestRunIDUnguessable proves minted run ids carry crypto-random entropy — not
// a walkable monotonic counter — while staying unique and filesystem/URL-safe.
// A run id doubles as the read capability for GET /runs/<id>. (#36 §13 review,
// item 3b.)
func TestRunIDUnguessable(t *testing.T) {
	seen := map[string]bool{}
	var suffixes []string
	for i := 0; i < 2000; i++ {
		id := newRunID()
		if !strings.HasPrefix(id, "r") {
			t.Fatalf("id %q missing r prefix", id)
		}
		if strings.ContainsAny(id, "/\\.") || strings.Contains(id, "..") {
			t.Fatalf("id %q is not filesystem/URL-safe", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		dash := strings.LastIndexByte(id, '-')
		if dash < 0 {
			t.Fatalf("id %q has no random suffix", id)
		}
		suffixes = append(suffixes, id[dash+1:])
	}
	// 128 bits base32 → 26 chars: long enough to be unguessable, not a counter.
	if len(suffixes[0]) < 24 {
		t.Fatalf("random suffix %q too short (%d chars) to be unguessable", suffixes[0], len(suffixes[0]))
	}
	// A monotonic counter would make consecutive suffixes near-identical; random
	// ones differ completely.
	if suffixes[0] == suffixes[1] {
		t.Fatalf("consecutive ids share a suffix — not random")
	}
}

func TestInvokeHMACAuth(t *testing.T) {
	body := `{"input":{"x":1}}`
	mac := hmac.New(sha256.New, []byte("hsecret"))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	cfg := config.CallableConfig{
		Listen: ":0",
		Tokens: []config.CallableToken{{
			Name:      "signed",
			HMAC:      &config.CallableHMAC{Secret: "hsecret", Header: "X-Sig", Scheme: "hex"},
			Workflows: []string{"triage"},
		}},
	}
	h := newHarness(t, cfg, map[string]bool{"triage": true})

	// Valid signature → accepted.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/invoke/triage", strings.NewReader(body))
	r.Header.Set("X-Sig", sig)
	h.svc.handleInvoke(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid HMAC status = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	// Tampered body → 401.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/invoke/triage", strings.NewReader(body+" "))
	r2.Header.Set("X-Sig", sig)
	h.svc.handleInvoke(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("tampered HMAC status = %d, want 401", w2.Code)
	}
}
