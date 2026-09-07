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
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
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

// defaultMaxInflight caps concurrent synchronous waits and in-flight callbacks
// (#36 §13 review, item 5). Each holds a goroutine for up to maxWaitTimeout, so
// an unbounded number is a trivial resource-exhaustion vector. Overridable per
// deployment; the default is a safe ceiling, not a tuning knob most reach.
const defaultMaxInflight = 64

// defaultMaxSkew bounds a signed request's timestamp vs. the server clock, and
// replayEntries bounds the recent-signature cache (#36 §13 review, item 4). The
// skew window is the real replay bound — a signature stops verifying once it
// passes — so the ring only needs to cover far more than a window of legitimate
// traffic, not all history.
const (
	defaultMaxSkew = 5 * time.Minute
	replayEntries  = 8192
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
	// waitSem / cbSem bound concurrent synchronous waits and in-flight callbacks
	// so neither can pin an unbounded number of goroutines (#36 §13 review 5).
	waitSem chan struct{}
	cbSem   chan struct{}
	// replay tracks recently-seen (token, signature) pairs so a signed request
	// cannot be replayed within the skew window (#36 §13 review 4).
	replay *replayGuard
	// now is the clock for timestamp-skew checks (overridable in tests).
	now func() time.Time
}

// New builds the service. Register mounts its handlers.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = func(string, ...any) {}
	}
	if d.HTTPPost == nil {
		// SSRF-guarded by default (#36 §13 review, item 1): a completion callback
		// is a daemon-side POST to a caller-supplied URL, so the default poster
		// requires https, refuses internal/rebinding target IPs, and never follows
		// redirects. Tests inject their own HTTPPost to capture without a network.
		d.HTTPPost = newCallbackPoster(d.Cfg).post
	}
	return &Service{
		d:       d,
		issued:  newIssuedSet(4096),
		waitSem: make(chan struct{}, inflightCap(d.Cfg.MaxWaitInflight)),
		cbSem:   make(chan struct{}, inflightCap(d.Cfg.MaxCallbackInflight)),
		replay:  newReplayGuard(replayEntries),
		now:     time.Now,
	}
}

// inflightCap resolves a configured concurrency limit, falling back to the safe
// default when unset (≤0).
func inflightCap(n int) int {
	if n <= 0 {
		return defaultMaxInflight
	}
	return n
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

	// Synchronous: block (bounded) for the result — but only within the inflight
	// cap. Each wait holds a server goroutine until the run finishes or the wait
	// deadline (up to 5m); past the cap we degrade to async (202 + run_id) rather
	// than pin another goroutine, and the caller polls GET /runs (#36 §13 review 5).
	if wantsWait(r) {
		select {
		case s.waitSem <- struct{}{}:
			defer func() { <-s.waitSem }()
			rec, done := s.await(r.Context(), runID, s.waitTimeout())
			code := http.StatusOK
			if !done {
				code = http.StatusAccepted // still running at the deadline — poll
			}
			writeJSON(w, code, s.result(runID, rec))
			return
		default:
			s.d.Log("callable: wait capacity reached (%d) — run %s returned async", cap(s.waitSem), runID)
			writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "accepted"})
			return
		}
	}

	// Callback: poll to completion in a background goroutine, then POST the
	// result — bounded by the same inflight cap. Past the cap the callback is not
	// scheduled (audited delivered:false) rather than spawning an unbounded
	// goroutine; the run still completes and is readable via GET /runs.
	if req.CallbackURL != "" {
		select {
		case s.cbSem <- struct{}{}:
			go func() {
				defer func() { <-s.cbSem }()
				s.deliverCallback(runID, req.CallbackURL)
			}()
		default:
			s.d.Log("callable: callback capacity reached (%d) — run %s callback not scheduled", cap(s.cbSem), runID)
			s.d.Audit(map[string]any{"event": "callable_callback", "run_id": runID, "delivered": false, "reason": "callback concurrency limit"})
		}
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
	// A run is readable ONLY by the token that invoked it (#36 §13 review, item
	// 3a — fail CLOSED). If the issuing token is unknown here — a run from before
	// this process started, or one whose entry the bounded map evicted — we
	// refuse rather than let any authenticated token read it. The old behaviour
	// failed OPEN (unknown owner ⇒ readable by anyone), which combined with
	// enumerable ids (now fixed, 3b) and weaponizable eviction (now defanged: an
	// evicted entry denies, it no longer discloses) let a caller read a victim's
	// run. A 404 (not 403) avoids disclosing that the id exists. The durable §20
	// record remains readable with local access via `conductor runs <id>`.
	owner, known := s.issued.owner(id)
	if !known {
		// Fail closed: refuse and do not disclose that the id exists.
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	if owner != tok.Name {
		// Known, but owned by another token — the documented per-token isolation.
		writeErr(w, http.StatusForbidden, "run not readable by this token")
		return
	}
	rec, found := s.d.ReadRun(id)
	if !found {
		// Issued by this token but not yet on disk (the run just started):
		// accepted, not missing — the caller keeps polling.
		writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "status": "accepted"})
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
		// A callable HMAC caller signs timestamp + method + path + body and
		// presents the timestamp — so the signature is bound to this endpoint at
		// this moment (#36 §13 review, item 4). A missing/stale timestamp, a bad
		// signature, or a replayed (already-seen) signature all fail authentication.
		ts := r.Header.Get(t.HMAC.TimestampHeaderName())
		if ts == "" || !s.freshTimestamp(ts) {
			continue
		}
		if !inbound.VerifySignedRequest(t.HMAC.Secret, ts, r.Method, r.URL.Path, body, sig, t.HMAC.Scheme) {
			continue
		}
		if !s.replay.checkAndRecord(t.Name + "\x00" + strings.TrimSpace(sig)) {
			s.d.Log("callable: replayed signature refused for token %s (%s %s)", t.Name, r.Method, r.URL.Path)
			return nil, false
		}
		return t, true
	}
	return nil, false
}

// freshTimestamp reports whether a unix-seconds timestamp string is within the
// configured skew window of the server clock. An unparseable timestamp is not
// fresh — a signed request must carry a valid one.
func (s *Service) freshTimestamp(ts string) bool {
	secs, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil {
		return false
	}
	skew := s.d.Cfg.MaxSkew.D()
	if skew <= 0 {
		skew = defaultMaxSkew
	}
	diff := s.now().Sub(time.Unix(secs, 0))
	if diff < 0 {
		diff = -diff
	}
	return diff <= skew
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
		// Generic message to the external caller (#36 §13 review, item 6): a raw
		// connector/step error can carry local paths/hostnames — non-secret, but
		// internal. The full detail stays in the §20 record + audit/log, read via
		// `conductor runs <id>`. `failed_step` is a step id (operator-defined,
		// safe) so the caller still learns *where* it failed.
		res["error"] = "workflow failed"
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

// lowerBase32 encodes the random id suffix: the standard base32 alphabet,
// lowercased and unpadded, so an id is filesystem-safe (used as a §20 history
// filename) and URL-safe (used as a /runs/<id> path segment).
var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewRunID mints a unique, filesystem-safe, time-ordered, UNGUESSABLE history id
// — exported for callers that dispatch through a different transport (the MCP
// face over the control socket) but need the same id shape to read the run back.
func NewRunID() string { return newRunID() }

// newRunID mints a unique, filesystem-safe, time-ordered, unguessable history
// id (#36 §13 review, item 3b). A run id doubles as the read capability for
// GET /runs/<id>, so it must not be enumerable: the leading time component
// keeps §20 records loosely ordered and collision-free across a nanosecond,
// while the trailing 128 bits of crypto/rand entropy make an id impossible to
// guess or walk. The old "r"+base36(nanos)+"x"+base36(seq) shape leaked the
// clock and a monotonic counter — trivially enumerable.
func newRunID() string {
	// The '-' separator is absent from both the base36 time part and the base32
	// suffix alphabet, so the id splits cleanly and stays filesystem/URL-safe.
	prefix := "r" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-"
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and near-impossible; fall back to a
		// still-unique (if not unguessable) id rather than panic mid-invoke.
		runIDSeq.Lock()
		runIDSeq.n++
		seq := runIDSeq.n
		runIDSeq.Unlock()
		return prefix + strconv.FormatInt(seq, 36)
	}
	return prefix + lowerBase32.EncodeToString(b[:])
}

// runIDSeq is the fallback disambiguator if crypto/rand ever fails.
var runIDSeq struct {
	sync.Mutex
	n int64
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
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

// replayGuard is a bounded set of recently-seen signature keys, so a signed
// request cannot be replayed within the skew window (#36 §13 review, item 4).
// It evicts oldest-first: the timestamp-skew window is the real replay bound, so
// the ring only needs to hold far more than a window of legitimate signatures.
type replayGuard struct {
	mu   sync.Mutex
	max  int
	seen map[string]struct{}
	ring []string
}

func newReplayGuard(max int) *replayGuard {
	if max <= 0 {
		max = replayEntries
	}
	return &replayGuard{max: max, seen: map[string]struct{}{}}
}

// checkAndRecord returns true if key was UNSEEN (recording it), false if it is a
// replay of a key still in the ring.
func (g *replayGuard) checkAndRecord(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[key]; ok {
		return false
	}
	g.seen[key] = struct{}{}
	g.ring = append(g.ring, key)
	if len(g.ring) > g.max {
		old := g.ring[0]
		g.ring = g.ring[1:]
		delete(g.seen, old)
	}
	return true
}
