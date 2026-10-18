package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// The agent's side of the wallet. Nothing here returns a value: a fill
// names a field by its meaning and conductor types the value; the agent
// learns only the facts the operator made visible, a purchase's status, and
// after a purchase its order number and total.

// walletInfo answers the actions that need no page: profiles, cards,
// purchase (propose) and purchase-status.
func (m *Manager) walletInfo(ctx context.Context, c Principal, st Settings, action string, args map[string]any, entry map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch action {
	case "profiles":
		var out []map[string]any
		for _, name := range sortedNames(st.Profiles) {
			p := st.Profiles[name]
			if !p.For(c) {
				continue
			}
			v := map[string]any{"name": name, "sites": p.Sites}
			prov := m.Provenance
			if prov == nil {
				prov = OperatorOnly
			}
			// Even a visible fact is the operator's request's to know.
			if ok, why := prov(c, ""); ok {
				v["facts"], _ = m.profileFacts(ctx, p)
			} else {
				v["note"] = why
			}
			out = append(out, v)
		}
		return map[string]any{"profiles": out}, nil
	case "cards":
		var out []map[string]any
		for _, name := range sortedNames(st.Cards) {
			cd := st.Cards[name]
			if !cd.For(c) {
				continue
			}
			out = append(out, map[string]any{"name": name, "kind": cd.Kind, "merchants": cd.Merchants, "frozen": cd.Frozen,
				"currency": cd.Limits.CurrencyOr(), "per_purchase": cd.Limits.PerPurchase})
		}
		return map[string]any{"cards": out, "note": "propose a purchase first (conductor browser purchase …); conductor fills the card only after the operator approves it"}, nil
	case "purchase-status":
		if m.Purchases == nil {
			return nil, refuse("no card is configured on this daemon")
		}
		p, ok, err := m.Purchases.Get(str("purchase"))
		if err != nil {
			return nil, err
		}
		if !ok || p.Identity != c.Identity || p.Bot != c.Bot {
			return nil, refuse("no purchase %s for this session", str("purchase"))
		}
		entry["purchase"] = p.ID
		return purchaseView(p), nil
	case "purchase":
		p, err := m.proposePurchase(ctx, c, st, args, entry)
		if err != nil {
			return nil, err
		}
		return purchaseView(p), nil
	}
	return nil, fmt.Errorf("browser: unknown action %q", action)
}

// purchaseView is what the agent may see of a purchase: its terms, its
// status, and after it, the order number and total.
func purchaseView(p Purchase) map[string]any {
	out := map[string]any{"purchase": p.ID, "status": p.Status, "card": p.Card, "merchant": p.Merchant,
		"total": p.Total, "currency": p.Currency, "recurring": p.Recurring}
	if !p.Expires.IsZero() && (p.Status == PurchasePending || p.Status == PurchaseApproved) {
		out["expires"] = p.Expires.Format(time.RFC3339)
	}
	if p.Reason != "" {
		out["reason"] = p.Reason
	}
	if p.BotProposal != "" {
		out["bot_proposal"] = p.BotProposal
	}
	if p.Supersedes != "" {
		out["supersedes"] = p.Supersedes
	}
	switch p.Status {
	case PurchasePending:
		out["note"] = "waiting for the operator's approval — check with conductor browser purchase-status " + p.ID
	case PurchaseApproved:
		out["note"] = "approved: on the merchant's checkout page, conductor browser pay " + p.ID + " <the place-order button's ref>"
	}
	if r := p.Receipt; r != nil {
		out["order_number"], out["charged"] = r.OrderNumber, r.Total
		if r.Note != "" {
			out["receipt_note"] = r.Note
		}
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

// proposePurchase records a purchase: checked against the card (bound,
// unfrozen, the merchant on its list, the limits as they stand), then
// pending an operator's signed approval — or, for a bot, through its gate.
func (m *Manager) proposePurchase(ctx context.Context, c Principal, st Settings, args map[string]any, entry map[string]any) (Purchase, error) {
	str := func(k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	if m.Purchases == nil {
		return Purchase{}, refuse("no card is configured on this daemon")
	}
	if m.Paused != nil && m.Paused() {
		return Purchase{}, refuse("automation is paused (conductor resume) — no purchase")
	}
	name := str("card")
	entry["card"] = name
	card, ok := st.Cards[name]
	switch {
	case !ok:
		return Purchase{}, refuse("no card %q", name)
	case !card.For(c):
		return Purchase{}, refuse("card %q is not bound to this identity (%s)", name, c.Identity)
	case card.Frozen:
		return Purchase{}, refuse("card %q is frozen", name)
	}
	merchant := strings.TrimSuffix(str("merchant"), "/")
	entry["merchant"] = merchant
	if exactOrigin(merchant) != nil {
		return Purchase{}, refuse("merchant must be the shop's exact origin (https://shop.example.com)")
	}
	if !card.merchantAllowed(merchant) {
		return Purchase{}, refuse("%s is not one of card %s's merchants — the operator adds merchants", merchant, name)
	}
	if err := checkURL(merchant+"/", allowFor(c, st)); err != nil {
		return Purchase{}, err
	}
	total, err := strconv.ParseFloat(str("total"), 64)
	if err != nil || total <= 0 || math.IsInf(total, 0) || math.IsNaN(total) {
		return Purchase{}, refuse("total must be a positive amount")
	}
	cur := strings.ToUpper(str("currency"))
	if cur == "" {
		cur = card.Limits.CurrencyOr()
	}
	if !currencyRE.MatchString(cur) {
		return Purchase{}, refuse("currency is an ISO code (USD, EUR, …)")
	}
	p := Purchase{Card: name, Merchant: merchant, Total: total, Currency: cur, Why: clip(str("why"), 500),
		Recurring: args["recurring"] == true || str("recurring") == "true", Interval: str("interval"),
		Identity: c.Identity, Groups: c.Groups, Requester: c.Requester, Dispatch: c.Dispatch, Bot: c.Bot, CaseID: c.CaseID}
	if p.Recurring && p.Interval == "" {
		return Purchase{}, refuse("a recurring purchase names its interval (month, year, …)")
	}
	if raw := str("items"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &p.Items); err != nil {
			return Purchase{}, refuse("items is a JSON list: [{\"name\":…, \"qty\":…, \"amount\":…}]")
		}
	}
	if len(p.Items) > 50 {
		return Purchase{}, refuse("at most 50 items")
	}
	for i := range p.Items {
		p.Items[i].Name = clip(p.Items[i].Name, 120)
	}
	if err := m.Purchases.CheckLimits(card, c, total, cur); err != nil {
		return Purchase{}, refuse("%v", err)
	}
	ttl := st.PurchaseTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	p, err = m.Purchases.Propose(p, ttl)
	if err != nil {
		return Purchase{}, err
	}
	entry["purchase"], entry["total"], entry["currency"], entry["recurring"] = p.ID, p.Total, p.Currency, p.Recurring
	if c.Bot != "" {
		if m.BotPurchase == nil {
			m.Purchases.Refuse(p.ID, "no bot gate on this daemon")
			return Purchase{}, refuse("a bot's purchase goes through its gate, and none is wired")
		}
		proposal, unattended, err := m.BotPurchase(ctx, c, p)
		if err != nil {
			m.Purchases.Refuse(p.ID, err.Error())
			return Purchase{}, refuse("%v", err)
		}
		if unattended && !p.Recurring {
			p, err = m.Purchases.Approve(p.ID, p.Digest, "unattended (bot "+c.Bot+")", ttl, true)
			entry["unattended"] = true
			return p, err
		}
		m.Purchases.SetBotProposal(p.ID, proposal)
		p.BotProposal = proposal
	}
	if m.OnPurchase != nil {
		m.OnPurchase(p)
	}
	return p, nil
}

// ApprovePurchase approves a pending purchase for an operator whose
// signature over its digest the caller verified.
func (m *Manager) ApprovePurchase(id, digest, operator string) (Purchase, error) {
	if m.Purchases == nil {
		return Purchase{}, fmt.Errorf("browser: no purchase ledger")
	}
	p, ok, err := m.Purchases.Get(id)
	if err != nil || !ok {
		return Purchase{}, fmt.Errorf("browser: no purchase %s", id)
	}
	if p.Bot != "" {
		return Purchase{}, fmt.Errorf("browser: purchase %s is bot %s's — approve its proposal %s (conductor bot approve %s %s)", id, p.Bot, p.BotProposal, p.Bot, p.BotProposal)
	}
	st := m.settings()
	if cd, ok := st.Cards[p.Card]; !ok || cd.Frozen || !cd.merchantAllowed(p.Merchant) {
		return Purchase{}, fmt.Errorf("browser: purchase %s: card %s is gone, frozen, or no longer allows %s", id, p.Card, p.Merchant)
	}
	out, err := m.Purchases.Approve(id, digest, operator, ttlOr(st.PurchaseTTL), false)
	if err == nil {
		m.audit(map[string]any{"event": "browser", "action": "purchase-approve", "purchase": id, "card": p.Card, "merchant": p.Merchant,
			"total": p.Total, "currency": p.Currency, "recurring": p.Recurring, "by": "operator", "operator": operator, "outcome": "ok"})
	}
	return out, err
}

// ApproveBotPurchase approves a bot's purchase once its proposal was
// approved and executed through the bot's gate.
func (m *Manager) ApproveBotPurchase(id, bot, proposal string) (Purchase, error) {
	if m.Purchases == nil {
		return Purchase{}, fmt.Errorf("browser: no purchase ledger")
	}
	p, ok, err := m.Purchases.Get(id)
	if err != nil || !ok || p.Bot != bot || p.BotProposal != proposal {
		return Purchase{}, fmt.Errorf("browser: purchase %s is not bot %s's proposal %s", id, bot, proposal)
	}
	out, err := m.Purchases.Approve(id, p.Digest, "bot proposal "+proposal, ttlOr(m.settings().PurchaseTTL), false)
	if err == nil {
		m.audit(map[string]any{"event": "browser", "action": "purchase-approve", "purchase": id, "bot": bot, "proposal": proposal, "outcome": "ok"})
	}
	return out, err
}

func ttlOr(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return time.Hour
}

// ---------------------------------------------------------------------------
// Profiles: facts, and fills.
// ---------------------------------------------------------------------------

// profileFacts are what the agent may learn of a profile: per visible
// field, that it is set, a non-sensitive fact, or (opted in) the value.
func (m *Manager) profileFacts(ctx context.Context, p Profile) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range sortedNames(p.Visible) {
		vis := p.Visible[f]
		ref := p.Fields[f]
		if vis == "hidden" {
			continue
		}
		if ref == nil {
			out[f] = "not set"
			continue
		}
		v, err := m.resolveRef(ctx, ref)
		if err != nil || v == "" {
			out[f] = "not readable"
			continue
		}
		switch vis {
		case "exists":
			out[f] = "set"
		case "value":
			out[f] = v
		case "fact":
			out[f] = profileFact(f, v)
		}
	}
	return out, nil
}

// profileFact derives the non-sensitive fact a field stands for.
func profileFact(field, v string) string {
	switch {
	case field == "email":
		if _, dom, ok := strings.Cut(v, "@"); ok {
			return "domain " + strings.ToLower(dom)
		}
	case strings.HasSuffix(field, ".country"):
		return "country " + strings.ToUpper(strings.TrimSpace(v))
	case field == "phone":
		if m := regexp.MustCompile(`^\s*\+(\d{1,3})`).FindStringSubmatch(v); m != nil {
			return "country code +" + m[1]
		}
	case field == "birthday":
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(v)); err == nil {
			if t.AddDate(18, 0, 0).Before(time.Now()) {
				return "18 or older"
			}
			return "under 18"
		}
	}
	return "set"
}

// profileFor resolves a profile the caller may fill from now.
func (m *Manager) profileFor(c Principal, st Settings, name, host string) (Profile, error) {
	p, ok := st.Profiles[name]
	if !ok {
		return Profile{}, refuse("no profile %q", name)
	}
	if !p.For(c) {
		return Profile{}, refuse("profile %q is not bound to this identity (%s)", name, c.Identity)
	}
	prov := m.Provenance
	if prov == nil {
		prov = OperatorOnly
	}
	if ok, why := prov(c, host); !ok {
		return Profile{}, refuse("profile %q: %s", name, why)
	}
	return p, nil
}

// walletSet re-checks the frame and its origin and sets a value — an
// input's, a textarea's, or a select's option (one of the candidates, by
// value or text) — from the isolated world, in one call. Never a password
// or file input.
const walletSet = `function(expected, cands){
	if (this.ownerDocument !== document || window !== window.top) return "the field is not in the page's own document (an embedded frame)";
	if (String(location.origin).toLowerCase() !== String(expected).toLowerCase()) return "the page moved to a different origin before the fill";
	if (this.disabled || this.readOnly) return "the field is disabled or read-only";
	let set, v = cands[0];
	if (this instanceof HTMLSelectElement) {
		const opts = Array.from(this.options);
		let opt = null;
		for (const c of cands) {
			const w = String(c).toLowerCase();
			opt = opts.find(o => o.value.toLowerCase() === w || o.text.trim().toLowerCase() === w);
			if (opt) break;
		}
		if (!opt) return "no option of the list matches";
		v = opt.value;
		set = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
	} else if (this instanceof HTMLInputElement || this instanceof HTMLTextAreaElement) {
		const t = (this.getAttribute("type") || "text").toLowerCase();
		if (["password", "hidden", "file", "checkbox", "radio", "submit", "button", "image"].includes(t)) return "not a field for this value (type=" + t + ")";
		set = Object.getOwnPropertyDescriptor(this instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype, "value").set;
	} else return "it is not a form field";
	this.focus();
	set.call(this, v);
	this.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText"}));
	this.dispatchEvent(new Event("change", {bubbles: true}));
	return "";
}`

// walletClear empties a field conductor filled (a refused checkout).
const walletClear = `function(){
	const proto = this instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : this instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
	try { Object.getOwnPropertyDescriptor(proto, "value").set.call(this, ""); this.dispatchEvent(new Event("change", {bubbles: true})); } catch (e) {}
	return "";
}`

// setWallet fills one node with a value conductor read, after the field
// checks, and remembers it for the scrub (a value under 4 characters only
// by its node: scrubbing "CA" or "123" from every reply would shred it).
func (m *Manager) setWallet(ctx context.Context, in *Instance, t *tab, fr frame, o origin, nodeID int64, cands []string, scrub []string) error {
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return err
	}
	obj, err := in.resolveIn(ctx, t, nodeID, w)
	if err != nil {
		return refuse("the field is not in the page's own document (an embedded frame, or the page changed)")
	}
	in.remember(nodeID, "")
	for _, s := range scrub {
		if len(s) >= 4 {
			in.remember(nodeID, s)
		}
	}
	res, err := in.callOn(ctx, t, obj, walletSet, o.browserOrigin(), cands)
	if err != nil {
		return fmt.Errorf("browser: the fill: the page changed")
	}
	if why := strings.Trim(string(res), `"`); why != "" {
		return refuse("cannot fill it: %s", why)
	}
	return nil
}

// fillProfile fills one field of a profile into the node ref names.
func (m *Manager) fillProfile(ctx context.Context, in *Instance, t *tab, c Principal, st Settings, name, field string, nodeID int64, allow []string, entry map[string]any) error {
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return err
	}
	o, err := parseOrigin(fr.SecurityOrigin)
	if err != nil {
		return refuse("the page has no origin a profile can be filled on")
	}
	entry["origin"] = o.browserOrigin()
	p, err := m.profileFor(c, st, name, o.host)
	if err != nil {
		return err
	}
	if !config.ProfileFieldRE.MatchString(field) {
		return refuse("no profile field %q", field)
	}
	if !p.allowed(field, fr.SecurityOrigin) {
		return refuse("profile %s's %s is not opted in for %s", name, field, o.browserOrigin())
	}
	if err := checkURL(fr.URL, allow); err != nil {
		return err
	}
	ref := p.Fields[field]
	if ref == nil {
		return refuse("profile %s has no %s", name, field)
	}
	v, err := m.secret(ctx, ref)
	if err != nil {
		return err
	}
	scrub := []string{v}
	if p.Visible[field] == "value" {
		scrub = nil // the operator made it visible
	}
	return m.setWallet(ctx, in, t, fr, o, nodeID, profileCandidates(field, v), scrub)
}

// profileCandidates are the forms a value may take in a select (a country
// by code or name, a region the same).
func profileCandidates(field, v string) []string {
	out := []string{v}
	if strings.HasSuffix(field, ".country") || strings.HasSuffix(field, ".region") {
		out = append(out, strings.ToUpper(v), strings.ToLower(v))
	}
	return out
}

// findForm marks the form fields the page asks for, by their meaning: the
// autocomplete tokens (with billing/shipping sections), then name, id and
// label. Hidden and disabled fields are skipped.
const findForm = `function(){
	for (const e of document.querySelectorAll("[data-conductor-profile]")) e.removeAttribute("data-conductor-profile");
	const vis = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
	const els = Array.from(document.querySelectorAll("input, select, textarea")).filter(e => vis(e) && !e.disabled && !e.readOnly &&
		!["password", "hidden", "file", "checkbox", "radio", "submit", "button", "image"].includes((e.getAttribute("type") || "text").toLowerCase()));
	const tok = {"name":"name","given-name":"given_name","family-name":"family_name","email":"email","tel":"phone","organization":"company","bday":"birthday",
		"address-line1":"line1","street-address":"line1","address-line2":"line2","address-level2":"city","address-level1":"region","postal-code":"postal_code","country":"country","country-name":"country"};
	const addr = {"line1":1,"line2":1,"city":1,"region":1,"postal_code":1,"country":1};
	const label = e => { const l = e.labels && e.labels[0]; return ((l ? l.textContent : "") + " " + (e.name || "") + " " + (e.id || "") + " " + (e.getAttribute("placeholder") || "")).toLowerCase(); };
	const guess = [[/e-?mail/, "email"], [/phone|tel\b|mobile/, "phone"], [/first.?name|given/, "given_name"], [/last.?name|surname|family/, "family_name"],
		[/full.?name|^\s*name\b|your name/, "name"], [/company|organi[sz]ation/, "company"], [/birth|dob\b/, "birthday"],
		[/address.?(line)?.?2|apt|suite/, "line2"], [/address|street/, "line1"], [/city|town/, "city"], [/state|province|region/, "region"],
		[/zip|postal|postcode/, "postal_code"], [/country/, "country"]];
	const out = [];
	for (const e of els) {
		const ac = (e.getAttribute("autocomplete") || "").toLowerCase().split(/\s+/).filter(Boolean);
		if (ac.some(a => a.startsWith("cc-"))) continue; // a card field: only pay fills it
		let f = "";
		for (const a of ac) if (tok[a]) f = tok[a];
		const billing = ac.includes("billing") || (!ac.length && /billing/.test(label(e)));
		if (!f) for (const [re, k] of guess) if (re.test(label(e))) { f = k; break; }
		if (!f) continue;
		if (addr[f]) f = (billing ? "billing." : "address.") + f;
		e.setAttribute("data-conductor-profile", f);
		out.push(f);
	}
	return out;
}`

// fillForm fills every form field the page asks for that the profile has
// and the site allows; it reports what it filled and what it left, never a
// value.
func (m *Manager) fillForm(ctx context.Context, in *Instance, t *tab, c Principal, st Settings, name string, only map[string]bool, allow []string, entry map[string]any) (map[string]any, error) {
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		return nil, err
	}
	o, err := parseOrigin(fr.SecurityOrigin)
	if err != nil {
		return nil, refuse("the page has no origin a profile can be filled on")
	}
	entry["origin"] = o.browserOrigin()
	p, err := m.profileFor(c, st, name, o.host)
	if err != nil {
		return nil, err
	}
	if err := checkURL(fr.URL, allow); err != nil {
		return nil, err
	}
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return nil, err
	}
	raw, err := in.callIn(ctx, t, w, findForm)
	if err != nil {
		return nil, err
	}
	var asked []string
	_ = json.Unmarshal(raw, &asked)
	var filled []string
	left := map[string]string{}
	seen := map[string]bool{}
	for _, f := range asked {
		if seen[f] {
			continue // one field per meaning (the first)
		}
		seen[f] = true
		switch {
		case len(only) > 0 && !only[f]:
			left[f] = "not asked for"
			continue
		case !p.allowed(f, fr.SecurityOrigin):
			left[f] = "not opted in for this site"
			continue
		case p.Fields[f] == nil:
			left[f] = "not in the profile"
			continue
		}
		id, err := in.markedNodeAttr(ctx, t, w, "data-conductor-profile", f)
		if err != nil {
			left[f] = "the page changed"
			continue
		}
		v, err := m.secret(ctx, p.Fields[f])
		if err != nil {
			left[f] = "its source did not resolve"
			continue
		}
		scrub := []string{v}
		if p.Visible[f] == "value" {
			scrub = nil
		}
		if err := m.setWallet(ctx, in, t, fr, o, id, profileCandidates(f, v), scrub); err != nil {
			left[f] = strings.TrimPrefix(err.Error(), "browser: refused: ")
			continue
		}
		filled = append(filled, f)
	}
	entry["fields"] = filled
	if len(filled) == 0 {
		return nil, refuse("no field of this form could be filled from profile %s (%v)", name, left)
	}
	return map[string]any{"filled": filled, "left": left}, nil
}

// markedNodeAttr finds the element a finder marked attr=val on.
func (in *Instance) markedNodeAttr(ctx context.Context, t *tab, ctxID int, attr, val string) (int64, error) {
	var r struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
	}
	sel := fmt.Sprintf("[%s=%q]", attr, val)
	if err := in.conn().Call(ctx, t.session, "Runtime.evaluate", map[string]any{
		"expression": "document.querySelector(" + strconv.Quote(sel) + ")", "contextId": ctxID,
	}, &r); err != nil || r.Result.ObjectID == "" {
		return 0, fmt.Errorf("browser: the field is gone")
	}
	var d struct {
		Node struct {
			BackendNodeID int64 `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := in.conn().Call(ctx, t.session, "DOM.describeNode", map[string]any{"objectId": r.Result.ObjectID}, &d); err != nil {
		return 0, err
	}
	return d.Node.BackendNodeID, nil
}

// ---------------------------------------------------------------------------
// Pay: the card, for one approved purchase.
// ---------------------------------------------------------------------------

// findCard marks a checkout's card fields by their autocomplete tokens, then
// name, id and label.
const findCard = `function(){
	for (const e of document.querySelectorAll("[data-conductor-card]")) e.removeAttribute("data-conductor-card");
	const vis = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
	const els = Array.from(document.querySelectorAll("input, select")).filter(e => vis(e) && !e.disabled && !e.readOnly &&
		!["password", "hidden", "file", "checkbox", "radio", "submit", "button", "image"].includes((e.getAttribute("type") || "text").toLowerCase()));
	const label = e => { const l = e.labels && e.labels[0]; return ((l ? l.textContent : "") + " " + (e.name || "") + " " + (e.id || "")).toLowerCase(); };
	const tok = {"cc-number":"number","cc-exp":"expiry","cc-exp-month":"month","cc-exp-year":"year","cc-csc":"cvc","cc-name":"name"};
	const guess = [[/card.?num|cc.?num|cardnumber/, "number"], [/exp.*month|month/, "month"], [/exp.*year|year/, "year"], [/expir|exp\b|mm.?yy/, "expiry"],
		[/cvc|cvv|csc|security.?code/, "cvc"], [/name.?on.?card|card.?holder|cc.?name/, "name"]];
	const out = {};
	for (const e of els) {
		const ac = (e.getAttribute("autocomplete") || "").toLowerCase().split(/\s+/);
		let f = "";
		for (const a of ac) if (tok[a]) f = tok[a];
		if (!f) for (const [re, k] of guess) if (re.test(label(e))) { f = k; break; }
		if (!f || out[f]) continue;
		e.setAttribute("data-conductor-card", f);
		out[f] = true;
	}
	return out;
}`

// pageTotals reads the amounts the page labels as a total (and the one the
// agent pointed at), from the main document in the isolated world.
const pageTotals = `function(ref){
	const out = [];
	const re = /(order\s+)?total|amount\s+due|grand\s+total|to\s+pay|you\s+pay/i;
	const skip = {SCRIPT: 1, STYLE: 1, TEMPLATE: 1, NOSCRIPT: 1};
	for (const e of document.body.querySelectorAll("*")) {
		// Hidden totals still count (one can only make conductor refuse);
		// code is not a total.
		if (skip[e.tagName] || e.closest("script, style, template, noscript")) continue;
		const own = Array.from(e.childNodes).filter(n => n.nodeType === 3).map(n => n.textContent).join(" ");
		if (!re.test(own)) continue;
		const txt = (e.textContent || "").trim();
		if (txt.length > 160) continue;
		out.push(txt);
		const pe = e.parentElement;
		if (pe && !skip[pe.tagName] && !pe.querySelector("script, style, template, noscript") && (pe.textContent || "").trim().length <= 160) out.push(pe.textContent.trim());
	}
	return out;
}`

// amountRE finds money: a currency symbol or code and a number.
var amountRE = regexp.MustCompile(`(?i)(USD|EUR|GBP|JPY|CAD|AUD|CHF|[$€£¥])\s?(\d{1,3}(?:,\d{3})+(?:\.\d{1,2})?|\d+(?:\.\d{1,2})?)|(\d{1,3}(?:,\d{3})+(?:\.\d{1,2})?|\d+(?:\.\d{1,2})?)\s?(USD|EUR|GBP|JPY|CAD|AUD|CHF|€|£)`)

var symbolCurrency = map[string]string{"$": "USD", "€": "EUR", "£": "GBP", "¥": "JPY"}

// money is one amount read from a page.
type money struct {
	Amount   float64
	Currency string
}

// parseMoney reads every amount in s (a currency marker is required: "3
// items" is not money).
func parseMoney(s string) []money {
	var out []money
	for _, m := range amountRE.FindAllStringSubmatch(s, -1) {
		cur, num := m[1], m[2]
		if num == "" {
			num, cur = m[3], m[4]
		}
		if c, ok := symbolCurrency[cur]; ok {
			cur = c
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", ""), 64)
		if err != nil {
			continue
		}
		out = append(out, money{Amount: f, Currency: strings.ToUpper(cur)})
	}
	return out
}

// checkTotal re-reads the page's total for a purchase: every amount the
// page labels a total must be in the approved currency and at most the
// approved total. No total on the page is a refusal too (unverifiable).
func checkTotal(texts []string, p Purchase) (float64, error) {
	var max float64
	found := false
	for _, t := range texts {
		for _, mo := range parseMoney(t) {
			found = true
			if mo.Currency != p.Currency {
				return 0, refuse("the page's total is in %s, the approval in %s", mo.Currency, p.Currency)
			}
			if mo.Amount > max {
				max = mo.Amount
			}
		}
	}
	if !found {
		return 0, refuse("conductor cannot find the order total on this page — it fills a card only once it has read the total (open the checkout page, or point at it: --total-ref <ref>)")
	}
	if max > p.Total+0.005 {
		return max, refuse("the page's total %.2f %s is higher than the approved %.2f %s", max, p.Currency, p.Total, p.Currency)
	}
	return max, nil
}

// readTotals collects the page's total-labelled texts (and ref's text).
func (m *Manager) readTotals(ctx context.Context, in *Instance, t *tab, fr frame, ref string) ([]string, error) {
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return nil, err
	}
	raw, err := in.callIn(ctx, t, w, pageTotals, "")
	if err != nil {
		return nil, err
	}
	var texts []string
	_ = json.Unmarshal(raw, &texts)
	if ref != "" {
		id, err := in.node(t, ref)
		if err != nil {
			return nil, err
		}
		obj, err := in.resolveIn(ctx, t, id, w)
		if err != nil {
			return nil, refuse("the total's element is not in the page's own document")
		}
		res, err := in.callOn(ctx, t, obj, `function(){ return (this.ownerDocument === document ? (this.textContent || "") : "").trim().slice(0, 160); }`)
		if err == nil {
			var s string
			_ = json.Unmarshal(res, &s)
			texts = append(texts, s)
		}
	}
	return texts, nil
}

// pay fills the card for one approved purchase and places the order.
func (m *Manager) pay(ctx context.Context, s *session, in *Instance, t *tab, c Principal, st Settings, args map[string]any, allow []string, entry map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	if m.Purchases == nil {
		return nil, refuse("no card is configured on this daemon")
	}
	id, submit := str("purchase"), str("submit")
	entry["purchase"] = id
	if submit == "" {
		return nil, refuse("pay needs --submit <the place-order button's ref>: conductor places the order itself, right after it re-reads the total")
	}
	if m.Paused != nil && m.Paused() {
		return nil, refuse("automation is paused (conductor resume) — no payment")
	}
	p, ok, err := m.Purchases.Get(id)
	if err != nil {
		return nil, err
	}
	if !ok || p.Identity != c.Identity || p.Bot != c.Bot {
		return nil, refuse("no purchase %s for this session", id)
	}
	entry["card"], entry["merchant"], entry["total"], entry["currency"] = p.Card, p.Merchant, p.Total, p.Currency
	if p.Status != PurchaseApproved {
		return nil, refuse("purchase %s is %s — a card is filled only for an approved purchase, once", id, p.Status)
	}
	card, ok := st.Cards[p.Card]
	switch {
	case !ok:
		return nil, refuse("card %s is gone", p.Card)
	case card.Frozen:
		return nil, refuse("card %s is frozen", p.Card)
	case !card.For(c):
		return nil, refuse("card %s is not bound to this identity", p.Card)
	case !card.merchantAllowed(p.Merchant):
		return nil, refuse("%s is no longer one of card %s's merchants", p.Merchant, p.Card)
	}
	o, err := parseOrigin(p.Merchant)
	if err != nil {
		return nil, err
	}
	fr, err := m.checkPage(ctx, in, t, o, allow)
	if err != nil {
		return nil, refuse("the page is not the approved merchant's (%s): %v", p.Merchant, strings.TrimPrefix(err.Error(), "browser: refused: "))
	}
	texts, err := m.readTotals(ctx, in, t, fr, str("total-ref"))
	if err != nil {
		return nil, err
	}
	if seen, err := checkTotal(texts, p); err != nil {
		if seen > 0 {
			// The price moved: a new approval for what the page shows.
			np := p
			np.Total, np.Supersedes, np.Why = seen, p.ID, "the checkout's total changed from the approved "+strconv.FormatFloat(p.Total, 'f', 2, 64)
			np.BotProposal, np.Approver, np.Receipt, np.Reason = "", "", nil, ""
			if q, perr := m.Purchases.Propose(np, ttlOr(st.PurchaseTTL)); perr == nil {
				if m.OnPurchase != nil {
					m.OnPurchase(q)
				}
				entry["new_purchase"] = q.ID
				return nil, refuse("%s — a new purchase %s for %.2f %s waits for approval", strings.TrimPrefix(err.Error(), "browser: refused: "), q.ID, seen, p.Currency)
			}
		}
		return nil, err
	}
	// The card fields, found before the approval is spent.
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		return nil, err
	}
	raw, err := in.callIn(ctx, t, w, findCard)
	if err != nil {
		return nil, err
	}
	var found map[string]bool
	_ = json.Unmarshal(raw, &found)
	if !found["number"] || !(found["expiry"] || found["month"] && found["year"]) {
		return nil, refuse("no card number and expiry field on this page's own document (a card form inside an embedded frame is not supported)")
	}
	// Spend the approval (single-use) and re-check the limits now.
	if _, err := m.Purchases.Consume(id, c, card); err != nil {
		return nil, err
	}
	m.recordCardUse(c, p, "consumed")
	var filledNodes []int64
	clear := func() {
		for _, n := range filledNodes {
			if obj, err := in.resolveIn(ctx, t, n, w); err == nil {
				_, _ = in.callOn(ctx, t, obj, walletClear)
			}
		}
	}
	fill := func(field string, ref *SecretRef, cands func(v string) ([]string, []string)) error {
		if !found[field] || ref == nil {
			return nil
		}
		v, err := m.secret(ctx, ref)
		if err != nil {
			return err
		}
		nid, err := in.markedNodeAttr(ctx, t, w, "data-conductor-card", field)
		if err != nil {
			return err
		}
		cs, scrub := cands(v)
		if err := m.setWallet(ctx, in, t, fr, o, nid, cs, scrub); err != nil {
			return err
		}
		filledNodes = append(filledNodes, nid)
		return nil
	}
	num := func(v string) ([]string, []string) {
		d := digitsOf(v)
		return []string{d}, cardNumberForms(d)
	}
	exp := func(v string) ([]string, []string) {
		mm, yyyy, _ := parseExpiry(v)
		forms := []string{mm + "/" + yyyy[2:], mm + " / " + yyyy[2:], mm + "/" + yyyy}
		return []string{forms[0]}, forms
	}
	month := func(v string) ([]string, []string) {
		mm, _, _ := parseExpiry(v)
		return []string{mm, strings.TrimPrefix(mm, "0")}, nil
	}
	year := func(v string) ([]string, []string) {
		_, yyyy, _ := parseExpiry(v)
		return []string{yyyy, yyyy[2:]}, nil
	}
	plain := func(v string) ([]string, []string) { return []string{v}, []string{v} }
	cvc := func(v string) ([]string, []string) { return []string{v}, nil } // by its node only (3 digits)
	for _, f := range []struct {
		field string
		ref   *SecretRef
		cands func(string) ([]string, []string)
	}{{"number", card.Number, num}, {"expiry", card.Expiry, exp}, {"month", card.Expiry, month}, {"year", card.Expiry, year},
		{"cvc", card.CVC, cvc}, {"name", card.HolderName, plain}} {
		if err := fill(f.field, f.ref, f.cands); err != nil {
			clear()
			m.Purchases.Unspend(id, "the fill failed: "+err.Error())
			return nil, err
		}
	}
	// The total once more, right before the order is placed; then place it.
	if fr2, err := m.checkPage(ctx, in, t, o, allow); err != nil || fr2.ID != fr.ID {
		clear()
		m.Purchases.Unspend(id, "the page left the merchant before the order was placed")
		return nil, refuse("the page left the merchant before the order was placed — nothing was submitted")
	}
	if texts, err = m.readTotals(ctx, in, t, fr, str("total-ref")); err == nil {
		_, err = checkTotal(texts, p)
	}
	if err != nil {
		clear()
		m.Purchases.Unspend(id, "the total changed after the card was filled")
		return nil, refuse("the total changed after the card was filled — the card was cleared and nothing was submitted: %s", strings.TrimPrefix(err.Error(), "browser: refused: "))
	}
	start, _ := in.pageInfo(ctx, t)
	if err := in.click(ctx, t, submit); err != nil {
		clear()
		m.Purchases.Unspend(id, "the order button could not be clicked")
		return nil, err
	}
	for i := 0; i < 100; i++ {
		if u, _ := in.pageInfo(ctx, t); u != start {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	in.settle(ctx, t, 10*time.Second)
	rc := m.readReceipt(ctx, in, t, p)
	done, _ := m.Purchases.Complete(id, rc)
	entry["order_number"], entry["charged"] = rc.OrderNumber, rc.Total
	if rc.Note != "" {
		entry["receipt_note"] = rc.Note
		if m.Notify != nil {
			m.Notify(fmt.Sprintf("purchase %s on card %s: %s", id, p.Card, rc.Note))
		}
	}
	m.recordCardUse(c, done, "completed")
	return purchaseView(done), nil
}

// cardNumberForms are the ways a page may echo a card number: bare, in
// groups of four by space or dash.
func cardNumberForms(d string) []string {
	out := []string{d}
	var g []string
	for i := 0; i < len(d); i += 4 {
		e := i + 4
		if e > len(d) {
			e = len(d)
		}
		g = append(g, d[i:e])
	}
	out = append(out, strings.Join(g, " "), strings.Join(g, "-"))
	return out
}

var orderRE = regexp.MustCompile(`(?i)order\s*(?:number|no\.?|#|id|reference)?\s*[:#]?\s*([A-Z0-9][A-Z0-9-]{3,40})`)

// readReceipt reads the order confirmation: its order number and the
// amount it says was charged (flagged when above the approval).
func (m *Manager) readReceipt(ctx context.Context, in *Instance, t *tab, p Purchase) Receipt {
	u, _ := in.pageInfo(ctx, t)
	r := Receipt{URL: redactURL(in.scrub(u)), At: time.Now().UTC(), Currency: p.Currency}
	fr, err := in.mainFrame(ctx, t)
	if err != nil {
		r.Note = "no confirmation page could be read"
		return r
	}
	w, err := in.world(ctx, t, fr.ID)
	if err != nil {
		r.Note = "no confirmation page could be read"
		return r
	}
	raw, err := in.callIn(ctx, t, w, `function(){ return (document.body && document.body.innerText || "").slice(0, 20000); }`)
	if err != nil {
		r.Note = "no confirmation page could be read"
		return r
	}
	var text string
	_ = json.Unmarshal(raw, &text)
	text = in.scrub(text)
	if mm := orderRE.FindStringSubmatch(text); mm != nil {
		r.OrderNumber = mm[1]
	} else {
		r.Note = "no order number on the confirmation page"
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if regexp.MustCompile(`(?i)total|charged|amount`).MatchString(l) {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		for _, mo := range parseMoney(l) {
			if mo.Amount > r.Total {
				r.Total, r.Currency = mo.Amount, mo.Currency
			}
		}
	}
	if r.Total > p.Total+0.005 || r.Currency != p.Currency {
		r.Note = fmt.Sprintf("the confirmation says %.2f %s was charged, the approval was %.2f %s — check the order", r.Total, r.Currency, p.Total, p.Currency)
	}
	return r
}

// recordCardUse keeps a card's use in the audit (never a value).
func (m *Manager) recordCardUse(c Principal, p Purchase, what string) {
	e := map[string]any{"event": "browser", "action": "card-" + what, "purchase": p.ID, "card": p.Card, "merchant": p.Merchant,
		"total": p.Total, "currency": p.Currency, "identity": c.Identity, "requester": c.Requester, "dispatch": c.Dispatch, "outcome": "ok"}
	if c.Bot != "" {
		e["bot"], e["case"] = c.Bot, c.CaseID
	}
	if p.Receipt != nil {
		e["order_number"], e["charged"] = p.Receipt.OrderNumber, p.Receipt.Total
	}
	m.audit(e)
}
