package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// THE GATE. Every verb a bot session calls reaches RunSkillVerb, passes
// the grant's access and resource checks, and then lands here. The gate
// answers one of three things:
//
//	allow    run it now
//	propose  hold it as a proposal; it runs only after an operator approves
//	         it out of band (approve.go) — the agent gets the proposal id back
//	deny     refuse
//
// The decision is a pure function of config (the bot's tiers, promotion
// list, shadow flag, spend cap), the sandbox's state, and the ledger (who asked
// for the case, what has been spent). Nothing the agent says — no option, no
// text — can lift it: an "approved: true" option is just an option, and the
// only thing that turns a proposal into a run is approve.go's verified
// operator signature.

// Requester classes, from least to most trusted for autonomous action. The
// class only ever LOWERS what runs autonomously; approvals need an operator
// signature whoever asked.
const (
	ReqUser      = "user"      // a third party (a chat user who is not an operator)
	ReqPeer      = "peer"      // another bot's message (requester "bot:<name>")
	ReqProactive = "proactive" // a proactive sweep, or a case one of its findings opened
	ReqEvent     = "event"     // an alert / schedule / webhook with no human requester
	ReqBot       = "bot"       // the bot's own initiative, or the home conversation
	ReqOperator  = "operator"  // a configured operator (by name or alias)
)

// PeerRequester is how a peer bot is named as a requester.
func PeerRequester(bot string) string { return "bot:" + bot }

// Sandbox is conductor's agent sandbox as bots see it. Until it is on
// main, NoSandbox: bot sessions run unconfined, promotion to autonomous
// write tiers is refused, and the home session launches on paseo's plain
// provider.
//
// Once it lands, the switch is a few lines in cmd/conductor (sandboxSeam):
// SandboxFuncs wired to "is the sandbox enforcing" and to the name of the
// conductor-owned paseo provider entry `conductor paseo setup` created for a
// base provider (claude → claude-sandboxed).
type Sandbox interface {
	// Active reports whether agent launches are sandboxed — the gate's
	// precondition for promotion.
	Active() bool
	// PaseoProvider names the sandboxed paseo provider entry to launch a
	// home session on in place of base ("" base = paseo's default), and
	// whether one exists.
	PaseoProvider(base string) (string, bool)
}

// NoSandbox is the seam's value before the sandbox is on main.
type NoSandbox struct{}

// Active implements Sandbox.
func (NoSandbox) Active() bool { return false }

// PaseoProvider implements Sandbox.
func (NoSandbox) PaseoProvider(string) (string, bool) { return "", false }

// SandboxFuncs adapts the sandbox's functions to the seam.
type SandboxFuncs struct {
	IsActive func() bool
	Provider func(base string) (string, bool)
}

// Active implements Sandbox.
func (s SandboxFuncs) Active() bool { return s.IsActive != nil && s.IsActive() }

// PaseoProvider implements Sandbox.
func (s SandboxFuncs) PaseoProvider(base string) (string, bool) {
	if s.Provider == nil {
		return "", false
	}
	return s.Provider(base)
}

// Ledger is the slice of internal/store the bot package uses.
type Ledger interface {
	Bot(name string) store.BotRecord
	UpdateBot(name string, fn func(*store.BotRecord) error) error
	BotsDegraded() bool
}

// Gate classifies and decides bot verb calls.
type Gate struct {
	Cfg     func() *config.Config
	Ledger  Ledger
	Sandbox Sandbox
	Now     func() time.Time
	// OnProposal is told about every new proposal (notify the operator,
	// queue a message to the home agent). Optional.
	OnProposal func(bot string, p store.Proposal)
}

var _ flow.BotGate = (*Gate)(nil)

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gate) sandboxed() bool { return g.Sandbox != nil && g.Sandbox.Active() }

// approvedKey marks a context as executing one approved proposal.
type approvedKey struct{}

type approvedCall struct {
	bot, proposal, digest string
}

// withApproved is how the approval executor (and ONLY it) runs a proposal's
// verb through the same RunSkillVerb chokepoint: the gate admits exactly the
// call whose digest the operator signed, once.
func withApproved(ctx context.Context, bot, proposal, digest string) context.Context {
	return context.WithValue(ctx, approvedKey{}, approvedCall{bot: bot, proposal: proposal, digest: digest})
}

// ApprovedProposal reports the approved proposal a context executes (the
// executor's withApproved), for a verb that acts on it elsewhere (a bot's
// purchase approves the browser's purchase record).
func ApprovedProposal(ctx context.Context) (bot, proposal string, ok bool) {
	ac, ok := ctx.Value(approvedKey{}).(approvedCall)
	return ac.bot, ac.proposal, ok
}

// RequesterClass is who the gate holds responsible for a bot session's
// calls right now: the requester class (operator, bot, event, proactive,
// user, peer) and the case. The browser's provenance reads it at every
// action.
func (g *Gate) RequesterClass(id flow.SkillIdentity) (class, caseID string) {
	cl := g.caller(id)
	return cl.class, cl.caseID
}

// Classify returns the tier a verb resolves to for a bot: the strictest
// tier any pattern admits it under, `propose` when nothing does. step.* and
// handoff.* act only on the caller's own dispatch (its done signal) and are
// always observe.
func Classify(m config.BotConfig, uses string) string {
	if strings.HasPrefix(uses, "step.") || strings.HasPrefix(uses, "handoff.") {
		return config.TierObserve
	}
	best := -1
	for tier, pats := range m.Autonomy.Tiers {
		if flow.MatchVerbPattern(pats, uses) && config.TierRank(tier) > best {
			best = config.TierRank(tier)
		}
	}
	if best < 0 {
		return config.TierPropose
	}
	return config.BotTiers[best]
}

// ActionDigest is the canonical digest of a verb action: what an operator
// signs, and what the gate re-checks when the approved call executes.
func ActionDigest(bot, kind, uses string, options map[string]any) (string, error) {
	b, err := json.Marshal(map[string]any{"v": 1, "bot": bot, "kind": kind, "uses": uses, "options": options})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// CheckVerb implements flow.BotGate.
func (g *Gate) CheckVerb(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (flow.BotVerdict, error) {
	cfg := g.Cfg()
	m, ok := cfg.Bots[id.Bot]
	if !ok || m.Disabled {
		return flow.BotVerdict{}, fmt.Errorf("bot %q is not configured", id.Bot)
	}
	tier := Classify(m, uses)
	if tier == config.TierNever {
		return flow.BotVerdict{}, fmt.Errorf("%s is classified never for bot %s", uses, id.Bot)
	}
	digest, err := ActionDigest(id.Bot, "verb", uses, options)
	if err != nil {
		return flow.BotVerdict{}, fmt.Errorf("digest: %w", err)
	}
	// An approved proposal executing: admit exactly the signed action.
	if ac, ok := ctx.Value(approvedKey{}).(approvedCall); ok {
		if ac.bot != id.Bot || ac.digest != digest {
			return flow.BotVerdict{}, errors.New("approved execution does not match the approved action")
		}
		rec := g.Ledger.Bot(id.Bot)
		p := rec.Proposals[ac.proposal]
		if p == nil || p.Status != "approved" || p.Digest != digest {
			return flow.BotVerdict{}, errors.New("proposal is not in the approved state")
		}
		if tier == config.TierCommitMoney {
			if err := g.checkSpend(m, rec, p.Amount, true); err != nil {
				return flow.BotVerdict{}, err
			}
		}
		return flow.BotVerdict{Allow: true}, nil
	}

	cl := g.caller(id)
	// A child's ceiling: a parent may have narrowed its verbs. The bot's
	// own policy already admitted the call; this only ever refuses more.
	if w := cl.worker; w != nil && len(w.Verbs) > 0 && !selfService(uses) && !flow.MatchVerbPattern(w.Verbs, uses) {
		return flow.BotVerdict{}, fmt.Errorf("%s is outside the verbs worker %s was given (%s)", uses, w.ID, strings.Join(w.Verbs, ", "))
	}
	switch tier {
	case config.TierObserve:
		return flow.BotVerdict{Allow: true}, nil
	case config.TierReversible:
		if !cl.readOnly() && g.autonomous(m, uses, cl.class) {
			return flow.BotVerdict{Allow: true}, nil
		}
	case config.TierCommitMoney:
		amt, err := amountOf(m, uses, options)
		if err != nil {
			return flow.BotVerdict{}, err
		}
		if err := g.checkSpend(m, g.Ledger.Bot(id.Bot), amt, false); err != nil {
			return flow.BotVerdict{}, err
		}
		if ok, err := g.unattended(id, m, uses, options, amt, cl); err != nil {
			return flow.BotVerdict{}, err
		} else if ok {
			return flow.BotVerdict{Allow: true}, nil
		}
		return g.propose(id, m, tier, uses, options, digest, amt)
	}
	return g.propose(id, m, tier, uses, options, digest, 0)
}

// autonomous reports whether a reversible-tier verb runs without approval:
// never in shadow mode, only when promoted, only while sandboxed, and only
// for an event's, the bot's own or an operator's request — never a third
// party's, a peer bot's, or a proactive sweep's (a sweep is read-only:
// everything above observe becomes a proposal).
func (g *Gate) autonomous(m config.BotConfig, uses, class string) bool {
	if m.Autonomy.ShadowMode() {
		return false
	}
	if !flow.MatchVerbPattern(m.Autonomy.Promote, uses) {
		return false
	}
	if !g.sandboxed() {
		return false
	}
	return autonomyClass[class]
}

// unattendedClass are the requesters a commit_money verb may run
// unattended for: the operator's own request, or the bot's own work with no
// third party's case open. Never an event, a peer, a sweep or a third party.
var unattendedClass = map[string]bool{ReqBot: true, ReqOperator: true}

// UnattendedTag marks an unattended spend in the bot ledger.
const UnattendedTag = "unattended"

// unattended reports whether a commit_money call runs without approval —
// only when the operator opted the verb in (autonomy.unattended: never the
// default), the bot is out of shadow mode and sandboxed, the requester is
// the operator or the bot itself, the charge is not recurring, the merchant
// is one the operator named, and the amount fits the unattended cap (its
// own spends) and the spend cap (every spend). The spend is recorded in the
// same ledger transaction that re-checks both caps, so two calls cannot
// jointly overshoot.
func (g *Gate) unattended(id flow.SkillIdentity, m config.BotConfig, uses string, options map[string]any, amt float64, cl callerInfo) (bool, error) {
	var u config.Unattended
	found := false
	for pat, x := range m.Autonomy.Unattended {
		if flow.MatchVerbPattern([]string{pat}, uses) {
			u, found = x, true
			break
		}
	}
	if !found || m.Autonomy.ShadowMode() || !g.sandboxed() || !unattendedClass[cl.class] || cl.readOnly() {
		return false, nil
	}
	if rec, _ := options["recurring"].(bool); rec {
		return false, nil
	}
	merchant, _ := options[u.MerchantOptionOr()].(string)
	merchant = strings.TrimSuffix(strings.TrimSpace(merchant), "/")
	listed := false
	for _, mo := range u.Merchants {
		if strings.EqualFold(strings.TrimSuffix(mo, "/"), merchant) && merchant != "" {
			listed = true
		}
	}
	if !listed {
		return false, nil
	}
	sc := m.Autonomy.SpendCap
	if sc == nil || g.Ledger.BotsDegraded() {
		return false, nil
	}
	allowed := false
	err := g.Ledger.UpdateBot(id.Bot, func(r *store.BotRecord) error {
		now := g.now()
		if u.SpendCap.Check(amt, now, func(since time.Time) float64 {
			var t float64
			for _, s := range r.Spend {
				if s.Proposal == UnattendedTag && s.At.After(since) {
					t += s.Amount
				}
			}
			return t
		}) != nil {
			return nil // over the unattended cap: an approval decides it
		}
		if err := sc.Check(amt, now, func(since time.Time) float64 { return spentSince(*r, since) }); err != nil {
			return err
		}
		r.Spend = append(r.Spend, store.SpendEntry{At: now, Amount: amt, Proposal: UnattendedTag})
		allowed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
}

// autonomyClass is the allowlist of requester classes a promoted verb may
// run unapproved for. A class missing here (an unknown one included) never
// runs anything above observe without an operator's signature.
var autonomyClass = map[string]bool{ReqEvent: true, ReqBot: true, ReqOperator: true}

// requesterRank orders requester classes by trust (least first).
var requesterRank = map[string]int{ReqUser: 0, ReqPeer: 0, ReqProactive: 1, ReqEvent: 2, ReqBot: 3, ReqOperator: 4}

// selfService verbs act only on the caller's own dispatch.
func selfService(uses string) bool {
	return strings.HasPrefix(uses, "step.") || strings.HasPrefix(uses, "handoff.")
}

// callerInfo is who the gate holds responsible for a call.
type callerInfo struct {
	class, who, caseID string
	worker             *store.Worker // the calling worker's record (a child)
}

func (c callerInfo) readOnly() bool { return c.worker != nil && c.worker.ReadOnly }

// caller resolves a call's provenance.
//
// A WORKER (a child) carries the provenance recorded when it was spawned:
// the least trusted of its case's requester and its parent's own trust at
// that moment. So a parent acting on a third party's request — or one
// talked into something by a prompt injection — cannot launder an action
// through a child: the child is gated as that requester. A worker with no
// ledger record is an anomaly and gets the least trust.
func (g *Gate) caller(id flow.SkillIdentity) callerInfo {
	if id.BotRole == config.BotRoleWorker {
		rec := g.Ledger.Bot(id.Bot)
		w := workerByDispatch(rec, id.Dispatch)
		if w == nil || w.RequesterClass == "" {
			return callerInfo{class: ReqUser, worker: w}
		}
		ci := callerInfo{class: w.RequesterClass, who: w.Requester, worker: w}
		if c := rec.Cases[w.CaseID]; c != nil {
			ci.caseID = c.ID
		}
		return ci
	}
	class, who, caseID := g.requester(id)
	return callerInfo{class: class, who: who, caseID: caseID}
}

func workerByDispatch(rec store.BotRecord, dispatchID string) *store.Worker {
	if dispatchID == "" {
		return nil
	}
	for _, w := range rec.Workers {
		if w.Dispatch == dispatchID {
			return w
		}
	}
	return nil
}

// requester is the HOME session's attribution. A home call names no case —
// it acts on a conversation that mixes the operator, events and third
// parties — so it is attributed to the LEAST-TRUSTED requester among the
// bot's open cases: while a third party's request is open, nothing the
// home session does runs on more trust than that third party's. No open
// case: the bot's own initiative.
func (g *Gate) requester(id flow.SkillIdentity) (class, who, caseID string) {
	rec := g.Ledger.Bot(id.Bot)
	return homeAttribution(&rec)
}

// homeAttribution is the least-trusted requester among a bot's open
// cases (ties: the lowest case id); the bot itself when none is open. An
// unknown class ranks as a third party.
func homeAttribution(rec *store.BotRecord) (class, who, caseID string) {
	class = ReqBot
	for _, c := range rec.Cases {
		if c.Status != "open" {
			continue
		}
		r, ok := requesterRank[c.RequesterClass]
		if !ok {
			r = 0
		}
		if r < requesterRank[class] || r == requesterRank[class] && caseID != "" && c.ID < caseID {
			class, who, caseID = c.RequesterClass, c.Requester, c.ID
			if !ok {
				class = ReqUser
			}
		}
	}
	return class, who, caseID
}

// lessTrusted picks the less trusted of two (class, requester) pairs; on a
// tie, the first — the case's own requester, the originating one.
func lessTrusted(aClass, aWho, bClass, bWho string) (string, string) {
	ra, ok := requesterRank[aClass]
	if !ok {
		aClass, ra = ReqUser, 0
	}
	rb, ok := requesterRank[bClass]
	if !ok {
		bClass, rb = ReqUser, 0
	}
	if rb < ra {
		return bClass, bWho
	}
	return aClass, aWho
}

// amountOf reads a commit-money verb's typed amount.
func amountOf(m config.BotConfig, uses string, options map[string]any) (float64, error) {
	opt := ""
	for pat, o := range m.Autonomy.Amounts {
		if flow.MatchVerbPattern([]string{pat}, uses) {
			opt = o
			break
		}
	}
	if opt == "" {
		return 0, fmt.Errorf("%s is commit_money but autonomy.amounts names no amount option for it", uses)
	}
	v, ok := options[opt]
	if !ok {
		return 0, fmt.Errorf("%s: amount option %q is required", uses, opt)
	}
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		x, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s: amount %q is not a number", uses, opt)
		}
		f = x
	default:
		return 0, fmt.Errorf("%s: amount %q must be a number, got %T", uses, opt, v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, fmt.Errorf("%s: amount %q must be a positive number", uses, opt)
	}
	return f, nil
}

// checkSpend enforces the hard cap. atExecution re-checks the period total
// at the moment of spending, so two approvals cannot jointly overshoot it.
func (g *Gate) checkSpend(m config.BotConfig, rec store.BotRecord, amt float64, atExecution bool) error {
	sc := m.Autonomy.SpendCap
	if sc == nil {
		return errors.New("commit_money without autonomy.spend_cap")
	}
	if g.Ledger.BotsDegraded() {
		return errors.New("the bot ledger was unreadable at boot (spend history unknown) — commit_money is refused until an operator restores bots.json")
	}
	if err := sc.Check(amt, g.now(), func(since time.Time) float64 { return spentSince(rec, since) }); err != nil {
		return err
	}
	_ = atExecution
	return nil
}

func spentSince(rec store.BotRecord, since time.Time) float64 {
	var t float64
	for _, s := range rec.Spend {
		if s.At.After(since) {
			t += s.Amount
		}
	}
	return t
}

// propose records a verb proposal and returns the agent's answer.
func (g *Gate) propose(id flow.SkillIdentity, m config.BotConfig, tier, uses string, options map[string]any, digest string, amount float64) (flow.BotVerdict, error) {
	idJSON, err := json.Marshal(id)
	if err != nil {
		return flow.BotVerdict{}, err
	}
	cl := g.caller(id)
	class, who, caseID := cl.class, cl.who, cl.caseID
	now := g.now()
	status := "pending"
	if m.Autonomy.ShadowMode() {
		status = "shadow"
	}
	var created store.Proposal
	err = g.Ledger.UpdateBot(id.Bot, func(r *store.BotRecord) error {
		p := &store.Proposal{
			ID: r.NextID("p"), Kind: "verb", Tier: tier, Uses: uses,
			Options: options, Amount: amount, Digest: digest,
			Role: id.BotRole, Agent: id.Agent, Dispatch: id.Dispatch, Identity: idJSON,
			Status: status, Created: now, Expires: now.Add(m.Autonomy.TTL()),
		}
		p.RequesterClass = class
		p.Requester = who
		p.Team, p.Approvers = string(m.Team), g.Cfg().BotOperators(id.Bot)
		if cl.worker != nil {
			p.Worker = cl.worker.ID
		}
		if c := r.Cases[caseID]; c != nil {
			p.CaseID = caseID
			c.Proposals = append(c.Proposals, p.ID)
			c.Updated = now
		}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		created = *p
		return nil
	})
	if err != nil {
		return flow.BotVerdict{}, fmt.Errorf("record proposal: %w", err)
	}
	if g.OnProposal != nil {
		g.OnProposal(id.Bot, created)
	}
	return flow.BotVerdict{Output: proposalAnswer(created, m)}, nil
}

// proposalAnswer is what the agent sees instead of the verb's output.
func proposalAnswer(p store.Proposal, m config.BotConfig) map[string]any {
	who := "An operator"
	if len(p.Approvers) > 0 {
		who = "An operator (" + strings.Join(p.Approvers, ", ") + ")"
		if p.Kind == KindKnowledge && p.Team != "" {
			who = "An operator of team " + p.Team + " (" + strings.Join(p.Approvers, ", ") + ")"
		}
	}
	msg := fmt.Sprintf("Not executed: %s is tier %s for this bot, so it is held as proposal %s for operator approval. "+
		"%s approves it with `conductor bot approve <bot> %s`; you cannot approve it yourself. "+
		"Tell the operator what it does and why, then continue with other work — `conductor bot wait %s` blocks until it is decided.",
		p.Uses, p.Tier, p.ID, who, p.ID, p.ID)
	if p.Kind == KindKnowledge {
		msg = fmt.Sprintf("Not landed yet: your promotion is held as proposal %s. %s approves it with `conductor bot approve <bot> %s`; you cannot approve it yourself. "+
			"Until then it is in your memory only. `conductor bot wait %s` blocks until it is decided.", p.ID, who, p.ID, p.ID)
		if p.Reason != "" {
			msg += " (" + p.Reason + ")"
		}
	}
	if p.Status == "shadow" {
		msg = fmt.Sprintf("Not executed (shadow mode): %s is tier %s; recorded as shadow proposal %s — what you WOULD do. "+
			"Shadow proposals are a calibration record and cannot be approved. Write up your reasoning and move on.",
			p.Uses, p.Tier, p.ID)
	}
	return map[string]any{
		"executed": false, "proposal": p.ID, "status": p.Status, "tier": p.Tier,
		"expires": p.Expires.Format(time.RFC3339), "message": msg,
	}
}
