package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/dashboard"
)

// dashboardBrowser adapts the browser control layer (browserWalletControl,
// browserLoginControl, the Manager's session/screencast/takeover methods) to the
// dashboard's Browser dependency (design §4.5). It reads the live config and
// manager through the same atomic pointers the control socket uses, so the
// dashboard and the CLI drive exactly one wallet. Every answer is sources only,
// never a value; widening and purchase decisions are gated by a passkey step-up
// in the dashboard before they reach here.
type dashboardBrowser struct{}

func (dashboardBrowser) Control(ctx context.Context, cmd, operator string, reqJSON json.RawMessage, dryRun bool) (json.RawMessage, string, error) {
	m := browserMgr.Load()
	cfg := browserCfg.Load()
	switch cmd {
	case "browser-sessions":
		var sessions []browser.SessionInfo
		if m != nil {
			sessions = m.Sessions()
		}
		out := make([]map[string]any, 0, len(sessions))
		for _, si := range sessions {
			out = append(out, sessionView(m, si))
		}
		b, _ := json.Marshal(map[string]any{"sessions": out})
		return b, "", nil
	case "browser-session":
		if m == nil {
			return nil, "", fmt.Errorf("browser: no browser on this daemon")
		}
		var q struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(reqJSON, &q)
		for _, si := range m.Sessions() {
			if si.ID == q.Session {
				b, _ := json.Marshal(map[string]any{"session": sessionView(m, si)})
				return b, "", nil
			}
		}
		return nil, "", fmt.Errorf("browser: no session %q", q.Session)
	}

	if m == nil || cfg == nil {
		// No browser configured: empty read models, so the screens open on their
		// empty states rather than an error.
		switch cmd {
		case "browser-wallet":
			b, _ := json.Marshal(map[string]any{"profiles": []any{}, "cards": []any{}})
			return b, "", nil
		case "browser-purchases":
			b, _ := json.Marshal(map[string]any{"purchases": []any{}})
			return b, "", nil
		case "browser-logins":
			b, _ := json.Marshal(map[string]any{"logins": []any{}})
			return b, "", nil
		}
		return nil, "", fmt.Errorf("browser: no wallet store on this daemon")
	}

	if cmd == "browser-logins" {
		cmd = "browser-login-list"
	}
	req := controlRequest{Cmd: cmd}
	switch {
	case browserLoginOps[cmd]:
		var lq browserLoginReq
		if len(reqJSON) > 0 {
			_ = json.Unmarshal(reqJSON, &lq)
		}
		lq.Via = "dashboard"
		req.Login = &lq
		resp := browserLoginControl(cfg, m, req)
		return resp.Data, changeOf(resp.Data), errOf(resp)
	case browserWalletOps[cmd]:
		var wq browserWalletReq
		if len(reqJSON) > 0 {
			_ = json.Unmarshal(reqJSON, &wq)
		}
		wq.Operator, wq.DryRun, wq.Via = operator, dryRun, "dashboard"
		req.Wallet = &wq
		resp := browserWalletControl(cfg, m, req)
		return resp.Data, changeOf(resp.Data), errOf(resp)
	}
	return nil, "", fmt.Errorf("browser: unknown command %q", cmd)
}

// sessionView is a live session's dashboard row: who, the agent, the profile and
// whether an operator is currently driving it.
func sessionView(m *browser.Manager, si browser.SessionInfo) map[string]any {
	v := map[string]any{
		"id": si.ID, "identity": si.Identity, "agent": si.Agent,
		"requester": si.Requester, "profile": si.Profile, "last_used": si.LastUsed,
	}
	if m != nil {
		v["leased_by"] = m.LeaseBy(si.ID)
	}
	return v
}

func (dashboardBrowser) Peek(ctx context.Context, id string) ([]byte, string, error) {
	m := browserMgr.Load()
	if m == nil {
		return nil, "", fmt.Errorf("browser: no browser on this daemon")
	}
	return m.Peek(ctx, id)
}

func (dashboardBrowser) Screencast(ctx context.Context, id string, fn func(json.RawMessage) error) error {
	m := browserMgr.Load()
	if m == nil {
		return fmt.Errorf("browser: no browser on this daemon")
	}
	return m.Screencast(ctx, id, func(f browser.ScreencastFrame) error {
		b, _ := json.Marshal(f)
		return fn(b)
	})
}

func (dashboardBrowser) Take(id, operator string) error {
	m := browserMgr.Load()
	if m == nil {
		return fmt.Errorf("browser: no browser on this daemon")
	}
	return m.TakeLease(id, operator)
}

func (dashboardBrowser) Release(id, operator string) error {
	m := browserMgr.Load()
	if m == nil {
		return fmt.Errorf("browser: no browser on this daemon")
	}
	return m.ReleaseLease(id, operator)
}

func (dashboardBrowser) Input(ctx context.Context, id, operator string, evJSON json.RawMessage) error {
	m := browserMgr.Load()
	if m == nil {
		return fmt.Errorf("browser: no browser on this daemon")
	}
	var ev browser.InputEvent
	if err := json.Unmarshal(evJSON, &ev); err != nil {
		return fmt.Errorf("browser: malformed input")
	}
	return m.Input(ctx, id, operator, ev)
}

func (dashboardBrowser) PurchaseView(id string) (string, string, []string, bool) {
	m := browserMgr.Load()
	cfg := browserCfg.Load()
	if m == nil || m.Purchases == nil {
		return "", "", nil, false
	}
	p, ok, err := m.Purchases.Get(id)
	if err != nil || !ok {
		return "", "", nil, false
	}
	var approvers []string
	if cfg != nil {
		approvers = cfg.PaymentApprovers()
	}
	// Under "action" the terms the purchase digest pins, which is what the
	// operator confirms; the bot's reason under "requester_summary".
	summary := map[string]any{
		"action": map[string]any{
			"merchant": p.Merchant, "items": p.Items, "total": p.Total, "currency": p.Currency,
			"card": p.Card, "recurring": p.Recurring, "interval": p.Interval,
			"identity": p.Identity, "bot": p.Bot, "case": p.CaseRef,
		},
		"title": purchaseTitle(p), "requester_summary": p.Why,
	}
	return p.Digest, marshalPlain(summary), approvers, true
}

func (dashboardBrowser) DecidePurchase(ctx context.Context, id, decision, operator, reason string, _ dashboard.StepupEvidence) error {
	m := browserMgr.Load()
	cfg := browserCfg.Load()
	if m == nil || m.Purchases == nil {
		return fmt.Errorf("browser: no purchase ledger")
	}
	p, ok, err := m.Purchases.Get(id)
	if err != nil || !ok {
		return fmt.Errorf("browser: no purchase %s", id)
	}
	if cfg != nil && !contains(cfg.PaymentApprovers(), operator) {
		return fmt.Errorf("browser: %q may not decide purchases", operator)
	}
	// The dashboard already verified the operator's WebAuthn step-up over the
	// per-decision digest (§6.6), so — unlike the SSH-signed CLI path — no
	// signature is re-checked here; the passkey IS the signature.
	audit := func(outcome string) {
		if m.Audit != nil {
			m.Audit(map[string]any{"event": "browser", "by": "operator", "via": "dashboard",
				"action": "purchase-" + decision, "purchase": p.ID, "operator": operator, "outcome": outcome, "method": "webauthn"})
		}
	}
	if decision == "deny" {
		if _, err := m.Purchases.Deny(p.ID, operator, reason); err != nil {
			audit("error")
			return err
		}
		audit("ok")
		return nil
	}
	if _, err := m.ApprovePurchase(p.ID, p.Digest, operator); err != nil {
		audit("error")
		return err
	}
	audit("ok")
	return nil
}

// purchaseTitle is a short human title for a purchase ("Buy 2 widgets for
// $19.98"), used in the signing summary.
func purchaseTitle(p browser.Purchase) string {
	item := "something"
	if len(p.Items) > 0 {
		item = p.Items[0].Name
		if len(p.Items) > 1 {
			item = fmt.Sprintf("%s and %d more", item, len(p.Items)-1)
		}
	}
	return fmt.Sprintf("Buy %s for %s %.2f", item, orDefaultCur(p.Currency), p.Total)
}

func orDefaultCur(c string) string {
	if c == "" {
		return "USD"
	}
	return c
}

// changeOf reads the "change" classification a wallet mutation reports.
func changeOf(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var v struct {
		Change string `json:"change"`
	}
	_ = json.Unmarshal(data, &v)
	return v.Change
}

func errOf(resp controlResponse) error {
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

var _ = time.Now
