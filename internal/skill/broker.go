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
	"github.com/NodeSpy/conductor/internal/memory"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

const (
	// HandoffSessionTTL is the session-token life for an INTERACTIVE hand-off
	// dispatch. Such an agent is held by the reaper (never culled) and waits on
	// a human who may not get to it for hours or days, so a 2h dispatch window
	// leaves its conductor creds (how it auths `conductor call …` to the broker)
	// dead by the time the human engages it. A week is plenty for a real
	// hand-off and still bounds a scraped token. Bot sessions use the same
	// durable token, so the constant lives in config, where a bot's
	// rotation age is validated against it.
	HandoffSessionTTL = config.HandoffSessionTTL
	// SessionTTL bounds a dispatch token's life — roughly a dispatch's
	// lifetime (typical wait_timeouts are minutes; 2h leaves room for slow
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
	UID       uint32 // SO_PEERCRED uid; a uid-bound session authorizes by this
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

// ProcessAnchor identifies the ROOT of a bound session's process tree: the
// one process that execs or forks the sandboxed agent (a sandboxed launch's
// own child process, or the `conductor sandbox exec` client) — not the
// agent's exact pid, which cannot work when the token is also read by
// short-lived `conductor call` subprocesses and an in-sandbox MCP tool
// server, each its own process (rt2 #12). Start is the root's own start
// time (platform units; comparable only to itself), re-read fresh at every
// Authorize call so a pid the kernel later reuses for an unrelated process
// never matches — the same PID-reuse guard Peer.matches gives an exact-peer
// binding, extended to a whole tree. Start == 0 means "could not be read at
// bind time" and is treated like Peer.matches treats it: skip the
// freshness check rather than refuse outright, matching this codebase's
// existing convention for platforms/moments where it is unavailable.
type ProcessAnchor struct {
	PID   int
	Start uint64
}

// Empty reports a has no process to anchor to (the zero value): binding is
// refused rather than treated as a wildcard root.
func (a ProcessAnchor) Empty() bool { return a.PID <= 0 }

// AnchorForPID builds a ProcessAnchor for a pid the caller already knows is
// the right root (a control connection's SO_PEERCRED pid) by reading its
// current start time. Used where the root is known at mint time (`conductor
// sandbox exec`'s sandbox-policy request); the in-daemon launch path instead
// mints pending and calls BindRoot once its child exists (prepareLaunch runs
// before exec.Cmd.Start, so the root does not exist yet at mint time).
func AnchorForPID(pid int) ProcessAnchor {
	_, start, _ := procAncestor(pid)
	return ProcessAnchor{PID: pid, Start: start}
}

// maxAncestryDepth bounds processInTree's walk up a peer's ppid chain —
// generous for any real sandbox (daemon child → sandbox wrapper → agent →
// `conductor call`, at most a handful of hops) while guaranteeing
// termination against a pathological /proc.
const maxAncestryDepth = 64

// processInTree reports whether pid is root itself or a live descendant of
// it, walking the kernel's own parent-pid chain (never anything the peer
// asserts). Finding root's pid in the chain is not enough on its own — pids
// are reused — so root's start time is re-read at the moment of the match
// and compared against what was recorded when the session was bound; a
// mismatch means the real root is gone and something else now holds that
// pid, and the match is refused.
func processInTree(pid int, root ProcessAnchor) bool {
	if root.Empty() || pid <= 0 {
		return false
	}
	cur := pid
	for depth := 0; depth < maxAncestryDepth; depth++ {
		ppid, start, ok := procAncestor(cur)
		if !ok {
			return false
		}
		if cur == root.PID {
			if root.Start == 0 || start == 0 {
				return true // unverifiable on this platform/moment — PID match is the best signal available
			}
			return start == root.Start
		}
		if ppid <= 1 || ppid == cur {
			return false // reached init (or a self-parented root entry) without finding root
		}
		cur = ppid
	}
	return false
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
	// TargetTrusted carries the originating dispatch's target provenance (see
	// core.Trigger.TargetTrusted). False — the zero value — means the scope
	// layer withholds own-target trust from this grant.
	TargetTrusted bool
	// Dispatch is the daemon-assigned id of this dispatch (see
	// dispatch.Request.DispatchID): the anchor a live tool's reconstructed
	// trigger is confined to when the target itself cannot be trusted.
	Dispatch string
	// Bot / BotRole / CaseID bind the session to a bot (home or
	// worker) and, for a worker, its case: what the bot autonomy gate
	// decides against. Minted daemon-side from config and the ledger.
	Bot     string
	BotRole string
	CaseID  string
	// Context is the originating trigger's context, held daemon-side for the
	// session's lifetime. It exists for RESOURCE SCOPING: the trigger's
	// declared target scope (Sem) reads its facts to decide which channel
	// (repo, …) this dispatch may address with no explicit grant. It is never handed to the
	// agent and never leaves the daemon — the agent's own context comes from
	// the prompt, not from here.
	Context map[string]any
	// Sem is the originating trigger's declared semantics, held daemon-side
	// with Context: its target scope is what the dispatch may address with
	// no explicit grant (the channel a chat event came from).
	Sem *sdk.EventSemantics
	// Requester is who asked for the work this session does: "operator"
	// (the operator's own request — a local `conductor run`, or an agent
	// they started in a directory of their own), "event" (a trigger), or a
	// member's case class ("member", "user"). "" is unknown and trusted
	// least. It only ever narrows: the browser acts logged in only for the
	// operator.
	Requester string
	// Sandbox binds a session minted for a sandboxed launch to that
	// launch's identity and workspace (agentsandbox): what per-identity
	// surfaces — the browser — key on. nil: resolved from the dispatch.
	Sandbox *SandboxBinding
	// MemoryScopes are the step's memory `scope:` grant lists rendered over
	// its facts at dispatch (memory.RenderGrantScopes): verb pattern → the
	// entries it admits. The facts exist only at dispatch, so the session
	// carries the rendered list rather than re-rendering later.
	MemoryScopes map[string][]memory.ScopePattern
}

// Requester classes (Identity.Requester).
const (
	RequesterOperator = "operator"
	RequesterEvent    = "event"
)

// SandboxBinding is the sandbox a session's agent runs in.
type SandboxBinding struct {
	Identity  string   `json:"identity"`
	Workspace string   `json:"workspace"`
	Egress    []string `json:"egress,omitempty"`
	// Deny are carve-outs inside the workspace (read-only, hidden).
	Deny []string `json:"deny,omitempty"`
}

type session struct {
	id      Identity
	expires time.Time
	// peer is the process that claimed the session (MCP path). When Valid,
	// every token-authorized call must come from the same live process — a
	// copied token is useless from anywhere else.
	peer Peer
	// uidBound marks a session with NO process anchor at all (today: only
	// MintHandoffSession's durable bots/hand-offs, which must keep working
	// across a daemon restart and may wait on a human for days — minting
	// against a single process tree that may not even exist yet buys
	// nothing there). It authorizes by same-uid (boundUID). On a transport
	// with no peer creds (remote HTTP), the endpoint authorizes by the
	// bearer token alone.
	uidBound bool
	boundUID uint32
	// treeBound marks a session bound to a process TREE (MintSession /
	// MintPendingSession, rt2 #12): root is the one process that execs or
	// forks the sandboxed agent, and Authorize accepts only root itself or a
	// live descendant of it (processInTree), never "any process at this
	// uid" — the fix for a token leaked from one sandbox being accepted from
	// any other same-uid sandbox or host process. pendingRoot marks a
	// session minted before root's pid is known (the in-daemon launch path
	// mints into the child's env before exec.Cmd.Start creates it): such a
	// session authorizes NO ONE, not even root once it exists, until
	// BindRoot names it — a token read out of its env before that point
	// would still find every call refused.
	treeBound   bool
	pendingRoot bool
	root        ProcessAnchor
	// verbCalls counts executed verbs against the session's cap.
	verbCalls int
	// durable marks an interactive hand-off session (MintHandoffSession): it
	// is persisted so it survives a daemon restart, and each authorized use
	// slides its expiry forward by HandoffSessionTTL.
	durable bool
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
	// restored holds durable sessions loaded from disk at boot, keyed by the
	// token's hash (the raw token is never written). A restored session moves
	// into sessions the first time its token is presented.
	restored map[string]session
	// persistPath is where durable sessions are saved; "" = in-memory only.
	persistPath string
	log         func(string, ...any)
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
		restored: map[string]session{},
	}
}

// SetLog installs a daemon log sink for broker events worth an operator's
// eye (a rejected session token). nil = silent.
func (b *Broker) SetLog(f func(string, ...any)) {
	b.mu.Lock()
	b.log = f
	b.mu.Unlock()
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

// MintSession registers one dispatch's identity and returns a REUSABLE
// session token bound to root's PROCESS TREE (rt2 #12), for a launch whose
// root process is already known (`conductor sandbox exec`'s sandbox-policy
// request: root is the pid on the other end of that control connection, via
// AnchorForPID). Unlike MintClaim (the MCP path: one-shot code →
// exact-process-bound token held by a long-lived subprocess), this token is
// delivered in the agent's env (CONDUCTOR_SKILL_TOKEN) and presented by many
// short-lived `conductor call` processes, and by an in-sandbox MCP tool
// server, across the run — all descendants of root — so Authorize walks the
// presenting peer's ancestry back to root rather than accepting "any process
// at this uid", which is what let a token leaked from one sandbox be
// replayed from any other same-uid sandbox or host process. root.Empty()
// refuses outright: a caller with no process to anchor to must use
// MintPendingSession (root not known yet), MintSessionUID (no anchor exists
// at all — an explicit, logged exception), or MintHandoffSession (a durable
// hand-off/bot session). Called by the daemon at dispatch time only. Short
// TTL + verb-allowlist scope + audit bound the exposure of a token scraped
// from env.
func (b *Broker) MintSession(id Identity, root ProcessAnchor, ttl time.Duration) (string, error) {
	if root.Empty() {
		return "", fmt.Errorf("skill: mint session: no process to anchor to (use MintPendingSession or MintHandoffSession)")
	}
	if ttl <= 0 {
		ttl = SessionTTL
	}
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.mu.Lock()
	b.sessions[tok] = session{id: id, expires: b.now().Add(ttl), treeBound: true, root: root}
	b.mu.Unlock()
	return tok, nil
}

// MintPendingSession is MintSession for a launch whose root process does not
// exist yet: the in-daemon controller path (prepareAgentSandbox, and the
// unsandboxed local CLI launch) mints the token into the child's env BEFORE
// starting it, because exec.Cmd's environment must be complete before
// Start() — the root's pid is only assigned once Start() returns. The
// session authorizes NO ONE until BindRoot names the real root; call it
// immediately after Start() succeeds.
func (b *Broker) MintPendingSession(id Identity, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = SessionTTL
	}
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.mu.Lock()
	b.sessions[tok] = session{id: id, expires: b.now().Add(ttl), treeBound: true, pendingRoot: true}
	b.mu.Unlock()
	return tok, nil
}

// BindRoot anchors a pending session (MintPendingSession) to its now-known
// root process — the pid the daemon just started (exec.Cmd.Process.Pid) that
// will exec or fork the sandboxed agent. Binding reads root's own start time
// itself (never trusts a caller-supplied one) so the anchor is exactly what
// a future Authorize call will re-verify against. Binding is one-shot: an
// unknown/expired token, or a token that is not a pending tree binding (already
// bound, or never tree-bound at all), is refused and the session's
// authorization state is unchanged.
func (b *Broker) BindRoot(token string, rootPID int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[token]
	if !ok {
		return fmt.Errorf("skill: bind root: unknown or expired session token")
	}
	if !s.treeBound || !s.pendingRoot {
		return fmt.Errorf("skill: bind root: session is not a pending process-tree binding")
	}
	s.root = AnchorForPID(rootPID)
	s.pendingRoot = false
	b.sessions[token] = s
	return nil
}

// MintSessionUID is the OLD MintSession behavior, kept for the token paths
// that genuinely have no process to anchor to (rt2 #12's fix narrows these
// deliberately rather than leaving every caller on it by default — see
// SkillEnvFor and its callers for which paths these are and why):
// authorizes any process running as the daemon's own uid, not a process
// tree. Non-durable (SessionTTL-scoped), unlike MintHandoffSession.
func (b *Broker) MintSessionUID(id Identity, uid uint32, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = SessionTTL
	}
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.mu.Lock()
	b.sessions[tok] = session{id: id, expires: b.now().Add(ttl), uidBound: true, boundUID: uid}
	b.mu.Unlock()
	return tok, nil
}

// MintHandoffSession is MintSession for an INTERACTIVE hand-off: the agent
// waits on a human for hours or days, across daemon restarts (every
// auto-update is one), and its token lives in an env conductor cannot
// rewrite. So the session is durable — persisted (by token hash) and
// reloaded at boot — and sliding: each authorized use pushes its expiry to
// HandoffSessionTTL from now. An abandoned hand-off's token still dies
// HandoffSessionTTL after its last use; releasing the hand-off revokes it
// at once (RevokeDispatch).
//
// This is the one deliberate EXCEPTION to process-tree binding (rt2 #12):
// uid-bound, not root-bound. A hand-off's process tree is not a stable
// anchor across the daemon restarts this session is specifically built to
// survive, and a bot's "home" session in particular has no single launch
// process at all — it is reattached by whichever worker picks up the case.
// The session still authorizes only within the daemon's own uid, same as
// every skill credential did before this fix; it is the one path where that
// is the best anchor available, not a shortcut taken for convenience.
func (b *Broker) MintHandoffSession(id Identity, uid uint32) (string, error) {
	tok, err := b.randomID(32)
	if err != nil {
		return "", fmt.Errorf("skill: mint session token: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[tok] = session{id: id, expires: b.now().Add(HandoffSessionTTL), uidBound: true, boundUID: uid, durable: true}
	b.saveLocked()
	return tok, nil
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
	if !ok {
		// A durable session restored from disk: promote it on first use.
		if r, rok := b.restored[tokenHash(token)]; rok {
			delete(b.restored, tokenHash(token))
			b.sessions[token] = r
			s, ok = r, true
		}
	}
	if !ok || b.now().After(s.expires) {
		reason := "unknown session token"
		if ok {
			reason = "expired session token"
		}
		b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "", reason)
		if b.log != nil {
			if ok {
				b.log("skill: rejected an expired session token (agent %q, %s#%d)", s.id.Agent, s.id.Repo, s.id.Number)
			} else {
				b.log("skill: rejected an unknown session token — minted by an earlier daemon, revoked, or never issued")
			}
		}
		return session{}, fmt.Errorf("skill: unknown or expired session token")
	}
	if s.durable {
		// Sliding expiry. Persist only when it moved materially, so a busy
		// hand-off doesn't rewrite the file on every call.
		next := b.now().Add(HandoffSessionTTL)
		if next.Sub(s.expires) >= durableSaveSlack {
			s.expires = next
			b.sessions[token] = s
			b.saveLocked()
		}
	}
	if s.treeBound {
		// Process-tree path (rt2 #12): authorize root itself or a live
		// descendant of it — never merely "any process at this uid", which
		// let a token leaked from one sandbox be replayed from any other
		// same-uid sandbox or host process. Fails closed in every direction:
		// pending (root not bound yet), no peer credentials on this
		// transport/platform, and "found root's pid but its start time has
		// moved on" (the real root exited and something else now holds that
		// pid) are all refusals, never a fall-through to trust.
		if s.pendingRoot {
			b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
				"session token not yet bound to its launch process")
			return session{}, fmt.Errorf("skill: session token not yet bound to its launch process")
		}
		if !peer.Valid {
			b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
				"no peer credentials available to verify the session's process-tree binding")
			return session{}, fmt.Errorf("skill: session token requires peer credentials this transport does not provide")
		}
		if !processInTree(peer.PID, s.root) {
			b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
				"session token presented by a process outside its launch's process tree")
			return session{}, fmt.Errorf("skill: session token presented by a process outside its launch's process tree")
		}
		return s, nil
	}
	if s.uidBound {
		// CLI/remote path with NO process anchor at all (MintHandoffSession:
		// a durable bot/hand-off session that must keep working across a
		// daemon restart and may wait on a human for days — see its doc
		// comment). Authorizes any process running as the bound uid. A local
		// socket carries peer creds; a peer with a different uid is refused. A
		// transport with no peer creds (remote HTTP over TLS) authorizes by the
		// bearer token alone — that endpoint has no kernel identity to check.
		if peer.Valid && peer.UID != s.boundUID {
			b.auditLocked("deny", s.id.Agent, s.id.Repo, "", "",
				"session token presented by a different uid than the dispatch's")
			return session{}, fmt.Errorf("skill: session token presented by a different uid than the dispatch's")
		}
		return s, nil
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
	if s.durable {
		b.saveLocked()
	}
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
	dropped := false
	for tok, s := range b.sessions {
		if now.After(s.expires) {
			delete(b.sessions, tok)
			dropped = dropped || s.durable
		}
	}
	for h, s := range b.restored {
		if now.After(s.expires) {
			delete(b.restored, h)
			dropped = true
		}
	}
	if dropped {
		b.saveLocked()
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
