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
	// SessionTTL bounds a dispatch token's life. Grants are the short-lived
	// artifact; the token only names WHO is asking, and dies with the daemon
	// anyway (the table is in-memory).
	SessionTTL = 24 * time.Hour
	// GrantTTL is the issued-grant lifetime: long enough to redeem right
	// away, short enough that a grant id that leaks into a log or transcript
	// is dead by the time anyone reads it.
	GrantTTL = 60 * time.Second
)

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

// RegisterSession mints an unguessable token for one dispatch and records the
// real identity it stands for. Called by the daemon at dispatch time only.
func (b *Broker) RegisterSession(id Identity) (string, error) {
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.mu.Lock()
	b.sessions[tok] = session{id: id, expires: b.now().Add(SessionTTL)}
	b.mu.Unlock()
	return tok, nil
}

// Issue requests a grant for one named secret. Deny-by-default: the session
// must be live, the profile's skill.secrets_via must be "broker", and the
// name must appear in skill.allow_secrets (exact match). The grant is scoped
// to the issuing session, single-use, and expires after GrantTTL.
func (b *Broker) Issue(token, name string) (grantID string, expires time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[token]
	if !ok || b.now().After(s.expires) {
		b.auditLocked("deny", "", "", name, "", "unknown or expired session token")
		return "", time.Time{}, fmt.Errorf("secret_issue: unknown or expired session token")
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
func (b *Broker) Redeem(token, grantID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
