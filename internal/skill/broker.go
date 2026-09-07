// Package skill implements the agent-facing conductor-skill surface (#36
// §12). This file is the secret broker: the minimized last resort for a
// dispatched agent that must run a raw tool needing a credential conductor
// would otherwise never hand out. The default path stays "act through
// conductor" (verbs as tools) — when that is not enough, the broker issues a
// grant for ONE named secret, single-use, short-TTL, and fully audited
// (issue, use, expiry).
//
// Identity is bound server-side. At dispatch time the daemon mints an
// unguessable session token, records token → (real dispatched profile,
// dispatch target, that profile's skill: policy) here, and bakes the token
// into the agent's MCP tool argv. Every broker call authorizes by token
// alone — client-asserted fields (like the memory tool's Source flags) are
// never consulted — so an agent, or any other same-user process that can
// reach the socket, cannot claim another profile's policy.
package skill

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

const (
	// SessionTTL bounds a dispatch token's life — roughly a dispatch's
	// lifetime (typical wait_timeouts are minutes; 2h leaves slack for slow
	// runs). A long-lived affinity session whose token ages out simply loses
	// its skill tools; it never gets a stale identity. The table is
	// in-memory, so tokens also die with the daemon.
	SessionTTL = 2 * time.Hour
	// ClaimTTL bounds the one-shot claim code's life: minted at dispatch,
	// exchanged by the tool subprocess at startup. Long enough for a slow
	// runtime launch, short enough that a code scraped from a process's
	// environment later is dead.
	ClaimTTL = 2 * time.Minute
	// GrantTTL is the issued-grant lifetime: long enough to redeem right
	// away, short enough that a grant id that leaks into a log or transcript
	// is dead by the time anyone reads it.
	GrantTTL = 60 * time.Second
	// DefaultVerbCallCap bounds verb executions per session when the profile
	// sets no skill.max_calls — analogous to agent_authored.limits, so a
	// session can't hammer verbs unbounded within its TTL.
	DefaultVerbCallCap = 256
)

// Peer identifies the process on the other end of the tool socket (Linux
// SO_PEERCRED + /proc start time). The zero value means "unknown" — a
// platform without peer credentials; binding is skipped there.
type Peer struct {
	PID       int
	StartTime uint64 // /proc/<pid>/stat field 22; 0 when unreadable
	Valid     bool
}

// matches reports whether two peer identities are the same live process:
// same PID and, when both sides read one, the same start time (PID reuse
// protection).
func (p Peer) matches(q Peer) bool {
	if p.PID != q.PID {
		return false
	}
	if p.StartTime != 0 && q.StartTime != 0 && p.StartTime != q.StartTime {
		return false
	}
	return true
}

// Identity is the REAL dispatch a session token stands for, captured by the
// daemon at dispatch time (never asserted by the client).
type Identity struct {
	Agent   string // the dispatched profile's name
	Repo    string
	Trigger string
	Number  int
	// Policy is the profile's skill: block at dispatch time. The zero value
	// denies everything (secrets_via defaults to none).
	Policy config.SkillPolicy
}

type session struct {
	id      Identity
	expires time.Time
	// peer is the process that claimed the session. When Valid, every
	// token-authorized call must come from the same live process — a copied
	// token is useless from anywhere else.
	peer Peer
	// verbCalls counts executed verbs against the session's cap.
	verbCalls int
}

// pendingClaim is a minted-but-unclaimed session: the one-shot code the
// daemon bakes into the tool subprocess's env at dispatch time.
type pendingClaim struct {
	id      Identity
	expires time.Time
}

type grant struct {
	token   string // the session token that issued it (redeem is scoped to it)
	secret  string // the named secret
	value   string // resolved value, held daemon-side until redeemed/expired
	agent   string // for audit
	repo    string
	expires time.Time
	used    bool
}

// Broker issues and redeems secret grants. All fields are set at construction;
// the zero value denies everything.
type Broker struct {
	lookup func(name string) (string, bool) // named `secrets:` value lookup
	audit  func(map[string]any)             // audit sink (store.Audit); nil = none
	now    func() time.Time                 // injectable clock (tests)
	rand   io.Reader                        // token entropy (tests may pin it)

	mu       sync.Mutex
	sessions map[string]session
	claims   map[string]pendingClaim
	grants   map[string]*grant
}

// NewBroker builds a broker over a named-secret lookup and an audit sink.
func NewBroker(lookup func(string) (string, bool), audit func(map[string]any)) *Broker {
	return &Broker{
		lookup:   lookup,
		audit:    audit,
		now:      time.Now,
		rand:     rand.Reader,
		sessions: map[string]session{},
		claims:   map[string]pendingClaim{},
		grants:   map[string]*grant{},
	}
}

// active is the daemon-wide broker the dispatch path (controller) reads to
// mint session tokens, mirroring memory.SetToolCommand's wiring style.
var active atomic.Pointer[Broker]

// SetActive installs the boot-built broker (nil clears it).
func SetActive(b *Broker) { active.Store(b) }

// Active returns the daemon's broker, or nil when no profile enables skill.
func Active() *Broker { return active.Load() }

// MintClaim registers one dispatch's identity and returns the ONE-SHOT claim
// code the daemon puts in the tool subprocess's environment. The code is not
// a session token: it must be exchanged (ClaimSession) within ClaimTTL,
// exactly once — a code scraped from /proc/<pid>/environ after startup is
// already dead, and the real token never appears on argv or in env at all.
// Called by the daemon at dispatch time only.
func (b *Broker) MintClaim(id Identity) (string, error) {
	code, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint claim code: %w", err)
	}
	b.mu.Lock()
	b.claims[code] = pendingClaim{id: id, expires: b.now().Add(ClaimTTL)}
	b.mu.Unlock()
	return code, nil
}

// ClaimSession exchanges a claim code for the real session token, exactly
// once, binding the session to the CLAIMING process (its socket peer
// credentials) — from then on every token-authorized call must come from
// that same live process. peer.Valid=false (no peer credentials on this
// platform) claims an unbound session.
func (b *Broker) ClaimSession(code string, peer Peer) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.claims[code]
	if !ok {
		b.auditLocked("deny", "", "", "", "", "unknown or already-claimed claim code")
		return "", fmt.Errorf("token_claim: unknown or already-claimed claim code")
	}
	delete(b.claims, code) // single-use, spent even when expired
	if b.now().After(c.expires) {
		b.auditLocked("deny", c.id.Agent, c.id.Repo, "", "", "claim code expired unclaimed")
		return "", fmt.Errorf("token_claim: claim code expired")
	}
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.sessions[tok] = session{id: c.id, expires: b.now().Add(SessionTTL), peer: peer}
	return tok, nil
}

// Authorize resolves a session token to the real dispatch identity it stands
// for — the authorization primitive every skill surface (broker ops, verb
// tools) shares. Unknown and expired tokens fail; so does a valid token
// presented by a DIFFERENT process than the one that claimed the session.
func (b *Broker) Authorize(token string, peer Peer) (Identity, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.authorizeLocked(token, peer)
	if err != nil {
		return Identity{}, err
	}
	return s.id, nil
}

// authorizeLocked is Authorize's body; callers hold b.mu.
func (b *Broker) authorizeLocked(token string, peer Peer) (session, error) {
	s, ok := b.sessions[token]
	if !ok || b.now().After(s.expires) {
		return session{}, fmt.Errorf("skill: unknown or expired session token")
	}
	if s.peer.Valid && (!peer.Valid || !s.peer.matches(peer)) {
		b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
			"session token presented by a different process than the one that claimed it")
		return session{}, fmt.Errorf("skill: session token presented by a different process than the one that claimed it")
	}
	return s, nil
}

// ChargeVerbCall counts one verb execution against the session's cap
// (skill.max_calls, default DefaultVerbCallCap) — called before dispatch, so
// a capped-out session executes nothing further. Refusals are audited.
func (b *Broker) ChargeVerbCall(token string, peer Peer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.authorizeLocked(token, peer)
	if err != nil {
		return err
	}
	limit := s.id.Policy.MaxCalls
	if limit <= 0 {
		limit = DefaultVerbCallCap
	}
	if s.verbCalls >= limit {
		b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
			fmt.Sprintf("session verb-call cap reached (%d; skill.max_calls)", limit))
		return fmt.Errorf("skill: session verb-call cap reached (%d) — raise skill.max_calls if this profile legitimately needs more", limit)
	}
	s.verbCalls++
	b.sessions[token] = s
	return nil
}

// Issue requests a grant for one named secret. Deny-by-default: the session
// must be live, the profile's skill.secrets_via must be "broker", and the
// name must appear in skill.allow_secrets (exact match). The grant is scoped
// to the issuing session, single-use, and expires after GrantTTL.
func (b *Broker) Issue(token, name string, peer Peer) (grantID string, expires time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, aerr := b.authorizeLocked(token, peer)
	if aerr != nil {
		b.auditLocked("deny", "", "", name, "", aerr.Error())
		return "", time.Time{}, fmt.Errorf("secret_issue: %s", aerr)
	}
	deny := func(reason string) (string, time.Time, error) {
		b.auditLocked("deny", s.id.Agent, s.id.Repo, name, "", reason)
		return "", time.Time{}, fmt.Errorf("secret_issue: %s", reason)
	}
	if s.id.Policy.SecretsVia != "broker" {
		return deny("this profile's skill.secrets_via does not allow the broker")
	}
	allowed := false
	for _, n := range s.id.Policy.AllowSecrets {
		if n == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return deny(fmt.Sprintf("secret %q is not in this profile's skill.allow_secrets", name))
	}
	v, ok := b.lookupValue(name)
	if !ok {
		return deny(fmt.Sprintf("secret %q is not configured (or did not resolve) on this daemon", name))
	}
	id, err := b.randomID(16)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("secret_issue: mint grant: %w", err)
	}
	id = "grant-" + id
	exp := b.now().Add(GrantTTL)
	b.grants[id] = &grant{
		token: token, secret: name, value: v,
		agent: s.id.Agent, repo: s.id.Repo, expires: exp,
	}
	b.auditLocked("issue", s.id.Agent, s.id.Repo, name, id, "")
	return id, exp, nil
}

// Redeem exchanges a grant for its secret value — exactly once, within the
// TTL, and only from the session that issued it. The value is dropped from
// the table on success; every failure is audited with the reason.
func (b *Broker) Redeem(token, grantID string, peer Peer) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, aerr := b.authorizeLocked(token, peer); aerr != nil {
		b.auditLocked("deny", "", "", "", grantID, aerr.Error())
		return "", fmt.Errorf("secret_redeem: %s", aerr)
	}
	g, ok := b.grants[grantID]
	if !ok || g.token != token {
		// A wrong-session redeem reads the same as an unknown grant: the
		// caller learns nothing about grants it didn't issue.
		b.auditLocked("deny", "", "", "", grantID, "unknown grant")
		return "", fmt.Errorf("secret_redeem: unknown grant")
	}
	if g.used {
		b.auditLocked("deny", g.agent, g.repo, g.secret, grantID, "grant already used (grants are single-use)")
		return "", fmt.Errorf("secret_redeem: grant already used (grants are single-use)")
	}
	if b.now().After(g.expires) {
		b.auditLocked("expire", g.agent, g.repo, g.secret, grantID, "grant expired before redemption")
		delete(b.grants, grantID)
		return "", fmt.Errorf("secret_redeem: grant expired")
	}
	v := g.value
	g.value = "" // the daemon holds the value no longer than it must
	g.used = true
	b.auditLocked("use", g.agent, g.repo, g.secret, grantID, "")
	return v, nil
}

// Sweep drops expired grants and sessions, auditing the expiry of any grant
// that was issued but never redeemed. The daemon runs it on a ticker; tests
// call it directly against an injected clock.
func (b *Broker) Sweep() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for id, g := range b.grants {
		if now.After(g.expires) {
			if !g.used {
				b.auditLocked("expire", g.agent, g.repo, g.secret, id, "grant expired unredeemed")
			}
			delete(b.grants, id)
		}
	}
	for tok, s := range b.sessions {
		if now.After(s.expires) {
			delete(b.sessions, tok)
		}
	}
	for code, c := range b.claims {
		if now.After(c.expires) {
			delete(b.claims, code)
		}
	}
}

// SweepLoop runs Sweep on a cadence until ctx ends.
func (b *Broker) SweepLoop(ctx interface{ Done() <-chan struct{} }, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Sweep()
		}
	}
}

func (b *Broker) lookupValue(name string) (string, bool) {
	if b.lookup == nil {
		return "", false
	}
	v, ok := b.lookup(name)
	return v, ok && v != ""
}

// auditLocked writes one broker audit entry. The secret VALUE never appears
// here — only the name, the grant id (dead within GrantTTL), and the real
// dispatch identity. Callers hold b.mu.
func (b *Broker) auditLocked(action, agent, repo, secret, grantID, reason string) {
	if b.audit == nil {
		return
	}
	e := map[string]any{"event": "secret_broker", "action": action}
	if agent != "" {
		e["agent"] = agent
	}
	if repo != "" {
		e["repo"] = repo
	}
	if secret != "" {
		e["secret"] = secret
	}
	if grantID != "" {
		e["grant"] = grantID
	}
	if reason != "" {
		e["reason"] = reason
	}
	b.audit(e)
}

func (b *Broker) randomID(n int) (string, error) {
	buf := make([]byte, n)
	r := b.rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
