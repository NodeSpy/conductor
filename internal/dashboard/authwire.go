package dashboard

// Auth wiring (design §6): building the auth Manager, composing the per-listener
// request handler (security headers → listener/peer tagging → the auth routes or
// the session-guarded API or the public SPA), the tailnet listener, and the
// proposal-approval endpoints that feed Executor.Decide behind a WebAuthn
// step-up. Kept out of server.go so the auth surface lives in one place and the
// config-edit owner's server.go stays small.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// buildAuth constructs the auth Manager with the localhost RP (the tailnet RP is
// added in startTailnet once the MagicDNS name is known). It never blocks or
// dials; a failure logs and leaves s.auth nil (the dashboard then serves open on
// localhost, as the bootstrap/e2e daemons do).
func (s *Server) buildAuth() {
	dir := s.deps.StateDir
	if dir == "" {
		if sf := s.deps.Cfg.Store.StateFile; sf != "" {
			dir = filepath.Join(filepath.Dir(sf), "dashboard")
		}
	}
	if dir == "" {
		s.deps.Log("dashboard: no state dir for auth; serving without sign-in")
		return
	}
	st, err := auth.OpenStore(dir, s.deps.Now)
	if err != nil {
		s.deps.Log("dashboard: auth store: %v; serving without sign-in", err)
		return
	}
	port := s.deps.Cfg.Dashboard.EffectivePort()
	localhostRP := auth.RP{
		ID:       "localhost",
		Origins:  []string{fmt.Sprintf("http://localhost:%d", port)},
		Listener: auth.ListenerLocalhost,
	}
	var auditFn func(map[string]any)
	if s.deps.Store != nil {
		auditFn = s.deps.Store.Audit
	}
	mgr, err := auth.NewManager(auth.Deps{
		Store:       st,
		Operators:   s.deps.Operators,
		SessionIdle: s.deps.Cfg.Dashboard.Session.IdleD(),
		SessionMax:  s.deps.Cfg.Dashboard.Session.MaxD(),
		Audit:       auditFn,
		Lifecycle:   s.deps.Lifecycle,
		Now:         s.deps.Now,
	}, localhostRP)
	if err != nil {
		s.deps.Log("dashboard: auth manager: %v; serving without sign-in", err)
		return
	}
	s.auth = mgr
}

// AuthManager exposes the auth Manager so the daemon can serve `conductor
// dashboard` on the control socket. It is nil when auth could not be wired.
func (s *Server) AuthManager() *auth.Manager { return s.auth }

// authEnforced reports whether a session is required: only when the auth Manager
// is wired AND operators are configured (a box with no operators can't sign in,
// so requiring it would make localhost bootstrap unusable; it binds localhost
// only by default).
func (s *Server) authEnforced() bool {
	if s.auth == nil || s.deps.Operators == nil {
		return false
	}
	return len(s.deps.Operators()) > 0
}

func (s *Server) authState() string {
	if s.authEnforced() {
		return "required (passkey)"
	}
	return "open — no operators configured"
}

// listenerHandler builds the full handler chain for one listener: security
// headers, the listener/peer tag, then the dispatch (auth routes, guarded API,
// or the public SPA).
func (s *Server) listenerHandler(l auth.Listener) http.Handler {
	return auth.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = auth.WithReqInfo(r, l, "", "")
		s.dispatch(w, r)
	}))
}

// dispatch routes a request: auth/enroll/passkeys/sessions/access go to the auth
// Mux (self-authenticating); other /api paths sit behind the session guard when
// enforced; everything else is the public SPA (the sign-in and enrol pages must
// load without a session).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if s.auth != nil && isAuthPath(p) {
		s.auth.Mux().ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/") {
		if s.authEnforced() {
			s.auth.Protect(s.mux).ServeHTTP(w, r)
			return
		}
		s.mux.ServeHTTP(w, r)
		return
	}
	s.mux.ServeHTTP(w, r) // SPA + static (public)
}

// isAuthPath reports whether a path is served by the auth Mux (which does its own
// authentication), so it must not be wrapped by the session guard.
func isAuthPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/api/v1/auth/"),
		strings.HasPrefix(p, "/api/v1/enroll"),
		strings.HasPrefix(p, "/api/v1/passkeys"),
		strings.HasPrefix(p, "/api/v1/sessions"),
		p == "/api/v1/access":
		return true
	}
	return false
}

// --- tailnet listener (design §6.2) ---

// startTailnet discovers the node's tailnet addresses and MagicDNS name through
// tailscaled's LocalAPI, adds the MagicDNS RP, and binds HTTPS on each tailnet
// address with the LocalAPI (or explicit) certificate. Peers are checked by
// WhoIs and the Host header is allow-listed, defeating DNS rebinding.
func (s *Server) startTailnet(ctx context.Context, port int) error {
	api := s.tsAPI
	if api == nil {
		api = newTSLocalAPI("")
	}
	status, err := api.Status(ctx)
	if err != nil {
		return err
	}
	if status.MagicDNSName == "" || len(status.IPs) == 0 {
		return fmt.Errorf("tailscaled reports no MagicDNS name or addresses")
	}
	// The passkey RP on the tailnet is the MagicDNS name.
	if s.auth != nil {
		origin := fmt.Sprintf("https://%s:%d", status.MagicDNSName, port)
		if err := s.auth.AddRP(auth.RP{ID: status.MagicDNSName, Origins: []string{origin}, Listener: auth.ListenerTailscale}); err != nil {
			return fmt.Errorf("tailnet RP: %w", err)
		}
	}

	tlsCfg, err := s.tailnetTLS(status.MagicDNSName)
	if err != nil {
		return err
	}
	tcfg := s.deps.Cfg.Dashboard.Tailscale
	handler := auth.SecurityHeaders(s.tailnetGuard(api, status, port))

	srv := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	var bound int
	for _, ip := range status.IPs {
		addr := net.JoinHostPort(ip, strconv.Itoa(port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.deps.Log("dashboard: could not bind tailnet %s: %v", addr, err)
			continue
		}
		bound++
		go func(ln net.Listener, addr string) {
			if err := srv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.deps.Log("dashboard: tailnet serve %s stopped: %v", addr, err)
			}
		}(ln, addr)
	}
	if bound == 0 {
		return fmt.Errorf("no tailnet address could be bound")
	}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	s.deps.Log("dashboard: serving on https://%s:%d (tailnet; allow_tagged=%v, %d allow_users)",
		status.MagicDNSName, port, tcfg.AllowTagged, len(tcfg.AllowUsers))
	return nil
}

// tailnetTLS builds the TLS config: the daily-refreshed LocalAPI cert for "auto",
// or an explicit cert/key pair.
func (s *Server) tailnetTLS(magicDNS string) (*tls.Config, error) {
	cert := s.deps.Cfg.Dashboard.Tailscale.Cert
	if !cert.Auto() {
		pair, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("dashboard.tailscale.cert: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
	}
	api := s.tsAPI
	if api == nil {
		api = newTSLocalAPI("")
	}
	cc := newCertCache(api, magicDNS, s.deps.Now)
	return &tls.Config{GetCertificate: cc.GetCertificate, MinVersion: tls.VersionTLS12}, nil
}

// tailnetGuard enforces the Host allowlist and the WhoIs peer checks on every
// tailnet request, tags it with the peer's node/user, then dispatches.
func (s *Server) tailnetGuard(api localAPI, status tsStatus, port int) http.Handler {
	tcfg := s.deps.Cfg.Dashboard.Tailscale
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, status.MagicDNSName, status.IPs, port) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		who, err := api.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			http.Error(w, "could not identify tailnet peer", http.StatusForbidden)
			return
		}
		if ok, reason := peerAllowed(who, tcfg.AllowTagged, tcfg.AllowUsers); !ok {
			http.Error(w, reason, http.StatusForbidden)
			return
		}
		r = auth.WithReqInfo(r, auth.ListenerTailscale, who.Node, who.Login)
		s.dispatch(w, r)
	})
}

// --- proposal approval (design §6.6) ---

// authRoutes registers the proposal view + decision endpoints on the API mux, so
// they sit behind the session guard like every other /api route.
func (s *Server) authRoutes() {
	s.mux.HandleFunc("GET /api/v1/bots/{bot}/proposals/{id}/approval", s.handleApprovalView)
	s.mux.HandleFunc("POST /api/v1/bots/{bot}/proposals/{id}/decision", s.handleApprovalDecision)
}

// handleApprovalView returns the canonical approval digest and a human summary
// (rendered from the payload, never agent text). The client steps up over this
// digest before deciding.
func (s *Server) handleApprovalView(w http.ResponseWriter, r *http.Request) {
	if s.deps.ApprovalView == nil {
		writeError(w, http.StatusNotFound, "no_bots", "no bots on this daemon", "")
		return
	}
	bot, id := r.PathValue("bot"), r.PathValue("id")
	actionSHA, summary, operators, ok := s.deps.ApprovalView(bot, id)
	if !ok {
		writeError(w, http.StatusNotFound, "no_proposal", "no such proposal", "")
		return
	}
	// The client steps up over the digest for the decision it is about to make;
	// the server recomputes the same digest in handleApprovalDecision, so the
	// client only forwards it (no client-side hashing that could drift).
	writeJSON(w, http.StatusOK, map[string]any{
		"bot": bot, "proposal": id, "action_sha256": actionSHA,
		"digests": map[string]string{
			"approve": approvalDigest(bot, id, actionSHA, "approve"),
			"deny":    approvalDigest(bot, id, actionSHA, "deny"),
		},
		"summary": json.RawMessage(nonEmptyJSON(summary)), "operators": operators,
		"purpose": string(auth.PurposeApprove),
	})
}

// approvalDigest is the canonical per-decision digest the step-up assertion
// commits to (design §6.6). Go marshals the map with sorted keys, so client and
// server — both computing it from this function — always agree.
func approvalDigest(bot, id, actionSHA, decision string) string {
	obj := map[string]string{
		"v": "1", "ns": "conductor-dashboard-approval",
		"bot": bot, "proposal": id, "action_sha256": actionSHA, "decision": decision,
	}
	b, _ := json.Marshal(obj)
	return auth.DigestOf(b)
}

// handleApprovalDecision applies an approve/deny behind a WebAuthn step-up bound
// to the proposal's approval digest (design §6.6). The daemon's DecideProposal
// feeds Executor.Decide with method "webauthn" and the assertion evidence.
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	if s.deps.DecideProposal == nil || s.deps.ApprovalView == nil {
		writeError(w, http.StatusNotFound, "no_bots", "no bots on this daemon", "")
		return
	}
	if !s.authEnforced() {
		writeError(w, http.StatusForbidden, "auth_required", "approvals need a signed-in operator with a passkey", "enrol an operator first")
		return
	}
	sess, ok := auth.SessionFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
		return
	}
	bot, id := r.PathValue("bot"), r.PathValue("id")
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
	actionSHA, _, _, ok := s.deps.ApprovalView(bot, id)
	if !ok {
		writeError(w, http.StatusNotFound, "no_proposal", "no such proposal", "")
		return
	}
	// The step-up assertion must be bound to THIS proposal's per-decision digest.
	digest := approvalDigest(bot, id, actionSHA, req.Decision)
	ev, err := s.auth.RequireStepup(sess, auth.PurposeApprove, digest, r.Header.Get(auth.StepupHeader))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "stepup_required", err.Error(), "touch your passkey to approve")
		return
	}
	if err := s.deps.DecideProposal(r.Context(), bot, id, req.Decision, sess.Operator, req.Reason, ev); err != nil {
		writeError(w, http.StatusBadRequest, "decide_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "proposal": id, "decision": req.Decision, "by": sess.Operator})
}

// nonEmptyJSON returns s if it is non-empty JSON, else a JSON null.
func nonEmptyJSON(s string) []byte {
	if strings.TrimSpace(s) == "" {
		return []byte("null")
	}
	return []byte(s)
}

// ensure time is referenced (BaseContext closures above use context only; keep
// the import honest if the file evolves).
var _ = time.Now
