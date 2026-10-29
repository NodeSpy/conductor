package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// Browsers and the wallet (design §4.5): live agent sessions with a CDP
// screencast and operator takeover, personal details (profiles), payment cards
// and purchase approvals. The read models and wallet mutations come from the
// daemon's browser control layer through the Browser dependency; no answer ever
// carries a value, only each field's source. Mutations that WIDEN (a new
// profile or card, a binding, a site, a shop, a higher limit, an unfreeze) and
// every purchase decision are gated by a passkey step-up, exactly as a config
// apply or a bot proposal is.

// Browser is the daemon's browser/wallet surface, wired by cmd/conductor. Nil
// when no browser runs on this daemon (the endpoints then report so).
type Browser interface {
	// Control dispatches one browser control command (a read, or a wallet
	// mutation) attributed to operator, and returns the JSON data payload plus
	// the "change" classification a mutation reports (widening|narrowing|
	// neutral|""). dryRun performs no change — it only classifies.
	Control(ctx context.Context, cmd, operator string, req json.RawMessage, dryRun bool) (data json.RawMessage, change string, err error)
	// Peek returns one screenshot (PNG) and the redacted URL of a live session.
	Peek(ctx context.Context, id string) (png []byte, url string, err error)
	// Screencast streams a live session's frames (each the raw CDP frame JSON)
	// to fn until ctx ends.
	Screencast(ctx context.Context, id string, fn func(frame json.RawMessage) error) error
	// Take/Release the operator takeover lease; Input forwards one event. All
	// attributed to operator.
	Take(id, operator string) error
	Release(id, operator string) error
	Input(ctx context.Context, id, operator string, ev json.RawMessage) error
	// PurchaseView returns a purchase's decision digest material, a human
	// summary (never agent text) and who may approve.
	PurchaseView(id string) (actionSHA, summaryJSON string, operators []string, ok bool)
	// DecidePurchase applies a passkey-signed approve/deny.
	DecidePurchase(ctx context.Context, id, decision, operator, reason string, ev StepupEvidence) error
}

func (s *Server) browserRoutes() {
	s.mux.HandleFunc("GET /api/v1/browser/sessions", s.handleBrowserSessions)
	s.mux.HandleFunc("GET /api/v1/browser/sessions/{id}", s.handleBrowserSession)
	s.mux.HandleFunc("GET /api/v1/browser/sessions/{id}/peek", s.handleBrowserPeek)
	s.mux.HandleFunc("GET /api/v1/browser/sessions/{id}/screencast", s.handleBrowserScreencast)
	s.mux.HandleFunc("POST /api/v1/browser/sessions/{id}/takeover", s.handleBrowserTakeover)
	s.mux.HandleFunc("POST /api/v1/browser/sessions/{id}/release", s.handleBrowserRelease)
	s.mux.HandleFunc("POST /api/v1/browser/sessions/{id}/input", s.handleBrowserInput)
	s.mux.HandleFunc("GET /api/v1/browser/wallet", s.handleBrowserWallet)
	s.mux.HandleFunc("GET /api/v1/browser/logins", s.handleBrowserLogins)
	s.mux.HandleFunc("GET /api/v1/browser/purchases", s.handleBrowserPurchases)
	s.mux.HandleFunc("GET /api/v1/browser/purchases/{id}", s.handleBrowserPurchase)
	s.mux.HandleFunc("GET /api/v1/browser/purchases/{id}/approval", s.handleBrowserPurchaseApproval)
	s.mux.HandleFunc("POST /api/v1/browser/purchases/{id}/decision", s.handleBrowserPurchaseDecision)
	s.mux.HandleFunc("POST /api/v1/browser/op", s.handleBrowserOp)
}

func (s *Server) browserReady(w http.ResponseWriter) bool {
	if s.deps.Browser == nil {
		writeError(w, http.StatusNotFound, "no_browser", "no browser runs on this daemon", "")
		return false
	}
	return true
}

// readCmd runs a read-only control command and relays its JSON data.
func (s *Server) readCmd(w http.ResponseWriter, r *http.Request, cmd string, req json.RawMessage) {
	if !s.browserReady(w) {
		return
	}
	op := operatorOf(r)
	data, _, err := s.deps.Browser.Control(r.Context(), cmd, op, req, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser_error", err.Error(), "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func operatorOf(r *http.Request) string {
	if sess, ok := auth.SessionFrom(r); ok {
		return sess.Operator
	}
	return ""
}

func (s *Server) handleBrowserSessions(w http.ResponseWriter, r *http.Request) {
	s.readCmd(w, r, "browser-sessions", nil)
}

func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(map[string]string{"session": r.PathValue("id")})
	s.readCmd(w, r, "browser-session", body)
}

// handleBrowserPeek returns a single still screenshot of a live session — the
// fallback the live view shows before (or instead of) the screencast stream.
func (s *Server) handleBrowserPeek(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	png, u, err := s.deps.Browser.Peek(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "peek_failed", err.Error(), "")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Browser-URL", u)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) handleBrowserWallet(w http.ResponseWriter, r *http.Request) {
	s.readCmd(w, r, "browser-wallet", nil)
}

func (s *Server) handleBrowserLogins(w http.ResponseWriter, r *http.Request) {
	s.readCmd(w, r, "browser-logins", nil)
}

func (s *Server) handleBrowserPurchases(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(map[string]string{"status": r.URL.Query().Get("status")})
	s.readCmd(w, r, "browser-purchases", body)
}

func (s *Server) handleBrowserPurchase(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(map[string]string{"purchase": r.PathValue("id")})
	s.readCmd(w, r, "browser-purchase", body)
}

// handleBrowserOp runs a wallet mutation. It classifies the change with a
// dry-run first; a widening one needs a passkey step-up bound to the request
// (design §4.5, §7.6). Narrowing, neutral and value-replacing changes don't.
func (s *Server) handleBrowserOp(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	var env struct {
		Cmd string          `json:"cmd"`
		Req json.RawMessage `json:"req"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Cmd == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a cmd and req are required", "")
		return
	}
	op := operatorOf(r)
	// Classify first (no change), so we only ask for a passkey when it widens.
	_, change, err := s.deps.Browser.Control(r.Context(), env.Cmd, op, env.Req, true)
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser_error", err.Error(), "")
		return
	}
	if change == "widening" && s.authEnforced() {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		digest := auth.DigestOf(raw)
		if _, err := s.auth.RequireStepup(sess, auth.PurposeBrowserWrite, digest, r.Header.Get(auth.StepupHeader)); err != nil {
			// Tell the client the digest + purpose to step up over, then retry.
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":  map[string]string{"code": "stepup_required", "message": err.Error(), "hint": "confirm it's you, then try again"},
				"stepup": map[string]string{"purpose": string(auth.PurposeBrowserWrite), "digest": digest},
			})
			return
		}
	}
	data, _, err := s.deps.Browser.Control(r.Context(), env.Cmd, op, env.Req, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser_error", err.Error(), "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// --- takeover (step-up lease) + input + screencast ---

// takeoverDigest binds the takeover step-up to this exact session.
func takeoverDigest(id string) string {
	b, _ := json.Marshal(map[string]string{"v": "1", "ns": "conductor-dashboard-takeover", "session": id})
	return auth.DigestOf(b)
}

func (s *Server) handleBrowserTakeover(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	id := r.PathValue("id")
	op := operatorOf(r)
	if s.authEnforced() {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		if _, err := s.auth.RequireStepup(sess, auth.PurposeBrowserTake, takeoverDigest(id), r.Header.Get(auth.StepupHeader)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":  map[string]string{"code": "stepup_required", "message": err.Error(), "hint": "confirm it's you to take over"},
				"stepup": map[string]string{"purpose": string(auth.PurposeBrowserTake), "digest": takeoverDigest(id)},
			})
			return
		}
	}
	if err := s.deps.Browser.Take(id, op); err != nil {
		writeError(w, http.StatusBadGateway, "takeover_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": id, "leased_by": op})
}

func (s *Server) handleBrowserRelease(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	if err := s.deps.Browser.Release(r.PathValue("id"), operatorOf(r)); err != nil {
		writeError(w, http.StatusBadGateway, "release_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": r.PathValue("id"), "released": true})
}

func (s *Server) handleBrowserInput(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if err := s.deps.Browser.Input(r.Context(), r.PathValue("id"), operatorOf(r), raw); err != nil {
		writeError(w, http.StatusBadGateway, "input_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBrowserScreencast relays a session's CDP screencast as WebSocket frames
// (design §4.5, NEW: Page.startScreencast over the --remote-debugging-pipe
// client, relayed to the dashboard). The __Host session cookie rides the
// same-origin upgrade; the connection is read-off-only and closes with the tab.
func (s *Server) handleBrowserScreencast(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	id := r.PathValue("id")
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return // Accept already wrote the failure
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// A reader goroutine turns a client close into ctx cancellation.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	err = s.deps.Browser.Screencast(ctx, id, func(frame json.RawMessage) error {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageText, frame)
	})
	if err != nil {
		reason := err.Error()
		if len(reason) > 120 {
			reason = reason[:120]
		}
		_ = c.Close(websocket.StatusInternalError, reason)
		return
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// --- purchase approval (passkey step-up), mirrors the bot-proposal path ---

func purchaseDigest(id, actionSHA, decision string) string {
	obj := map[string]string{"v": "1", "ns": "conductor-dashboard-purchase", "purchase": id, "action_sha256": actionSHA, "decision": decision}
	b, _ := json.Marshal(obj)
	return auth.DigestOf(b)
}

func (s *Server) handleBrowserPurchaseApproval(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) || s.deps.Browser == nil {
		return
	}
	id := r.PathValue("id")
	actionSHA, summary, operators, ok := s.deps.Browser.PurchaseView(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no_purchase", "no such purchase", "")
		return
	}
	approve, deny := purchaseDigest(id, actionSHA, "approve"), purchaseDigest(id, actionSHA, "deny")
	s.labels.note(auth.PurposePurchase, approve, "approve purchase "+id+": "+compactJSON(summary))
	s.labels.note(auth.PurposePurchase, deny, "deny purchase "+id+": "+compactJSON(summary))
	writeJSON(w, http.StatusOK, map[string]any{
		"purchase": id, "action_sha256": actionSHA,
		"digests": map[string]string{
			"approve": approve,
			"deny":    deny,
		},
		"summary": json.RawMessage(nonEmptyJSON(summary)), "operators": operators,
		"purpose": string(auth.PurposePurchase),
	})
}

func (s *Server) handleBrowserPurchaseDecision(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w) {
		return
	}
	if !s.authEnforced() {
		writeError(w, http.StatusForbidden, "auth_required", "a purchase decision needs a signed-in operator with a passkey", "enrol an operator first")
		return
	}
	sess, ok := auth.SessionFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if req.Decision != "approve" && req.Decision != "deny" {
		writeError(w, http.StatusBadRequest, "bad_decision", "decision must be approve or deny", "")
		return
	}
	actionSHA, _, _, ok := s.deps.Browser.PurchaseView(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no_purchase", "no such purchase", "")
		return
	}
	digest := purchaseDigest(id, actionSHA, req.Decision)
	ev, err := s.auth.RequireStepup(sess, auth.PurposePurchase, digest, r.Header.Get(auth.StepupHeader))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "stepup_required", err.Error(), "touch your passkey to decide")
		return
	}
	if err := s.deps.Browser.DecidePurchase(r.Context(), id, req.Decision, sess.Operator, req.Reason, ev); err != nil {
		writeError(w, http.StatusBadRequest, "decide_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purchase": id, "decision": req.Decision, "by": sess.Operator})
}
