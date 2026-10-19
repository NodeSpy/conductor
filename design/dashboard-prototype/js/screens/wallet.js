/* Personal details and payment cards, under Browsers (the conductor side: docs/wiki/Browser.md on feat/agent-browser,
 * "Personal details (profiles)" and "Payment cards and purchases"; fixtures in js/walletdata.js).
 *
 *   #/browser/details[/<profile>|/new]   named profiles: details filled by meaning, never shown; where (exact origins,
 *                                        the details allowed on each), who (bots, teams), what a bot may learn
 *   #/browser/cards[/<card>|/new]        cards: the freeze switch, spend against the limits, the shops a card may pay
 *                                        (none by default), who may use it; purchases waiting for you and receipts
 *   #/browser/purchases/<id>             one purchase: its approval (a bot's opens its proposal), or its record and receipt
 *
 * Also used elsewhere: UI.purchaseWhat (a bot's purchase approval), UI.purchaseFacts (Needs you rows),
 * UI.ticketPurchases (receipts on a ticket), UI.botWalletHTML (a bot's What it can do: Can buy, the unattended widening,
 * the details it can fill), UI.browserTabs (the Browsers tab bar).
 *
 * Rules from the backend, kept here: the dashboard never receives a value (a field's source only); a config-defined
 * profile or card is changed in the config; anything that widens (a new profile or card, a bot or team, a site or a
 * detail, a looser "may learn", a shop, a higher limit, an unfreeze) asks for your passkey, and so does every purchase
 * decision (it's signed); freezing, removing and lowering never do. */
(function () {
  const { icon, esc } = UI;
  const now = () => new Date().toISOString();

  // Lucide 1.59 (ISC) shapes this screen adds; icons.js is generated, so they join the set here
  Object.assign(window.ICONS, {
    'obj-card': '<rect width="20" height="14" x="2" y="5" rx="2"/><line x1="2" x2="22" y1="10" y2="10"/><path d="M6 14h2"/>',
    'obj-profile': '<path d="M13 19a4 4 0 0 0-8 0"/><path d="M16 10h2"/><path d="M16 14h2"/><circle cx="9" cy="12" r="3"/><rect x="2" y="5" width="20" height="14" rx="2"/>',
    freeze: '<path d="m10 20-1.25-2.5L6 18"/><path d="M10 4 8.75 6.5 6 6"/><path d="m14 20 1.25-2.5L18 18"/><path d="m14 4 1.25 2.5L18 6"/><path d="m17 21-3-6h-4"/><path d="m17 3-3 6 1.5 3"/><path d="M2 12h6.5L10 9"/><path d="m20 10-1.5 2 1.5 2"/><path d="M22 12h-6.5L14 15"/><path d="m4 10 1.5 2L4 14"/><path d="m7 21 3-6-1.5-3"/><path d="m7 3 3 6h4"/>',
    store: '<path d="M15 21v-5a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v5"/><path d="M17.774 10.31a1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.451 0 1.12 1.12 0 0 0-1.548 0 2.5 2.5 0 0 1-3.452 0 1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.77-3.248l2.889-4.184A2 2 0 0 1 7 2h10a2 2 0 0 1 1.653.873l2.895 4.192a2.5 2.5 0 0 1-3.774 3.244"/><path d="M4 10.95V19a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8.05"/>',
    repeat: '<path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/>',
  });
  Object.assign(window.ICON_MAP || {}, { 'obj-card': 'credit-card', 'obj-profile': 'id-card', freeze: 'snowflake', store: 'store', repeat: 'repeat' });

  /* ---------- words ---------- */
  const W = () => D.wallet || { profiles: [], cards: [] };
  const card = (n) => W().cards.find((c) => c.name === n);
  const profile = (n) => W().profiles.find((p) => p.name === n);
  const host = (o) => String(o || '').replace(/^https?:\/\//, '').replace(/\/$/, '');
  const money = (n, cur = 'USD') => { try { return new Intl.NumberFormat('en-US', { style: 'currency', currency: cur, currencyDisplay: cur === 'USD' ? 'narrowSymbol' : 'code' }).format(n || 0); } catch (e) { return `${(+n || 0).toFixed(2)} ${cur}`; } };
  UI.money = money;
  const list = (xs) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`);
  /** Who an identity is, in words: a bot by its name, a team, an agent you start in a directory, a repo's agent. */
  const who = (id) => { const s = String(id || ''); if (/^bot:/.test(s)) return UI.botName(s.slice(4)); if (/^team:/.test(s)) return `team ${s.slice(5)}`; if (/^dir:/.test(s)) return `agents you start in ${s.slice(4)}`; if (/^agent:/.test(s)) { const [p, r] = s.slice(6).split('@'); return `the ${p} agent${r ? ` on ${r}` : ''}`; } return s; };
  const KIND = { virtual: 'Virtual card', 'single-use': 'Single-use card', physical: 'Physical card', '': 'Card' };
  const cardName = (c) => `${c.brand || 'Card'} •••• ${c.last4 || '····'}`;
  /** A profile's details in words. */
  const FW = { name: 'Name', given_name: 'First name', family_name: 'Last name', email: 'Email', phone: 'Phone', company: 'Company', birthday: 'Birthday', line1: 'Street', line2: 'Street, line 2', city: 'City', region: 'Region', postal_code: 'Postcode', country: 'Country' };
  const fieldWord = (f) => { const [a, b] = String(f).split('.'); if (a === 'address') return FW[b] || b; if (a === 'billing') return `Billing ${String(FW[b] || b).toLowerCase()}`; if (a === 'custom') return b.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase()); return FW[a] || a; };
  const siteFieldWord = (f) => (f === 'address' ? 'the address' : f === 'billing' ? 'the billing address' : fieldWord(f).toLowerCase());
  // what a bot may learn about a field (hidden by default): exists, a fact (an email's domain, an address's country, a
  // phone's country code, "18 or older"), or the value
  const FACT = { email: 'its domain', phone: 'its country code', birthday: 'whether 18 or older', 'address.country': 'the country', 'billing.country': 'the country' };
  const VIS = [['hidden', 'Nothing'], ['exists', 'Whether it’s set'], ['fact', 'A fact'], ['value', 'The value']];
  const RANK = { hidden: 0, exists: 1, fact: 2, value: 3 };
  const visWord = (f, v) => (v === 'exists' ? 'whether it’s set' : v === 'fact' ? FACT[f] || 'a fact about it' : v === 'value' ? 'the value' : 'nothing');
  const srcWord = (s) => { if (!s || s.kind === 'stored') return 'typed in, stored write-only'; const v = String(s.source || ''); let x;
    if ((x = v.match(/vault "(\w+)" "([^"]+)"/))) return `${{ op: '1Password', house: 'the conductor vault', store: 'pass' }[x[1]] || x[1]} · ${x[2]}`;
    if ((x = v.match(/env\.(\w+)/))) return `environment variable ${x[1]}`; if ((x = v.match(/secret\.(\w+)/))) return `secret ${x[1]}`; return v; };
  /** What a bot may learn, as a phrase: "the email’s domain", "whether the phone is set", "the company itself". */
  const LEARN = { email: 'the email’s domain', phone: 'the phone’s country code', birthday: 'whether you’re 18 or older', 'address.country': 'the country', 'billing.country': 'the billing country' };
  const learnWord = (f, v) => (v === 'exists' ? `whether the ${fieldWord(f).toLowerCase()} is set` : v === 'fact' ? LEARN[f] || `a fact about the ${fieldWord(f).toLowerCase()}` : v === 'value' ? `the ${fieldWord(f).toLowerCase()} itself` : '');
  const learns = (p) => Object.entries(p.visible || {}).filter(([, v]) => v !== 'hidden').map(([f, v]) => learnWord(f, v));
  const visOpts = (f) => VIS.filter(([k]) => k !== 'fact' || FACT[f]);

  /* ---------- spend: the ledger's charges, a rolling 24 hours and 30 days ---------- */
  const PAID = new Set(['used', 'completed']);
  const charged = (p) => (p.receipt && p.receipt.total != null ? p.receipt.total : p.total);
  const spent = (cardN, identity) => { const t = Date.now(), out = { day: 0, month: 0 };
    (D.purchases || []).filter((p) => p.card === cardN && PAID.has(UI.puStatus(p)) && (!identity || p.identity === identity || (identity.startsWith('team:') && p.bot && ((S.view().bots.find((b) => b.name === p.bot) || {}).cfg || {}).team === identity.slice(5)))).forEach((p) => {
      const a = Date.parse((p.receipt && p.receipt.at) || p.decided || p.created), age = t - a; if (age <= 864e5) out.day += charged(p); if (age <= 30 * 864e5) out.month += charged(p); });
    return out; };
  UI.walletSpent = spent;
  const meter = (used, cap, cur) => { const pct = cap ? Math.min(100, Math.round((used / cap) * 100)) : 0;
    return `<span class="wl-meter" role="img" aria-label="${esc(`${money(used, cur)} of ${money(cap, cur)}`)}"><i style="width:${pct}%" data-hi="${pct >= 80 ? 1 : 0}"></i></span>`; };

  /* ---------- purchases ---------- */
  /** A purchase's status: a bot's follows its proposal (you approve the proposal); an unused one lapses. */
  UI.puStatus = (p) => { let s = p.status; const pr = p.bot_proposal && (D.proposals || []).find((x) => x.id === p.bot_proposal && x.bot === p.bot);
    if (s === 'pending' && pr) s = pr.status === 'pending' ? 'pending' : pr.status === 'denied' || pr.status === 'refused' ? 'denied' : 'approved';
    if ((s === 'pending' || s === 'approved') && p.expires && Date.parse(p.expires) < Date.now()) s = 'expired';
    return s; };
  const PST = { pending: ['ember', 'Waiting for you'], approved: ['ok', 'Approved'], used: ['run', 'Paying'], completed: ['ok', 'Paid'], denied: ['fail', 'Denied'], refused: ['fail', 'Not paid'], expired: ['idle', 'Expired'] };
  const pst = (p) => PST[UI.puStatus(p)] || ['idle', UI.puStatus(p)];
  const itemsWord = (p) => { const it = p.items || []; if (!it.length) return 'a purchase'; const a = it[0]; return `${a.qty > 1 ? `${a.qty} × ` : ''}${a.name}${it.length > 1 ? ` and ${it.length - 1} more` : ''}`; };
  const perWord = (p) => (p.recurring ? ` a ${p.interval || 'period'}` : '');
  UI.purchaseTitle = (p) => `Buy ${itemsWord(p)} for ${money(p.total, p.currency)}${perWord(p)}`;
  const byWord = (p) => (p.bot ? UI.botName(p.bot) : /^dir:/.test(p.identity || '') ? `An agent you started in ${p.identity.slice(4)}` : who(p.identity).replace(/^./, (c) => c.toUpperCase()));
  /** How long an approval may be used, in words ('1h' → 'an hour'). */
  const ttlWord = () => { const t = (D.payments || {}).approval_ttl || '1h'; return t === '1h' ? 'an hour' : UI.humanSpan(t); };
  /** Where a purchase is decided: a bot's on its proposal (its Approvals), anyone else's here. */
  const puHref = (p) => (p.bot && p.bot_proposal && UI.puStatus(p) === 'pending' ? `#/bots/${p.bot}/proposals/${p.bot_proposal}` : `#/browser/purchases/${p.id}`);
  UI.purchaseOfHref = (href) => { const h = String(href || ''); let x;
    if ((x = h.match(/#\/browser\/purchases\/(\w+)/))) return (D.purchases || []).find((p) => p.id === x[1]);
    if ((x = h.match(/#\/bots\/([\w-]+)\/proposals\/(\w+)/))) return (D.purchases || []).find((p) => p.bot === x[1] && p.bot_proposal === x[2]); return null; };
  /** A purchase's facts (the facts line): who, the shop, the total, the card, a subscription. */
  UI.purchaseFacts = (p, short) => { const c = card(p.card) || {};
    // the money and the shop first: on a phone the facts that don't fit one line fold away
    return [{ icon: p.recurring ? 'repeat' : 'act-money', value: `${money(p.total, p.currency)}${perWord(p)}`, risky: true, pri: 1, label: p.recurring ? 'A subscription' : 'Total' }, { icon: 'store', value: host(p.merchant), pri: 1, label: 'Shop' },
      p.bot ? { icon: 'obj-bot', value: UI.botName(p.bot), href: `#/bots/${p.bot}`, pri: 1, label: 'Bot' } : { icon: 'obj-sandbox', value: who(p.identity), pri: 2, label: 'Asked for by' },
      short ? null : { icon: 'obj-card', value: `${p.card} •••• ${c.last4 || ''}`.trim(), href: `#/browser/cards/${p.card}`, pri: 2, label: 'Card' }]; };
  /* ---------- spend limits stack, and the lowest wins (Browser.md, "Spend limits stack") ----------
     1 the card's limits · 2 the card's limits for the caller (identity_limits) · 3 the bot's purchases: caps (and per card)
     under its team's budget.purchases (the ceiling) · 4 the ticket's spend_limit, which counts everything proposed under
     it for its whole life (pending, approved, being paid, paid; a denied, refused or expired one frees its share). */
  const botCfgOf = (bot) => S.staged('bots', bot) || ((S.view().bots.find((b) => b.name === bot) || {}).cfg) || {};
  const teamCfgOf = (t) => S.staged('teams', t) || ((S.view().teams.find((x) => x.name === t) || {}).cfg) || {};
  const ticketOf = (id) => (D.cases || []).find((c) => c.id === id);
  const FREE = new Set(['denied', 'refused', 'expired']);
  /** What a ticket's budget holds: everything proposed under it that isn't denied, refused or expired. */
  UI.ticketCommitted = (caseId) => { const ps = (D.purchases || []).filter((p) => p.case === caseId && !FREE.has(UI.puStatus(p)));
    return { all: ps.reduce((n, p) => n + charged(p), 0), paid: ps.filter((p) => PAID.has(UI.puStatus(p))).reduce((n, p) => n + charged(p), 0), waiting: ps.filter((p) => !PAID.has(UI.puStatus(p))).reduce((n, p) => n + p.total, 0), list: ps }; };
  const spentWhere = (pred) => { const t = Date.now(), out = { day: 0, month: 0 };
    (D.purchases || []).filter((p) => PAID.has(UI.puStatus(p)) && pred(p)).forEach((p) => { const age = t - Date.parse((p.receipt && p.receipt.at) || p.decided || p.created); if (age <= 864e5) out.day += charged(p); if (age <= 30 * 864e5) out.month += charged(p); });
    return out; };
  const inTeam = (bot, t) => botCfgOf(bot).team === t;
  const DIMW = { per_purchase: 'per-purchase', per_day: 'daily', per_month: '30-day' };
  /** Every layer that applies to a purchase of `total` on `cardN` for `who` (bot or identity) under ticket `caseId`, each
      with the room it leaves: [{ key, name, says, limit, left, dim, over }]; the binding one has the least room. */
  UI.limitLayers = ({ cardN, bot, identity, caseId, total = 0, excludeId }) => { const c = card(cardN), out = [], cur = (c && c.limits.currency) || 'USD';
    const not = (p) => p.id !== excludeId;
    const caps = (key, name, says, L, sp) => { if (!L) return; [['per_purchase', total], ['per_day', sp.day + total], ['per_month', sp.month + total]].forEach(([d, used]) => { if (L[d] == null) return; const left = L[d] - used;
        const cur2 = out.find((x) => x.key === key); if (!cur2 || left < cur2.left) { if (cur2) out.splice(out.indexOf(cur2), 1); out.push({ key, name, says, limit: L[d], left: Math.round(left * 100) / 100, before: Math.round((left + total) * 100) / 100, dim: d, over: left < -0.004 }); } }); };
    if (caseId) { const tk = ticketOf(caseId), sl = tk && tk.spend_limit; if (sl) { const amt = Number(sl.amount), used = (D.purchases || []).filter((p) => p.case === caseId && !FREE.has(UI.puStatus(p)) && not(p)).reduce((n, p) => n + charged(p), 0), left = amt - used - total;
      out.push({ key: 'ticket', name: 'This ticket', says: 'this ticket’s', limit: amt, left: Math.round(left * 100) / 100, before: Math.round((left + total) * 100) / 100, dim: 'total', over: left < -0.004, ticket: tk }); } }
    if (bot) { const bc = botCfgOf(bot).purchases, nm = UI.botName(bot), sp = spentWhere((p) => p.bot === bot && not(p));
      caps('bot', `${nm}’s own caps`, `bot ${bot}’s`, bc, sp);
      if (bc && bc.cards && bc.cards[cardN]) caps('botcard', `${nm}’s caps on ${cardN}`, `bot ${bot}’s ${cardN}`, bc.cards[cardN], spentWhere((p) => p.bot === bot && p.card === cardN && not(p)));
      const t = botCfgOf(bot).team, tc = t && S.getPath(teamCfgOf(t), 'budget.purchases');
      if (tc) { caps('team', `Team ${t}’s caps`, `team ${t}’s`, tc, spentWhere((p) => p.bot && inTeam(p.bot, t) && not(p))); if (tc.cards && tc.cards[cardN]) caps('teamcard', `Team ${t}’s caps on ${cardN}`, `team ${t}’s ${cardN}`, tc.cards[cardN], spentWhere((p) => p.bot && inTeam(p.bot, t) && p.card === cardN && not(p))); } }
    if (c) { const ids = [identity, bot && 'bot:' + bot, bot && botCfgOf(bot).team && 'team:' + botCfgOf(bot).team].filter(Boolean), il = ids.map((i) => (c.identity_limits || {})[i]).find(Boolean), iid = ids.find((i) => (c.identity_limits || {})[i]);
      if (il) caps('caller', `The card’s limits for ${who(iid)}`, `card ${cardN}’s ${who(iid)}`, il, spentWhere((p) => p.card === cardN && (p.identity === iid || (iid.startsWith('bot:') && p.bot === iid.slice(4))) && not(p)));
      caps('card', `The card’s limits`, `card ${cardN}’s`, c.limits, spentWhere((p) => p.card === cardN && not(p))); }
    out.cur = cur; const bind = out.slice().sort((x, y) => x.left - y.left)[0]; if (bind) bind.bind = true; return out; };
  /** A refusal, in the backend's words: which layer stopped it and what is left in it. */
  UI.refusalWords = (l, cur = 'USD') => `over ${l.says} ${money(l.limit, cur)} ${l.dim === 'total' ? '' : DIMW[l.dim] + ' '}limit${l.dim === 'per_purchase' ? '' : `: ${money(Math.max(0, l.before), cur)} left`}`.replace(/  +/g, ' ');
  const roomWords = (l, cur) => (l.over ? `over by ${money(-l.left, cur)}` : l.dim === 'total' ? `${money(l.left, cur)} left of ${money(l.limit, cur)}` : l.dim === 'per_purchase' ? `${money(l.left, cur)} under the ${money(l.limit, cur)} per-purchase cap` : `${money(l.left, cur)} left ${l.dim === 'per_day' ? 'today' : 'in 30 days'}, of ${money(l.limit, cur)}`);
  /** The layers on an approval: what each leaves after this purchase, the tightest marked; an over-limit one is a refusal. */
  const layersHTML = (p) => { const ls = UI.limitLayers({ cardN: p.card, bot: p.bot, identity: p.identity, caseId: p.case, total: p.total, excludeId: p.id }), cur = ls.cur, over = ls.filter((l) => l.over);
    return `<ul class="wl-layers">${ls.map((l) => `<li class="${l.bind ? 'bind' : ''}${l.over ? ' over' : ''}">${l.over ? UI.mark('fail') : l.bind ? UI.mark('wait') : '<i></i>'}<span>${l.key === 'ticket' ? `This ticket’s budget` : esc(l.name)}</span><b>${roomWords(l, cur)}</b>${l.bind && !l.over ? '<span class="wl-tight">the tightest</span>' : ''}</li>`).join('')}</ul>
      ${over.length ? `<p class="wl-flag">${UI.mark('fail')}<span><b>Conductor would refuse this now:</b> ${esc(UI.refusalWords(over[0], cur))}.</span></p>` : ''}`; };
  /** What a purchase buys and on what terms: the items and total, a subscription flagged, the card and shop, the limits,
      what happens after you approve. A bot's approval page and this page's approval both draw it. */
  UI.purchaseWhat = (pOrProp) => { const p = pOrProp.purchase && typeof pOrProp.purchase === 'string' ? (D.purchases || []).find((x) => x.id === pOrProp.purchase) : pOrProp; if (!p) return '';
    const c = card(p.card) || {}, cur = p.currency, sup = p.supersedes && (D.purchases || []).find((x) => x.id === p.supersedes);
    const rows = (p.items || []).map((it) => `<tr><td>${esc(it.name)}</td><td class="num">${it.qty || 1}</td><td class="num">${money(it.amount, cur)}</td></tr>`).join('');
    return `<div class="dl-pwhy wl-what"><span class="dl-lbl">What it buys</span>
        <table class="wl-items"><thead><tr><th>Item</th><th class="num">Qty</th><th class="num">Each</th></tr></thead><tbody>${rows}</tbody>
          <tfoot><tr><td colspan="2">Total${p.recurring ? `, every ${esc(p.interval || 'period')}` : ''}</td><td class="num"><b>${money(p.total, cur)}</b> <span class="dl-sub">${esc(cur)}</span></td></tr></tfoot></table>
        ${p.recurring ? `<p class="wl-flag">${UI.mark('wait')}<span><b>A subscription: it charges ${money(p.total, cur)} every ${esc(p.interval || 'period')}.</b> You’re approving that it repeats, not only the first charge. A subscription is never bought without asking you.</span></p>` : ''}
        ${sup ? `<p class="wl-flag">${UI.mark('wait')}<span><b>Asks again for a higher price.</b> You approved ${money(sup.total, sup.currency)}; at checkout the page showed ${money(p.total, cur)}, so nothing was paid and it asks you for what the page shows.</span></p>` : ''}</div>
      <dl class="dl-where wl-where"><dt>Card</dt><dd><a class="dl-link" href="#/browser/cards/${esc(p.card)}">${esc(p.card)}</a> · ${esc(cardName(c))} · ${esc(KIND[c.kind || ''].toLowerCase())}${c.frozen ? ` · ${UI.statusMark('idle', 'frozen', { sm: true })}` : ''}</dd>
        <dt>Shop</dt><dd>${esc(host(p.merchant))} <span class="dl-sub">· ${(c.merchants || []).includes(p.merchant) ? 'one of the shops this card may pay' : 'not on this card’s list'}</span></dd>
        <dt>Limits</dt><dd><span class="dl-sub">What each limit leaves after this purchase; the lowest wins:</span>${layersHTML(p)}</dd>
        <dt>Then</dt><dd>${esc(byWord(p))} may pay once, within ${esc(ttlWord())}, on ${esc(host(p.merchant))}’s checkout page. Conductor checks the page shows this total, fills the card itself and reads the receipt; if the page asks for more, nothing is paid and you’re asked again. ${p.bot ? esc(UI.botName(p.bot)) : 'The agent'} never sees the card.</dd>
        <dt>Undo</dt><dd>Conductor can’t undo a purchase; a refund is the shop’s.</dd></dl>`; };
  /** The receipt, after the fact: the order number and what was charged, flagged when it's more than you approved. */
  const receiptHTML = (p) => { const r = p.receipt; if (!r) return '';
    return `<div class="wl-receipt${r.note ? ' flag' : ''}"><span class="dl-lbl">Receipt</span><p>${r.note ? UI.mark('wait') : UI.mark('ok')}<span>Order <b class="mono">${esc(r.order_number || '—')}</b> · ${money(r.total, r.currency || p.currency)} charged · ${UI.agoText(r.at)}${r.note ? `<br><b>${esc(r.note)}</b> You were notified; the card’s own provider limits are what stop this.` : ''}</span></p></div>`; };

  /* ---------- the tab bar every Browsers page shares ---------- */
  UI.browserTabs = (on) => UI.tabs([['sessions', 'Sessions', '#/browser', D.browsers.length], ['logins', 'Logins', '#/browser/logins', D.access.credentials.length + D.access.logins.length],
    ['details', 'Personal details', '#/browser/details', W().profiles.length], ['cards', 'Payment cards', '#/browser/cards', W().cards.length]], on);

  /* ---------- the passkey gate: only a widening change asks ---------- */
  const audit = (event, detail) => D.audit.unshift({ ts: now(), event, who: 'operator:ops', detail, run: '' });
  /** What a widening signs, in words (the passkey sheet's digest box is monospace; a sentence isn't). */
  const say = (html) => `<span class="wl-say">${html}</span>`;
  const gate = (widens, title, what, fn, ok = 'Confirm with passkey') => { if (!widens) { fn(); return; } UI.stepup({ title, purpose: 'widening', ok, what }).then((y) => { if (y) fn(); }); };
  const done = (msg, sub) => { S.save(); UI.rerender(); UI.toast(msg, sub || '', 'ok'); };
  const cfgOnly = (x) => (x.editable === false ? UI.off(`${x.name} is defined in the config: change it there`) : '');

  /* ---------- Personal details ---------- */
  const profRow = (p) => { const n = Object.keys(p.fields || {}).length, used = (D.audit || []).find((a) => a.event === 'browser_profile_fill' && new RegExp(`profile ${p.name}\\b`).test(a.detail || ''));
    const learn = Object.entries(p.visible || {}).filter(([, v]) => v !== 'hidden');
    return `<li class="dl-need"><a class="dl-need-a" href="#/browser/details/${esc(p.name)}"><span class="dl-ng">${icon('obj-profile')}</span>
      <span class="dl-nt"><b>${esc(p.name)} <span class="tgt">${n} detail${n === 1 ? '' : 's'}</span></b><span class="dl-why">For ${esc(list((p.identities || []).map(who)) || 'nobody yet')} · on ${esc(list((p.sites || []).map((s) => host(s.origin))) || 'no site yet')}${learn.length ? ` · may learn ${esc(list(learns(p)))}` : ' · learns nothing about them'}</span>
      ${UI.factsLine([p.method === 'config' ? { icon: 'obj-config', value: 'In the config', pri: 2, label: 'Where it’s defined' } : null])}</span>
      <span class="dl-age">${used ? `used ${UI.agoText(used.ts)}` : 'not used yet'}</span><span class="dl-na"><span class="btn sm secondary">Open</span></span></a></li>`; };
  const detailsPage = () => `<div class="page dl-page wl-page">${UI.head('Personal details', { crumbs: [['Browsers', '#/browser'], ['Personal details']], lede2: 'Conductor types these into forms for a bot. The bot never receives a value.', actions: `<a class="btn primary" href="#/browser/details/new">${icon('plus')}Add personal details</a>` })}
    ${UI.browserTabs('details')}
    <section class="dl-card">${W().profiles.length ? `<ul class="dl-list">${W().profiles.map(profRow).join('')}</ul>` : UI.empty({ title: 'No personal details yet', body: 'Add a set of details (a name, an email, an address) for a bot to fill in on the sites you choose.', action: `<a class="btn sm secondary" href="#/browser/details/new">Add personal details</a>`, small: true })}</section>
    <div class="wz-notes wl-notes">${[['lock', 'Values never come back', 'After you save one, the page says set, never the value; the bot asks for a detail by what it means.'], ['globe', 'Only where you say', 'A detail is filled only on a site you list, and only the details you allow there.'], ['users', 'Only for your own requests', 'A request from someone else, an event or a bot’s own check gets nothing.']].map(([i, t, d]) => `<div class="wz-note">${icon(i)}<b>${t}</b><span>${d}</span></div>`).join('')}</div></div>`;

  const pv = { };
  function profilePage(p) {
    const ed = p.editable !== false, cfg = ed ? '' : `<div class="callout small wl-cfg">${icon('obj-config')}<span><b>Defined in the config</b> (<span class="mono">browser.profiles.${esc(p.name)}</span>), so it changes there. <a class="dl-link" href="#/config">Open Config</a></span></div>`;
    const fields = Object.entries(p.fields || {}), vis = p.visible || {};
    const fieldRow = ([f, s]) => `<tr><th scope="row">${esc(fieldWord(f))}</th><td><span class="wl-masked" aria-label="set, hidden">••••••••</span> <span class="dl-sub">set</span></td><td class="wl-src">${esc(srcWord(s))}</td>
      <td>${ed ? `<select class="select sm wl-vis" data-on-change="w-vis" data-p="${esc(p.name)}" data-f="${esc(f)}" aria-label="What a bot may learn about ${esc(fieldWord(f).toLowerCase())}">${visOpts(f).map(([k, l]) => `<option value="${k}"${(vis[f] || 'hidden') === k ? ' selected' : ''}>${k === 'fact' ? `A fact: ${FACT[f]}` : l}</option>`).join('')}</select>` : esc(visWord(f, vis[f] || 'hidden').replace(/^./, (c) => c.toUpperCase()))}</td>
      <td class="num"><button class="icon-btn" ${ed ? `data-act="w-field-more" data-p="${esc(p.name)}" data-f="${esc(f)}"` : cfgOnly(p)} aria-label="More for ${esc(fieldWord(f).toLowerCase())}">${icon('more')}</button></td></tr>`;
    const siteRow = (s) => `<li class="dl-conn"><span class="dl-ng">${icon('globe')}</span><span class="dl-nt"><b>${esc(host(s.origin))}</b><span class="dl-why">Fills ${esc(list(s.fields.map(siteFieldWord)))}</span></span>
      <span class="dl-na"><button class="btn sm secondary" ${ed ? `data-act="w-site-rm" data-p="${esc(p.name)}" data-o="${esc(s.origin)}"` : cfgOnly(p)}>Remove</button></span></li>`;
    const whoRow = (id) => `<li class="dl-conn"><span class="dl-ng">${icon(/^team:/.test(id) ? 'obj-team' : /^bot:/.test(id) ? 'obj-bot' : 'obj-sandbox')}</span><span class="dl-nt"><b>${esc(who(id))}</b><span class="dl-why mono">${esc(id)}</span></span>
      <span class="dl-na"><button class="btn sm secondary" ${ed ? `data-act="w-who-rm" data-kind="profile" data-n="${esc(p.name)}" data-id="${esc(id)}"` : cfgOnly(p)}>Remove</button></span></li>`;
    const fills = (D.audit || []).filter((a) => a.event === 'browser_profile_fill' && new RegExp(`profile ${p.name}\\b`).test(a.detail || '')).slice(0, 5);
    return `<div class="page dl-page wl-page">${UI.head(esc(p.name), { crumbs: [['Browsers', '#/browser'], ['Personal details', '#/browser/details'], [p.name]], sub: `${fields.length} detail${fields.length === 1 ? '' : 's'} for ${list((p.identities || []).map(who)) || 'nobody yet'}`,
      actions: `<button class="btn secondary" ${ed ? `data-act="w-field-add" data-p="${esc(p.name)}"` : cfgOnly(p)}>${icon('plus')}Add a detail</button><button class="icon-btn ph-more" data-act="w-prof-more" data-p="${esc(p.name)}" aria-label="More for ${esc(p.name)}">${icon('more')}</button>` })}
      ${cfg}
      <section class="dl-card dl-flush"><header class="dl-ch dl-pad"><h2>Details</h2></header><div class="wl-tblwrap"><table class="dl-tbl wl-fields"><thead><tr><th>Detail</th><th>Value</th><th>Where it comes from</th><th>A bot may learn</th><th><span class="sr-only">Actions</span></th></tr></thead><tbody>${fields.map(fieldRow).join('') || '<tr><td colspan="5" class="dl-sub">No details yet.</td></tr>'}</tbody></table></div>
        <p class="dl-sub wl-foot">A bot asks conductor to fill a detail by what it means; conductor types it into the page and hides it from what the bot sees, even when the page shows it back.</p></section>
      <div class="dl-cols wl-cols"><section class="dl-card"><header class="dl-ch"><h2>Where it may be filled</h2><span class="dl-sp"></span><button class="btn sm secondary" ${ed ? `data-act="w-site-add" data-p="${esc(p.name)}"` : cfgOnly(p)}>${icon('plus')}Add a site</button></header>
          ${(p.sites || []).length ? `<ul class="dl-list dl-conns wl-flushl">${p.sites.map(siteRow).join('')}</ul>` : '<p class="dl-sub">Nowhere yet: nothing is filled until you add a site.</p>'}
          <p class="dl-sub">Exact addresses only: a lookalike, another port or a frame from another site gets nothing.</p></section>
        <section class="dl-card"><header class="dl-ch"><h2>Who may use it</h2><span class="dl-sp"></span><button class="btn sm secondary" ${ed ? `data-act="w-who-add" data-kind="profile" data-n="${esc(p.name)}"` : cfgOnly(p)}>${icon('plus')}Add</button></header>
          ${(p.identities || []).length ? `<ul class="dl-list dl-conns wl-flushl">${p.identities.map(whoRow).join('')}</ul>` : '<p class="dl-sub">Nobody yet.</p>'}
          <p class="dl-sub">Only on work you asked for: a request from someone else, an event or a bot’s own check gets nothing.</p></section></div>
      <section class="dl-card wl-gap"><header class="dl-ch"><h2>Recently filled</h2><span class="dl-sub">from the audit log; never a value</span></header>${fills.length ? `<ul class="dl-list">${fills.map((a) => `<li class="wl-audit"><span>${esc(a.words || a.detail)}</span><span class="dl-sub">${UI.agoText(a.ts)}</span></li>`).join('')}</ul>` : '<p class="dl-sub">Not filled anywhere yet.</p>'}</section></div>`;
  }
  const editP = (name, fn) => { const p = profile(name); fn(p); p.updated = now(); };
  UI.act('w-vis', (sel) => { const p = profile(sel.dataset.p), f = sel.dataset.f, before = (p.visible || {})[f] || 'hidden', v = sel.value, wider = RANK[v] > RANK[before];
    gate(wider, `Let bots learn more about ${esc(fieldWord(f).toLowerCase())}`, say(`Bots using <b>${esc(p.name)}</b> may learn <b>${esc(learnWord(f, v) || 'nothing')}</b> (before: ${esc(before === 'hidden' ? 'nothing' : learnWord(f, before))}).`), () => { editP(p.name, (x) => { x.visible = x.visible || {}; if (v === 'hidden') delete x.visible[f]; else x.visible[f] = v; }); audit('browser_profile_bind', `profile ${p.name} · ${f} visible ${v}`); done('Saved', `a bot may learn ${visWord(f, v)}`); }, 'Allow with passkey');
    if (wider) sel.value = before; });
  UI.act('w-field-more', (b) => UI.menu(b, [['Replace the value…', 'updates', 'w-field-set', `data-p="${b.dataset.p}" data-f="${b.dataset.f}"`], ['-'], ['Remove this detail…', 'trash', 'w-field-rm', `data-p="${b.dataset.p}" data-f="${b.dataset.f}"`]]));
  UI.act('w-prof-more', (b) => { const p = profile(b.dataset.p); UI.menu(b, [['See it in the audit log', 'logs', 'w-go', 'data-h="#/logs/audit"'], ['-'], ['Remove these details…', 'trash', 'w-prof-rm', p.editable === false ? UI.off('Defined in the config: remove it there') : `data-p="${esc(p.name)}"`]]); });
  UI.act('w-go', (b) => UI.go(b.dataset.h));
  UI.act('w-prof-rm', (b) => UI.confirm({ title: `Remove ${esc(b.dataset.p)}?`, body: 'Its stored values are deleted, and no bot can fill these details anywhere. Values from a vault stay in the vault.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; D.wallet.profiles = W().profiles.filter((x) => x.name !== b.dataset.p); audit('browser_profile_remove', `profile ${b.dataset.p}`); S.save(); UI.go('#/browser/details'); UI.toast('Removed', `${esc(b.dataset.p)} is gone`, 'ok'); }));
  UI.act('w-field-rm', (b) => UI.confirm({ title: `Remove ${esc(fieldWord(b.dataset.f).toLowerCase())} from ${esc(b.dataset.p)}?`, body: 'Bots can no longer fill it. A typed value is deleted; a vault keeps its own.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; editP(b.dataset.p, (p) => { delete p.fields[b.dataset.f]; if (p.visible) delete p.visible[b.dataset.f]; }); audit('browser_profile_unset', `profile ${b.dataset.p} · ${b.dataset.f}`); done('Removed', `${esc(fieldWord(b.dataset.f))} is no longer set`); }));
  /** A value: typed once (write-only), or a reference to a vault, an environment variable or a secret. */
  /** A value: the app's value-source field (UI.VS): a typed value, write-only (the daemon's `--stdin`: it goes to the
      wallet's sealed store and never comes back), or a vault item (with Browse…) or an environment variable, as a
      reference. A setting is plain config, so the field itself refuses it for these sensitive values. */
  const valueField = (k, label, opt = true, help = '') => UI.fieldHTML({ k, type: 'value', sensitive: true, label, req: !opt, defaultSrc: 'typed', typedPh: 'Type it once: stored write-only', sources: ['typed', 'vault', 'env', 'setting'], help }, '', 'wv-');
  /** Read the value fields: { field: {kind, source} }, marking the bad ones; null on an error. Typed values stay in
      their inputs until clearTyped() (after the save is confirmed). */
  const readValues = (root) => { const out = {}; let n = 0;
    root.querySelectorAll('.vs').forEach((el) => { const k = el.dataset.vs, box = el.closest('.field'), err = box.querySelector('.ferr'), v = el.querySelector('input[type=hidden]').value.trim(), p = UI.VS.parse(v);
      box.classList.remove('bad'); err.textContent = '';
      if (el.dataset.pending === '1') { out[k] = { kind: 'stored', source: '' }; return; }
      if (!v) return;
      const why = UI.VS.blocked(true, v) ? 'Not from a setting: pick a vault item or an environment variable, or type it.' : p.retired ? 'Pick the vault item again.' : '';
      if (why) { box.classList.add('bad'); err.textContent = why; n++; return; }
      out[k] = { kind: p.src, source: v }; });
    return n ? null : out; };
  const needValue = (root, k) => { const box = root.querySelector(`[data-vs="${k}"]`).closest('.field'); box.classList.add('bad'); box.querySelector('.ferr').textContent = 'Required: type it, or pick where it comes from.'; };
  /** Forget what was typed once it's saved: the page never holds a value afterwards. */
  const clearTyped = (root) => root.querySelectorAll('.vs[data-pending="1"]').forEach((el) => { el.querySelector('.vs-in').value = ''; el.dataset.pending = ''; UI.VS.refresh(el, true); });
  /** Replace one value (a profile's detail or a card's): neutral, so no passkey; write-only either way. */
  const replaceModal = (caps, title, label, onSave) => { const s = UI.modal(`<div class="modal-head"><div><div class="caps">${caps}</div><h2>${title}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-set">${valueField('v', label, false, 'The page will say set, never the value.')}</div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-set-go">Save</button></div>`);
    UI.$('#w-set-go', s).addEventListener('click', () => { const root = UI.$('#w-set', s), v = readValues(root); if (!v) return; if (!v.v) return needValue(root, 'v'); clearTyped(root); UI.closeModal(); onSave(v.v); }); };
  UI.act('w-field-set', (b) => { const p = profile(b.dataset.p), f = b.dataset.f;
    replaceModal('write-only', `Replace ${esc(fieldWord(f).toLowerCase())}`, fieldWord(f), (v) => { editP(p.name, (x) => { x.fields[f] = v; }); audit('browser_profile_set', `profile ${p.name} · ${f} · value not recorded`); done('Saved', `${esc(fieldWord(f))} is set`); }); });
  UI.act('w-card-set', (b) => { const c = card(b.dataset.c), f = b.dataset.f, w = CARDF[f];
    replaceModal('write-only', `Replace the ${esc(w.toLowerCase())}`, w, (v) => { c.fields = c.fields || {}; c.fields[f] = v; c.updated = now(); audit('browser_card_set', `card ${c.name} · ${f} · value not recorded`); done('Saved', `the ${esc(w.toLowerCase())} is set`); }); });
  const FIELDS_ALL = ['name', 'given_name', 'family_name', 'email', 'phone', 'company', 'birthday', 'address.line1', 'address.line2', 'address.city', 'address.region', 'address.postal_code', 'address.country', 'billing.line1', 'billing.line2', 'billing.city', 'billing.region', 'billing.postal_code', 'billing.country'];
  UI.act('w-field-add', (b) => { const p = profile(b.dataset.p), free = FIELDS_ALL.filter((f) => !(p.fields || {})[f]);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(p.name)}</div><h2>Add a detail</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-fa"><div class="field"><label for="w-fa-f">Which detail <span class="req" aria-label="required">*</span></label><select class="select" id="w-fa-f">${free.map((f) => `<option value="${f}">${esc(fieldWord(f))}</option>`).join('')}<option value="custom">Something else…</option></select></div>
        <div class="field hide" id="w-fa-cbox"><label for="w-fa-c">Its name</label><input class="input mono" id="w-fa-c" placeholder="loyalty_number"><div class="hint">Letters, digits and _; the bot asks for custom.&lt;name&gt;.</div><div class="ferr" role="alert"></div></div>
        ${valueField('v', 'Value', false, 'Adding a detail doesn’t fill it anywhere yet: add it to a site below.')}</div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-fa-go">Add</button></div>`);
    const sel = UI.$('#w-fa-f', s); sel.addEventListener('change', () => UI.$('#w-fa-cbox', s).classList.toggle('hide', sel.value !== 'custom'));
    UI.$('#w-fa-go', s).addEventListener('click', () => { let f = sel.value; if (f === 'custom') { const c = UI.$('#w-fa-c', s).value.trim(); if (!/^[a-z0-9_]{1,40}$/.test(c)) { UI.$('#w-fa-cbox', s).classList.add('bad'); UI.$('#w-fa-cbox .ferr', s).textContent = 'Letters, digits and _, up to 40.'; return; } f = 'custom.' + c; }
      const root = UI.$('#w-fa', s), v = readValues(root); if (!v) return; if (!v.v) return needValue(root, 'v'); clearTyped(root);
      UI.closeModal(); editP(p.name, (x) => { x.fields[f] = v.v; }); audit('browser_profile_set', `profile ${p.name} · ${f} · value not recorded`); done('Added', `${esc(fieldWord(f))} is set`); }); });
  const ORIGIN = /^https?:\/\/[a-z0-9.-]+(:\d{1,5})?$/i;
  const originErr = (v) => (ORIGIN.test(v || '') ? '' : 'The exact address: https:// and the host, with a port if it has one; no path.');
  const fieldChoices = (p) => { const ks = Object.keys(p.fields || {}), out = []; ks.forEach((k) => { const top = /^(address|billing)\./.test(k) ? k.split('.')[0] : k; if (!out.includes(top)) out.push(top); }); return out; };
  UI.act('w-site-add', (b) => { const p = profile(b.dataset.p), fc = fieldChoices(p);
    UI.form({ caps: esc(p.name), title: 'Add a site', intro: 'Where these details may be filled, and which of them.', fields: [{ k: 'origin', type: 'string', req: true, label: 'Site (its exact address)', ph: 'https://shop.example.com', validate: originErr },
      ...fc.map((f) => ({ k: 'f_' + f.replace('.', '_'), type: 'bool', label: f === 'address' ? 'The address' : f === 'billing' ? 'The billing address' : fieldWord(f), on: 'fill it here' }))],
      note: 'The browser must already be allowed to reach this site.', submit: 'Add with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { const fields = fc.filter((f) => v['f_' + f.replace('.', '_')]); if (!fields.length) return 'Pick at least one detail to fill there.'; const origin = v.origin.replace(/\/$/, '');
        UI.closeModal(); gate(true, `Fill ${esc(p.name)} on ${esc(host(origin))}`, say(`<b>${esc(p.name)}</b> may be filled on <b>${esc(host(origin))}</b>: ${esc(list(fields.map(siteFieldWord)))}.`), () => { editP(p.name, (x) => { const cur = (x.sites || []).find((s) => s.origin === origin); if (cur) cur.fields = [...new Set(cur.fields.concat(fields))]; else (x.sites = x.sites || []).push({ origin, fields }); }); audit('browser_profile_bind', `profile ${p.name} · site ${origin} · widening`); done('Added', `${esc(p.name)} may be filled on ${esc(host(origin))}`); }, 'Add with passkey'); return false; } }); });
  UI.act('w-site-rm', (b) => UI.confirm({ title: `Stop filling ${esc(b.dataset.p)} on ${esc(host(b.dataset.o))}?`, body: 'Nothing from these details is filled there any more. This takes effect at once.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; editP(b.dataset.p, (p) => { p.sites = p.sites.filter((s) => s.origin !== b.dataset.o); }); audit('browser_profile_bind', `profile ${b.dataset.p} · removed site ${b.dataset.o}`); done('Removed', `not filled on ${esc(host(b.dataset.o))} any more`); }));
  // bots and teams, by name (a team covers the bots on it)
  const idOpts = (have) => S.view().bots.map((b) => ['bot:' + b.name, `${UI.botName(b.name)} (${b.name})${b.cfg && b.cfg.team ? ` · team ${b.cfg.team}` : ''}`]).concat(S.view().teams.map((t) => ['team:' + t.name, `Team ${t.name}: every bot on it`])).filter(([v]) => !have.includes(v));
  UI.act('w-who-add', (b) => { const isCard = b.dataset.kind === 'card', x = isCard ? card(b.dataset.n) : profile(b.dataset.n);
    UI.form({ caps: esc(x.name), title: isCard ? 'Let a bot or team use this card' : 'Let a bot or team use these details', fields: [{ k: 'id', type: 'enum', req: true, label: 'Who', options: idOpts(x.identities || []) }],
      note: isCard ? 'It can propose purchases with the card; every one still waits for your approval.' : 'Only on work you asked for.', submit: 'Allow with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { UI.closeModal(); gate(true, `Let ${esc(who(v.id))} use ${esc(x.name)}`, say(`<b>${esc(who(v.id))}</b> may use ${isCard ? 'the card' : 'the personal details'} <b>${esc(x.name)}</b>.`), () => { x.identities = (x.identities || []).concat([v.id]); x.updated = now(); audit(`browser_${isCard ? 'card' : 'profile'}_bind`, `${isCard ? 'card' : 'profile'} ${x.name} · bind ${v.id} · widening`); done('Allowed', `${esc(who(v.id))} may use ${esc(x.name)}`); }, 'Allow with passkey'); return false; } }); });
  UI.act('w-who-rm', (b) => { const isCard = b.dataset.kind === 'card', x = isCard ? card(b.dataset.n) : profile(b.dataset.n);
    UI.confirm({ title: `Stop ${esc(who(b.dataset.id))} using ${esc(x.name)}?`, body: isCard ? 'It can no longer propose purchases with this card. Purchases already approved for it lapse unused.' : 'It can no longer fill these details.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; x.identities = x.identities.filter((i) => i !== b.dataset.id); if (isCard && x.identity_limits) delete x.identity_limits[b.dataset.id]; x.updated = now(); audit(`browser_${isCard ? 'card' : 'profile'}_bind`, `${isCard ? 'card' : 'profile'} ${x.name} · unbind ${b.dataset.id}`); done('Removed', `${esc(who(b.dataset.id))} can’t use ${esc(x.name)} any more`); }); });

  /* ---------- form parts the add pages share ---------- */
  /** An amount in dollars: the $ inside the field, no example number that could pass for a real limit. */
  const moneyField = (k, label, help = '') => `<div class="field" data-field="${k}"><label for="w-m-${k}">${label} <span class="req" aria-label="required">*</span></label><div class="wl-money"><span aria-hidden="true">$</span><input class="input mono" id="w-m-${k}" data-money="${k}" inputmode="decimal" autocomplete="off" placeholder="Amount"></div>${help ? `<div class="hint">${help}</div>` : ''}<div class="ferr" role="alert"></div></div>`;
  const moneyOf = (root, k) => { const v = String((root.querySelector(`[data-money="${k}"]`) || {}).value || '').replace(/[$,\s]/g, ''); return /^\d+(\.\d{1,2})?$/.test(v) ? Number(v) : NaN; };
  /** Put the $ inside a shared form's amount inputs (Change limits, buying without asking). */
  const moneyize = (root, keys) => keys.forEach((k) => { const i = root.querySelector(`#f-${k}`); if (!i || i.parentElement.classList.contains('wl-money')) return; const w = document.createElement('div'); w.className = 'wl-money'; w.innerHTML = '<span aria-hidden="true">$</span>'; i.replaceWith(w); w.appendChild(i); i.placeholder = 'Amount'; });
  /** An allowlist: one exact address a row, added and removed row by row, each checked as an origin. */
  const rowHTML = (k, v = '') => `<div class="wl-row"><input class="input mono" data-row="${k}" value="${esc(v)}" placeholder="https://shop.example.com" aria-label="A shop’s exact address" autocomplete="off"><button type="button" class="icon-btn" data-act="w-row-rm" aria-label="Remove this shop">${icon('trash')}</button></div>`;
  const rowsField = (k, label, help) => `<div class="field" data-field="${k}"><span class="flabel">${label} <span class="opt">optional</span></span><div class="wl-rows" data-rows="${k}"></div><button type="button" class="btn sm secondary wl-row-add" data-act="w-row-add" data-k="${k}">${icon('plus')}Add a shop</button><div class="hint">${help}</div><div class="ferr" role="alert"></div></div>`;
  UI.act('w-row-add', (b) => { const box = b.parentElement.querySelector('.wl-rows'); box.insertAdjacentHTML('beforeend', rowHTML(b.dataset.k)); box.lastElementChild.querySelector('input').focus(); });
  UI.act('w-row-rm', (b) => { const r = b.closest('.wl-row'), f = r.closest('.field'); r.remove(); f.classList.remove('bad'); f.querySelector('.ferr').textContent = ''; });
  /** The rows' addresses, or an error naming the first that isn't an exact origin (or repeats). */
  const rowsOf = (root, k) => { const xs = [...root.querySelectorAll(`[data-row="${k}"]`)].map((i) => i.value.trim().replace(/\/$/, '')).filter(Boolean), badOne = xs.find((x) => originErr(x)), dup = xs.find((x, i) => xs.indexOf(x) !== i);
    // only the row that's wrong is marked, with the reason under the list
    root.querySelectorAll(`[data-row="${k}"]`).forEach((i) => { const v = i.value.trim().replace(/\/$/, ''), wrong = v && (originErr(v) || xs.indexOf(v) !== xs.lastIndexOf(v)); i.classList.toggle('wl-bad', !!wrong); i.setAttribute('aria-invalid', wrong ? 'true' : 'false'); });
    return { xs, err: badOne ? `“${badOne}”: ${originErr(badOne)}` : dup ? `${host(dup)} is listed twice.` : '' }; };
  /** Who may use it: bots and teams, several at once (the backend's identities is a list), with the shared picker. */
  const whoField = (help) => UI.fieldHTML({ k: 'who', type: 'select-multi', req: true, label: 'Who may use it', options: idOpts([]), help }, [], 'w-');
  const whoOf = (root) => [...root.querySelectorAll('[data-listk="who"] .chip-x [data-v]')].map((x) => x.dataset.v);

  /* ---------- Add personal details ---------- */
  const NEWF = ['name', 'email', 'phone', 'address.line1', 'address.city', 'address.region', 'address.postal_code', 'address.country'];
  function newProfilePage() {
    return `<div class="page dl-page wl-page">${UI.head('Add personal details', { crumbs: [['Browsers', '#/browser'], ['Personal details', '#/browser/details'], ['Add personal details']] })}
      <form class="dl-card wl-form" id="w-np" novalidate>
        <div class="field" data-field="name"><label for="w-np-name">Name for this set <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-np-name" placeholder="home" autocomplete="off"><div class="hint">How you and your bots refer to these details. Letters, digits, - and _.</div><div class="ferr" role="alert"></div></div>
        <h3 class="wl-h">The details</h3><p class="dl-sub">Each one typed once (stored write-only, never shown again) or from a vault item or an environment variable. Leave out what you don’t need.</p>
        <div class="wl-grid2">${NEWF.map((f) => valueField(f, fieldWord(f))).join('')}</div>
        <h3 class="wl-h">Where, and which details</h3>
        <div class="field" data-field="origin"><label for="w-np-origin">Site (its exact address) <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-np-origin" placeholder="https://shop.example.com"><div class="hint">The browser must already be allowed to reach it. Add more sites afterwards.</div><div class="ferr" role="alert"></div></div>
        <div class="field" data-field="sitef"><span class="flabel">Fill there</span><div class="wl-checks">${[['name', 'Name'], ['email', 'Email'], ['phone', 'Phone'], ['address', 'The address']].map(([k, l]) => `<label class="row small"><input type="checkbox" class="cbx" data-sf="${k}" checked> ${l}</label>`).join('')}</div><div class="ferr" role="alert"></div></div>
        <h3 class="wl-h">Who may use it</h3>
        ${whoField('Bots and teams; nobody can use it until you add one. Only on work you asked for.')}
        <h3 class="wl-h">What a bot may learn</h3><p class="dl-sub">Nothing, unless you tick it. A fact helps a bot reason without the value.</p>
        <div class="wl-checks">${[['email', 'The email’s domain'], ['address.country', 'The country'], ['phone', 'The phone’s country code']].map(([k, l]) => `<label class="row small"><input type="checkbox" class="cbx" data-vf="${k}"> ${l}</label>`).join('')}</div>
        <div class="form-err callout danger small hide" role="alert" id="w-np-err"></div>
        <div class="wizard-foot"><a class="btn ghost" href="#/browser/details">Cancel</a><span class="spacer"></span><button type="button" class="btn primary" data-act="w-np-save">${icon('passkey')}Save with passkey</button></div>
      </form></div>`;
  }
  const bad = (root, k, msg) => { const box = root.querySelector(`[data-field="${k}"]`); box.classList.toggle('bad', !!msg); box.querySelector('.ferr').textContent = msg || ''; return !!msg; };
  UI.act('w-np-save', () => { const f = UI.$('#w-np'), name = UI.$('#w-np-name').value.trim(), origin = UI.$('#w-np-origin').value.trim().replace(/\/$/, ''), ids = whoOf(UI.$('#w-np'));
    let n = 0; n += bad(f, 'name', !/^[A-Za-z0-9_-]{1,40}$/.test(name) ? 'Letters, digits, - and _, up to 40.' : profile(name) ? 'Personal details by that name exist.' : '') ? 1 : 0;
    n += bad(f, 'origin', originErr(origin)) ? 1 : 0; n += bad(f, 'who', ids.length ? '' : 'Add at least one bot or team.') ? 1 : 0;
    const sitef = [...f.querySelectorAll('[data-sf]:checked')].map((x) => x.dataset.sf); n += bad(f, 'sitef', sitef.length ? '' : 'Pick at least one.') ? 1 : 0;
    if (n) { const first = f.querySelector('.field.bad input, .field.bad select'); if (first) first.focus(); return; }
    const err = UI.$('#w-np-err'), vals = readValues(f); if (!vals) return; if (!Object.keys(vals).length) { err.textContent = 'Give at least one detail.'; err.classList.remove('hide'); return; } err.classList.add('hide');
    const visible = {}; f.querySelectorAll('[data-vf]:checked').forEach((x) => { if (vals[x.dataset.vf]) visible[x.dataset.vf] = 'fact'; });
    gate(true, `Add personal details: ${esc(name)}`, say(`New personal details <b>${esc(name)}</b>: ${esc(list(Object.keys(vals).map((k) => fieldWord(k).toLowerCase())))}, stored write-only and never shown, filled on <b>${esc(host(origin))}</b> for <b>${esc(list(ids.map(who)))}</b>.`), () => {
      clearTyped(f); D.wallet.profiles.push({ name, method: 'stored', editable: true, added: now(), updated: now(), identities: ids, fields: vals, sites: [{ origin, fields: sitef }], visible });
      audit('browser_profile_add', `profile ${name} · ${ids.join(', ')} · values not recorded · widening`); S.save(); UI.go(`#/browser/details/${name}`); UI.toast('Saved', `${esc(name)}: values stored write-only`, 'ok'); }, 'Save with passkey'); });

  /* ---------- Payment cards ---------- */
  /** validate's reminders about a card (WalletNotes), in words. */
  const notes = (c) => [c.kind === 'physical' || !c.kind ? 'A physical card: prefer a virtual or single-use card with its own provider limits. Conductor’s checks are the first layer; a limited card is the second.' : '', !(c.merchants || []).length ? 'No shop is allowed yet, so nothing can be bought with it.' : ''].filter(Boolean);
  const frozenWord = (c) => (c.frozen ? `Frozen${c.frozen_at ? ` ${UI.agoText(c.frozen_at)}` : ' in the config'}` : '');
  const cardRow = (c) => { const L = c.limits, cur = L.currency || 'USD', s = spent(c.name), ns = notes(c), att = !c.frozen && ns.length;
    return `<li class="dl-conn wl-crow${att ? ' attn' : ''}" data-s="${c.frozen ? 'idle' : 'wait'}" data-card="${esc(c.name)}"><span class="dl-ng">${c.frozen ? `<span class="wl-ice" role="img" aria-label="Frozen">${icon('freeze')}</span>` : ns.length ? UI.mark('wait', { lg: true, label: 'Needs a look' }) : icon('obj-card')}</span>
      <span class="dl-nt"><b><a class="dl-link-q" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> <span class="tgt">${esc(cardName(c))}</span></b>
        <span class="${att || c.frozen ? 'reason' : 'dl-why'}">${c.frozen ? `${frozenWord(c)}: nothing can be paid with it` : ns.length ? esc(ns[ns.length - 1]) : `${money(s.day, cur)} of ${money(L.per_day, cur)} in 24 hours · ${money(s.month, cur)} of ${money(L.per_month, cur)} in 30 days`}</span>
        ${UI.factsLine([{ icon: 'obj-card', value: KIND[c.kind || ''], pri: 2, label: 'Kind' }, { icon: 'users', value: list((c.identities || []).map(who)) || 'Nobody yet', pri: 1, label: 'Who may use it' }, { icon: 'store', value: `${(c.merchants || []).length} shop${(c.merchants || []).length === 1 ? '' : 's'}`, pri: 2, label: 'Shops it may pay' }, c.method === 'config' ? { icon: 'obj-config', value: 'In the config', pri: 3, label: 'Where it’s defined' } : null])}</span>
      <span class="dl-na">${freezeBtn(c, 'sm')}<a class="btn sm secondary" href="#/browser/cards/${esc(c.name)}">Open</a></span></li>`; };
  const CARDF = { number: 'Number', expiry: 'Expiry', cvc: 'Security code', name: 'Name on the card' };
  const freezeBtn = (c, sz = '') => (c.frozen ? `<button class="btn ${sz} secondary" data-act="w-unfreeze" data-c="${esc(c.name)}" ${c.frozen && !c.frozen_at ? UI.off('Frozen in the config (frozen: true): unfreeze it there') : ''}>${icon('passkey')}Unfreeze</button>` : `<button class="btn ${sz} secondary" data-act="w-freeze" data-c="${esc(c.name)}">${icon('freeze')}Freeze</button>`);
  const puRow = (p) => { const [s, word] = pst(p), r = p.receipt;
    return `<li class="dl-need" data-s="${s}"><a class="dl-need-a" href="${puHref(p)}"><span class="dl-ng">${UI.mark(r && r.note ? 'wait' : s, { lg: true, label: word })}</span>
      <span class="dl-nt"><b>${esc(UI.purchaseTitle(p))}</b><span class="dl-why">${s === 'ember' ? `${esc(byWord(p))} asks before it buys` : r ? `${r.note ? 'Charged more than you approved · ' : 'Paid · '}order ${esc(r.order_number)}, ${money(r.total, r.currency)}` : esc(word + (UI.puStatus(p) === 'refused' && p.reason ? `: ${p.reason.split(/\.\s/)[0].replace(/\.$/, '')}` : ''))}</span>${UI.factsLine(UI.purchaseFacts(p))}</span>
      <span class="dl-age">${UI.agoText(p.created)}</span><span class="dl-na"><span class="btn sm secondary">${s === 'ember' ? 'Review' : 'Open'}</span></span></a></li>`; };
  UI.purchaseRow = puRow;
  const cardsPage = () => { const P = (D.purchases || []).slice().sort((a, b) => Date.parse(b.created) - Date.parse(a.created)), wait = P.filter((p) => UI.puStatus(p) === 'pending'), past = P.filter((p) => UI.puStatus(p) !== 'pending');
    const ap = (D.payments || {}).approvers || [];
    return `<div class="page dl-page wl-page">${UI.head('Payment cards', { crumbs: [['Browsers', '#/browser'], ['Payment cards']], lede2: 'Every purchase waits for your approval, and the card never reaches a bot.', actions: `<a class="btn primary" href="#/browser/cards/new">${icon('plus')}Add a card</a>` })}
      ${UI.browserTabs('cards')}
      ${D.daemon && D.daemon.paused ? `<div class="callout warn small wl-gapb">${icon('pause')}<span><b>All automation is paused,</b> so nothing is paid until you resume it. <a class="dl-link" href="#/safety?show=pause">Safety</a></span></div>` : ''}
      ${wait.length ? `<section class="dl-card wl-gapb"><header class="dl-ch"><h2>Waiting for you</h2>${UI.count(wait.length, { hot: true })}</header><ul class="dl-list dl-needs">${wait.map(puRow).join('')}</ul></section>` : ''}
      <section class="dl-card"><header class="dl-ch"><h2>Cards</h2></header>${W().cards.length ? `<ul class="dl-list dl-conns wl-flushl">${W().cards.map(cardRow).join('')}</ul>` : UI.empty({ title: 'No cards yet', body: 'Add a virtual or single-use card with its own limits; bots can then propose purchases with it for you to approve.', action: `<a class="btn sm secondary" href="#/browser/cards/new">Add a card</a>`, small: true })}
        <p class="dl-sub wl-foot">${icon('shield')} ${ap.length ? `${esc(list(ap))} approve${ap.length === 1 ? 's' : ''} purchases, each with a passkey. An approval is used once, within ${esc(ttlWord())}.` : '<b>Nobody can approve a purchase yet</b> (no operator has a key), so every purchase will wait.'} Pause all automation also stops every payment.</p></section>
      <section class="dl-card wl-gap"><header class="dl-ch"><h2>Purchases</h2><span class="dl-sub">receipts and decisions</span></header>${past.length ? `<ul class="dl-list dl-needs">${past.map(puRow).join('')}</ul>` : '<p class="dl-sub">Nothing bought yet.</p>'}</section></div>`; };

  function cardPage(c) {
    const ed = c.editable !== false, L = c.limits, cur = L.currency || 'USD', s = spent(c.name), ns = notes(c), P = (D.purchases || []).filter((p) => p.card === c.name).sort((a, b) => Date.parse(b.created) - Date.parse(a.created));
    const cfg = ed ? '' : `<div class="callout small wl-cfg">${icon('obj-config')}<span><b>Defined in the config</b> (<span class="mono">browser.cards.${esc(c.name)}</span>), so its shops, limits and who may use it change there. Freezing works here too. <a class="dl-link" href="#/config">Open Config</a></span></div>`;
    const lim = (lbl, used, cap) => `<div class="wl-lrow"><span class="wl-ll">${lbl}</span><span class="wl-lv"><b>${money(used, cur)}</b> of ${money(cap, cur)}</span>${meter(used, cap, cur)}</div>`;
    const idl = Object.entries(c.identity_limits || {});
    const merchRow = (o) => `<li class="dl-conn"><span class="dl-ng">${icon('store')}</span><span class="dl-nt"><b>${esc(host(o))}</b>${/^https:\/\/[^:]+$/.test(o) ? '' : `<span class="dl-why mono">${esc(o)}</span>`}</span><span class="dl-na"><button class="btn sm secondary" ${ed ? `data-act="w-merch-rm" data-c="${esc(c.name)}" data-o="${esc(o)}"` : cfgOnly(c)}>Remove</button></span></li>`;
    const whoRow = (id) => { const l = (c.identity_limits || {})[id];
      return `<li class="dl-conn"><span class="dl-ng">${icon(/^team:/.test(id) ? 'obj-team' : /^bot:/.test(id) ? 'obj-bot' : 'obj-sandbox')}</span><span class="dl-nt"><b>${esc(who(id))}</b><span class="dl-why">${l ? `Its own limit: ${money(l.per_purchase, cur)} a purchase, ${money(l.per_day, cur)} a day, ${money(l.per_month, cur)} in 30 days` : 'The card’s limits'}</span></span>
        <span class="dl-na"><button class="btn sm secondary" ${ed ? `data-act="w-who-rm" data-kind="card" data-n="${esc(c.name)}" data-id="${esc(id)}"` : cfgOnly(c)}>Remove</button></span></li>`; };
    const state = c.frozen ? `<section class="dl-card wl-frozen"><p><span class="wl-ice" aria-hidden="true">${icon('freeze')}</span><span><b>${frozenWord(c)}.</b> Nothing is filled with it: no purchase can be proposed, approved or paid until you unfreeze it.${c.frozen_at ? '' : ' It’s frozen in the config (frozen: true), so it unfreezes there.'}</span></p></section>` : '';
    return `<div class="page dl-page wl-page">${UI.head(esc(c.name), { crumbs: [['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], [c.name]], sub: `${cardName(c)} · ${KIND[c.kind || ''].toLowerCase()}${c.frozen ? ' · frozen' : ''}`,
      actions: `${freezeBtn(c)}<button class="icon-btn ph-more" data-act="w-card-more" data-c="${esc(c.name)}" aria-label="More for ${esc(c.name)}">${icon('more')}</button>` })}
      ${state}${cfg}${ns.map((n) => `<div class="callout warn small wl-note">${UI.mark('wait')}<span>${esc(n)}</span></div>`).join('')}
      <div class="dl-cols wl-cols">
        <section class="dl-card"><header class="dl-ch"><h2>Spending</h2><span class="dl-sp"></span><button class="btn sm secondary" ${ed ? `data-act="w-limits" data-c="${esc(c.name)}"` : cfgOnly(c)}>${icon('edit')}Change limits</button></header>
          <div class="wl-limits"><div class="wl-lrow"><span class="wl-ll">A purchase</span><span class="wl-lv">up to <b>${money(L.per_purchase, cur)}</b></span></div>${lim('Last 24 hours', s.day, L.per_day)}${lim('Last 30 days', s.month, L.per_month)}</div>
          ${idl.map(([id, l]) => { const si = spent(c.name, id); return `<div class="wl-limits wl-idl"><h3>${esc(who(id))}</h3><div class="wl-lrow"><span class="wl-ll">A purchase</span><span class="wl-lv">up to <b>${money(l.per_purchase, cur)}</b></span></div>${lim('Last 24 hours', si.day, l.per_day)}${lim('Last 30 days', si.month, l.per_month)}</div>`; }).join('')}
          <p class="dl-sub">Checked when a purchase is proposed, when you approve it, and again when it’s paid.</p></section>
        <div class="stack wl-side">
          <section class="dl-card"><header class="dl-ch"><h2>Shops it may pay</h2><span class="dl-sp"></span><button class="btn sm secondary" ${ed ? `data-act="w-merch-add" data-c="${esc(c.name)}"` : cfgOnly(c)}>${icon('plus')}Add a shop</button></header>
            ${(c.merchants || []).length ? `<ul class="dl-list dl-conns wl-flushl">${c.merchants.map(merchRow).join('')}</ul>` : `<p class="wl-empty">${UI.mark('wait')}<span>None yet, so nothing can be bought with this card. Add the exact address of a shop you trust.</span></p>`}</section>
          <section class="dl-card"><header class="dl-ch"><h2>Who may use it</h2><span class="dl-sp"></span><button class="btn sm secondary" ${ed ? `data-act="w-who-add" data-kind="card" data-n="${esc(c.name)}"` : cfgOnly(c)}>${icon('plus')}Add</button></header>
            ${(c.identities || []).length ? `<ul class="dl-list dl-conns wl-flushl">${c.identities.map(whoRow).join('')}</ul>` : '<p class="dl-sub">Nobody yet.</p>'}</section>
        </div></div>
      <section class="dl-card wl-gap"><header class="dl-ch"><h2>Purchases with this card</h2></header>${P.length ? `<ul class="dl-list dl-needs">${P.map(puRow).join('')}</ul>` : '<p class="dl-sub">Nothing bought with it yet.</p>'}</section>
      <section class="dl-card wl-gap"><header class="dl-ch"><h2>The card’s details</h2><span class="dl-sub">never shown, never sent to a bot</span></header><ul class="dl-list dl-conns wl-flushl">${Object.keys(CARDF).map((k) => { const f = (c.fields || {})[k]; return `<li class="dl-conn"><span class="dl-ng">${icon('lock')}</span><span class="dl-nt"><b>${CARDF[k]}</b><span class="dl-why">${f ? `<span class="wl-masked">${k === 'number' ? `•••• ${esc(c.last4 || '')}` : '••••'}</span> set · ${esc(srcWord(f))}` : 'Not set'}</span></span><span class="dl-na"><button class="btn sm secondary" ${ed ? `data-act="w-card-set" data-c="${esc(c.name)}" data-f="${k}"` : cfgOnly(c)}>${f ? 'Replace' : 'Set'}</button></span></li>`; }).join('')}</ul></section></div>`;
  }
  UI.act('w-card-more', (b) => { const c = card(b.dataset.c); UI.menu(b, [['See it in the audit log', 'logs', 'w-go', 'data-h="#/logs/audit"'], ['-'], ['Remove this card…', 'trash', 'w-card-rm', c.editable === false ? UI.off('Defined in the config: remove it there') : `data-c="${esc(c.name)}"`]]); });
  UI.act('w-card-rm', (b) => UI.confirm({ title: `Remove ${esc(b.dataset.c)}?`, body: 'Its stored values are deleted and nothing more can be bought with it. Approved purchases lapse unused. Receipts stay.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; D.wallet.cards = W().cards.filter((x) => x.name !== b.dataset.c); audit('browser_card_remove', `card ${b.dataset.c}`); S.save(); UI.go('#/browser/cards'); UI.toast('Removed', `${esc(b.dataset.c)} is gone`, 'ok'); }));
  // the kill switch: freezing is one click (it narrows); unfreezing widens, so it asks for your passkey
  UI.act('w-freeze', (b) => { const c = card(b.dataset.c); c.frozen = true; c.frozen_at = now(); audit('browser_card_freeze', `card ${c.name}`); S.save(); UI.rerender(); UI.toast(`${esc(c.name)} is frozen`, 'nothing is paid with it until you unfreeze it', 'ok'); });
  UI.act('w-unfreeze', (b) => { const c = card(b.dataset.c); gate(true, `Unfreeze ${esc(c.name)}`, say(`The card <b>${esc(c.name)}</b> (${esc(cardName(c))}) can be used again.`), () => { c.frozen = false; delete c.frozen_at; audit('browser_card_unfreeze', `card ${c.name} · widening`); done(`${esc(c.name)} is in use again`, 'purchases with it can be proposed again'); }, 'Unfreeze with passkey'); });
  UI.act('w-merch-add', (b) => { const c = card(b.dataset.c);
    UI.form({ caps: esc(c.name), title: 'Add a shop', intro: 'The card is filled only on this exact address, and only for a purchase you approve.', fields: [{ k: 'origin', type: 'string', req: true, label: 'The shop’s address', ph: 'https://shop.example.com', validate: (v) => originErr(v) || ((c.merchants || []).includes(String(v).replace(/\/$/, '')) ? 'Already on the list.' : '') }],
      note: 'The browser must already be allowed to reach it. A checkout inside another site’s frame (hosted payment fields) isn’t filled yet.', submit: 'Add with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { const o = v.origin.replace(/\/$/, ''); UI.closeModal(); gate(true, `Let ${esc(c.name)} pay ${esc(host(o))}`, say(`The card <b>${esc(c.name)}</b> may pay at <b>${esc(host(o))}</b>.`), () => { c.merchants = (c.merchants || []).concat([o]); c.updated = now(); audit('browser_card_bind', `card ${c.name} · merchant ${o} · widening`); done('Added', `${esc(c.name)} may pay ${esc(host(o))}`); }, 'Add with passkey'); return false; } }); });
  UI.act('w-merch-rm', (b) => UI.confirm({ title: `Stop ${esc(b.dataset.c)} paying ${esc(host(b.dataset.o))}?`, body: 'Nothing more is bought there with this card. Takes effect at once.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; const c = card(b.dataset.c); c.merchants = c.merchants.filter((x) => x !== b.dataset.o); audit('browser_card_bind', `card ${c.name} · removed merchant ${b.dataset.o}`); done('Removed', `${esc(c.name)} won’t pay ${esc(host(b.dataset.o))} any more`); }));
  const LIMF = [['per_purchase', 'A purchase'], ['per_day', 'A day (24 hours)'], ['per_month', '30 days']];
  UI.act('w-limits', (b) => { const c = card(b.dataset.c), cur = c.limits.currency || 'USD';
    const fm = UI.form({ caps: esc(c.name), title: 'Change the limits', intro: `In ${esc(cur)}. Raising one asks for your passkey; lowering one doesn’t.`, edit: true, values: Object.assign({}, c.limits),
      fields: LIMF.map(([k, l]) => ({ k, type: 'number', req: true, label: l, validate: (v) => (typeof v === 'number' && v > 0 ? '' : 'An amount above 0.') })), submit: 'Save', submitIcon: 'check',
      onSubmit: (v) => { if (v.per_purchase > v.per_day || v.per_day > v.per_month) return 'A purchase’s limit fits inside a day’s, and a day’s inside 30 days’.'; const up = LIMF.some(([k]) => v[k] > c.limits[k]); UI.closeModal();
        gate(up, `Raise ${esc(c.name)}’s limits`, say(`The card <b>${esc(c.name)}</b>’s limits become ${LIMF.map(([k, l]) => `${l.toLowerCase()} ${money(c.limits[k])} → <b>${money(v[k])}</b>`).join(', ')}.`), () => { LIMF.forEach(([k]) => { c.limits[k] = v[k]; }); c.updated = now(); audit('browser_card_bind', `card ${c.name} · limits${up ? ' · widening' : ''}`); done('Saved', 'the new limits apply to the next purchase'); }, 'Save with passkey'); return false; } }); moneyize(fm, LIMF.map(([k]) => k)); });

  /* ---------- Add a card ---------- */
  function newCardPage() {
    return `<div class="page dl-page wl-page">${UI.head('Add a card', { crumbs: [['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], ['Add a card']] })}
      <form class="dl-card wl-form" id="w-nc" novalidate>
        <div class="callout small wl-tip">${icon('shield')}<span><b>Use a limited card.</b> A virtual or single-use card with its own limits (a masked card from 1Password, Privacy.com, Stripe Issuing) is the second layer behind conductor’s checks.</span></div>
        <div class="field" data-field="name"><label for="w-nc-name">Name for this card <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-nc-name" placeholder="household" autocomplete="off"><div class="hint">How you and your bots refer to this card, on purchases and receipts. Letters, digits, - and _.</div><div class="ferr" role="alert"></div></div>
        <div class="field" data-field="kind"><span class="flabel">Kind</span><div class="seg" role="group" aria-label="Kind" id="w-nc-kind">${[['virtual', 'Virtual'], ['single-use', 'Single-use'], ['physical', 'Physical']].map(([k, l], i) => `<button type="button" data-act="w-kind" data-v="${k}" class="${i ? '' : 'on'}" aria-pressed="${!i}">${l}</button>`).join('')}</div><div class="hint" id="w-nc-kh">A virtual card with its own limits: good.</div></div>
        <h3 class="wl-h">The card</h3><p class="dl-sub">Each typed once (stored write-only) or from a vault item or an environment variable. The page will only ever show the last four digits.</p>
        <div class="wl-grid2">${valueField('number', 'Card number', false)}${valueField('expiry', 'Expiry (MM/YY)', false)}${valueField('cvc', 'Security code')}${valueField('cardname', 'Name on the card')}</div>
        <h3 class="wl-h">Limits</h3><p class="dl-sub">In US dollars; checked when a purchase is proposed, when you approve it and when it’s paid. A purchase fits inside a day, and a day inside 30 days.</p>
        <div class="wl-grid3">${LIMF.map(([k, l]) => moneyField(k, l)).join('')}</div>
        <h3 class="wl-h">Where, and who</h3>
        ${rowsField('merchants', 'Shops it may pay', 'Each shop’s exact address (https:// and the host). None by default: until you add one, nothing can be bought with it.')}
        ${whoField('Bots and teams. Each may propose purchases; every one waits for your approval.')}
        <div class="form-err callout danger small hide" role="alert" id="w-nc-err"></div>
        <div class="wizard-foot"><a class="btn ghost" href="#/browser/cards">Cancel</a><span class="spacer"></span><button type="button" class="btn primary" data-act="w-nc-save">${icon('passkey')}Save with passkey</button></div>
      </form></div>`;
  }
  UI.act('w-kind', (b) => { b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); UI.$('#w-nc-kh').textContent = b.dataset.v === 'physical' ? 'Conductor will remind you to prefer a virtual or single-use card.' : b.dataset.v === 'single-use' ? 'A single-use card: good for one purchase.' : 'A virtual card with its own limits: good.'; });
  UI.act('w-nc-save', () => { const f = UI.$('#w-nc'), name = UI.$('#w-nc-name').value.trim(), ms = rowsOf(UI.$('#w-nc'), 'merchants'), ids = whoOf(UI.$('#w-nc')), kind = (f.querySelector('#w-nc-kind .on') || {}).dataset.v || 'virtual';
    let n = 0; n += bad(f, 'name', !/^[A-Za-z0-9_-]{1,40}$/.test(name) ? 'Letters, digits, - and _, up to 40.' : card(name) ? 'A card by that name exists.' : '') ? 1 : 0;
    const lim = {}; LIMF.forEach(([k]) => { const v = moneyOf(f, k); lim[k] = v; n += bad(f, k, v > 0 ? '' : 'An amount in dollars, above 0 (up to two decimals).') ? 1 : 0; });
    if (!n && (lim.per_purchase > lim.per_day || lim.per_day > lim.per_month)) n += bad(f, 'per_purchase', 'A purchase fits inside a day, and a day inside 30 days.') ? 1 : 0;
    { const box = f.querySelector('[data-field="merchants"]'); box.querySelector('.ferr').textContent = ms.err; if (ms.err) n++; } n += bad(f, 'who', ids.length ? '' : 'Add at least one bot or team.') ? 1 : 0;
    const vals = readValues(f); if (!vals) n++;
    if (vals && !vals.number) { needValue(f, 'number'); n++; } if (vals && !vals.expiry) { needValue(f, 'expiry'); n++; }
    if (n) { const first = f.querySelector('.field.bad input, .field.bad select'); if (first) first.focus(); return; }
    gate(true, `Add a card: ${esc(name)}`, say(`A new ${esc(KIND[kind].toLowerCase())} <b>${esc(name)}</b> for <b>${esc(list(ids.map(who)))}</b>, ${ms.xs.length ? `at ${esc(list(ms.xs.map(host)))}` : 'at no shop yet'}, up to ${money(lim.per_purchase)} a purchase, ${money(lim.per_day)} a day and ${money(lim.per_month)} in 30 days. Its details are stored write-only.`), () => {
      clearTyped(f); const fields = {}; [['number', 'number'], ['expiry', 'expiry'], ['cvc', 'cvc'], ['cardname', 'name']].forEach(([a, k]) => { if (vals[a]) fields[k] = vals[a]; });
      // NEW: the last four digits and the brand come from the daemon when it stores the number (a typed one is never echoed)
      D.wallet.cards.push({ name, method: 'stored', editable: true, kind, brand: 'Visa', last4: '4242', added: now(), updated: now(), fields, merchants: ms.xs, identities: ids, limits: Object.assign({ currency: 'USD' }, lim), identity_limits: {}, frozen: false });
      audit('browser_card_add', `card ${name} · ${ids.join(', ')} · values not recorded · widening`); S.save(); UI.go(`#/browser/cards/${name}`); UI.toast('Card added', `${esc(name)}: values stored write-only`, 'ok'); }, 'Save with passkey'); });

  /* ---------- a purchase ---------- */
  function purchasePage(p) {
    const st = UI.puStatus(p), [s, word] = pst(p), pend = st === 'pending', c = card(p.card) || {};
    const ops = (D.payments || {}).approvers || [];
    const ask = `${esc(byWord(p))} wants to buy this with the ${esc(p.card)} card. You approve exactly these terms; ${p.bot ? 'it' : 'the agent'} can then pay once, on ${esc(host(p.merchant))} only.`;
    const origin = /^operator:/.test(p.requester || '') ? `${icon('users')}Asked for by ${p.requester === 'operator:ops' ? 'you' : esc(p.requester.slice(9))}${/^dir:/.test(p.identity) ? `, in an agent you started in <span class="mono">${esc(p.identity.slice(4))}</span>` : ''}.` : `${icon('warn')}Asked for by <span class="mono">${esc(p.requester || 'someone else')}</span>.`;
    const ticket = p.case && (D.cases || []).find((x) => x.id === p.case);
    const auditD = `<details class="dl-audit-d"><summary>${icon('chev')}For audit: the purchase, its identity and the digest your passkey signs</summary><dl class="dl-facts"><dt>Purchase</dt><dd class="mono">${esc(p.id)}</dd><dt>Card</dt><dd class="mono">${esc(p.card)}</dd><dt>Merchant</dt><dd class="mono">${esc(p.merchant)}</dd><dt>Identity</dt><dd class="mono">${esc(p.identity)}</dd>${p.bot_proposal ? `<dt>Bot proposal</dt><dd class="mono">${esc(p.bot)} ${esc(p.bot_proposal)}</dd>` : ''}${p.supersedes ? `<dt>Replaces</dt><dd class="mono">${esc(p.supersedes)}</dd>` : ''}<dt>Digest</dt><dd class="mono">${esc(p.digest)}</dd><dt>Signed by</dt><dd>${p.approver ? esc(p.approver) : 'not yet: your passkey signs this digest'}</dd></dl></details>`;
    const card2 = `<article class="dl-card dl-proposal wl-pu${pend ? '' : ' decided'}">${pend ? `<div class="dl-kick">${UI.mark('ember')}Needs you · <span class="nowrap">asked ${UI.agoText(p.created)}</span>${p.expires ? ` · <span class="nowrap">expires in ${UI.humanSpan(Math.max(1, Math.round((Date.parse(p.expires) - Date.now()) / 60000)) + 'm')}</span>` : ''}</div>` : UI.purchaseDecided(p) || `<div class="dl-kick dl-sub">${UI.statusMark(s, word, { sm: true })} · ${UI.agoText(p.created)}</div>`}
      <h2 class="dl-ptitle">${esc(UI.purchaseTitle(p))}</h2>${pend ? `<p class="dl-ask">${ask}</p>` : ''}
      <div class="prop-body">${UI.purchaseWhat(p)}
        ${p.why ? `<div class="dl-pwhy"><span class="dl-lbl">Why</span><p>${UI.taskWords(p.why, true)}</p></div>` : ''}
        <p class="dl-origin">${origin}</p>
        ${receiptHTML(p)}
        ${ticket ? `<p class="dl-sub prop-can-link"><a class="dl-link" href="#/tickets/${ticket.id}">The ticket: ${esc(ticket.title)} →</a></p>` : ''}${auditD}</div>
      ${pend ? `<div class="dl-decide prop-decide"><button class="btn lg primary" data-act="w-pu-decide" data-p="${esc(p.id)}" data-d="approve">${icon('passkey')}Approve with passkey</button><button class="btn lg secondary" data-act="w-pu-decide" data-p="${esc(p.id)}" data-d="deny">Deny…</button><span class="hint">${ops.length ? `Only ${esc(ops.join(' or '))} can approve.` : 'No operator can approve yet.'}${c.frozen ? ' The card is frozen: unfreeze it first.' : ''}</span></div>` : ''}</article>`;
    return `<div class="page dl-page wl-page">${UI.crumbs([['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], [UI.purchaseTitle(p)]])}<div class="prop-single">${card2}</div></div>`;
  }
  /* ---------- the signing sheet: what you're agreeing to in words first; ids and the digest only under For audit ----------
     One sheet for every purchase decision: a bot's (its approval) and an agent's (the purchase page). Its confirm row stays on
     screen on a phone (the body scrolls between the title and the buttons). */
  const askedWord = (p) => (/^operator:/.test(p.requester || '') ? (p.requester === 'operator:ops' ? 'you' : p.requester.slice(9)) : p.requester ? `someone else (${p.requester})` : 'you');
  const itemsList = (p) => list((p.items || []).map((it) => `${it.qty > 1 ? `${it.qty} × ` : ''}${it.name}`)) || 'a purchase';
  /** Sign a purchase decision. `onSigned(reason, device)` applies it once the passkey answers. */
  UI.purchaseSeal = (p, deny, onSigned) => { const c = card(p.card) || {}, tk = p.case && ticketOf(p.case), by = byWord(p), cur = p.currency, dev = UI.authenticator();
    const ls = deny ? [] : UI.limitLayers({ cardN: p.card, bot: p.bot, identity: p.identity, caseId: p.case, total: p.total, excludeId: p.id }), bind = ls.find((l) => l.bind);
    const say = deny ? `You’re refusing to let <b>${esc(by)}</b> buy ${esc(itemsList(p))} for ${money(p.total, cur)}${perWord(p)}. Nothing is paid, and ${esc(by)} is told.`
      : `You’re letting <b>${esc(by)}</b> buy <b>${esc(itemsList(p))}</b> for <b>${money(p.total, cur)}</b>${p.recurring ? `, charged every ${esc(p.interval || 'period')} until it’s cancelled at the shop` : ', once'}, at ${esc(host(p.merchant))}, with the ${esc(p.card)} card.`;
    const s2 = UI.modal(`<div class="modal-head"><div><div class="caps">${deny ? 'Deny' : 'Approve'} a purchase · signed with your passkey</div><h2>${deny ? 'Deny' : 'Approve'}: ${esc(UI.purchaseTitle(p))}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body wl-seal-body"><p class="seal-words">${say}</p>
        <dl class="dl-where wl-seal-where"><dt>Total</dt><dd><b>${money(p.total, cur)}</b> ${esc(cur)}${p.recurring ? ` every ${esc(p.interval || 'period')}: a subscription` : ', one payment'}</dd>
          <dt>Card</dt><dd>${esc(p.card)} · ${esc(cardName(c))}</dd><dt>Shop</dt><dd>${esc(host(p.merchant))}</dd>
          <dt>For</dt><dd>${tk ? `the ticket “${esc(tk.title)}”, ` : ''}asked for by ${esc(askedWord(p))}</dd>
          ${bind ? `<dt>Tightest limit</dt><dd>${esc(bind.key === 'ticket' ? 'This ticket’s budget' : bind.name)}: ${roomWords(bind, ls.cur)}</dd>` : ''}
          ${deny ? '' : `<dt>Then</dt><dd>${esc(by)} may pay once, within ${esc(ttlWord())}, on ${esc(host(p.merchant))}’s checkout page. Conductor fills the card itself; ${esc(by)} never sees it.</dd>`}</dl>
        ${deny ? `<div class="field"><label for="seal-reason">Tell ${esc(by)} why <span class="opt">optional</span></label><textarea class="textarea" id="seal-reason" rows="2" maxlength="500" placeholder="e.g. Not this month."></textarea></div>` : ''}
        <details class="dl-audit-d wl-seal-audit"><summary>${icon('chev')}For audit: what your passkey signs</summary><dl class="dl-facts"><dt>Purchase</dt><dd class="mono">${esc(p.id)}</dd>${p.bot_proposal ? `<dt>The bot’s approval</dt><dd class="mono">${esc(p.bot)} · ${esc(p.bot_proposal)}</dd>` : ''}${p.case ? `<dt>Ticket</dt><dd class="mono">${esc(p.case)}</dd>` : ''}<dt>Merchant</dt><dd class="mono">${esc(p.merchant)}</dd><dt>Identity</dt><dd class="mono">${esc(p.identity)}</dd><dt>Decision</dt><dd class="mono">${deny ? 'deny' : 'approve'}</dd><dt>Digest</dt><dd class="mono">${esc(p.digest)}</dd></dl></details>
        <div class="seal" id="seal"><div id="seal-msg"><b>Confirm with your passkey</b><div class="muted small">This signature covers only this purchase, once.</div></div></div></div>
      <div class="modal-foot"><span class="proto-note">authenticator simulated</span><button class="btn ghost" data-close>Cancel</button><button class="btn ${deny ? 'danger' : 'primary'}" id="seal-go">${icon('passkey')}${deny ? 'Deny with passkey' : 'Approve with passkey'}</button></div>`, { sticky: true, cls: 'wl-seal' });
    UI.$('#seal-go', s2).addEventListener('click', (e) => { const seal = UI.$('#seal', s2), reason = deny ? ((UI.$('#seal-reason', s2) || {}).value || '').trim() : ''; e.currentTarget.disabled = true; seal.classList.add('signing');
      UI.$('#seal-msg', s2).innerHTML = `<b>Confirm on your ${esc(dev.split(' · ')[0])}…</b>`;
      setTimeout(() => { seal.classList.remove('signing'); seal.classList.add(deny ? 'denied' : 'done'); UI.$('#seal-msg', s2).innerHTML = `<b>${deny ? 'Denied' : 'Approved'}</b>`;
        setTimeout(() => { UI.closeModal(); onSigned(reason, dev); const mn = UI.$('#main'); if (mn && mn.scrollTo) mn.scrollTo(0, 0); window.scrollTo(0, 0); }, UI.fast ? 30 : 400); }, UI.fast ? 40 : 900); }); };
  /** A bot's purchase is its approval: sign it here, and the approval and the purchase both say so. */
  UI.approvePurchase = (prop, decision, onDone) => { const pu = (D.purchases || []).find((x) => x.id === prop.purchase); if (!pu) return false; const deny = decision === 'deny';
    UI.purchaseSeal(pu, deny, (reason, dev) => { Object.assign(prop, { status: deny ? 'denied' : 'approved', approver: `ops (passkey · ${dev.split(' · ')[0]})`, decided: now() }); if (reason) prop.reason = reason;
      Object.assign(pu, { approver: 'ops', decided: now() }); if (reason) pu.reason = reason; if (!deny) pu.expires = new Date(Date.now() + 36e5).toISOString();
      if (deny) (D.chats[prop.bot] = D.chats[prop.bot] || []).push({ who: 'op', at: now(), text: `Denied buying ${itemsList(pu)}${reason ? `: ${reason}` : '.'}` });
      D.attention = D.attention.filter((a) => !String(a.href).includes(`/proposals/${prop.id}`));
      audit(deny ? 'browser_purchase_deny' : 'browser_purchase_approve', `${UI.botName(prop.bot)}: ${itemsList(pu)} for ${pu.total.toFixed(2)} ${pu.currency} at ${host(pu.merchant)}${pu.case ? ` · ticket ${pu.case}` : ''}${reason ? ' · reason given' : ''}`);
      S.save(); UI.refreshChrome(); UI.toast(deny ? 'Denied' : 'Approved', deny ? `${esc(UI.botName(prop.bot))} won’t buy it; it was told` : `${esc(UI.botName(prop.bot))} may pay once, within ${esc(ttlWord())}`, deny ? 'fail' : 'ok'); if (onDone) onDone(); });
    return true; };
  /** After you decide: what happened, by whom, when, and what's next, at the top of the purchase (a bot's or an agent's). */
  UI.purchaseDecided = (p) => { const st = UI.puStatus(p), r = p.receipt, by = byWord(p), when = p.decided ? UI.agoText(p.decided) : '', who = /^ops/.test(p.approver || '') ? 'you' : p.approver || '';
    const [mk, head, next] = st === 'approved' ? ['ok', `Approved${who ? ` by ${who}` : ''}`, `${by} may pay once${p.expires ? `, before ${UI.hm(p.expires)}` : ''}, on ${host(p.merchant)}’s checkout page. The receipt will show here.`]
      : st === 'denied' ? ['fail', `Denied${who ? ` by ${who}` : ''}`, `Nothing was paid. ${by} was told${p.reason ? `: “${p.reason}”` : '.'}`]
      : st === 'completed' ? [r && r.note ? 'wait' : 'ok', r && r.note ? 'Paid, more than you approved' : 'Paid', r ? `Order ${r.order_number}, ${money(r.total, r.currency || p.currency)} charged${r.note ? `. ${r.note}` : ''}.` : '']
      : st === 'used' ? ['run', 'Paying now', `${by} is at the checkout; the receipt will show here.`]
      : st === 'refused' ? ['fail', 'Not paid', p.reason || ''] : st === 'expired' ? ['idle', 'Expired', 'The approval lapsed unused. Nothing was paid.'] : [null];
    if (!mk) return ''; return `<div class="wl-done" data-s="${mk}">${UI.mark(mk, { lg: true })}<div><b>${esc(head)}</b>${when ? ` <span class="dl-sub">· ${esc(when)}</span>` : ''}${next ? `<p>${esc(next)}</p>` : ''}</div></div>`; };

  // approve or deny: signed with your passkey (the CLI signs with the operator's SSH key; the dashboard with a passkey)
  UI.act('w-pu-decide', (b) => { const p = (D.purchases || []).find((x) => x.id === b.dataset.p), deny = b.dataset.d === 'deny', c = card(p.card) || {};
    if (!deny && c.frozen) return UI.toast(`${esc(p.card)} is frozen`, 'unfreeze it before approving a purchase with it', 'fail');
    UI.purchaseSeal(p, deny, (reason) => { Object.assign(p, { status: deny ? 'denied' : 'approved', approver: 'ops', decided: now() }); if (reason) p.reason = reason; if (!deny) p.expires = new Date(Date.now() + 36e5).toISOString();
      audit('browser_purchase_' + (deny ? 'deny' : 'approve'), `${byWord(p)}: ${itemsList(p)} for ${p.total.toFixed(2)} ${p.currency} at ${host(p.merchant)}${reason ? ' · reason given' : ''}`);
      D.attention = D.attention.filter((a) => !String(a.href).includes(p.id)); S.save(); UI.rerender(); UI.refreshChrome();
      UI.toast(deny ? 'Denied' : 'Approved', deny ? 'nothing will be paid' : `it may pay once, within ${esc(ttlWord())}`, deny ? 'fail' : 'ok'); }); });

  /* ---------- the routes ---------- */
  UI.walletPage = (sec) => (sec === 'details' ? detailsPage() : cardsPage());
  UI.route('browser/:sec/:name', ({ sec, name }) => {
    if (sec === 'details') { if (name === 'new') return newProfilePage(); const p = profile(name); return p ? profilePage(p) : `<div class="page"><div class="empty"><span class="serif">No personal details by that name</span><a class="btn" href="#/browser/details">Personal details</a></div></div>`; }
    if (sec === 'cards') { if (name === 'new') return newCardPage(); const c = card(name); return c ? cardPage(c) : `<div class="page"><div class="empty"><span class="serif">No card by that name</span><a class="btn" href="#/browser/cards">Payment cards</a></div></div>`; }
    if (sec === 'purchases') { const p = (D.purchases || []).find((x) => x.id === name); if (!p) return `<div class="page"><div class="empty"><span class="serif">No purchase here</span><a class="btn" href="#/browser/cards">Payment cards</a></div></div>`;
      // a bot's waiting purchase is decided on its proposal, in the bot's Approvals
      if (p.bot && p.bot_proposal && UI.puStatus(p) === 'pending') { setTimeout(() => location.replace(puHref(p))); return ''; }
      return purchasePage(p); }
    setTimeout(() => location.replace('#/browser')); return '';
  }, { nav: 'browser' });

  /* ---------- a ticket's purchases: what was asked for, decided and paid ---------- */
  UI.ticketPurchases = (c) => { const P = (D.purchases || []).filter((p) => p.case === c.id).sort((a, b) => Date.parse(b.created) - Date.parse(a.created)); if (!P.length) return '';
    return `<section class="panel wl-tk"><div class="panel-head"><h3>Purchases</h3></div><ul class="cm-side-l">${P.map((p) => { const [s, word] = pst(p), r = p.receipt;
      return `<li><a class="cm-side-a" href="${puHref(p)}"><b>${UI.mark(r && r.note ? 'wait' : s)} ${esc(itemsWord(p))}</b><span class="dl-sub">${r ? `${r.note ? 'Charged more than approved' : 'Paid'} · order <span class="mono">${esc(r.order_number)}</span> · ${money(r.total, r.currency)}` : `${esc(word)} · ${money(p.total, p.currency)}${perWord(p)}`} · ${esc(host(p.merchant))}</span></a></li>`; }).join('')}</ul></section>`; };

  /* ---------- a ticket's spending limit: spend_limit { amount, currency, card, merchants } on a bot's case ---------- */
  /** The lowest 30-day cap above a ticket's budget (its bot's, the bot's on that card, its team's, the card's): a budget
      above it is clamped there at every check, and setting one says so. */
  const lowest30 = (bot, cardN) => { const xs = [], bc = botCfgOf(bot).purchases, t = botCfgOf(bot).team, tc = t && S.getPath(teamCfgOf(t), 'budget.purchases'), c = cardN && card(cardN);
    if (bc) { xs.push([bc.per_month, `${UI.botName(bot)}’s 30-day cap`]); if (bc.cards && bc.cards[cardN]) xs.push([bc.cards[cardN].per_month, `${UI.botName(bot)}’s 30-day cap on ${cardN}`]); }
    if (tc) xs.push([tc.per_month, `team ${t}’s 30-day cap`]); if (c) xs.push([c.limits.per_month, `card ${cardN}’s 30-day limit`]);
    return xs.sort((x, y) => x[0] - y[0])[0]; };
  const clampWarn = (bot, sl) => { if (!sl || !bot) return ''; const lo = lowest30(bot, sl.card); return lo && Number(sl.amount) > lo[0] ? `Above ${lo[1]} (${money(lo[0])}), so it counts as ${money(lo[0])}, never more.` : ''; };
  /** The backend's classification of a budget change: a new or larger one (or a looser card or shop) widens. */
  const budgetChange = (b, a) => { const amt = (x) => (x ? Number(x.amount) : 0);
    if (!a && !b) return 'neutral'; if (!a) return 'narrowing';
    if (!b || amt(a) > amt(b) || a.currency !== b.currency || (b.card && a.card !== b.card) || ((b.merchants || []).length && (!(a.merchants || []).length || a.merchants.some((m) => !b.merchants.includes(m))))) return 'widening';
    return 'narrowing'; };
  UI.ticketBudget = (c) => { const sl = c.spend_limit, nm = c.bot ? UI.botName(c.bot) : c.team ? `whichever bot on team ${c.team} takes it` : 'whoever takes it';
    if (!sl && !c.bot && !c.team) return '';
    if (!sl) return `<section class="panel wl-tkb"><div class="panel-head"><h3>Spending limit</h3></div><div class="panel-body"><p class="dl-sub">None: what ${esc(nm)} buys for this ticket is held to ${c.bot ? 'its' : 'their'} own caps and the card’s limits.</p><button class="btn sm secondary" data-act="w-tkb" data-t="${c.id}">${icon('plus')}Set a spending limit</button></div></section>`;
    const amt = Number(sl.amount), cm = UI.ticketCommitted(c.id), left = Math.max(0, Math.round((amt - cm.all) * 100) / 100), w = clampWarn(c.bot, sl);
    return `<section class="panel wl-tkb"><div class="panel-head"><h3>Spending limit</h3><span class="dl-sp"></span><button class="btn sm secondary" data-act="w-tkb" data-t="${c.id}">Change</button></div><div class="panel-body">
      <p class="wl-tkb-big"><b>${money(left, sl.currency)}</b> left of ${money(amt, sl.currency)}</p>${meter(cm.all, amt, sl.currency)}
      <p class="dl-sub">${money(cm.paid, sl.currency)} paid${cm.waiting ? ` · ${money(cm.waiting, sl.currency)} waiting or approved` : ''}. A denied or refused purchase frees its share; what’s left never moves to another ticket.</p>
      ${sl.card || (sl.merchants || []).length ? `<p class="dl-sub">${sl.card ? `Only with ${esc(sl.card)}` : 'With any card it may use'}${(sl.merchants || []).length ? `, only at ${esc(list(sl.merchants.map(host)))}` : ''}.</p>` : ''}
      ${c.bot ? '' : `<p class="dl-sub">It applies to ${esc(nm)}.</p>`}${w ? `<p class="wl-flag">${UI.mark('wait')}<span>${esc(w)}</span></p>` : ''}</div></section>`; };
  /** The fields: an amount ($ inside), optionally one card and some shops (each an exact origin). */
  const budgetFields = (bot, sl = {}) => { const cards = W().cards.filter((c) => !bot || forBot(c.identities, (S.view().bots.find((b) => b.name === bot) || { name: bot })));
    return `${moneyField('budget', 'Up to, for this ticket', 'Everything proposed under it counts until it’s denied, refused or expires.')}
      <div class="field" data-field="bcard"><label for="w-bcard">Only with this card <span class="opt">optional</span></label><select class="select" id="w-bcard"><option value="">Any card it may use</option>${cards.map((c) => `<option value="${esc(c.name)}"${sl.card === c.name ? ' selected' : ''}>${esc(c.name)} · ${esc(cardName(c))}</option>`).join('')}</select><div class="ferr" role="alert"></div></div>
      ${rowsField('bmerch', 'Only at these shops', 'Optional: none means any shop the card may pay.')}`; };
  const fillBudget = (root, sl) => { if (!sl) return; const i = root.querySelector('[data-money="budget"]'); if (i) i.value = Number(sl.amount).toFixed(2); const box = root.querySelector('[data-rows="bmerch"]'); (sl.merchants || []).forEach((m) => box.insertAdjacentHTML('beforeend', rowHTML('bmerch', m))); };
  /** Read the budget fields: undefined when left empty, null (with the reasons shown) when wrong. */
  const readBudget = (root, required) => { const raw = (root.querySelector('[data-money="budget"]') || {}).value || '', ms = rowsOf(root, 'bmerch'), mbox = root.querySelector('[data-field="bmerch"]');
    mbox.querySelector('.ferr').textContent = ms.err;
    if (!raw.trim() && !ms.xs.length && !root.querySelector('#w-bcard').value && !required) { bad(root, 'budget', ''); return undefined; }
    const amt = moneyOf(root, 'budget'), e1 = bad(root, 'budget', amt > 0 ? '' : 'An amount in dollars, above 0 (up to two decimals).');
    if (e1 || ms.err) return null;
    const sl = { amount: amt.toFixed(2), currency: 'USD' }, cd = root.querySelector('#w-bcard').value; if (cd) sl.card = cd; if (ms.xs.length) sl.merchants = ms.xs; return sl; };
  UI.act('w-tkb', (b) => { const c = ticketOf(b.dataset.t), before = c.spend_limit || null;
    const s2 = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(c.title)}</div><h2>${before ? 'Change the spending limit' : 'Set a spending limit'}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-tkb">${budgetFields(c.bot, before || {})}<p class="dl-sub" id="w-tkb-warn"></p></div>
      <div class="modal-foot">${before ? '<button class="btn ghost danger-text" id="w-tkb-rm" style="margin-right:auto">Remove the limit</button>' : ''}<button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-tkb-go">Save</button></div>`);
    const root = UI.$('#w-tkb', s2); fillBudget(root, before);
    const apply = (next) => { const ch = budgetChange(before, next); UI.closeModal();
      gate(ch === 'widening', `${next ? 'Let it spend up to ' + money(Number(next.amount)) : 'Remove the limit'} on this ticket`, say(next ? `The ticket <b>“${esc(c.title)}”</b> may spend up to <b>${money(Number(next.amount))}</b>${next.card ? `, only with ${esc(next.card)}` : ''}${(next.merchants || []).length ? `, only at ${esc(list(next.merchants.map(host)))}` : ''}.` : `The ticket <b>“${esc(c.title)}”</b> has no spending limit of its own.`), () => {
        if (next) c.spend_limit = next; else delete c.spend_limit; audit('browser_case_budget', `ticket ${c.id} · ${next ? `${next.amount} ${next.currency}` : 'removed'} · ${ch}`);
        const w = clampWarn(c.bot, next); done(next ? 'Spending limit saved' : 'Spending limit removed', w || (next ? `${money(Number(next.amount))} for this ticket` : 'purchases fall back to its own caps and the card’s')); }, 'Save with passkey'); };
    UI.$('#w-tkb-go', s2).addEventListener('click', () => { const next = readBudget(root, true); if (next) apply(next); });
    const rm = UI.$('#w-tkb-rm', s2); if (rm) rm.addEventListener('click', () => apply(null)); });
  /** New ticket: an optional spending limit (the ticket store records spend_limit on the case). */
  UI.ticketBudgetFields = () => `<details class="tk-l wl-tkb-new" id="w-nt-b"><summary class="dl-lbl">${icon('chev')}Spending limit <span class="opt">optional</span></summary><div class="wl-tkb-body">${budgetFields('')}</div></details>`;
  UI.readTicketBudget = (root) => { const d = root.querySelector('#w-nt-b'); if (!d) return undefined; const r = readBudget(d, false); if (r === null) d.open = true; return r; };
  /** A new budget widens (the backend's rule), so a ticket created with one asks for the passkey first. */
  UI.ticketBudgetGate = (title, sl, fn, stage = 'ready') => gate(true, `${stage === 'draft' ? 'Save the draft' : 'Create the ticket'} with a spending limit of ${money(Number(sl.amount))}`, say(`A new ticket <b>“${esc(title)}”</b>, ${stage === 'draft' ? 'saved as a draft' : 'ready to start'}, that may spend up to <b>${money(Number(sl.amount))}</b>${sl.card ? `, only with ${esc(sl.card)}` : ''}${(sl.merchants || []).length ? `, only at ${esc(list(sl.merchants.map(host)))}` : ''}.`), fn, stage === 'draft' ? 'Save draft with passkey' : 'Create with passkey');

  /* ---------- a bot's What it can do: Can buy, buying without asking, the details it can fill ---------- */
  const forBot = (ids, m) => (ids || []).some((i) => i === 'bot:' + m.name || (m.cfg && m.cfg.team && i === 'team:' + m.cfg.team));
  const UNV = 'browser.purchase';
  UI.botWalletHTML = (m) => { const cfg = S.staged('bots', m.name) || m.cfg || {}, cards = W().cards.filter((c) => forBot(c.identities, m)), profs = W().profiles.filter((p) => forBot(p.identities, m));
    const verbs = S.getPath(cfg, 'skill.verbs') || [], canVerb = (Array.isArray(verbs) ? verbs : Object.keys(verbs)).includes(UNV);
    if (!cards.length && !profs.length && !canVerb) return '';
    const un = (S.getPath(cfg, 'autonomy.unattended') || {})[UNV];
    const shadow = S.getPath(cfg, 'autonomy.shadow');
    // the effective caps on a card: the lowest of every layer above a ticket, each dimension on its own
    const effOn = (c) => { const L = [['bot', cfg.purchases], ['botcard', cfg.purchases && cfg.purchases.cards && cfg.purchases.cards[c.name]], ['team', cfg.team && S.getPath(teamCfgOf(cfg.team), 'budget.purchases')], ['caller', (c.identity_limits || {})['bot:' + m.name] || (cfg.team && (c.identity_limits || {})['team:' + cfg.team])], ['card', c.limits]].filter(([, x]) => x);
      const W2 = { card: 'the card', caller: `the card for ${UI.botName(m.name)}`, bot: 'its own cap', botcard: `its cap on ${c.name}`, team: `team ${cfg.team}` };
      const out = {}; ['per_purchase', 'per_day', 'per_month'].forEach((d) => { const [k, v] = L.filter(([, x]) => x[d] != null).sort((x, y) => x[1][d] - y[1][d])[0] || []; out[d] = { v: v && v[d], by: W2[k] }; }); return out; };
    const cardLine = (c) => { const cur = c.limits.currency || 'USD', e = effOn(c), s = spentWhere((p) => p.bot === m.name && p.card === c.name);
      return `<li class="wl-can"><p><b>Can buy:</b> ${c.frozen ? `nothing with <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> while it’s frozen.` : (c.merchants || []).length ? `asks you first, up to ${money(e.per_purchase.v, cur)} a purchase, ${money(e.per_day.v, cur)} a day and ${money(e.per_month.v, cur)} in 30 days, at ${esc(list(c.merchants.map(host)))}, with <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> (${esc(cardName(c))}).` : `nothing yet: <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> has no shop.`}</p>
        <p class="dl-sub wl-by">The lowest limit wins: a purchase is set by ${esc(e.per_purchase.by)}, a day by ${esc(e.per_day.by)}, 30 days by ${esc(e.per_month.by)}. A ticket’s spending limit can only lower them.</p>
        <div class="wl-lrow"><span class="wl-ll">Last 30 days</span><span class="wl-lv"><b>${money(s.month, cur)}</b> of ${money(e.per_month.v, cur)}</span>${meter(s.month, e.per_month.v, cur)}</div></li>`; };
    const pc = cfg.purchases, tcap = cfg.team && S.getPath(teamCfgOf(cfg.team), 'budget.purchases');
    const capsLine = `<div class="wl-caps"><p><b>Its own caps:</b> ${pc ? `${money(pc.per_purchase)} a purchase, ${money(pc.per_day)} a day, ${money(pc.per_month)} in 30 days${pc.cards ? `, tighter on ${esc(list(Object.keys(pc.cards)))}` : ''}.` : `none: it’s held to ${tcap ? `team ${esc(cfg.team)}’s caps and ` : ''}the card’s limits.`}${tcap ? ` Team ${esc(cfg.team)}’s are its ceiling: ${money(tcap.per_purchase)} · ${money(tcap.per_day)} · ${money(tcap.per_month)}.` : ''}</p><button class="btn sm secondary" ${canVerb ? `data-act="w-caps" data-kind="bot" data-n="${esc(m.name)}"` : UI.off(`${m.name} can’t buy at all yet`)}>${pc ? 'Change its caps' : 'Set its own caps'}</button></div>`;
    const unLine = un ? `<p><b>Without asking:</b> up to ${money(un.per_action)} a purchase and ${money(un.per_period)} every ${esc(UI.humanSpan(un.period || '24h'))}, at ${esc(list((un.merchants || []).map(host)))}; only for your requests or its own, never a subscription.</p><button class="btn sm secondary" data-act="w-un-off" data-m="${esc(m.name)}">Always ask me</button>`
      : `<p><b>Without asking:</b> never. Every purchase waits for your approval.</p><button class="btn sm secondary" ${!canVerb ? UI.off(`${m.name} can’t buy at all yet`) : shadow ? UI.off(`${m.name} is in practice mode: nothing it does runs`) : `data-act="w-un-on" data-m="${esc(m.name)}"`}>Let it buy small things without asking…</button>`;
    return `<section class="dl-card wl-bot" id="wl-bot"><header class="dl-ch"><h2>Buying, and your details</h2></header>
      ${canVerb || cards.length ? `<ul class="wl-cans">${cards.map(cardLine).join('') || `<li class="wl-can"><p><b>Can buy:</b> nothing yet: no card names ${esc(m.name)}. <a class="dl-link" href="#/browser/cards">Payment cards</a></p></li>`}</ul>
        ${capsLine}<div class="wl-un${un ? ' on' : ''}">${unLine}</div>` : ''}
      ${profs.length ? `<div class="wl-fills"><p><b>Fills your details:</b> ${profs.map((p) => `<a class="dl-link" href="#/browser/details/${esc(p.name)}">${esc(p.name)}</a> on ${esc(list(p.sites.map((s) => `${host(s.origin)} (${list(s.fields.map(siteFieldWord))})`)))}`).join('; ')}. It never sees them${profs.some((p) => learns(p).length) ? `; it may learn ${esc(list([...new Set(profs.flatMap(learns))]))}` : ''}.</p></div>` : ''}
      <p class="dl-sub">The card never reaches it: conductor fills it, only on a shop’s page, only for a purchase you approved.</p></section>`; };
  // buying without asking: bots only, opt-in, a widening; under its spend cap, at named shops (it goes through Review)
  UI.act('w-un-on', (b) => { const m = S.view().bots.find((x) => x.name === b.dataset.m), cfg = JSON.parse(JSON.stringify(S.staged('bots', m.name) || m.cfg)), cap = S.getPath(cfg, 'autonomy.spend_cap') || {};
    const shops = [...new Set(W().cards.filter((c) => forBot(c.identities, m) && !c.frozen).flatMap((c) => c.merchants || []))], team = cfg.team && S.view().teams.find((t) => t.name === cfg.team);
    if (team && S.getPath(team.cfg, 'autonomy.ceiling') !== 'commit_money') return UI.toast('Not on this team', `team ${esc(team.name)}’s ceiling keeps every purchase an approval`, 'fail');
    const fm = UI.form({ caps: `bot ${esc(m.name)} · a widening`, title: 'Buy small things without asking', intro: `${esc(UI.botName(m.name))} may buy without your approval only under these caps, at the shops you tick, and only when you or it asked (never someone else, an event or a check). A subscription always asks.`,
      fields: [{ k: 'per_action', type: 'number', req: true, label: 'Up to, a purchase', validate: (v) => (!(v > 0) ? 'An amount above 0.' : cap.per_action != null && v > cap.per_action ? `It must fit inside its spend cap: ${money(cap.per_action)} an action.` : '') },
        { k: 'per_period', type: 'number', req: true, label: 'Up to, in total', validate: (v, all) => (!(v > 0) ? 'An amount above 0.' : v < all.per_action ? 'At least one purchase’s worth.' : cap.per_period != null && v > cap.per_period ? `It must fit inside its spend cap: ${money(cap.per_period)} a period.` : '') },
        { k: 'period', type: 'seg', label: 'Every', options: [['24h', '24 hours'], ['168h', '7 days'], ['720h', '30 days']], def: '24h' },
        ...shops.map((o, i) => ({ k: 'shop' + i, type: 'bool', label: host(o), on: 'it may buy here without asking' }))],
      note: 'Each purchase still counts against the card’s own limits and shops, and against its spend cap. You review the change before it applies.', submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const merchants = shops.filter((o, i) => v['shop' + i]); if (!merchants.length) return 'Tick at least one shop.';
        const A = (cfg.autonomy = cfg.autonomy || {}); A.unattended = A.unattended || {}; A.unattended[UNV] = { per_action: v.per_action, per_period: v.per_period, period: v.period || '24h', merchants };
        UI.staged(S.stage('bots', m.name, cfg, `let ${m.name} buy up to ${money(v.per_action)} without asking`)); } }); moneyize(fm, ['per_action', 'per_period']); });
  UI.act('w-un-off', (b) => { const m = S.view().bots.find((x) => x.name === b.dataset.m), cfg = JSON.parse(JSON.stringify(S.staged('bots', m.name) || m.cfg));
    if (cfg.autonomy && cfg.autonomy.unattended) { delete cfg.autonomy.unattended[UNV]; if (!Object.keys(cfg.autonomy.unattended).length) delete cfg.autonomy.unattended; }
    UI.staged(S.stage('bots', m.name, cfg, `${m.name} asks before every purchase`)); });
  /** A bot's purchases: or a team's budget.purchases: all three caps, each in dollars; a bot's can't exceed its team's.
      Through the review (it's config): raising a cap widens, setting or lowering one narrows. */
  const CAPF = [['per_purchase', 'A purchase'], ['per_day', 'A day (24 hours)'], ['per_month', '30 days']];
  UI.act('w-caps', (b) => { const isBot = b.dataset.kind === 'bot', n = b.dataset.n, cfg = JSON.parse(JSON.stringify(isBot ? botCfgOf(n) : teamCfgOf(n))), path = isBot ? 'purchases' : 'budget.purchases', cur = S.getPath(cfg, path);
    const ceil = isBot && cfg.team && S.getPath(teamCfgOf(cfg.team), 'budget.purchases'), floor = !isBot && S.view().bots.filter((x) => (x.cfg || {}).team === n && (x.cfg || {}).purchases).map((x) => [x.name, x.cfg.purchases]);
    const s2 = UI.modal(`<div class="modal-head"><div><div class="caps">${isBot ? `bot ${esc(n)}` : `team ${esc(n)}`} · ${esc(path)}</div><h2>${isBot ? 'What it may spend' : 'What its bots may spend'}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-caps"><p class="dl-sub">${isBot ? `On top of the card’s limits${ceil ? ` and inside team ${esc(cfg.team)}’s caps (${money(ceil.per_purchase)} · ${money(ceil.per_day)} · ${money(ceil.per_month)})` : ''}. The lowest wins.` : 'A ceiling for every bot on the team: a bot’s own caps can only be lower.'} All three are needed.</p>
        <div class="wl-grid3">${CAPF.map(([k, l]) => moneyField(k, l)).join('')}</div><div class="form-err callout danger small hide" role="alert" id="w-caps-err"></div></div>
      <div class="modal-foot">${cur ? `<button class="btn ghost danger-text" id="w-caps-rm" style="margin-right:auto">${isBot ? 'Use the team’s and the card’s' : 'No team caps'}</button>` : ''}<button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-caps-go">Review change</button></div>`);
    const root = UI.$('#w-caps', s2); if (cur) CAPF.forEach(([k]) => { root.querySelector(`[data-money="${k}"]`).value = Number(cur[k]).toFixed(2); });
    const stage = (next) => { if (next) S.setPath(cfg, path, Object.assign({}, cur && cur.cards ? { cards: cur.cards } : {}, next, { currency: 'USD' })); else if (isBot) delete cfg.purchases; else if (cfg.budget) delete cfg.budget.purchases;
      UI.closeModal(); UI.staged(S.stage(isBot ? 'bots' : 'teams', n, cfg, `${isBot ? 'bot' : 'team'} ${n}: purchase caps ${next ? CAPF.map(([k]) => next[k]).join(' / ') : 'removed'}`)); };
    UI.$('#w-caps-go', s2).addEventListener('click', () => { const v = {}; let bad2 = 0; CAPF.forEach(([k]) => { v[k] = moneyOf(root, k); bad2 += bad(root, k, v[k] > 0 ? '' : 'An amount in dollars, above 0.') ? 1 : 0; }); if (bad2) return;
      const err = UI.$('#w-caps-err', s2), why = v.per_purchase > v.per_day || v.per_day > v.per_month ? 'A purchase fits inside a day, and a day inside 30 days.' : ceil && CAPF.some(([k]) => v[k] > ceil[k]) ? `Above team ${cfg.team}’s caps: a bot’s can only be lower (${CAPF.filter(([k]) => v[k] > ceil[k]).map(([k, l]) => `${l.toLowerCase()} ${money(ceil[k])}`).join(', ')}).` : floor && floor.length && floor.some(([, c]) => CAPF.some(([k]) => c[k] > v[k])) ? `Below ${list(floor.filter(([, c]) => CAPF.some(([k]) => c[k] > v[k])).map(([x]) => UI.botName(x)))}’s own caps: lower theirs first.` : '';
      err.textContent = why; err.classList.toggle('hide', !why); if (!why) stage(v); });
    const rm = UI.$('#w-caps-rm', s2); if (rm) rm.addEventListener('click', () => stage(null)); });
  /** A team's What its bots may spend, on its Ceilings & scopes tab. */
  UI.teamWalletHTML = (t) => { const tc = S.getPath(teamCfgOf(t.name), 'budget.purchases'), bots = S.view().bots.filter((x) => (x.cfg || {}).team === t.name);
    return `<section class="dl-card wl-team"><header class="dl-ch"><h2>What its bots may spend</h2><span class="dl-sp"></span><button class="btn sm secondary" data-act="w-caps" data-kind="team" data-n="${esc(t.name)}">${tc ? 'Change' : 'Set caps'}</button></header>
      <p>${tc ? `<b>Can buy:</b> each bot up to ${money(tc.per_purchase)} a purchase, ${money(tc.per_day)} a day and ${money(tc.per_month)} in 30 days, as a ceiling: a bot’s own caps, a card’s limits and a ticket’s spending limit can only be lower.` : 'No team caps: each bot is held to its own caps and the cards’ limits.'}</p>
      ${tc ? `<ul class="dl-list wl-teamb">${bots.map((x) => { const pc = (x.cfg || {}).purchases, sp = spentWhere((p) => p.bot === x.name); return `<li class="wl-audit"><span>${UI.botAv ? UI.botAv(x.name, 'xs') : ''} <b>${esc(UI.botName(x.name))}</b> ${pc ? `its own: ${money(pc.per_purchase)} · ${money(pc.per_day)} · ${money(pc.per_month)}` : 'uses the team’s caps'}</span><span class="dl-sub">${money(sp.month)} in 30 days</span></li>`; }).join('')}</ul>` : ''}</section>`; };
  /** browser.purchase in a bot's lists, in words. */
  UI.builtinVerbWords = (v) => ({ 'browser.purchase': 'buy things with a card you allow' }[v] || null);
})();
