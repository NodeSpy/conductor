// Package callable is conductor's authenticated inbound invoke surface (#36
// §13): the HTTP face of the `manual` source / `conductor run` machinery. An
// external orchestrator (n8n, cron, a queue, plain curl, or — via the MCP face
// — any MCP client) fires a named callable workflow, passes inputs, and gets a
// structured result back. It is NOT a new engine: every invoke flows through
// the same emit → policy → quiet-hours → budget → audit path as any trigger.
//
// It is a control surface that dispatches agents, so it is gated like one:
//   - authenticated — a bearer token or an HMAC body signature per caller;
//   - deny-by-default scope — a token invokes only the workflows it is granted;
//   - explicit opt-in — a trigger is reachable only with `callable: true`.
//
// Both the trigger opt-in and the token scope must pass; neither alone is
// enough. Every invoke is audited with the caller identity and the run id.
package callable

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/inbound"
	"github.com/NodeSpy/conductor/internal/store"
)

// maxBody bounds an invoke request body — inputs are template data, not
// payloads. Generous but finite so a caller can't stream us out of memory.
const maxBody = 1 << 20 // 1 MiB

// defaultWaitTimeout / maxWaitTimeout bound a synchronous ?wait=true call.
const (
	defaultWaitTimeout = 30 * time.Second
	maxWaitTimeout     = 5 * time.Minute
)

// Deps are the daemon-provided seams the service drives. Keeping them as
// closures keeps this package free of the engine/cmd wiring and trivially
// testable with fakes.
type Deps struct {
	Cfg config.CallableConfig
	// Callable reports whether name is an addressable `callable: true` manual
	// trigger (the trigger-side opt-in). Independent of token scope.
	Callable func(name string) bool
	// Invoke fires the named manual trigger with the given inputs, pinning the
	// run's §20 history id to histID so the caller can read it back by run_id.
	// It returns an error only for an unknown/unfireable name.
	Invoke func(ctx context.Context, name string, input map[string]any, histID string) error
	// ReadRun reads a §20 history record by id (display read — already scrubbed
	// of tracked secret values at write time). ok=false when not yet on disk.
	ReadRun func(id string) (store.RunHistory, bool)
	// Audit records an invoke (caller identity + run id + workflow).
	Audit func(map[string]any)
	// HTTPPost delivers a completion callback (nil → the default client). Split
	// out so tests can capture callbacks without a real network.
	HTTPPost func(ctx context.Context, url string, body []byte) error
	Log      func(string, ...any)
}

// Service serves the invoke endpoints on the shared inbound listener.
type Service struct {
	d      Deps
	issued *issuedSet // run_id → issuing token name (in-process, bounded)
}

// New builds the service. Register mounts its handlers.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = func(string, ...any) {}
	}
	if d.HTTPPost == nil {
		d.HTTPPost = defaultPost
	}
	return &Service{d: d, issued: newIssuedSet(4096)}
}

// Register mounts POST /invoke/<name> and GET /runs/<id> on the callable
// listen address. Both are prefix routes (the tail is dynamic).
func (s *Service) Register(ctx context.Context) {
	addr := s.d.Cfg.Listen
	inbound.RegisterPrefix(ctx, addr, "/invoke/", http.HandlerFunc(s.handleInvoke), s.d.Log)
	inbound.RegisterPrefix(ctx, addr, "/runs/", http.HandlerFunc(s.handleRun), s.d.Log)
	s.d.Log("callable: invoke surface on %s (POST /invoke/<name>, GET /runs/<id>)", addr)
}

// invokeRequest is the POST /invoke/<name> body.
type invokeRequest struct {
	Input       map[string]any `json:"input"`
	CallbackURL string         `json:"callback_url"`
}

func (s *Service) handleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST /invoke/<name>")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	tok, ok := s.authenticate(r, body)
	if !ok {
		// A uniform 401 for a bad/absent credential — never reveal whether a
		// token exists or which scheme it uses.
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/invoke/")
	if name == "" || strings.Contains(name, "/") {
		writeErr(w, http.StatusNotFound, "invoke path is /invoke/<name>")
		return
	}
	// Both gates: the trigger must have opted in AND this token must be scoped
	// for it. A uniform 403 for either failure — an out-of-scope caller learns
	// nothing about which workflows exist.
	if !s.d.Callable(name) || !tok.Allows(name) {
		s.d.Log("callable: %s denied invoke of %q (not callable or out of scope)", tok.Name, name)
		writeErr(w, http.StatusForbidden, "workflow is not callable by this token")
		return
	}

	var req invokeRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	input := req.Input
	if input == nil {
		input = map[string]any{}
	}

	runID := newRunID()
	s.issued.add(runID, tok.Name)
	s.d.Audit(map[string]any{
		"event": "callable_invoke", "caller": tok.Name, "workflow": name, "run_id": runID,
	})

	// Fire through the manual machinery (policy / quiet-hours / budget / audit
	// all apply — the entry point changes, the containment doesn't).
	if err := s.d.Invoke(r.Context(), name, input, runID); err != nil {
		s.d.Log("callable: invoke %q by %s failed: %v", name, tok.Name, err)
		writeErr(w, http.StatusInternalServerError, "invoke failed")
		return
	}
	s.d.Log("callable: %s invoked %q → run %s", tok.Name, name, runID)

	// Synchronous: block (bounded) for the result.
	if wantsWait(r) {
		rec, done := s.await(r.Context(), runID, s.waitTimeout())
		code := http.StatusOK
		if !done {
			code = http.StatusAccepted // still running at the deadline — poll
		}
		writeJSON(w, code, s.result(runID, rec))
		return
	}

	// Callback: poll to completion in the background, then POST the result.
	if req.CallbackURL != "" {
		go s.deliverCallback(runID, req.CallbackURL)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "accepted"})
}

func (s *Service) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET /runs/<id>")
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	tok, ok := s.authenticate(r, body)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/runs/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "run path is /runs/<id>")
		return
	}
	// A run is readable by the token that invoked it. When the issuing token is
	// unknown here (a run from before this process started — the map is
	// in-memory), any authenticated token may read it; all tokens are
	// first-class operators of this instance and the record is already readable
	// via the CLI to anyone with local access.
	if owner, known := s.issued.owner(id); known && owner != tok.Name {
		writeErr(w, http.StatusForbidden, "run not readable by this token")
		return
	}
	rec, found := s.d.ReadRun(id)
	if !found {
		if _, issued := s.issued.owner(id); issued {
			// Issued but not yet on disk (the run just started): accepted, not
			// missing — the caller keeps polling.
			writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "status": "accepted"})
			return
		}
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	writeJSON(w, http.StatusOK, s.result(id, rec))
}

// authenticate resolves the presenting credential to a configured token. A
// bearer that matches nothing does NOT fall through to HMAC. Empty secrets are
// impossible (validation rejects them); the guards here are belt-and-braces.
func (s *Service) authenticate(r *http.Request, body []byte) (*config.CallableToken, bool) {
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		presented := strings.TrimPrefix(ah, "Bearer ")
		for i := range s.d.Cfg.Tokens {
			t := &s.d.Cfg.Tokens[i]
			if t.Bearer != "" && subtle.ConstantTimeCompare([]byte(t.Bearer), []byte(presented)) == 1 {
				return t, true
			}
		}
		return nil, false
	}
	for i := range s.d.Cfg.Tokens {
		t := &s.d.Cfg.Tokens[i]
		if t.HMAC == nil || t.HMAC.Secret == "" {
			continue
		}
		sig := r.Header.Get(t.HMAC.Header)
		if sig == "" {
			continue
		}
		if inbound.VerifyHMAC(t.HMAC.Secret, body, sig, t.HMAC.Scheme) {
			return t, true
		}
	}
	return nil, false
}

// await polls the run's record until it reaches a terminal status or the
// deadline. done=false means still running at the deadline.
func (s *Service) await(ctx context.Context, id string, timeout time.Duration) (store.RunHistory, bool) {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		if rec, ok := s.d.ReadRun(id); ok && terminal(rec.Status) {
			return rec, true
		}
		if time.Now().After(deadline) {
			rec, _ := s.d.ReadRun(id)
			return rec, false
		}
		select {
		case <-ctx.Done():
			rec, _ := s.d.ReadRun(id)
			return rec, false
		case <-tick.C:
		}
	}
}

// deliverCallback polls the run to completion (bounded generously) and POSTs the
// structured result to url. Best-effort: a failed callback is logged + audited,
// never retried in a tight loop.
func (s *Service) deliverCallback(id, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), maxWaitTimeout)
	defer cancel()
	rec, done := s.await(ctx, id, maxWaitTimeout)
	if !done {
		s.d.Log("callable: callback for run %s: still running at %s — not delivered", id, maxWaitTimeout)
		s.d.Audit(map[string]any{"event": "callable_callback", "run_id": id, "delivered": false, "reason": "timeout"})
		return
	}
	body, _ := json.Marshal(s.result(id, rec))
	if err := s.d.HTTPPost(ctx, url, body); err != nil {
		s.d.Log("callable: callback POST for run %s failed: %v", id, err)
		s.d.Audit(map[string]any{"event": "callable_callback", "run_id": id, "delivered": false, "reason": err.Error()})
		return
	}
	s.d.Audit(map[string]any{"event": "callable_callback", "run_id": id, "delivered": true, "status": rec.Status})
}

// result builds the structured invoke result from a run record.
func (s *Service) result(id string, rec store.RunHistory) map[string]any {
	return Result(id, rec)
}

// Result builds the structured invoke result from a §20 run record: per-step
// outputs keyed by step id (typed data, not a log scrape), plus status/error.
// Exported so the HTTP and MCP faces return byte-identical bodies.
func Result(id string, rec store.RunHistory) map[string]any {
	status := rec.Status
	if status == "" {
		status = "accepted" // issued, record not yet written
	}
	outputs := map[string]any{}
	for _, st := range rec.Steps {
		if len(st.Outputs) > 0 {
			outputs[st.ID] = st.Outputs
		}
	}
	res := map[string]any{"run_id": id, "status": status, "outputs": outputs}
	if rec.Error != "" {
		res["error"] = rec.Error
	}
	if rec.FailedStep != "" {
		res["failed_step"] = rec.FailedStep
	}
	return res
}

// Terminal reports whether a run status is final (not still running). Exported
// for the MCP face's completion poll.
func Terminal(status string) bool { return terminal(status) }

func (s *Service) waitTimeout() time.Duration {
	d := s.d.Cfg.WaitTimeout.D()
	if d <= 0 {
		d = defaultWaitTimeout
	}
	if d > maxWaitTimeout {
		d = maxWaitTimeout
	}
	return d
}

// terminal reports whether a run status is final (not still running).
func terminal(status string) bool { return status != "" && status != "running" }

func wantsWait(r *http.Request) bool {
	v := r.URL.Query().Get("wait")
	return v == "1" || strings.EqualFold(v, "true")
}

// runIDSeq disambiguates two invokes that land in the same nanosecond.
var runIDSeq struct {
	sync.Mutex
	n int64
}

// NewRunID mints a unique, filesystem-safe, time-ordered history id — exported
// for callers that dispatch through a different transport (the MCP face over
// the control socket) but need the same id shape to read the run back.
func NewRunID() string { return newRunID() }

// newRunID mints a unique, filesystem-safe, time-ordered history id. Same shape
// family as the engine's own ("r" + base36), with a per-process counter so
// concurrent invokes never collide.
func newRunID() string {
	runIDSeq.Lock()
	runIDSeq.n++
	seq := runIDSeq.n
	runIDSeq.Unlock()
	return "r" + strconv.FormatInt(time.Now().UnixNano(), 36) + "x" + strconv.FormatInt(seq, 36)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func defaultPost(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned %d", resp.StatusCode)
	}
	return nil
}

// issuedSet is a bounded run_id → issuing-token-name map, so the service can
// distinguish "issued, not yet on disk" from "never issued" and enforce that a
// run is read back by the token that invoked it — without unbounded growth.
type issuedSet struct {
	mu     sync.Mutex
	max    int
	owners map[string]string
	ring   []string
}

func newIssuedSet(max int) *issuedSet {
	if max <= 0 {
		max = 4096
	}
	return &issuedSet{max: max, owners: map[string]string{}}
}

func (s *issuedSet) add(id, owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.owners[id]; !ok {
		s.ring = append(s.ring, id)
		if len(s.ring) > s.max {
			old := s.ring[0]
			s.ring = s.ring[1:]
			delete(s.owners, old)
		}
	}
	s.owners[id] = owner
}

func (s *issuedSet) owner(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.owners[id]
	return o, ok
}
