package member

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/grant"
	"github.com/NodeSpy/conductor/internal/store"
)

// APPROVALS. An approval is authenticated OUTSIDE the agent: it is an SSH
// signature (the OpenSSH SSHSIG format, `ssh-keygen -Y sign`) over a
// canonical payload naming the member, the proposal and the digest of the
// exact action, made with an operator's private key and verified here
// against the operator's configured public key. It arrives only on the
// operator control socket, never on the agent socket, and there is no verb,
// option or message text that approves anything.
//
// What that buys, honestly: the agent cannot approve its own proposal unless
// it can make the operator's key sign. Once sessions are jailed (#154) the
// key, the ssh-agent socket and the control socket are all outside the jail.
// Until then a member session runs as the operator's uid, so the guarantee is
// exactly as strong as the key's protection — keep the key in an agent that
// confirms each signature (a hardware key, 1Password's agent, `ssh-add -c`).
// docs/wiki/Members.md says so.

// SigNamespace is the SSHSIG namespace approvals are signed in, so a
// signature made for git or anything else can never be replayed here.
const SigNamespace = "conductor-member-approval"

// ApprovalPayload is the exact text an operator signs.
func ApprovalPayload(member, proposal, digest, decision string) []byte {
	return []byte(fmt.Sprintf("conductor member approval v1\nmember: %s\nproposal: %s\ndigest: %s\ndecision: %s\n",
		member, proposal, digest, decision))
}

// Decision is an operator's signed decision on one proposal.
type Decision struct {
	Member    string `json:"member"`
	Proposal  string `json:"proposal"`
	Operator  string `json:"operator"`
	Decision  string `json:"decision"`  // approve | deny
	Signature string `json:"signature"` // armored SSHSIG
	Reason    string `json:"reason,omitempty"`
}

// SignApproval signs a payload with ssh-keygen. key is a private key file,
// or a PUBLIC key file whose private half is in the ssh-agent (ssh-keygen
// then asks the agent — and a confirming agent asks the human).
func SignApproval(ctx context.Context, key string, payload []byte) (string, error) {
	dir, err := os.MkdirTemp("", "conductor-approve-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	msg := filepath.Join(dir, "payload")
	if err := os.WriteFile(msg, payload, 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "sign", "-q", "-f", key, "-n", SigNamespace, msg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin // a passphrase / confirmation prompt reaches the operator
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen sign: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	sig, err := os.ReadFile(msg + ".sig")
	if err != nil {
		return "", err
	}
	return string(sig), nil
}

// VerifyApproval checks that sig is operator's SSHSIG over payload, against
// the operator's configured public key only.
func VerifyApproval(ctx context.Context, operator, pubKey string, payload []byte, sig string) error {
	if strings.TrimSpace(sig) == "" {
		return errors.New("no signature")
	}
	if strings.ContainsAny(operator, " \t\n\",") || operator == "" {
		return errors.New("bad operator name")
	}
	dir, err := os.MkdirTemp("", "conductor-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	allowed := filepath.Join(dir, "allowed_signers")
	line := fmt.Sprintf("%s namespaces=%q %s\n", operator, SigNamespace, strings.TrimSpace(pubKey))
	if err := os.WriteFile(allowed, []byte(line), 0o600); err != nil {
		return err
	}
	sigFile := filepath.Join(dir, "sig")
	if err := os.WriteFile(sigFile, []byte(sig), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "verify", "-f", allowed, "-I", operator, "-n", SigNamespace, "-s", sigFile)
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("signature does not verify for operator %q: %s", operator, strings.TrimSpace(out.String()))
	}
	return nil
}

// Executor carries out approved proposals.
type Executor struct {
	Cfg    func() *config.Config
	Ledger Ledger
	Gate   *Gate
	Grants grant.Store
	// RunVerb is flow.Runner.RunSkillVerb: an approved verb runs through the
	// same chokepoint every agent call does (access, scope, then the gate,
	// which admits it because ctx carries this approval).
	RunVerb func(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (map[string]any, error)
	// Verify is VerifyApproval (tests substitute it).
	Verify func(ctx context.Context, operator, pubKey string, payload []byte, sig string) error
	Now    func() time.Time
	// Notify tells the member (its home inbox, the proposing worker) what was
	// decided. Optional.
	Notify func(member string, p store.Proposal)
	// Audit records every decision. Optional.
	Audit func(entry map[string]any)
	// GrantTTL bounds a minted grant (default 1h).
	GrantTTL time.Duration
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) audit(e map[string]any) {
	if x.Audit != nil {
		e["event"] = "member_approval"
		x.Audit(e)
	}
}

// Decide applies an operator's signed decision. It is the ONLY way a
// proposal leaves pending.
func (x *Executor) Decide(ctx context.Context, d Decision) (store.Proposal, error) {
	cfg := x.Cfg()
	m, ok := cfg.Members[d.Member]
	if !ok {
		return store.Proposal{}, fmt.Errorf("unknown member %q", d.Member)
	}
	if d.Decision != "approve" && d.Decision != "deny" {
		return store.Proposal{}, fmt.Errorf("decision must be approve or deny")
	}
	refuse := func(err error) (store.Proposal, error) {
		x.audit(map[string]any{"member": d.Member, "proposal": d.Proposal, "operator": d.Operator, "decision": d.Decision, "outcome": "refused", "error": err.Error()})
		return store.Proposal{}, err
	}
	allowed := false
	for _, op := range cfg.MemberOperators(d.Member) {
		if op == d.Operator {
			allowed = true
		}
	}
	if !allowed {
		return refuse(fmt.Errorf("%q is not an operator of member %s", d.Operator, d.Member))
	}
	opCfg := cfg.Operators[d.Operator]
	rec := x.Ledger.Member(d.Member)
	p := rec.Proposals[d.Proposal]
	if p == nil {
		return refuse(fmt.Errorf("no proposal %s for member %s", d.Proposal, d.Member))
	}
	verify := x.Verify
	if verify == nil {
		verify = VerifyApproval
	}
	if err := verify(ctx, d.Operator, opCfg.SSHKey, ApprovalPayload(d.Member, p.ID, p.Digest, d.Decision), d.Signature); err != nil {
		return refuse(err)
	}
	switch {
	case p.Status == "shadow":
		return refuse(fmt.Errorf("proposal %s is a shadow proposal (member %s is in shadow mode) — a calibration record, not approvable", p.ID, d.Member))
	case p.Status != "pending":
		return refuse(fmt.Errorf("proposal %s is %s, not pending", p.ID, p.Status))
	case !x.now().Before(p.Expires):
		_ = x.setStatus(d.Member, p.ID, "expired", "", nil)
		return refuse(fmt.Errorf("proposal %s expired at %s", p.ID, p.Expires.Format(time.RFC3339)))
	}
	if m.Autonomy.ShadowMode() {
		return refuse(fmt.Errorf("member %s is in shadow mode; set autonomy.shadow: false to allow approved execution", d.Member))
	}
	if d.Decision == "deny" {
		out, err := x.transition(d, "denied", nil, "")
		x.audit(map[string]any{"member": d.Member, "proposal": p.ID, "operator": d.Operator, "decision": "deny", "outcome": "ok"})
		x.notify(d.Member, out)
		return out, err
	}
	if _, err := x.transition(d, "approved", nil, ""); err != nil {
		return refuse(err)
	}
	x.audit(map[string]any{"member": d.Member, "proposal": p.ID, "operator": d.Operator, "decision": "approve", "outcome": "ok", "uses": p.Uses, "tool": p.Tool})
	var out store.Proposal
	var err error
	switch p.Kind {
	case "verb":
		out, err = x.execVerb(ctx, d, *p)
	case "command":
		out, err = x.mintGrant(ctx, d, *p)
	default:
		err = fmt.Errorf("unknown proposal kind %q", p.Kind)
	}
	x.notify(d.Member, out)
	return out, err
}

func (x *Executor) notify(member string, p store.Proposal) {
	if x.Notify != nil && p.ID != "" {
		x.Notify(member, p)
	}
}

// transition moves a pending proposal to a decided state atomically (the
// re-check inside the transaction is what makes a double approval run once).
func (x *Executor) transition(d Decision, status string, result map[string]any, grantID string) (store.Proposal, error) {
	var out store.Proposal
	err := x.Ledger.UpdateMember(d.Member, func(r *store.MemberRecord) error {
		p := r.Proposals[d.Proposal]
		if p == nil {
			return fmt.Errorf("proposal %s vanished", d.Proposal)
		}
		if p.Status != "pending" {
			return fmt.Errorf("proposal %s is %s, not pending", p.ID, p.Status)
		}
		p.Status, p.Approver, p.Decided, p.Reason = status, d.Operator, x.now(), d.Reason
		out = *p
		return nil
	})
	return out, err
}

func (x *Executor) setStatus(member, id, status, reason string, result map[string]any) error {
	return x.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		p := r.Proposals[id]
		if p == nil {
			return nil
		}
		p.Status = status
		if reason != "" {
			p.Reason = reason
		}
		if result != nil {
			p.Result = result
		}
		return nil
	})
}

func (x *Executor) execVerb(ctx context.Context, d Decision, p store.Proposal) (store.Proposal, error) {
	var id flow.SkillIdentity
	if err := json.Unmarshal(p.Identity, &id); err != nil {
		_ = x.setStatus(d.Member, p.ID, "failed", "stored identity unreadable", nil)
		return p, fmt.Errorf("proposal %s: stored identity: %w", p.ID, err)
	}
	if x.RunVerb == nil {
		return p, errors.New("no verb runner")
	}
	res, err := x.RunVerb(withApproved(ctx, d.Member, p.ID, p.Digest), id, p.Uses, p.Options)
	var out store.Proposal
	uerr := x.Ledger.UpdateMember(d.Member, func(r *store.MemberRecord) error {
		q := r.Proposals[p.ID]
		if q == nil {
			return nil
		}
		if err != nil {
			q.Status, q.Reason = "failed", err.Error()
		} else {
			q.Status, q.Result = "executed", res
			if q.Tier == config.TierCommitMoney {
				r.Spend = append(r.Spend, store.SpendEntry{At: x.now(), Amount: q.Amount, Proposal: q.ID})
			}
		}
		out = *q
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, uerr
}

func (x *Executor) mintGrant(ctx context.Context, d Decision, p store.Proposal) (store.Proposal, error) {
	if x.Grants == nil {
		_ = x.setStatus(d.Member, p.ID, "failed", "no grant store", nil)
		return p, errors.New("no grant store on this daemon")
	}
	ttl := x.GrantTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	var digests []grant.FileDigest
	for path, sum := range p.Files {
		digests = append(digests, grant.FileDigest{Path: path, SHA256: sum})
	}
	g, err := x.Grants.Mint(ctx, grant.GrantSpec{
		AgentID: p.AgentID, Dispatch: p.Dispatch, Tool: p.Tool, Argv: grant.ParseArgv(p.Argv),
		FileDigests: digests, Expiry: x.now().Add(ttl), SingleUse: true,
		ApprovedBy: d.Operator, CaseID: p.CaseID,
	})
	var out store.Proposal
	uerr := x.Ledger.UpdateMember(d.Member, func(r *store.MemberRecord) error {
		q := r.Proposals[p.ID]
		if q == nil {
			return nil
		}
		if err != nil {
			q.Status, q.Reason = "failed", err.Error()
		} else {
			q.Status, q.Grant = "executed", g.ID
			q.Result = map[string]any{"grant": g.ID, "expires": g.Expiry.Format(time.RFC3339)}
		}
		out = *q
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, uerr
}

// ExpireProposals marks pending proposals past their window expired.
func (x *Executor) ExpireProposals(member string) {
	now := x.now()
	_ = x.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		for _, p := range r.Proposals {
			if p.Status == "pending" && !now.Before(p.Expires) {
				p.Status = "expired"
			}
		}
		return nil
	})
}
