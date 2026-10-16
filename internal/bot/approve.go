package bot

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
	"github.com/NodeSpy/conductor/internal/store"
)

// APPROVALS. An approval is authenticated OUTSIDE the agent: it is an SSH
// signature (the OpenSSH SSHSIG format, `ssh-keygen -Y sign`) over a
// canonical payload naming the bot, the proposal and the digest of the
// exact action, made with an operator's private key and verified here
// against the operator's configured public key. It arrives only on the
// operator control socket, never on the agent socket, and there is no verb,
// option or message text that approves anything.
//
// What that buys, honestly: the agent cannot approve its own proposal unless
// it can make the operator's key sign. Once sessions are sandboxed the key,
// the ssh-agent socket and the control socket are all outside the sandbox.
// Until then a bot session runs as the operator's uid, so the guarantee is
// exactly as strong as the key's protection — keep the key in an agent that
// confirms each signature (a hardware key, 1Password's agent, `ssh-add -c`).
// docs/wiki/Bots.md says so.

// SigNamespace is the SSHSIG namespace approvals are signed in, so a
// signature made for git or anything else can never be replayed here.
const SigNamespace = "conductor-bot-approval"

// ApprovalPayload is the exact text an operator signs. edit is the final
// content of an approve-with-changes (a knowledge promotion), reason a
// denial's (or approval's) reason: both are covered by their SHA-256, so
// neither can be swapped after signing ("-" when absent).
func ApprovalPayload(bot, proposal, digest, decision, edit, reason string) []byte {
	h := func(s string) string {
		if s == "" {
			return "-"
		}
		return sha256Hex(s)
	}
	return []byte(fmt.Sprintf("conductor bot approval v1\nbot: %s\nproposal: %s\ndigest: %s\ndecision: %s\nedit-sha256: %s\nreason-sha256: %s\n",
		bot, proposal, digest, decision, h(edit), h(reason)))
}

// Decision is an operator's signed decision on one proposal.
type Decision struct {
	Bot       string `json:"bot"`
	Proposal  string `json:"proposal"`
	Operator  string `json:"operator"`
	Decision  string `json:"decision"`  // approve | deny
	Signature string `json:"signature"` // armored SSHSIG
	Reason    string `json:"reason,omitempty"`
	// Edit is approve-with-changes: the final content the operator lands
	// instead of the proposed text (knowledge promotions only).
	Edit string `json:"edit,omitempty"`
}

// MaxDecisionReason bounds a decision's reason (it reaches the bot).
const MaxDecisionReason = 4000

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
	// Terraform applies an approved terraform proposal (nil: refused).
	Terraform *Terraform
	// Knowledge lands an approved knowledge promotion in its team's repo
	// (Service.LandApproved; nil: refused).
	Knowledge func(ctx context.Context, bot string, p store.Proposal) (string, error)
	// RunVerb is flow.Runner.RunSkillVerb: an approved verb runs through the
	// same chokepoint every agent call does (access, scope, then the gate,
	// which admits it because ctx carries this approval).
	RunVerb func(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (map[string]any, error)
	// Verify is VerifyApproval (tests substitute it).
	Verify func(ctx context.Context, operator, pubKey string, payload []byte, sig string) error
	Now    func() time.Time
	// Notify tells the bot (its home inbox, the proposing worker) what was
	// decided. Optional.
	Notify func(bot string, p store.Proposal)
	// Audit records every decision. Optional.
	Audit func(entry map[string]any)
	// Background runs a long execution (a terraform apply) off the
	// decision's own call; nil = `go fn()`. Tests run it inline.
	Background func(fn func())
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) audit(e map[string]any) {
	if x.Audit != nil {
		e["event"] = "bot_approval"
		x.Audit(e)
	}
}

// Decide applies an operator's signed decision. It is the ONLY way a
// proposal leaves pending.
func (x *Executor) Decide(ctx context.Context, d Decision) (store.Proposal, error) {
	cfg := x.Cfg()
	m, ok := cfg.Bots[d.Bot]
	if !ok {
		return store.Proposal{}, fmt.Errorf("unknown bot %q", d.Bot)
	}
	if d.Decision != "approve" && d.Decision != "deny" {
		return store.Proposal{}, fmt.Errorf("decision must be approve or deny")
	}
	refuse := func(err error) (store.Proposal, error) {
		x.audit(map[string]any{"bot": d.Bot, "proposal": d.Proposal, "operator": d.Operator, "decision": d.Decision, "outcome": "refused", "error": err.Error()})
		return store.Proposal{}, err
	}
	rec := x.Ledger.Bot(d.Bot)
	p := rec.Proposals[d.Proposal]
	if p == nil {
		return refuse(fmt.Errorf("no proposal %s for bot %s", d.Proposal, d.Bot))
	}
	if len(d.Reason) > MaxDecisionReason {
		return refuse(fmt.Errorf("reason too long (%d bytes, max %d)", len(d.Reason), MaxDecisionReason))
	}
	if d.Edit != "" {
		switch {
		case d.Decision != "approve":
			return refuse(errors.New("changes go with an approval, not a denial"))
		case p.Kind != KindKnowledge:
			return refuse(fmt.Errorf("proposal %s is a %s proposal — only a knowledge promotion can be approved with changes (its text is the content)", p.ID, p.Kind))
		case len(d.Edit) > 256<<10:
			return refuse(fmt.Errorf("edited content too large (%d bytes, max 256KiB)", len(d.Edit)))
		}
	}
	// Who may decide comes from the config now, not from the proposal's
	// record: the bot's operators, or — for a knowledge promotion — its
	// team's.
	allowed := false
	for _, op := range Approvers(cfg, d.Bot, *p) {
		if op == d.Operator {
			allowed = true
		}
	}
	if !allowed {
		if p.Kind == KindKnowledge && p.Knowledge != nil {
			return refuse(fmt.Errorf("%q is not an operator of team %s", d.Operator, p.Knowledge.Team))
		}
		return refuse(fmt.Errorf("%q is not an operator of bot %s", d.Operator, d.Bot))
	}
	opCfg := cfg.Operators[d.Operator]
	verify := x.Verify
	if verify == nil {
		verify = VerifyApproval
	}
	if err := verify(ctx, d.Operator, opCfg.SSHKey, ApprovalPayload(d.Bot, p.ID, p.Digest, d.Decision, d.Edit, d.Reason), d.Signature); err != nil {
		return refuse(err)
	}
	switch {
	case p.Status == "shadow":
		return refuse(fmt.Errorf("proposal %s is a shadow proposal (bot %s is in shadow mode) — a calibration record, not approvable", p.ID, d.Bot))
	case p.Status != "pending":
		return refuse(fmt.Errorf("proposal %s is %s, not pending", p.ID, p.Status))
	case !x.now().Before(p.Expires):
		_ = x.setStatus(d.Bot, p.ID, "expired", "", nil)
		return refuse(fmt.Errorf("proposal %s expired at %s", p.ID, p.Expires.Format(time.RFC3339)))
	}
	if m.Autonomy.ShadowMode() {
		return refuse(fmt.Errorf("bot %s is in shadow mode; set autonomy.shadow: false to allow approved execution", d.Bot))
	}
	if d.Decision == "deny" {
		out, err := x.transition(d, "denied")
		x.audit(map[string]any{"bot": d.Bot, "proposal": p.ID, "operator": d.Operator, "decision": "deny", "outcome": "ok"})
		if p.Kind == KindTerraform && x.Terraform != nil {
			x.Terraform.Discard(d.Bot, p.ID)
		}
		x.notify(d.Bot, out)
		return out, err
	}
	if _, err := x.transition(d, "approved"); err != nil {
		return refuse(err)
	}
	x.audit(map[string]any{"bot": d.Bot, "proposal": p.ID, "operator": d.Operator, "decision": "approve", "outcome": "ok", "uses": p.Uses})
	var out store.Proposal
	var err error
	switch p.Kind {
	case "verb":
		out, err = x.execVerb(ctx, d, *p)
	case KindKnowledge:
		out, err = x.execKnowledge(ctx, d, *p)
	case KindTerraform:
		if x.Terraform == nil {
			_ = x.setStatus(d.Bot, p.ID, "failed", "no terraform runner on this daemon", nil)
			return *p, errors.New("no terraform runner on this daemon")
		}
		// An apply can take minutes: it runs off the decision's call, and its
		// result reaches the ledger, the episode log and the home inbox.
		approved := *p
		approved.Status = "approved"
		run := func() {
			res := x.Terraform.Apply(context.WithoutCancel(ctx), d.Bot, approved)
			x.notify(d.Bot, res)
		}
		if x.Background != nil {
			x.Background(run)
		} else {
			go run()
		}
		return approved, nil
	default:
		err = fmt.Errorf("unknown proposal kind %q", p.Kind)
	}
	x.notify(d.Bot, out)
	return out, err
}

func (x *Executor) notify(bot string, p store.Proposal) {
	if x.Notify != nil && p.ID != "" {
		x.Notify(bot, p)
	}
}

// transition moves a pending proposal to a decided state atomically (the
// re-check inside the transaction is what makes a double approval run once).
func (x *Executor) transition(d Decision, status string) (store.Proposal, error) {
	var out store.Proposal
	err := x.Ledger.UpdateBot(d.Bot, func(r *store.BotRecord) error {
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

func (x *Executor) setStatus(bot, id, status, reason string, result map[string]any) error {
	return x.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
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
		_ = x.setStatus(d.Bot, p.ID, "failed", "stored identity unreadable", nil)
		return p, fmt.Errorf("proposal %s: stored identity: %w", p.ID, err)
	}
	if x.RunVerb == nil {
		return p, errors.New("no verb runner")
	}
	res, err := x.RunVerb(withApproved(ctx, d.Bot, p.ID, p.Digest), id, p.Uses, p.Options)
	var out store.Proposal
	uerr := x.Ledger.UpdateBot(d.Bot, func(r *store.BotRecord) error {
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

// Approvers is who may decide a proposal under cfg: the bot's operators;
// for a knowledge promotion, the operators of the team it promotes into.
func Approvers(cfg *config.Config, bot string, p store.Proposal) []string {
	if p.Kind == KindKnowledge && p.Knowledge != nil {
		return cfg.TeamOperators(p.Knowledge.Team)
	}
	return cfg.BotOperators(bot)
}

// execKnowledge lands an approved promotion: exactly the signed text (its
// hash and the digest re-checked), in the team repo.
func (x *Executor) execKnowledge(ctx context.Context, d Decision, p store.Proposal) (store.Proposal, error) {
	fail := func(err error) (store.Proposal, error) {
		_ = x.setStatus(d.Bot, p.ID, "failed", err.Error(), nil)
		p.Status, p.Reason = "failed", err.Error()
		return p, err
	}
	k := p.Knowledge
	if k == nil {
		return fail(errors.New("knowledge proposal without its change"))
	}
	digest, err := ActionDigest(d.Bot, KindKnowledge, promoteVerb, knowledgeOptions(*k))
	if err != nil || digest != p.Digest {
		return fail(errors.New("the promotion does not match the approved digest — not landed"))
	}
	if x.Knowledge == nil {
		return fail(errors.New("no team knowledge on this daemon"))
	}
	p.Approver = d.Operator
	if d.Edit != "" {
		// Approve with changes: the operator's final text lands; the record
		// keeps what was proposed (its hash) and who edited it.
		ek := *k
		ek.ProposedSHA256, ek.Text, ek.SHA256, ek.EditedBy = k.SHA256, d.Edit, sha256Hex(d.Edit), d.Operator
		p.Knowledge = &ek
		k = &ek
	}
	sha, err := x.Knowledge(ctx, d.Bot, p)
	if err != nil {
		return fail(err)
	}
	var out store.Proposal
	uerr := x.Ledger.UpdateBot(d.Bot, func(r *store.BotRecord) error {
		q := r.Proposals[p.ID]
		if q == nil {
			return nil
		}
		q.Status, q.Result = "executed", map[string]any{"commit": sha, "team": k.Team, "path": k.To}
		if k.EditedBy != "" {
			q.Result["edited_by"] = k.EditedBy
			cp := *k
			q.Knowledge = &cp
		}
		if q.Knowledge != nil {
			q.Knowledge.Landed = sha
		}
		out = *q
		return nil
	})
	return out, uerr
}

// ExpireProposals marks pending proposals past their window expired — and
// deletes an expired terraform proposal's plan and checkout.
func (x *Executor) ExpireProposals(bot string) {
	now := x.now()
	var gone []string
	_ = x.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
		for _, p := range r.Proposals {
			if p.Status == "pending" && !now.Before(p.Expires) {
				p.Status = "expired"
				if p.Kind == KindTerraform {
					gone = append(gone, p.ID)
				}
			}
		}
		return nil
	})
	for _, id := range gone {
		if x.Terraform != nil {
			x.Terraform.Discard(bot, id)
		}
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
