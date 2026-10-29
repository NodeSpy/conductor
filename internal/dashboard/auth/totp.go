package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// TOTP parameters (RFC 6238): SHA-1, 30 s steps, 6 digits — what every
// authenticator app accepts — with ±1 step of clock skew.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1
	totpKeyLen = 20 // 160-bit shared secret (RFC 4226 §4 recommends ≥128)

	totpEnrollTTL = 10 * time.Minute
)

var timeZero time.Time

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpPending is an authenticator being added: the secret is shown once (QR and
// key) and becomes active only after a code from it is confirmed.
type totpPending struct {
	secret  string
	expires time.Time
}

// TOTPCode returns the RFC 6238 code for a base32 secret at time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("bad TOTP secret: %w", err)
	}
	return hotp(key, uint64(step)), nil
}

// hotp is RFC 4226 HOTP with SHA-1 and dynamic truncation to 6 digits.
func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1_000_000)
}

// totpStep is the RFC 6238 time step for t.
func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// normalizeCode strips the spaces an app shows ("123 456") and checks shape.
func normalizeCode(code string) (string, bool) {
	c := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(c) != totpDigits {
		return "", false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return c, true
}

// matchTOTP returns the step within ±skew of now whose code equals code, newest
// first, or false.
func matchTOTP(secret, code string, now time.Time) (int64, bool) {
	c, ok := normalizeCode(code)
	if !ok {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	cur := totpStep(now)
	for d := int64(totpSkew); d >= -totpSkew; d-- {
		if hmac.Equal([]byte(hotp(key, uint64(cur+d))), []byte(c)) {
			return cur + d, true
		}
	}
	return 0, false
}

// checkTOTP verifies a code against an operator's active authenticator and
// spends its time step, so the same code (or an older one) never works twice.
func (m *Manager) checkTOTP(operator, code string) error {
	rec, ok := m.deps.Store.OperatorSecret(operator)
	if !ok || rec.TOTPSecret == "" {
		return errors.New("no authenticator is set up")
	}
	step, ok := matchTOTP(rec.TOTPSecret, code, m.now())
	if !ok {
		return errors.New("wrong code")
	}
	if !m.deps.Store.UseTOTPStep(operator, step) {
		return errors.New("that code was already used — wait for the next one")
	}
	return nil
}

// TOTPStepup mints a step-up token from an authenticator code. Failures count
// toward the operator's lockout, so a stolen session can't walk the code space.
func (m *Manager) TOTPStepup(sess Session, purpose Purpose, digest, code string) (string, error) {
	if !m.policy().StepupAllowed(MethodTOTP) {
		return "", errors.New("TOTP step-up is not enabled (dashboard.auth.stepup)")
	}
	a, until, ok := m.lock.Begin(operatorLockKey(sess.Operator))
	if !ok {
		return "", fmt.Errorf("too many wrong codes — try again after %s", until.UTC().Format(time.RFC3339))
	}
	if err := m.checkTOTP(sess.Operator, code); err != nil {
		a.Fail()
		return "", err
	}
	a.Succeed()
	return m.mintStepup(sess, purpose, digest, StepupEvidence{Method: MethodTOTP})
}

// TOTPEnrollment is what the Access screen (or the enroll link) shows for a new
// authenticator: the key to type, the otpauth URI, and that URI as a QR PNG.
type TOTPEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
	QR     string `json:"qr"` // data:image/png;base64,…
}

// newTOTPEnrollment mints a fresh secret and its presentation for operator.
func (m *Manager) newTOTPEnrollment(operator string) (TOTPEnrollment, error) {
	raw, err := m.randBytes(totpKeyLen)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	secret := b32.EncodeToString(raw)
	label := url.PathEscape("conductor:" + operator)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", "conductor")
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(totpPeriod))
	uri := "otpauth://totp/" + label + "?" + v.Encode()
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	code.Scale = 6
	return TOTPEnrollment{Secret: secret, URI: uri, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())}, nil
}

// BeginTOTP starts adding an authenticator for a signed-in session. It replaces
// any earlier unconfirmed one for the same session.
func (m *Manager) BeginTOTP(sess Session) (TOTPEnrollment, error) {
	en, err := m.newTOTPEnrollment(sess.Operator)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	m.mu.Lock()
	m.gcCeremonies()
	m.totpNew[sess.ID] = &totpPending{secret: en.Secret, expires: m.now().Add(totpEnrollTTL)}
	m.mu.Unlock()
	return en, nil
}

// ConfirmTOTP activates the session's pending authenticator once code matches
// it. The confirming code's step is spent, so it can't also sign in.
func (m *Manager) ConfirmTOTP(sess Session, code string) error {
	m.mu.Lock()
	p, ok := m.totpNew[sess.ID]
	m.mu.Unlock()
	if !ok || m.now().After(p.expires) {
		return errors.New("no authenticator setup in progress — start again")
	}
	return m.activateTOTP(sess.Operator, p.secret, code, func() {
		m.mu.Lock()
		delete(m.totpNew, sess.ID)
		m.mu.Unlock()
	})
}

// activateTOTP stores secret as operator's authenticator after code matches it.
func (m *Manager) activateTOTP(operator, secret, code string, done func()) error {
	step, ok := matchTOTP(secret, code, m.now())
	if !ok {
		return errors.New("that code doesn't match — check the time on your device and try the next code")
	}
	if err := m.deps.Store.UpdateOperatorSecret(operator, func(o *OperatorSecret) {
		o.TOTPSecret, o.TOTPSet, o.TOTPLastStep, o.TOTPUsed = secret, m.now(), step, []int64{step}
	}); err != nil {
		return err
	}
	if done != nil {
		done()
	}
	m.audit(map[string]any{"event": "dashboard_totp_set", "operator": operator})
	return nil
}

// ClearTOTP removes an operator's authenticator.
func (m *Manager) ClearTOTP(operator string) error {
	if err := m.deps.Store.UpdateOperatorSecret(operator, func(o *OperatorSecret) {
		o.TOTPSecret, o.TOTPSet, o.TOTPLastStep, o.TOTPUsed = "", timeZero, 0, nil
	}); err != nil {
		return err
	}
	m.audit(map[string]any{"event": "dashboard_totp_removed", "operator": operator})
	return nil
}
