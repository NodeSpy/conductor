package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds an auth request body (assertions are small).
const maxBody = 1 << 20

// Mux returns the auth, enrollment and Access routes. They are mounted directly
// (not behind Protect): each handler does exactly the auth it needs, because
// sign-in and enrollment happen BEFORE there is a session. server.go wraps the
// rest of /api with Protect.
func (m *Manager) Mux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/auth/state", m.handleState)
	mux.HandleFunc("POST /api/v1/auth/login/begin", m.handleLoginBegin)
	mux.HandleFunc("POST /api/v1/auth/login/finish", m.handleLoginFinish)
	mux.HandleFunc("POST /api/v1/auth/logout", m.handleLogout)
	mux.HandleFunc("POST /api/v1/auth/stepup/begin", m.handleStepupBegin)
	mux.HandleFunc("POST /api/v1/auth/stepup/finish", m.handleStepupFinish)

	mux.HandleFunc("POST /api/v1/enroll/redeem", m.handleEnrollRedeem)
	mux.HandleFunc("POST /api/v1/enroll/begin", m.handleEnrollBegin)
	mux.HandleFunc("POST /api/v1/enroll/finish", m.handleEnrollFinish)

	mux.HandleFunc("GET /api/v1/passkeys", m.handlePasskeys)
	mux.HandleFunc("DELETE /api/v1/passkeys/{id}", m.handlePasskeyDelete)
	mux.HandleFunc("GET /api/v1/sessions", m.handleSessions)
	mux.HandleFunc("DELETE /api/v1/sessions", m.handleSessionsDeleteAll)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", m.handleSessionDelete)
	mux.HandleFunc("GET /api/v1/access", m.handleAccess)
	m.methodRoutes(mux)

	return mux
}

// authed resolves the session and enforces CSRF (design §6.4). Protected auth
// handlers call it; a failure writes the response and returns ok=false.
func (m *Manager) authed(w http.ResponseWriter, r *http.Request) (Session, bool) {
	info := reqInfoFrom(r)
	sess, err := m.ValidateCookie(r, info.Listener, info.Node)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
		return Session{}, false
	}
	if err := checkCSRF(sess, r); err != nil {
		writeErr(w, http.StatusForbidden, "csrf", err.Error(), "reload to refresh the token")
		return Session{}, false
	}
	return sess, true
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// clientKey is the lockout bucket for a request: the peer address (and tailnet
// node, when present), so one client's failures don't lock another's.
func clientKey(r *http.Request) string {
	info := reqInfoFrom(r)
	if info.Node != "" {
		return "node:" + info.Node
	}
	host := r.RemoteAddr
	if h, _, ok := strings.Cut(host, ":"); ok && h != "" {
		host = h
	}
	return "addr:" + host
}

func deviceLabel(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	switch {
	case ua == "":
		return "a browser"
	case strings.Contains(ua, "iPhone"):
		return "iPhone · Safari"
	case strings.Contains(ua, "Firefox"):
		return "Firefox"
	case strings.Contains(ua, "Chrome"):
		return "Chrome"
	case strings.Contains(ua, "Safari"):
		return "Safari"
	default:
		return "a browser"
	}
}

// --- state / sign-in ---

func (m *Manager) handleState(w http.ResponseWriter, r *http.Request) {
	info := reqInfoFrom(r)
	// required is true when operators are configured: sign-in is then enforced on
	// the API, and the SPA shows the sign-in gate. With no operators the dashboard
	// serves open (a bootstrap box), so the gate stays out of the way.
	required := m.deps.Operators != nil && len(m.deps.Operators()) > 0
	pol := m.policy()
	sess, err := m.ValidateCookie(r, info.Listener, info.Node)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"signed_in": false, "required": required, "listener": string(info.Listener),
			"transport": string(info.Transport), "methods": m.SignInMethods(r),
			"password_needs_totp": pol.PasswordRequiresTOTP,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signed_in":           true,
		"required":            required,
		"operator":            sess.Operator,
		"listener":            string(sess.Listener),
		"transport":           string(info.Transport),
		"method":              string(sess.EffectiveMethod()),
		"expires":             sess.Expires.UTC().Format(time.RFC3339),
		"csrf":                sess.CSRF,
		"stepup_at":           tsOrEmpty(sess.StepupAt),
		"stepup_methods":      m.StepupMethods(r, sess),
		"password_needs_totp": pol.PasswordRequiresTOTP,
	})
}

func (m *Manager) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	key := clientKey(r)
	if locked, until := m.lock.Locked(key); locked {
		writeErr(w, http.StatusTooManyRequests, "locked", "too many sign-in attempts — try again shortly", "locked until "+until.UTC().Format(time.RFC3339))
		return
	}
	info := reqInfoFrom(r)
	if !m.policy().SignIn(MethodPasskey) {
		writeErr(w, http.StatusForbidden, "method_disabled", "passkey sign-in is not enabled", "")
		return
	}
	if _, ok := m.passkeyRP(r); !ok {
		writeErr(w, http.StatusBadRequest, "no_passkeys_here", "passkeys need HTTPS (or localhost) on a hostname with a relying party", "sign in with SSH or a password")
		return
	}
	id, opts, err := m.BeginLoginFor(info.Listener, info.Host)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login_begin", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": json.RawMessage(opts)})
}

func (m *Manager) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Response   json.RawMessage `json:"response"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	key := clientKey(r)
	if locked, until := m.lock.Locked(key); locked {
		writeErr(w, http.StatusTooManyRequests, "locked", "too many sign-in attempts — try again shortly", "locked until "+until.UTC().Format(time.RFC3339))
		return
	}
	operator, credID, _, err := m.FinishLogin(req.CeremonyID, req.Response)
	if err != nil {
		m.lock.Fail(key)
		writeErr(w, http.StatusUnauthorized, "login_failed", err.Error(), "")
		return
	}
	m.signedIn(w, r, operator, MethodPasskey, credID)
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Logout is lenient: no CSRF needed to end your own session.
	ck := m.Logout(r)
	http.SetCookie(w, ck)
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

// --- step-up ---

func (m *Manager) handleStepupBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	var req struct {
		Purpose string `json:"purpose"`
		Digest  string `json:"digest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	info := reqInfoFrom(r)
	id, opts, err := m.BeginStepupFor(info.Listener, info.Host, sess, Purpose(req.Purpose), req.Digest)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "stepup_begin", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": json.RawMessage(opts)})
}

func (m *Manager) handleStepupFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Response   json.RawMessage `json:"response"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	token, err := m.FinishStepup(req.CeremonyID, sess, req.Response)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "stepup_failed", err.Error(), "")
		return
	}
	// Rotate the session secret on step-up (fixation resistance, design §6.3).
	if ck, err := m.RotateSecret(sess); err == nil {
		http.SetCookie(w, ck)
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

// --- enrollment (localhost only to redeem/begin/finish) ---

func (m *Manager) localhostOnly(w http.ResponseWriter, r *http.Request) bool {
	if reqInfoFrom(r).Listener != ListenerLocalhost {
		writeErr(w, http.StatusForbidden, "localhost_only", "enrollment starts on http://localhost on the box", "run conductor dashboard enroll on the machine")
		return false
	}
	return true
}

func (m *Manager) handleEnrollRedeem(w http.ResponseWriter, r *http.Request) {
	if !m.localhostOnly(w, r) {
		return
	}
	if !m.lock.RedeemAllowed(clientKey(r)) {
		writeErr(w, http.StatusTooManyRequests, "locked", "too many enrollment attempts this hour", "")
		return
	}
	var req struct {
		T           string `json:"t"`
		PairingCode string `json:"pairing_code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	id, operator, existing, err := m.RedeemTicket(req.T, req.PairingCode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "enroll_redeem", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "operator": operator, "existing": existing})
}

func (m *Manager) handleEnrollBegin(w http.ResponseWriter, r *http.Request) {
	if !m.localhostOnly(w, r) {
		return
	}
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		RP         string          `json:"rp"`
		Name       string          `json:"name"`
		Assertion  json.RawMessage `json:"assertion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	info := reqInfoFrom(r)
	var assertion []byte
	if len(req.Assertion) > 0 && string(req.Assertion) != "null" {
		assertion = req.Assertion
	}
	need, opts, err := m.BeginEnroll(info.Listener, req.CeremonyID, req.RP, req.Name, assertion)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "enroll_begin", err.Error(), "")
		return
	}
	if need {
		writeJSON(w, http.StatusOK, map[string]any{"need_assertion": true, "options": json.RawMessage(opts)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"need_assertion": false, "options": json.RawMessage(opts)})
}

func (m *Manager) handleEnrollFinish(w http.ResponseWriter, r *http.Request) {
	if !m.localhostOnly(w, r) {
		return
	}
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Name       string          `json:"name"`
		Response   json.RawMessage `json:"response"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	c, err := m.FinishEnroll(req.CeremonyID, req.Name, req.Response)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "enroll_finish", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": passkeyView(c)})
}

// --- Access: passkeys & sessions CRUD ---

func (m *Manager) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": m.passkeyList(sess.Operator)})
}

func (m *Manager) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// A passkey can only be removed by its own operator, and never the last one
	// (you'd lock yourself out — add another first).
	owned := m.deps.Store.CredentialsFor(sess.Operator)
	var target *Credential
	for _, c := range owned {
		if c.ID == id {
			target = c
		}
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such passkey", "")
		return
	}
	if len(owned) <= 1 {
		writeErr(w, http.StatusConflict, "last_passkey", "this is your only passkey — add another first, so you can still sign in", "")
		return
	}
	if _, err := m.deps.Store.DeleteCredential(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete", err.Error(), "")
		return
	}
	m.audit(map[string]any{"event": "dashboard_passkey_revoked", "operator": sess.Operator, "credential": id, "name": target.Name})
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
}

func (m *Manager) handleSessions(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": m.sessionList(sess)})
}

func (m *Manager) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	target, exists := m.deps.Store.Session(id)
	if !exists || target.Operator != sess.Operator {
		writeErr(w, http.StatusNotFound, "not_found", "no such session", "")
		return
	}
	if err := m.deps.Store.DeleteSession(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete", err.Error(), "")
		return
	}
	m.audit(map[string]any{"event": "dashboard_session_end", "operator": sess.Operator, "session": id})
	writeJSON(w, http.StatusOK, map[string]any{"ended": id})
}

func (m *Manager) handleSessionsDeleteAll(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	if err := m.deps.Store.DeleteSessionsFor(sess.Operator); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete", err.Error(), "")
		return
	}
	m.audit(map[string]any{"event": "dashboard_logout_all", "operator": sess.Operator})
	http.SetCookie(w, m.clearCookie(reqInfoFrom(r).Transport))
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

func (m *Manager) handleAccess(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	pol := m.policy()
	rec, _ := m.deps.Store.OperatorSecret(sess.Operator)
	_, passkeyHere := m.passkeyRP(r)
	digests := map[string]string{}
	for _, a := range []string{"password.set", "password.remove", "totp.set", "totp.remove", "passkey.add"} {
		digests[a] = CredentialDigest(sess.Operator, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operator":  sess.Operator,
		"passkeys":  m.passkeyList(sess.Operator),
		"sessions":  m.sessionList(sess),
		"listeners": m.listenerFacts(),
		"session_policy": map[string]any{
			"idle": m.deps.SessionIdle.String(),
			"max":  m.deps.SessionMax.String(),
		},
		"password":     map[string]any{"set": rec.PasswordHash != "", "set_at": tsOrEmpty(rec.PasswordSet)},
		"totp":         map[string]any{"set": rec.TOTPSecret != "", "set_at": tsOrEmpty(rec.TOTPSet)},
		"passkey_here": passkeyHere,
		"auth_policy": map[string]any{
			"methods": pol.Methods, "password_requires_totp": pol.PasswordRequiresTOTP, "stepup": pol.Stepup,
		},
		"stepup_methods":     m.StepupMethods(r, sess),
		"credential_digests": digests,
		"transport":          string(reqInfoFrom(r).Transport),
	})
}

func tsOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
