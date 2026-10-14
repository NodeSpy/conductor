package member

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

// THE GATE. Every verb a member session calls reaches RunSkillVerb, passes
// the grant's access and resource checks, and then lands here. The gate
// answers one of three things:
//
//	allow    run it now
//	propose  hold it as a proposal; it runs only after an operator approves
//	         it out of band (approve.go) — the agent gets the proposal id back
//	deny     refuse
//
// The decision is a pure function of config (the member's tiers, promotion
// list, shadow flag, spend cap), the jail's state, and the ledger (who asked
// for the case, what has been spent). Nothing the agent says — no option, no
// text — can lift it: an "approved: true" option is just an option, and the
// only thing that turns a proposal into a run is approve.go's verified
// operator signature.

// Requester classes, from least to most trusted for autonomous action. The
// class only ever LOWERS what runs autonomously; approvals need an operator
// signature whoever asked.
const (
	ReqUser     = "user"     // a third party (a chat user who is not an operator)
	ReqEvent    = "event"    // an alert / schedule / webhook with no human requester
	ReqMember   = "member"   // the member's own initiative, or the home conversation
	ReqOperator = "operator" // a configured operator (by name or alias)
)

// Jail reports whether member sessions run inside the agent jail (#154).
// Until the jail lands, NoJail: promotion to autonomous write tiers is
// refused without it.
type Jail interface{ Active() bool }

// NoJail is the jail seam's value before #154 lands.
type NoJail struct{}

// Active implements Jail.
func (NoJail) Active() bool { return false }

// Ledger is the slice of internal/store the member package uses.
type Ledger interface {
	Member(name string) store.MemberRecord
	UpdateMember(name string, fn func(*store.MemberRecord) error) error
	MembersDegraded() bool
}

// Gate classifies and decides member verb calls.
type Gate struct {
	Cfg    func() *config.Config
	Ledger Ledger
	Jail   Jail
	Now    func() time.Time
	// OnProposal is told about every new proposal (notify the operator,
	// queue a message to the home agent). Optional.
	OnProposal func(member string, p store.Proposal)
}

var _ flow.MemberGate = (*Gate)(nil)

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gate) jailed() bool { return g.Jail != nil && g.Jail.Active() }

// approvedKey marks a context as executing one approved proposal.
type approvedKey struct{}

type approvedCall struct {
	member, proposal, digest string
}

// withApproved is how the approval executor (and ONLY it) runs a proposal's
// verb through the same RunSkillVerb chokepoint: the gate admits exactly the
// call whose digest the operator signed, once.
func withApproved(ctx context.Context, member, proposal, digest string) context.Context {
	return context.WithValue(ctx, approvedKey{}, approvedCall{member: member, proposal: proposal, digest: digest})
}

// Classify returns the tier a verb resolves to for a member: the strictest
// tier any pattern admits it under, `propose` when nothing does. step.* and
// handoff.* act only on the caller's own dispatch (its done signal) and are
// always observe.
func Classify(m config.MemberConfig, uses string) string {
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
	return config.MemberTiers[best]
}

// ActionDigest is the canonical digest of a verb action: what an operator
// signs, and what the gate re-checks when the approved call executes.
func ActionDigest(member, kind, uses string, options map[string]any) (string, error) {
	b, err := json.Marshal(map[string]any{"v": 1, "member": member, "kind": kind, "uses": uses, "options": options})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// CheckVerb implements flow.MemberGate.
func (g *Gate) CheckVerb(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (flow.MemberVerdict, error) {
	cfg := g.Cfg()
	m, ok := cfg.Members[id.Member]
	if !ok || m.Disabled {
		return flow.MemberVerdict{}, fmt.Errorf("member %q is not configured", id.Member)
	}
	tier := Classify(m, uses)
	if tier == config.TierNever {
		return flow.MemberVerdict{}, fmt.Errorf("%s is classified never for member %s", uses, id.Member)
	}
	digest, err := ActionDigest(id.Member, "verb", uses, options)
	if err != nil {
		return flow.MemberVerdict{}, fmt.Errorf("digest: %w", err)
	}
	// An approved proposal executing: admit exactly the signed action.
	if ac, ok := ctx.Value(approvedKey{}).(approvedCall); ok {
		if ac.member != id.Member || ac.digest != digest {
			return flow.MemberVerdict{}, errors.New("approved execution does not match the approved action")
		}
		rec := g.Ledger.Member(id.Member)
		p := rec.Proposals[ac.proposal]
		if p == nil || p.Status != "approved" || p.Digest != digest {
			return flow.MemberVerdict{}, errors.New("proposal is not in the approved state")
		}
		if tier == config.TierCommitMoney {
			if err := g.checkSpend(m, rec, p.Amount, true); err != nil {
				return flow.MemberVerdict{}, err
			}
		}
		return flow.MemberVerdict{Allow: true}, nil
	}

	class := g.requesterClass(id)
	switch tier {
	case config.TierObserve:
		return flow.MemberVerdict{Allow: true}, nil
	case config.TierReversible:
		if g.autonomous(m, uses, class) {
			return flow.MemberVerdict{Allow: true}, nil
		}
	case config.TierCommitMoney:
		amt, err := amountOf(m, uses, options)
		if err != nil {
			return flow.MemberVerdict{}, err
		}
		if err := g.checkSpend(m, g.Ledger.Member(id.Member), amt, false); err != nil {
			return flow.MemberVerdict{}, err
		}
		return g.propose(id, m, tier, uses, options, digest, amt)
	}
	return g.propose(id, m, tier, uses, options, digest, 0)
}

// autonomous reports whether a reversible-tier verb runs without approval:
// never in shadow mode, only when promoted, only while jailed, and never on
// a third party's request.
func (g *Gate) autonomous(m config.MemberConfig, uses, class string) bool {
	if m.Autonomy.ShadowMode() {
		return false
	}
	if !flow.MatchVerbPattern(m.Autonomy.Promote, uses) {
		return false
	}
	if !g.jailed() {
		return false
	}
	return class != ReqUser
}

// requesterClass is what the ledger says about who asked for the caller's
// case. A worker's case is bound at launch; a home-session call names no
// case and counts as the member's own.
func (g *Gate) requesterClass(id flow.SkillIdentity) string {
	if id.CaseID == "" {
		if id.MemberRole == config.MemberRoleWorker {
			// A worker with no case binding is an anomaly: least trust.
			return ReqUser
		}
		return ReqMember
	}
	rec := g.Ledger.Member(id.Member)
	c := rec.Cases[id.CaseID]
	if c == nil || c.RequesterClass == "" {
		return ReqUser
	}
	return c.RequesterClass
}

// amountOf reads a commit-money verb's typed amount.
func amountOf(m config.MemberConfig, uses string, options map[string]any) (float64, error) {
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
func (g *Gate) checkSpend(m config.MemberConfig, rec store.MemberRecord, amt float64, atExecution bool) error {
	sc := m.Autonomy.SpendCap
	if sc == nil {
		return errors.New("commit_money without autonomy.spend_cap")
	}
	if g.Ledger.MembersDegraded() {
		return errors.New("the member ledger was unreadable at boot (spend history unknown) — commit_money is refused until an operator restores members.json")
	}
	cur := sc.CurrencyOr()
	if amt > sc.PerAction {
		return fmt.Errorf("amount %.2f %s exceeds the per-action cap %.2f %s", amt, cur, sc.PerAction, cur)
	}
	spent := spentSince(rec, g.now().Add(-sc.PeriodD()))
	if spent+amt > sc.PerPeriod {
		return fmt.Errorf("amount %.2f %s would bring spend in the last %s to %.2f, over the cap %.2f %s", amt, cur, sc.PeriodD(), spent+amt, sc.PerPeriod, cur)
	}
	_ = atExecution
	return nil
}

func spentSince(rec store.MemberRecord, since time.Time) float64 {
	var t float64
	for _, s := range rec.Spend {
		if s.At.After(since) {
			t += s.Amount
		}
	}
	return t
}

// propose records a verb proposal and returns the agent's answer.
func (g *Gate) propose(id flow.SkillIdentity, m config.MemberConfig, tier, uses string, options map[string]any, digest string, amount float64) (flow.MemberVerdict, error) {
	idJSON, err := json.Marshal(id)
	if err != nil {
		return flow.MemberVerdict{}, err
	}
	now := g.now()
	status := "pending"
	if m.Autonomy.ShadowMode() {
		status = "shadow"
	}
	var created store.Proposal
	err = g.Ledger.UpdateMember(id.Member, func(r *store.MemberRecord) error {
		p := &store.Proposal{
			ID: r.NextID("p"), CaseID: id.CaseID, Kind: "verb", Tier: tier, Uses: uses,
			Options: options, Amount: amount, Digest: digest,
			Role: id.MemberRole, Agent: id.Agent, Dispatch: id.Dispatch, Identity: idJSON,
			Status: status, Created: now, Expires: now.Add(m.Autonomy.TTL()),
		}
		if c := r.Cases[id.CaseID]; c != nil {
			p.Requester, p.RequesterClass = c.Requester, c.RequesterClass
			c.Proposals = append(c.Proposals, p.ID)
			c.Updated = now
		} else {
			p.RequesterClass = ReqMember
		}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		created = *p
		return nil
	})
	if err != nil {
		return flow.MemberVerdict{}, fmt.Errorf("record proposal: %w", err)
	}
	if g.OnProposal != nil {
		g.OnProposal(id.Member, created)
	}
	return flow.MemberVerdict{Output: proposalAnswer(created, m)}, nil
}

// proposalAnswer is what the agent sees instead of the verb's output.
func proposalAnswer(p store.Proposal, m config.MemberConfig) map[string]any {
	msg := fmt.Sprintf("Not executed: %s is tier %s for this member, so it is held as proposal %s for operator approval. "+
		"An operator approves it with `conductor member approve <member> %s`; you cannot approve it yourself. "+
		"Tell the operator what it does and why, then continue with other work — `conductor member wait %s` blocks until it is decided.",
		p.Uses, p.Tier, p.ID, p.ID, p.ID)
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
