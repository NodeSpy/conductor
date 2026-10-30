package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Routes for the sign-in and step-up methods beyond passkeys (SSH, password +
// TOTP) and for managing those credentials from the Access screen. Mounted by
// Mux.
func (m *Manager) methodRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/password/login", m.handlePasswordLogin)
	mux.HandleFunc("POST /api/v1/auth/ssh/begin", m.handleSSHBegin)
	mux.HandleFunc("POST /api/v1/auth/ssh/finish", m.handleSSHFinish)
	mux.HandleFunc("POST /api/v1/auth/ssh/poll", m.handleSSHPoll)
	// approve-login's two calls: no session, no CSRF — the signature is the
	// credential. sameOriginJSON (Mux) keeps a cross-site page from sending
	// them.
	mux.HandleFunc("POST /api/v1/auth/ssh/pending", m.handleSSHPending)
	mux.HandleFunc("POST /api/v1/auth/ssh/approve", m.handleSSHApprove)

	mux.HandleFunc("GET /api/v1/auth/stepup/methods", m.handleStepupMethods)
	mux.HandleFunc("POST /api/v1/auth/stepup/ssh/begin", m.handleStepupSSHBegin)
	mux.HandleFunc("POST /api/v1/auth/stepup/ssh/finish", m.handleStepupSSHFinish)
	mux.HandleFunc("POST /api/v1/auth/stepup/ssh/poll", m.handleStepupSSHPoll)
	mux.HandleFunc("POST /api/v1/auth/stepup/totp", m.handleStepupTOTP)
	mux.HandleFunc("POST /api/v1/auth/stepup/password", m.handleStepupPassword)

	mux.HandleFunc("POST /api/v1/auth/password", m.handlePasswordSet)
	mux.HandleFunc("DELETE /api/v1/auth/password", m.handlePasswordClear)
	mux.HandleFunc("POST /api/v1/auth/totp/begin", m.handleTOTPBegin)
	mux.HandleFunc("POST /api/v1/auth/totp/confirm", m.handleTOTPConfirm)
	mux.HandleFunc("DELETE /api/v1/auth/totp", m.handleTOTPClear)
	mux.HandleFunc("POST /api/v1/passkeys/register/begin", m.handlePasskeyRegisterBegin)
	mux.HandleFunc("POST /api/v1/passkeys/register/finish", m.handlePasskeyRegisterFinish)

	mux.HandleFunc("POST /api/v1/enroll/password/begin", m.handleEnrollPasswordBegin)
	mux.HandleFunc("POST /api/v1/enroll/password/finish", m.handleEnrollPasswordFinish)
}

// SignInMethods lists the sign-in methods this connection can use, strongest
// first: passkey only on a secure context whose Host has a relying party.
func (m *Manager) SignInMethods(r *http.Request) []Method {
	pol := m.policy()
	var out []Method
	if pol.SignIn(MethodPasskey) {
		if _, ok := m.passkeyRP(r); ok {
			out = append(out, MethodPasskey)
		}
	}
	for _, x := range []Method{MethodSSH, MethodPassword} {
		if pol.SignIn(x) {
			out = append(out, x)
		}
	}
	return out
}

// StepupMethods lists the step-up methods a session can use on this
// connection, strongest first: allowed by policy, and a credential the operator
// actually has.
func (m *Manager) StepupMethods(r *http.Request, sess Session) []Method {
	pol := m.policy()
	out := []Method{}
	if pol.StepupAllowed(MethodPasskey) {
		if rp, ok := m.passkeyRP(r); ok {
			for _, c := range m.deps.Store.CredentialsFor(sess.Operator) {
				if c.RPID == rp.ID && !c.Locked {
					out = append(out, MethodPasskey)
					break
				}
			}
		}
	}
	if pol.StepupAllowed(MethodSSH) {
		if _, ok := m.operatorPubKey(sess.Operator); ok {
			out = append(out, MethodSSH)
		}
	}
	rec, _ := m.deps.Store.OperatorSecret(sess.Operator)
	if pol.StepupAllowed(MethodTOTP) && rec.TOTPSecret != "" {
		out = append(out, MethodTOTP)
	}
	if pol.StepupAllowed(MethodPassword) && rec.PasswordHash != "" {
		out = append(out, MethodPassword)
	}
	return out
}

// reserve reserves one guess on the given lockout buckets (lockout.Begin),
// writing a 429 when that is refused.
func (m *Manager) reserve(w http.ResponseWriter, keys ...string) (*attempt, bool) {
	a, until, refused, ok := m.lock.Begin(keys...)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "locked", lockMessage(refused, until), "")
		return nil, false
	}
	return a, true
}

// lockedOut writes a 429 when any of keys is locked.
func (m *Manager) lockedOut(w http.ResponseWriter, keys ...string) bool {
	for _, k := range keys {
		if locked, until := m.lock.Locked(k); locked {
			writeErr(w, http.StatusTooManyRequests, "locked", lockMessage(k, until), "")
			return true
		}
	}
	return false
}

// signedIn finishes any sign-in: it records the lockout success, creates the
// session for this request and writes the cookie.
func (m *Manager) signedIn(w http.ResponseWriter, r *http.Request, operator string, method Method, credID string) {
	m.lock.Succeed(clientKey(r))
	ck, sess, err := m.CreateSessionFor(r, operator, method, credID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "session", err.Error(), "")
		return
	}
	http.SetCookie(w, ck)
	writeJSON(w, http.StatusOK, map[string]any{"operator": operator, "csrf": sess.CSRF, "method": string(method)})
}

// --- password sign-in ---

func (m *Manager) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operator string `json:"operator"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if !m.policy().SignIn(MethodPassword) {
		writeErr(w, http.StatusForbidden, "method_disabled", "password sign-in is not enabled", "")
		return
	}
	op := strings.TrimSpace(req.Operator)
	// The guess is counted (client, network and operator) inside, before the
	// hash; a refusal while any is locked counts against none.
	if err := m.passwordLoginFrom(lockKeys(r, guessSecret, op), op, req.Password, req.Code); err != nil {
		code := http.StatusUnauthorized
		var locked lockedError
		if errors.As(err, &locked) {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, "login_failed", err.Error(), "")
		return
	}
	m.signedIn(w, r, op, MethodPassword, "")
}

// --- SSH sign-in ---

func (m *Manager) handleSSHBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operator string `json:"operator"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if m.lockedOut(w, lockKeys(r, guessCredential, "")...) {
		return
	}
	v, err := m.BeginSSHLogin(r, strings.TrimSpace(req.Operator))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ssh_begin", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (m *Manager) handleSSHFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"challenge_id"`
		Signature string `json:"signature"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	a, ok := m.reserve(w, lockKeys(r, guessCredential, "")...)
	if !ok {
		return
	}
	op, err := m.FinishSSHLogin(r.Context(), r, req.ID, req.Signature)
	if err != nil {
		a.Fail()
		writeErr(w, http.StatusUnauthorized, "login_failed", err.Error(), "")
		return
	}
	a.Succeed()
	m.signedIn(w, r, op, MethodSSH, "")
}

func (m *Manager) handleSSHPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"challenge_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	op, done, err := m.PollSSHLogin(r, req.ID)
	if err != nil {
		writeErr(w, http.StatusGone, "ssh_poll", err.Error(), "")
		return
	}
	if !done {
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	m.signedIn(w, r, op, MethodSSH, "")
}

func (m *Manager) handleSSHPending(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	a, ok := m.reserve(w, lockKeys(r, guessCode, "")...)
	if !ok {
		return
	}
	v, err := m.PendingSSH(r, req.Code)
	if err != nil {
		a.Fail() // guessing codes counts toward the lockout
		writeErr(w, http.StatusNotFound, "no_pending", err.Error(), "")
		return
	}
	// Finding a code is not a success that clears failures: the client may
	// have begun that challenge itself.
	a.Release()
	writeJSON(w, http.StatusOK, v)
}

func (m *Manager) handleSSHApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code      string `json:"code"`
		Signature string `json:"signature"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	a, ok := m.reserve(w, lockKeys(r, guessCode, "")...)
	if !ok {
		return
	}
	if err := m.ApproveSSH(r.Context(), r, req.Code, req.Signature); err != nil {
		a.Fail()
		writeErr(w, http.StatusUnauthorized, "approve_failed", err.Error(), "")
		return
	}
	a.Release()
	writeJSON(w, http.StatusOK, map[string]any{"approved": true})
}

// --- step-up by SSH, TOTP, password ---

func (m *Manager) handleStepupMethods(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"methods": m.StepupMethods(r, sess)})
}

type stepupReq struct {
	Purpose   string `json:"purpose"`
	Digest    string `json:"digest"`
	ID        string `json:"challenge_id"`
	Signature string `json:"signature"`
	Code      string `json:"code"`
	Password  string `json:"password"`
}

// stepupDone writes the minted token and rotates the session secret, as the
// passkey step-up does (fixation resistance, design §6.3).
func (m *Manager) stepupDone(w http.ResponseWriter, sess Session, token string) {
	if ck, err := m.RotateSecret(sess); err == nil {
		http.SetCookie(w, ck)
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

func (m *Manager) readStepup(w http.ResponseWriter, r *http.Request) (Session, stepupReq, bool) {
	sess, ok := m.authed(w, r)
	if !ok {
		return Session{}, stepupReq{}, false
	}
	var req stepupReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return Session{}, stepupReq{}, false
	}
	return sess, req, true
}

func (m *Manager) handleStepupSSHBegin(w http.ResponseWriter, r *http.Request) {
	sess, req, ok := m.readStepup(w, r)
	if !ok {
		return
	}
	v, err := m.BeginSSHStepup(r, sess, Purpose(req.Purpose), req.Digest)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "stepup_begin", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (m *Manager) handleStepupSSHFinish(w http.ResponseWriter, r *http.Request) {
	sess, req, ok := m.readStepup(w, r)
	if !ok {
		return
	}
	tok, err := m.FinishSSHStepup(r.Context(), r, sess, req.ID, req.Signature)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "stepup_failed", err.Error(), "")
		return
	}
	m.stepupDone(w, sess, tok)
}

func (m *Manager) handleStepupSSHPoll(w http.ResponseWriter, r *http.Request) {
	sess, req, ok := m.readStepup(w, r)
	if !ok {
		return
	}
	tok, done, err := m.PollSSHStepup(r, sess, req.ID)
	if err != nil {
		writeErr(w, http.StatusGone, "stepup_poll", err.Error(), "")
		return
	}
	if !done {
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	m.stepupDone(w, sess, tok)
}

func (m *Manager) handleStepupTOTP(w http.ResponseWriter, r *http.Request) {
	sess, req, ok := m.readStepup(w, r)
	if !ok {
		return
	}
	tok, err := m.TOTPStepup(sess, Purpose(req.Purpose), req.Digest, req.Code)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "stepup_failed", err.Error(), "")
		return
	}
	m.stepupDone(w, sess, tok)
}

func (m *Manager) handleStepupPassword(w http.ResponseWriter, r *http.Request) {
	sess, req, ok := m.readStepup(w, r)
	if !ok {
		return
	}
	tok, err := m.PasswordStepup(sess, Purpose(req.Purpose), req.Digest, req.Password, req.Code)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "stepup_failed", err.Error(), "")
		return
	}
	m.stepupDone(w, sess, tok)
}

// --- managing credentials while signed in (each needs a step-up) ---

// credentialStepup spends a PurposeCredential step-up for action.
func (m *Manager) credentialStepup(w http.ResponseWriter, r *http.Request, sess Session, action string) bool {
	if _, err := m.RequireStepup(sess, PurposeCredential, CredentialDigest(sess.Operator, action), r.Header.Get(StepupHeader)); err != nil {
		writeErr(w, http.StatusForbidden, "stepup_required", err.Error(), "confirm it's you first")
		return false
	}
	return true
}

func (m *Manager) handlePasswordSet(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if err := checkPasswordShape(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, "weak_password", err.Error(), "")
		return
	}
	if !m.credentialStepup(w, r, sess, "password.set") {
		return
	}
	if err := m.SetPassword(sess.Operator, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, "password", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"password": true})
}

func (m *Manager) handlePasswordClear(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok || !m.credentialStepup(w, r, sess, "password.remove") {
		return
	}
	if err := m.ClearPassword(sess.Operator); err != nil {
		writeErr(w, http.StatusInternalServerError, "password", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"password": false})
}

func (m *Manager) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	en, err := m.BeginTOTP(sess)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "totp", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, en)
}

func (m *Manager) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	// Check the code against the pending secret before spending the step-up, so
	// a mistyped code doesn't cost the operator another step-up.
	m.mu.Lock()
	p, pending := m.totpNew[sess.ID]
	m.mu.Unlock()
	if !pending {
		writeErr(w, http.StatusBadRequest, "totp", "no authenticator setup in progress — start again", "")
		return
	}
	if _, match := matchTOTP(p.secret, req.Code, m.now()); !match {
		writeErr(w, http.StatusBadRequest, "totp", "that code doesn't match — check the time on your device and try the next code", "")
		return
	}
	if !m.credentialStepup(w, r, sess, "totp.set") {
		return
	}
	if err := m.ConfirmTOTP(sess, req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, "totp", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp": true})
}

func (m *Manager) handleTOTPClear(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok || !m.credentialStepup(w, r, sess, "totp.remove") {
		return
	}
	if err := m.ClearTOTP(sess.Operator); err != nil {
		writeErr(w, http.StatusInternalServerError, "totp", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp": false})
}

func (m *Manager) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if _, ok := m.passkeyRP(r); !ok {
		writeErr(w, http.StatusBadRequest, "no_passkeys_here", "passkeys need HTTPS (or localhost) on a hostname with a relying party", "")
		return
	}
	if !m.credentialStepup(w, r, sess, "passkey.add") {
		return
	}
	id, opts, err := m.BeginSessionPasskey(r, sess, strings.TrimSpace(req.Name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "register_begin", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": json.RawMessage(opts)})
}

func (m *Manager) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authed(w, r)
	if !ok {
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
	c, err := m.FinishSessionPasskey(sess, req.CeremonyID, req.Name, req.Response)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "register_finish", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": passkeyView(c)})
}

// --- the SSH-signed enroll link can set a password (+ authenticator) ---

func (m *Manager) handleEnrollPasswordBegin(w http.ResponseWriter, r *http.Request) {
	if !m.localhostOnly(w, r) {
		return
	}
	var req struct {
		CeremonyID string `json:"ceremony_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	en, err := m.BeginEnrollPassword(req.CeremonyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "enroll_password", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp": en, "totp_required": m.policy().PasswordRequiresTOTP})
}

func (m *Manager) handleEnrollPasswordFinish(w http.ResponseWriter, r *http.Request) {
	if !m.localhostOnly(w, r) {
		return
	}
	var req struct {
		CeremonyID string `json:"ceremony_id"`
		Password   string `json:"password"`
		Code       string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return
	}
	if err := m.FinishEnrollPassword(req.CeremonyID, req.Password, req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, "enroll_password", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"password": true})
}
