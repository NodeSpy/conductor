package main

// `conductor dashboard` — the operator face of the local web dashboard's auth
// (design §6.3). enroll mints a one-time, SSH-signed localhost enrollment link;
// reset removes an operator's passkeys and sessions; status prints the listeners
// and enrollment state. All three talk to the running daemon over the operator
// control socket (never mounted into a sandbox), and enroll/reset are
// authenticated by an SSH signature against operators.<name>.ssh_key — the same
// trust root as SSH-signed approvals. The CLI refuses to run inside a conductor
// agent session, as `bot approve` does.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/store"
)

// dashboardMgr is the live dashboard auth Manager, set when the dashboard starts
// (cmd/conductor/main.go). The control-socket handler reads it; nil means the
// dashboard is off on this daemon.
var dashboardMgr *auth.Manager

const dashboardUsage = `conductor dashboard — the local web dashboard

    conductor dashboard enroll --operator <name> [--key <ssh private key>]
    conductor dashboard reset  --operator <name> [--key <ssh private key>]
    conductor dashboard status
    conductor dashboard approve-login <code> --url <dashboard url> --operator <name> [--key <ssh key>] [--yes]
    conductor dashboard sign '<challenge>' [--key <ssh key>]

enroll prints a one-time localhost link (open it on THIS machine within 10m) and
a pairing code. reset removes an operator's passkeys, password, authenticator
and sessions. Both are signed with the operator's SSH key (ssh-agent, a
hardware key, or --key) over the control socket.

approve-login signs the sign-in (or confirmation) request a dashboard page shows
as a login code, from any machine with the key, and posts it to --url; the
waiting page then continues. sign prints the signature for a challenge the page
shows, to paste back. Neither needs the daemon socket or a session.`

func cmdDashboard(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, dashboardUsage)
		return &exitError{code: 2}
	}
	// A conductor agent session must not drive enrollment or reset (design §6.3),
	// the same guard `bot approve` uses.
	if os.Getenv("CONDUCTOR_SKILL_TOKEN") != "" {
		return fmt.Errorf("refusing: this shell is a conductor agent session — enroll and reset are for an operator, from their own shell with their own key")
	}
	sub := args[0]
	if sub == "approve-login" || sub == "sign" {
		return dashboardSSHCommand(sub, args[1:])
	}
	cfg, rest, err := loadConfig(args[1:])
	if err != nil {
		return err
	}
	pos, fl, _, _ := botFlags(rest)
	_ = pos
	switch sub {
	case "enroll":
		return dashboardEnroll(cfg, fl)
	case "reset":
		return dashboardReset(cfg, fl)
	case "status":
		return dashboardStatus(cfg)
	case "help", "-h", "--help":
		fmt.Println(dashboardUsage)
		return nil
	default:
		return fmt.Errorf("unknown dashboard command %q", sub)
	}
}

func dashboardEnroll(cfg *config.Config, fl map[string]string) error {
	op := fl["operator"]
	if op == "" {
		return fmt.Errorf("say which operator with --operator <name>")
	}
	opCfg, ok := cfg.Operators[op]
	if !ok || strings.TrimSpace(opCfg.SSHKey) == "" {
		return fmt.Errorf("operator %q is not configured with an ssh_key", op)
	}
	issued := time.Now()
	sig, err := signDashboard(fl["key"], opCfg.SSHKey, auth.EnrollPayload(op, issued))
	if err != nil {
		return fmt.Errorf("%w\n(no ssh-agent holding operators.%s.ssh_key? pass --key <private key file>)", err, op)
	}
	out, _, err := dashboardControl(cfg, "enroll", map[string]any{"operator": op, "issued": issued.Format(time.RFC3339), "signature": sig})
	if err != nil {
		return err
	}
	secret, _ := out["secret"].(string)
	code, _ := out["pairing_code"].(string)
	port := cfg.Dashboard.EffectivePort()
	fmt.Printf("operator %s: open this on THIS machine within 10m (single use):\n  %s\npairing code: %s\n",
		auth.SafeText(op), auth.SafeText(auth.EnrollURL(port, secret)), auth.SafeText(code))
	return nil
}

func dashboardReset(cfg *config.Config, fl map[string]string) error {
	op := fl["operator"]
	if op == "" {
		return fmt.Errorf("say which operator with --operator <name>")
	}
	opCfg, ok := cfg.Operators[op]
	if !ok || strings.TrimSpace(opCfg.SSHKey) == "" {
		return fmt.Errorf("operator %q is not configured with an ssh_key", op)
	}
	issued := time.Now()
	sig, err := signDashboard(fl["key"], opCfg.SSHKey, auth.ResetPayload(op, issued))
	if err != nil {
		return err
	}
	out, msg, err := dashboardControl(cfg, "reset", map[string]any{"operator": op, "issued": issued.Format(time.RFC3339), "signature": sig})
	if err != nil {
		return err
	}
	if msg != "" {
		fmt.Println(auth.SafeText(msg))
	} else {
		removed, _ := out["removed"].(string)
		if removed == "" {
			removed = fmt.Sprintf("%v passkey(s) and %v session(s) removed", out["passkeys"], out["sessions"])
		}
		fmt.Printf("reset %s: %s; first enrollment applies again\n", auth.SafeText(op), auth.SafeText(removed))
	}
	return nil
}

func dashboardStatus(cfg *config.Config) error {
	out, _, err := dashboardControl(cfg, "status", nil)
	if err != nil {
		return err
	}
	ls, _ := out["listeners"].([]any)
	for _, l := range ls {
		m, _ := l.(map[string]any)
		fmt.Printf("listener %s: RP %s  %s\n", safeAny(m["listener"]), safeAny(m["rp_id"]), safeAny(m["origins"]))
	}
	ops, _ := out["operators"].(map[string]any)
	names := make([]string, 0, len(ops))
	for n := range ops {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s, _ := ops[n].(map[string]any)
		live := ""
		if b, _ := s["live_ticket"].(bool); b {
			live = " · enrollment link live"
		}
		extra := ""
		if b, _ := s["password"].(bool); b {
			extra += " · password"
		}
		if b, _ := s["totp"].(bool); b {
			extra += " · authenticator"
		}
		fmt.Printf("operator %s: %v passkey(s), %v session(s)%s%s\n", auth.SafeText(n), s["passkeys"], s["sessions"], extra, live)
	}
	return nil
}

// safeAny prints a value from the daemon through auth.SafeText.
func safeAny(v any) string { return auth.SafeText(fmt.Sprint(v)) }

// signDashboard signs payload with the operator's key: a --key private key file,
// or the configured public key through the ssh-agent (a confirming agent or a
// hardware key asks the human).
func signDashboard(keyFlag, pubKey string, payload []byte) (string, error) {
	key := keyFlag
	if key == "" {
		f, err := os.CreateTemp("", "conductor-operator-*.pub")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(strings.TrimSpace(pubKey) + "\n"); err != nil {
			return "", err
		}
		f.Close()
		key = f.Name()
	}
	return auth.SignEnrollTicket(context.Background(), key, payload)
}

// dashboardControl dials the daemon control socket with a dashboard command.
func dashboardControl(cfg *config.Config, sub string, a map[string]any) (map[string]any, string, error) {
	p := controlSockPath(cfg)
	conn, err := net.Dial("unix", p)
	if err != nil {
		return nil, "", fmt.Errorf("connect to daemon control socket %s — is it running? %w", p, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(controlRequest{Cmd: "dashboard", Sub: sub, Args: a}); err != nil {
		return nil, "", err
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, "", err
	}
	if resp.Error != "" {
		return nil, "", fmt.Errorf("%s", resp.Error)
	}
	return resp.Result, resp.Msg, nil
}

// dashboardOperators maps every configured operator to its authorized_keys
// public key, for the dashboard's enrollment SSH gate and sign-in gating.
func dashboardOperators(cfg *config.Config) func() map[string]string {
	return func() map[string]string {
		m := make(map[string]string, len(cfg.Operators))
		for n, o := range cfg.Operators {
			m[n] = o.SSHKey
		}
		return m
	}
}

// dashboardApprovalView is the dashboard's ApprovalView: a proposal's action
// digest, a human summary rendered from the proposal (never agent text), and the
// operators who may decide. It reads the live bot stack (botCtl), which is wired
// after the dashboard starts, so it resolves per call.
func dashboardApprovalView(botName, id string) (actionSHA, summaryJSON string, operators []string, ok bool) {
	if botCtl == nil {
		return "", "", nil, false
	}
	rec := botCtl.Ledger.Bot(botName)
	p := rec.Proposals[id]
	if p == nil {
		return "", "", nil, false
	}
	cfg := botCtl.Cfg()
	ops := bot.Approvers(cfg, botName, *p)
	return p.Digest, approvalSummary(*p), ops, true
}

// approvalSummary is the approval view's JSON: under "action" the canonical
// action the digest pins (kind, uses and options as proposed, or the
// terraform, knowledge or template change), which is what the operator
// confirms; display fields beside it; and the requester's own summary under
// "requester_summary", which nothing checks.
func approvalSummary(p store.Proposal) string {
	action := map[string]any{"kind": p.Kind}
	if p.Uses != "" {
		action["uses"] = p.Uses
	}
	if p.Options != nil {
		action["options"] = p.Options
	}
	if p.Terraform != nil {
		action["terraform"] = p.Terraform
	}
	if p.Knowledge != nil {
		action["knowledge"] = p.Knowledge
	}
	if p.Template != nil {
		action["template"] = p.Template
	}
	if p.Amount != 0 {
		action["amount"] = p.Amount
	}
	return marshalPlain(map[string]any{
		"action": action, "tier": p.Tier, "requester_class": p.RequesterClass,
		"status": p.Status, "case": p.CaseID, "requester_summary": p.Summary,
	})
}

// marshalPlain is json.Marshal without HTML escaping, so a command reads as
// written (> stays >) when the view is pretty-printed for the operator.
func marshalPlain(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// dashboardDecide is the dashboard's DecideProposal: it applies an operator's
// stepped-up decision through the same Executor.Decide the CLI uses (design
// §6.6). A passkey step-up goes on the webauthn method with the assertion as
// evidence; any other step-up (SSH, TOTP) on the dashboard method with what
// verified it.
func dashboardDecide(ctx context.Context, botName, id, decision, operator, reason string, ev dashboard.StepupEvidence) error {
	if botCtl == nil || botCtl.Exec == nil {
		return fmt.Errorf("no bots on this daemon")
	}
	_, err := botCtl.Exec.Decide(ctx, dashboardDecision(botName, id, decision, operator, reason, ev))
	return err
}

// dashboardDecision maps a dashboard step-up's evidence onto a bot.Decision.
func dashboardDecision(botName, id, decision, operator, reason string, ev dashboard.StepupEvidence) bot.Decision {
	d := bot.Decision{Bot: botName, Proposal: id, Operator: operator, Decision: decision, Reason: reason}
	if ev.Method == "" || ev.Method == auth.MethodPasskey {
		d.Method = "webauthn"
		d.WebAuthn = &bot.WebAuthnEvidence{
			Credential: ev.CredID, AuthenticatorData: ev.AuthenticatorData,
			ClientDataJSON: ev.ClientDataJSON, Signature: ev.Signature,
		}
		return d
	}
	d.Method = "dashboard"
	d.Dashboard = &bot.DashboardStepup{
		Method: string(ev.Method), SSHNamespace: ev.SSHNamespace, SSHPayload: ev.SSHPayload, SSHSignature: ev.SSHSignature,
	}
	return d
}

// dashboardBotRoutes is the bots' operator API routes for the web dashboard,
// which puts its chokepoint in front of each (the same routes the unix socket
// serves). Nil when no bots run on this daemon, so the dashboard reports the
// bot endpoints as unavailable rather than 500ing.
func dashboardBotRoutes(bots *botStack) ([]dashboard.BotRoute, dashboard.BotAuthority) {
	if bots == nil {
		return nil, nil
	}
	var out []dashboard.BotRoute
	for _, r := range bots.operatorRoutes() {
		out = append(out, dashboard.BotRoute{Pattern: r.Pattern, Handler: r.Handler})
	}
	return out, bots.svc
}

// dashboardAvatarStageFor is the dashboard's avatar apply: it stages the
// planned change as a config edit, and the config pipeline writes it. Nil with
// no bots.
func dashboardAvatarStageFor(bots *botStack) func(kind, name, token string) ([]string, any, bool, error) {
	if bots == nil {
		return nil
	}
	return bots.dashboardAvatarStage
}

// handleDashboardControl serves the daemon side of `conductor dashboard` on the
// control socket. It runs only when the dashboard is enabled (dashboardMgr set).
func handleDashboardControl(ctx context.Context, conn net.Conn, req controlRequest, log func(string, ...any)) {
	if dashboardMgr == nil {
		writeControlResp(conn, controlResponse{Error: "the dashboard is not enabled on this daemon (add a dashboard: block)"})
		return
	}
	str := func(k string) string { v, _ := req.Args[k].(string); return v }
	switch req.Sub {
	case "enroll":
		issued, err := time.Parse(time.RFC3339, str("issued"))
		if err != nil {
			writeControlResp(conn, controlResponse{Error: "bad issued time: " + err.Error()})
			return
		}
		secret, code, exp, err := dashboardMgr.MintTicket(ctx, str("operator"), issued, str("signature"))
		if err != nil {
			writeControlResp(conn, controlResponse{Error: err.Error()})
			return
		}
		log("dashboard: enrollment ticket minted for operator %s (expires %s)", str("operator"), exp.Format(time.RFC3339))
		writeControlResp(conn, controlResponse{OK: true, Result: map[string]any{"secret": secret, "pairing_code": code, "expires": exp.Format(time.RFC3339)}})
	case "reset":
		issued, err := time.Parse(time.RFC3339, str("issued"))
		if err != nil {
			writeControlResp(conn, controlResponse{Error: "bad issued time: " + err.Error()})
			return
		}
		if err := dashboardMgr.AuthorizeReset(ctx, str("operator"), issued, str("signature")); err != nil {
			writeControlResp(conn, controlResponse{Error: err.Error()})
			return
		}
		res := dashboardMgr.ResetOperator(str("operator"))
		log("dashboard: reset operator %s (%s)", str("operator"), res)
		writeControlResp(conn, controlResponse{OK: true, Result: map[string]any{"passkeys": res.Passkeys, "sessions": res.Sessions,
			"password": res.Password, "totp": res.TOTP, "ticket": res.Ticket, "removed": res.String()}})
	case "status":
		s := dashboardMgr.Status()
		b, _ := json.Marshal(s)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		writeControlResp(conn, controlResponse{OK: true, Result: m})
	default:
		writeControlResp(conn, controlResponse{Error: "unknown dashboard sub-command " + req.Sub})
	}
}
