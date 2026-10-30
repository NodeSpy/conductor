package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SSH sign-in and SSH step-up (dashboard-auth-methods design). The server
// issues a one-line challenge that names the operator, the origin the browser
// is on, the purpose (and, for a step-up, the request digest), a nonce and an
// expiry. The operator signs it with operators.<name>.ssh_key, either pasting
// the armored signature into the page, or — from another device — with
// `conductor dashboard approve-login <code>`, which posts the signature against
// a short login code while the page polls. The server always verifies its own
// stored copy of the text, never one the client sends back.
const (
	// LoginNamespace and StepupNamespace are the SSHSIG namespaces of the two
	// challenge kinds. Both differ from EnrollNamespace and the bot-approval
	// namespace, so a signature made for one can never be replayed as another.
	LoginNamespace  = "conductor-dashboard-login"
	StepupNamespace = "conductor-dashboard-stepup"

	loginChallengePrefix  = "conductor-dashboard-login v1 "
	stepupChallengePrefix = "conductor-dashboard-stepup v1 "

	sshChallengeTTL = 5 * time.Minute
	// Pending sign-in challenges need no session, so they are capped per client
	// (clientKey: one address), per network (clientNetwork: an IPv4 /24, an
	// IPv6 /64) and overall. The network cap is wide (a neighbour needs 86
	// addresses at 3 each to fill it) and only bounds one network's share of
	// the table. Step-up challenges belong to a signed-in session and are
	// capped per session instead, so sign-in traffic can never crowd them out.
	maxSSHPending        = 4096
	maxSSHPendingClient  = 3
	maxSSHPendingNetwork = 256
	maxSSHPendingSession = 5
	maxSSHCodeTries      = 3 // wrong signatures against a code before it burns
	sshKindLogin         = "login"
	sshKindStepup        = "stepup"
	loginCodeAlphabet    = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I
)

// safeOperator is the operator-name shape SSH challenges accept: it appears in
// a shell command the page shows, inside single quotes.
var safeOperator = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// safeOrigin bounds the origin that goes into a challenge (the Host header is
// allow-listed upstream; this keeps the shell command quotable regardless).
var safeOrigin = regexp.MustCompile(`^https?://[a-z0-9.\-\[\]:]{1,255}$`)

// sshChallenge is one in-flight SSH sign-in or step-up.
type sshChallenge struct {
	id        string // the browser's handle (never shown, never derivable from the code)
	code      string // the short login code shown for cross-device approval
	kind      string // sshKindLogin | sshKindStepup
	operator  string
	listener  Listener
	host      string
	transport Transport
	text      string // the exact text to sign
	namespace string
	session   string // step-up: the session that asked
	purpose   Purpose
	digest    string
	summary   string // step-up: what it confirms, bound when it began
	device    string // the requesting browser, shown by approve-login
	addr      string
	client    string // lockout/pending bucket
	network   string // the client's network, for the per-network pending cap
	started   time.Time
	expires   time.Time
	tries     int    // wrong signatures (paste or code)
	approved  string // the verified signature, once approve-login posted it
}

// SSHChallengeView is what the browser gets back from a begin.
type SSHChallengeView struct {
	ID        string `json:"challenge_id"`
	Text      string `json:"challenge"`
	Namespace string `json:"namespace"`
	Code      string `json:"login_code"`
	Expires   string `json:"expires"`
}

// SSHPendingView is what approve-login shows before it signs.
type SSHPendingView struct {
	Kind      string `json:"kind"` // login | stepup
	Operator  string `json:"operator"`
	Challenge string `json:"challenge"`
	Namespace string `json:"namespace"`
	Purpose   string `json:"purpose,omitempty"`
	Device    string `json:"device"`  // the requesting browser, from its User-Agent (the requester controls it)
	Addr      string `json:"addr"`    // the address the request came from
	Started   string `json:"started"` // when the request began
	Expires   string `json:"expires"`
	// A step-up's action: its digest and, when the dashboard knows it, what
	// it is in words.
	Digest  string `json:"digest,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// SSHChallengeText builds the one-line text a challenge asks the operator to
// sign. Exported so the CLI and tests build and parse the same shape.
func SSHChallengeText(kind, operator, origin string, purpose Purpose, digest, nonce string, expires time.Time) string {
	exp := expires.UTC().Format(time.RFC3339)
	if kind == sshKindStepup {
		return fmt.Sprintf("%soperator=%s origin=%s purpose=%s digest=%s nonce=%s expires=%s",
			stepupChallengePrefix, operator, origin, purpose, digest, nonce, exp)
	}
	return fmt.Sprintf("%soperator=%s origin=%s nonce=%s expires=%s", loginChallengePrefix, operator, origin, nonce, exp)
}

// ParsedChallenge is a challenge text split into its fields.
type ParsedChallenge struct {
	Kind      string
	Namespace string
	Fields    map[string]string
}

// ParseSSHChallenge parses a challenge text and names its namespace. It refuses
// anything that isn't a login or step-up challenge, so `conductor dashboard
// sign` can never be pointed at an enrollment or approval payload.
func ParseSSHChallenge(text string) (ParsedChallenge, error) {
	text = strings.TrimSpace(text)
	var p ParsedChallenge
	var rest string
	switch {
	case strings.HasPrefix(text, loginChallengePrefix):
		p.Kind, p.Namespace, rest = sshKindLogin, LoginNamespace, strings.TrimPrefix(text, loginChallengePrefix)
	case strings.HasPrefix(text, stepupChallengePrefix):
		p.Kind, p.Namespace, rest = sshKindStepup, StepupNamespace, strings.TrimPrefix(text, stepupChallengePrefix)
	default:
		return p, errors.New("not a conductor dashboard sign-in or step-up challenge")
	}
	p.Fields = map[string]string{}
	for _, kv := range strings.Fields(rest) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return p, fmt.Errorf("malformed challenge field %q", kv)
		}
		p.Fields[k] = v
	}
	for _, need := range []string{"operator", "origin", "nonce", "expires"} {
		if p.Fields[need] == "" {
			return p, fmt.Errorf("challenge has no %s", need)
		}
	}
	return p, nil
}

// normalizeLoginCode uppercases a typed code and drops separators.
func normalizeLoginCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return strings.ToUpper(r.Replace(strings.TrimSpace(code)))
}

// mintLoginCode returns an 8-character code over a 32-symbol alphabet (40 bits).
func (m *Manager) mintLoginCode() (string, error) {
	b, err := m.randBytes(8)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i, x := range b {
		out[i] = loginCodeAlphabet[int(x)%len(loginCodeAlphabet)]
	}
	return string(out), nil
}

// FormatLoginCode shows a code as "ABCD-EFGH".
func FormatLoginCode(code string) string {
	if len(code) == 8 {
		return code[:4] + "-" + code[4:]
	}
	return code
}

// beginSSH creates a challenge of kind for the request.
func (m *Manager) beginSSH(r *http.Request, kind, operator string, sess Session, purpose Purpose, digest string) (SSHChallengeView, error) {
	if !safeOperator.MatchString(operator) {
		return SSHChallengeView{}, errors.New("that is not a valid operator name")
	}
	info := reqInfoFrom(r)
	origin := requestOrigin(r)
	if !safeOrigin.MatchString(origin) {
		return SSHChallengeView{}, errors.New("this connection's host can't be put in a challenge")
	}
	nonce, err := m.randBytes(16)
	if err != nil {
		return SSHChallengeView{}, err
	}
	idb, err := m.randBytes(32)
	if err != nil {
		return SSHChallengeView{}, err
	}
	code, err := m.mintLoginCode()
	if err != nil {
		return SSHChallengeView{}, err
	}
	ns := LoginNamespace
	if kind == sshKindStepup {
		ns = StepupNamespace
	}
	exp := m.now().Add(sshChallengeTTL)
	summary := ""
	if kind == sshKindStepup {
		if summary = m.describeStepup(sess.ID, operator, purpose, digest); summary == "" {
			return SSHChallengeView{}, ErrNotDescribed
		}
	}
	ch := &sshChallenge{
		id: b64(idb), code: code, kind: kind, operator: operator,
		listener: info.Listener, host: info.Host, transport: info.Transport,
		text:      SSHChallengeText(kind, operator, origin, purpose, digest, b64(nonce), exp),
		summary:   summary,
		namespace: ns, session: sess.ID, purpose: purpose, digest: digest,
		device: deviceLabel(r), addr: r.RemoteAddr, client: clientKey(r), network: clientNetwork(r),
		started: m.now(), expires: exp,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcCeremonies()
	if !m.sshRoomLocked(ch) {
		return SSHChallengeView{}, errors.New("too many SSH challenges waiting — finish or let one expire first")
	}
	if _, dup := m.sshCode[code]; dup {
		return SSHChallengeView{}, errors.New("login code collision — try again")
	}
	m.sshCh[ch.id] = ch
	m.sshCode[code] = ch.id
	return SSHChallengeView{ID: ch.id, Text: ch.text, Namespace: ns, Code: FormatLoginCode(code), Expires: exp.UTC().Format(time.RFC3339)}, nil
}

// sshRoomLocked reports whether ch fits under the pending caps. Caller holds m.mu.
func (m *Manager) sshRoomLocked(ch *sshChallenge) bool {
	if ch.kind == sshKindStepup {
		n := 0
		for _, c := range m.sshCh {
			if c.kind == sshKindStepup && c.session == ch.session {
				n++
			}
		}
		return n < maxSSHPendingSession
	}
	total, client, network := 0, 0, 0
	for _, c := range m.sshCh {
		if c.kind != sshKindLogin {
			continue
		}
		total++
		if c.client == ch.client {
			client++
		}
		if c.network == ch.network {
			network++
		}
	}
	return total < maxSSHPending && client < maxSSHPendingClient && network < maxSSHPendingNetwork
}

// BeginSSHLogin issues a sign-in challenge for operator on this connection. It
// does not reveal whether the operator exists: an unknown operator's challenge
// simply never verifies.
func (m *Manager) BeginSSHLogin(r *http.Request, operator string) (SSHChallengeView, error) {
	if !m.policy().SignIn(MethodSSH) {
		return SSHChallengeView{}, errors.New("SSH sign-in is not enabled (dashboard.auth.methods)")
	}
	return m.beginSSH(r, sshKindLogin, operator, Session{}, "", "")
}

// BeginSSHStepup issues a step-up challenge over {purpose, digest} for the
// signed-in session.
func (m *Manager) BeginSSHStepup(r *http.Request, sess Session, purpose Purpose, digest string) (SSHChallengeView, error) {
	if !m.policy().StepupAllowed(MethodSSH) {
		return SSHChallengeView{}, errors.New("SSH step-up is not enabled (dashboard.auth.stepup)")
	}
	if digest == "" || purpose == "" {
		return SSHChallengeView{}, errors.New("step-up needs a purpose and a request digest")
	}
	return m.beginSSH(r, sshKindStepup, sess.Operator, sess, purpose, digest)
}

// ErrNotDescribed refuses an SSH step-up for an action the dashboard never
// described to this session: approve-login would have nothing true to show.
var ErrNotDescribed = errors.New("the dashboard has no description of this action for your session, so it can't be confirmed by SSH — reload the page and try again")

// lookupSSH returns a snapshot of the challenge id for the request, checking it
// is live, of kind, and bound to this listener, host and transport (a
// challenge begun on one origin can't be finished on another).
func (m *Manager) lookupSSH(r *http.Request, id, kind string, sess Session) (sshChallenge, error) {
	m.mu.Lock()
	ch, ok := m.sshCh[id]
	var snap sshChallenge
	if ok {
		snap = *ch
	}
	m.mu.Unlock()
	if !ok || snap.kind != kind {
		return sshChallenge{}, errors.New("unknown or used SSH challenge")
	}
	if m.now().After(snap.expires) {
		m.dropSSH(id)
		return sshChallenge{}, errors.New("the SSH challenge expired — start again")
	}
	info := reqInfoFrom(r)
	if snap.listener != info.Listener || snap.host != info.Host || snap.transport != info.Transport {
		return sshChallenge{}, errors.New("the SSH challenge belongs to a different address")
	}
	if kind == sshKindStepup && (snap.session != sess.ID || snap.operator != sess.Operator) {
		return sshChallenge{}, errors.New("the SSH challenge is for a different session")
	}
	return snap, nil
}

// verifySSHChallenge checks sig over the stored challenge text with the
// operator's configured key, in the challenge's namespace.
//
// An unknown operator (or one without a key) is verified against a throwaway
// key nobody holds, with the same error, so neither the answer nor the time it
// takes says whether the operator exists.
func (m *Manager) verifySSHChallenge(ctx context.Context, ch sshChallenge, sig string) error {
	pub, ok := m.operatorPubKey(ch.operator)
	if !ok {
		pub = unheldSSHKey()
	}
	if err := m.deps.VerifySSHNS(ctx, ch.namespace, ch.operator, pub, []byte(ch.text), sig); err != nil || !ok {
		return errSSHVerify
	}
	return nil
}

var errSSHVerify = errors.New("the signature does not verify for this operator, challenge and namespace")

// unheldSSHKey is an ed25519 public key whose private half was discarded at
// birth: no signature verifies against it.
var unheldSSHKey = sync.OnceValue(func() string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		pub = make(ed25519.PublicKey, ed25519.PublicKeySize)
	}
	field := func(b []byte) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...)
	}
	blob := append(field([]byte("ssh-ed25519")), field(pub)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
})

// consumeSSH removes a challenge if it is still present, reporting whether this
// caller won it (two racing finishes can both verify; only one spends it).
func (m *Manager) consumeSSH(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.sshCh[id]
	if !ok {
		return false
	}
	delete(m.sshCh, id)
	m.unindexCode(ch)
	return true
}

// unindexCode drops a challenge's login code if it still points at it (a spent
// code may since have been reissued to another challenge). Caller holds m.mu.
func (m *Manager) unindexCode(ch *sshChallenge) {
	if m.sshCode[ch.code] == ch.id {
		delete(m.sshCode, ch.code)
	}
}

func (m *Manager) dropSSH(id string) { m.consumeSSH(id) }

// failSSH records a wrong signature against a challenge; the third burns it.
func (m *Manager) failSSH(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.sshCh[id]; ok {
		ch.tries++
		if ch.tries >= maxSSHCodeTries {
			delete(m.sshCh, id)
			m.unindexCode(ch)
		}
	}
}

// FinishSSHLogin verifies a pasted signature over a sign-in challenge and spends
// it, returning the operator to sign in.
func (m *Manager) FinishSSHLogin(ctx context.Context, r *http.Request, id, sig string) (string, error) {
	ch, err := m.lookupSSH(r, id, sshKindLogin, Session{})
	if err != nil {
		return "", err
	}
	if err := m.verifySSHChallenge(ctx, ch, sig); err != nil {
		m.failSSH(id)
		return "", err
	}
	if !m.consumeSSH(id) {
		return "", errors.New("unknown or used SSH challenge")
	}
	return ch.operator, nil
}

// PollSSHLogin reports whether approve-login has signed a sign-in challenge; if
// so it spends it and returns the operator. done=false means keep waiting.
func (m *Manager) PollSSHLogin(r *http.Request, id string) (operator string, done bool, err error) {
	ch, err := m.lookupSSH(r, id, sshKindLogin, Session{})
	if err != nil {
		return "", false, err
	}
	if ch.approved == "" {
		return "", false, nil
	}
	if !m.consumeSSH(id) {
		return "", false, errors.New("unknown or used SSH challenge")
	}
	return ch.operator, true, nil
}

// FinishSSHStepup verifies a pasted signature over a step-up challenge and mints
// the step-up token.
func (m *Manager) FinishSSHStepup(ctx context.Context, r *http.Request, sess Session, id, sig string) (string, error) {
	ch, err := m.lookupSSH(r, id, sshKindStepup, sess)
	if err != nil {
		return "", err
	}
	if err := m.verifySSHChallenge(ctx, ch, sig); err != nil {
		m.failSSH(id)
		return "", err
	}
	if !m.consumeSSH(id) {
		return "", errors.New("unknown or used SSH challenge")
	}
	return m.mintSSHStepup(sess, ch, sig)
}

// PollSSHStepup is the step-up counterpart of PollSSHLogin.
func (m *Manager) PollSSHStepup(r *http.Request, sess Session, id string) (token string, done bool, err error) {
	ch, err := m.lookupSSH(r, id, sshKindStepup, sess)
	if err != nil {
		return "", false, err
	}
	if ch.approved == "" {
		return "", false, nil
	}
	if !m.consumeSSH(id) {
		return "", false, errors.New("unknown or used SSH challenge")
	}
	tok, err := m.mintSSHStepup(sess, ch, ch.approved)
	return tok, err == nil, err
}

func (m *Manager) mintSSHStepup(sess Session, ch sshChallenge, sig string) (string, error) {
	return m.mintStepup(sess, ch.purpose, ch.digest, StepupEvidence{
		Method: MethodSSH, SSHNamespace: ch.namespace, SSHPayload: ch.text, SSHSignature: sig,
	})
}

// codeChallenge returns a snapshot of the live challenge behind a login code.
// A code is only found on the listener, host and transport it was issued on,
// like the challenge id (lookupSSH): the most exposed listener can't serve as a
// code oracle for the others.
func (m *Manager) codeChallenge(r *http.Request, code string) (sshChallenge, bool) {
	code = normalizeLoginCode(code)
	info := reqInfoFrom(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.sshCode[code]
	if !ok {
		return sshChallenge{}, false
	}
	ch, ok := m.sshCh[id]
	if !ok || m.now().After(ch.expires) || ch.approved != "" {
		return sshChallenge{}, false
	}
	if ch.listener != info.Listener || ch.host != info.Host || ch.transport != info.Transport {
		return sshChallenge{}, false
	}
	return *ch, true
}

// describeStepup is what a step-up by session over (purpose, digest)
// confirms, in words, for approve-login.
func (m *Manager) describeStepup(session, operator string, purpose Purpose, digest string) string {
	if purpose == PurposeCredential {
		for _, a := range credentialActions {
			if CredentialDigest(operator, a.action) == digest {
				return a.text
			}
		}
		return ""
	}
	if m.deps.DescribeStepup != nil && session != "" {
		return m.deps.DescribeStepup(session, purpose, digest)
	}
	return ""
}

// credentialActions are the credential changes a step-up can confirm.
var credentialActions = []struct{ action, text string }{
	{"password.set", "set or change your dashboard password"},
	{"password.remove", "remove your dashboard password"},
	{"totp.set", "set up or replace your authenticator app"},
	{"totp.remove", "remove your authenticator app"},
	{"passkey.add", "add a passkey"},
}

// PendingSSH describes the request behind a login code for approve-login to
// show before it signs. It needs no session: the code only reveals what the
// operator is about to approve, and approving needs their signature.
func (m *Manager) PendingSSH(r *http.Request, code string) (SSHPendingView, error) {
	ch, ok := m.codeChallenge(r, code)
	if !ok {
		return SSHPendingView{}, errors.New("no pending request with that code (it may have expired or been used)")
	}
	v := SSHPendingView{
		Kind: ch.kind, Operator: ch.operator, Challenge: ch.text, Namespace: ch.namespace,
		Purpose: string(ch.purpose), Device: ch.device, Addr: ch.addr,
		Started: ch.started.UTC().Format(time.RFC3339),
		Expires: ch.expires.UTC().Format(time.RFC3339),
	}
	if ch.kind == sshKindStepup {
		v.Digest = ch.digest
		v.Summary = ch.summary
	}
	return v, nil
}

// ApproveSSH verifies a signature posted against a login code (from
// approve-login on another device) and marks the challenge approved, so the
// waiting page's next poll receives its session or step-up token. The code is
// single use: a success spends it; three wrong signatures burn it.
func (m *Manager) ApproveSSH(ctx context.Context, r *http.Request, code, sig string) error {
	ch, ok := m.codeChallenge(r, code)
	if !ok {
		return errors.New("no pending request with that code (it may have expired or been used)")
	}
	if err := m.verifySSHChallenge(ctx, ch, sig); err != nil {
		m.failSSH(ch.id)
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	live, ok := m.sshCh[ch.id]
	if !ok || live.approved != "" {
		return errors.New("no pending request with that code (it may have expired or been used)")
	}
	live.approved = sig
	m.unindexCode(live) // the code is spent; the browser still holds the id
	m.audit(map[string]any{"event": "dashboard_ssh_approved", "operator": live.operator, "kind": live.kind, "device": live.device})
	return nil
}
