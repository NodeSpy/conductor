package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters for new hashes (OWASP's minimum argon2id profile:
// 19 MiB, 2 passes, 1 lane). Verification reads the parameters from the stored
// PHC string, so raising these later doesn't invalidate existing passwords.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16

	// Password length bounds, in characters. The upper bound caps the hashing
	// input; the lower is the floor for a credential that may stand alone when
	// the password-only knob is on.
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

var errBadCredentials = errors.New("wrong operator, password or code")

// HashPassword returns an argon2id PHC string for pw with a fresh salt.
func (m *Manager) HashPassword(pw string) (string, error) {
	if err := checkPasswordShape(pw); err != nil {
		return "", err
	}
	salt, err := m.randBytes(argonSaltLen)
	if err != nil {
		return "", err
	}
	m.hash <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-m.hash
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// checkPasswordShape enforces the length bounds.
func checkPasswordShape(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return fmt.Errorf("a password needs at least %d characters", MinPasswordLen)
	}
	if n > MaxPasswordLen {
		return fmt.Errorf("a password can have at most %d characters", MaxPasswordLen)
	}
	return nil
}

// verifyPassword checks pw against a PHC string. An empty phc (no password set,
// or an unknown operator) still runs one hash against a fixed dummy so the
// response time doesn't reveal which operators have a password.
func (m *Manager) verifyPassword(phc, pw string) bool {
	if len(pw) > MaxPasswordLen*4 {
		return false
	}
	if phc == "" {
		phc = dummyPHC
	}
	var v, mem, t, p int
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	if mem <= 0 || mem > 1<<20 || t <= 0 || t > 16 || p <= 0 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	m.hash <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, uint32(t), uint32(mem), uint8(p), uint32(len(want)))
	<-m.hash
	ok := subtle.ConstantTimeCompare(got, want) == 1
	return ok && phc != dummyPHC
}

// dummyPHC is a well-formed hash no password matches by construction: the
// verifier refuses it after hashing (see verifyPassword).
var dummyPHC = fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
	base64.RawStdEncoding.EncodeToString(make([]byte, argonSaltLen)), base64.RawStdEncoding.EncodeToString(make([]byte, argonKeyLen)))

// SetPassword stores a new password for an operator (Access screen, enroll
// link). The caller has authorized it (a step-up, or the SSH-signed ticket).
func (m *Manager) SetPassword(operator, pw string) error {
	phc, err := m.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := m.deps.Store.UpdateOperatorSecret(operator, func(o *OperatorSecret) {
		o.PasswordHash, o.PasswordSet = phc, m.now()
	}); err != nil {
		return err
	}
	m.audit(map[string]any{"event": "dashboard_password_set", "operator": operator})
	return nil
}

// ClearPassword removes an operator's password.
func (m *Manager) ClearPassword(operator string) error {
	if err := m.deps.Store.UpdateOperatorSecret(operator, func(o *OperatorSecret) {
		o.PasswordHash, o.PasswordSet = "", timeZero
	}); err != nil {
		return err
	}
	m.audit(map[string]any{"event": "dashboard_password_removed", "operator": operator})
	return nil
}

// PasswordLogin checks a password sign-in: the method must be enabled, the
// password must match, and — unless the password-only knob is on and the
// operator has no authenticator — the TOTP code must be valid and unused. Every
// failure returns the same error, so the response doesn't say which part was
// wrong or whether the operator exists.
func (m *Manager) PasswordLogin(operator, pw, code string) error {
	return m.passwordLoginFrom("", operator, pw, code)
}

// passwordLoginFrom is PasswordLogin for a request from client: the guess is
// reserved on the client's and the operator's lockout buckets before the hash
// runs, so a concurrent burst can't have more guesses evaluated than the lockout
// allows. A refused reservation counts against neither bucket.
func (m *Manager) passwordLoginFrom(client, operator, pw, code string) error {
	pol := m.policy()
	if !pol.SignIn(MethodPassword) {
		return errors.New("password sign-in is not enabled (dashboard.auth.methods)")
	}
	a, _, ok := m.lock.Begin(guessKeys(client, operator)...)
	if !ok {
		return errLocked
	}
	rec, _ := m.deps.Store.OperatorSecret(operator)
	if !safeOperator.MatchString(operator) || !m.operatorExists(operator) {
		rec = OperatorSecret{} // a password for a removed operator never signs in
	}
	if err := m.passwordFactors(pol, operator, rec, pw, code); err != nil {
		a.Fail()
		return err
	}
	a.Succeed()
	return nil
}

// guessKeys are the lockout buckets a guess at operator's secrets counts
// against: the client's (when known) and the operator's — only for a
// well-formed name, so an attacker's arbitrary strings make no buckets.
func guessKeys(client, operator string) []string {
	var keys []string
	if client != "" {
		keys = append(keys, client)
	}
	if safeOperator.MatchString(operator) {
		keys = append(keys, operatorLockKey(operator))
	}
	return keys
}

// passwordFactors checks the password and, when policy or enrolment asks for
// it, the TOTP code.
func (m *Manager) passwordFactors(pol Policy, operator string, rec OperatorSecret, pw, code string) error {
	if !m.verifyPassword(rec.PasswordHash, pw) {
		return errBadCredentials
	}
	if !pol.PasswordRequiresTOTP && rec.TOTPSecret == "" {
		return nil // the explicit password-only knob, and no authenticator to ask for
	}
	if rec.TOTPSecret == "" {
		return errBadCredentials // a password alone is not a credential under this policy
	}
	if err := m.checkTOTP(operator, code); err != nil {
		return errBadCredentials
	}
	return nil
}

// errLocked is returned while an operator's guessable-secret lockout runs.
var errLocked = errors.New("too many failed attempts for this operator — try again later")

// operatorLockKey is the lockout bucket for guessable-secret failures (password,
// TOTP) against one operator, whatever client they come from.
func operatorLockKey(operator string) string { return "op:" + operator }

// operatorExists reports whether operator is configured.
func (m *Manager) operatorExists(operator string) bool {
	if m.deps.Operators == nil || operator == "" {
		return false
	}
	_, ok := m.deps.Operators()[operator]
	return ok
}

// PasswordStepup checks a password re-entry for a step-up. Policy decides
// whether it may (only with the password-only knob on).
func (m *Manager) PasswordStepup(sess Session, purpose Purpose, digest, pw, code string) (string, error) {
	if !m.policy().StepupAllowed(MethodPassword) {
		return "", errors.New("password step-up is not enabled (dashboard.auth.stepup)")
	}
	a, until, ok := m.lock.Begin(operatorLockKey(sess.Operator))
	if !ok {
		return "", fmt.Errorf("too many wrong attempts — try again after %s", until.UTC().Format(time.RFC3339))
	}
	rec, _ := m.deps.Store.OperatorSecret(sess.Operator)
	if !m.verifyPassword(rec.PasswordHash, pw) {
		a.Fail()
		return "", errors.New("wrong password or code")
	}
	// With an authenticator the password alone is not enough, here as at sign-in.
	if rec.TOTPSecret != "" {
		if err := m.checkTOTP(sess.Operator, code); err != nil {
			a.Fail()
			return "", errors.New("wrong password or code")
		}
	}
	a.Succeed()
	return m.mintStepup(sess, purpose, digest, StepupEvidence{Method: MethodPassword})
}
