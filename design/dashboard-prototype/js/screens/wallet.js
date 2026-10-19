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
  UI.purchaseFacts = (p) => { const c = card(p.card) || {};
    return [p.bot ? { icon: 'obj-bot', value: UI.botName(p.bot), href: `#/bots/${p.bot}`, pri: 1, label: 'Bot' } : { icon: 'obj-sandbox', value: who(p.identity), pri: 2, label: 'Asked for by' },
      { icon: 'store', value: host(p.merchant), pri: 1, label: 'Shop' }, { icon: p.recurring ? 'repeat' : 'act-money', value: `${money(p.total, p.currency)}${perWord(p)}`, risky: true, pri: 1, label: p.recurring ? 'A subscription' : 'Total' },
      { icon: 'obj-card', value: `${p.card} •••• ${c.last4 || ''}`.trim(), href: `#/browser/cards/${p.card}`, pri: 2, label: 'Card' }]; };
  /** The limits a purchase is checked against, with what's been charged: the card's, and the identity's on top. */
  const limitLines = (p) => { const c = card(p.card); if (!c) return []; const L = c.limits, cur = L.currency || 'USD', il = (c.identity_limits || {})[p.identity] || (p.bot && (c.identity_limits || {})['bot:' + p.bot]);
    const s = spent(c.name), si = spent(c.name, p.identity), out = [];
    const line = (lbl, l, sp) => { const xs = [[p.total, l.per_purchase, 'a purchase'], [sp.day + p.total, l.per_day, 'in 24 hours'], [sp.month + p.total, l.per_month, 'in 30 days']], over = xs.some(([a, b]) => a > b + 1e-9);
      return `${over ? UI.mark('fail') + ' ' : ''}<b>${lbl}</b> ${xs.map(([a, b, w]) => `${money(a, cur)} of ${money(b, cur)} ${w}`).join(' · ')}${over ? ' <b>(over: conductor refuses it)</b>' : ''}`; };
    if (il) out.push(line(`${esc(byWord(p))}’s limits:`, il, si));
    out.push(line('The card’s limits:', L, s)); return out; };
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
        <dt>Limits</dt><dd><span class="dl-sub">Counting this purchase:</span>${limitLines(p).map((l) => `<span class="wl-lim">${l}</span>`).join('')}</dd>
        <dt>Then</dt><dd>${esc(byWord(p))} may pay once, within ${esc(ttlWord())}, on ${esc(host(p.merchant))}’s checkout page. Conductor checks the page shows this total, fills the card itself and reads the receipt; if the page asks for more, nothing is paid and you’re asked again. ${p.bot ? esc(UI.botName(p.bot)) : 'The agent'} never sees the card.</dd>
        <dt>Undo</dt><dd>Conductor can’t undo a purchase; a refund is the shop’s.</dd></dl>`; };
  /** The receipt, after the fact: the order number and what was charged, flagged when it's more than you approved. */
  const receiptHTML = (p) => { const r = p.receipt; if (!r) return '';
    return `<div class="wl-receipt${r.note ? ' flag' : ''}"><span class="dl-lbl">Receipt</span><p>${r.note ? UI.mark('wait') : UI.mark('ok')}<span>Order <b class="mono">${esc(r.order_number || '—')}</b> · ${money(r.total, r.currency || p.currency)} charged · ${UI.agoText(r.at)}${r.note ? `<br><b>${esc(r.note)}</b> You were notified; the card’s own provider limits are what stop this.` : ''}</span></p></div>`; };
  const decidedWords = (p) => { const s = UI.puStatus(p), by = p.approver ? ` by ${esc(p.approver)}` : '';
    return { pending: '', approved: `Approved${by}${p.unattended ? ' (without asking: under its own cap)' : ''}. ${esc(byWord(p))} may pay once, before ${UI.hm(p.expires)}.`, used: `Approved${by}; paying now.`, completed: `Approved${by}${p.unattended ? ' (without asking: under its own cap)' : ''}, and paid.`, denied: `Denied${by}${p.reason ? `: “${esc(p.reason)}”` : ''}.`, refused: `Not paid. ${esc(p.reason || '')}`, expired: 'Expired before it was used. Nothing was paid.' }[s] || ''; };

  /* ---------- the tab bar every Browsers page shares ---------- */
  UI.browserTabs = (on) => UI.tabs([['sessions', 'Sessions', '#/browser', D.browsers.length], ['logins', 'Logins', '#/browser/logins', D.access.credentials.length + D.access.logins.length],
    ['details', 'Personal details', '#/browser/details', W().profiles.length], ['cards', 'Payment cards', '#/browser/cards', W().cards.length]], on);

  /* ---------- the passkey gate: only a widening change asks ---------- */
  const audit = (event, detail) => D.audit.unshift({ ts: now(), event, who: 'operator:ops', detail, run: '' });
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
    gate(wider, `Let bots learn more about ${esc(fieldWord(f).toLowerCase())}`, `{ profile: <b>${esc(p.name)}</b>, field: <b>${esc(f)}</b>, visible: <b>${before} → ${v}</b> }`, () => { editP(p.name, (x) => { x.visible = x.visible || {}; if (v === 'hidden') delete x.visible[f]; else x.visible[f] = v; }); audit('browser_profile_bind', `profile ${p.name} · ${f} visible ${v}`); done('Saved', `a bot may learn ${visWord(f, v)}`); }, 'Allow with passkey');
    if (wider) sel.value = before; });
  UI.act('w-field-more', (b) => UI.menu(b, [['Replace the value…', 'updates', 'w-field-set', `data-p="${b.dataset.p}" data-f="${b.dataset.f}"`], ['-'], ['Remove this detail…', 'trash', 'w-field-rm', `data-p="${b.dataset.p}" data-f="${b.dataset.f}"`]]));
  UI.act('w-prof-more', (b) => { const p = profile(b.dataset.p); UI.menu(b, [['See it in the audit log', 'logs', 'w-go', 'data-h="#/logs/audit"'], ['-'], ['Remove these details…', 'trash', 'w-prof-rm', p.editable === false ? UI.off('Defined in the config: remove it there') : `data-p="${esc(p.name)}"`]]); });
  UI.act('w-go', (b) => UI.go(b.dataset.h));
  UI.act('w-prof-rm', (b) => UI.confirm({ title: `Remove ${esc(b.dataset.p)}?`, body: 'Its stored values are deleted, and no bot can fill these details anywhere. Values from a vault stay in the vault.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; D.wallet.profiles = W().profiles.filter((x) => x.name !== b.dataset.p); audit('browser_profile_remove', `profile ${b.dataset.p}`); S.save(); UI.go('#/browser/details'); UI.toast('Removed', `${esc(b.dataset.p)} is gone`, 'ok'); }));
  UI.act('w-field-rm', (b) => UI.confirm({ title: `Remove ${esc(fieldWord(b.dataset.f).toLowerCase())} from ${esc(b.dataset.p)}?`, body: 'Bots can no longer fill it. A typed value is deleted; a vault keeps its own.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; editP(b.dataset.p, (p) => { delete p.fields[b.dataset.f]; if (p.visible) delete p.visible[b.dataset.f]; }); audit('browser_profile_unset', `profile ${b.dataset.p} · ${b.dataset.f}`); done('Removed', `${esc(fieldWord(b.dataset.f))} is no longer set`); }));
  /** A value: typed once (write-only), or a reference to a vault, an environment variable or a secret. */
  const valueField = (k, label, opt = true) => `<div class="field wl-val" data-wv="${esc(k)}"><label for="wv-${esc(k)}">${esc(label)} ${opt ? '<span class="opt">optional</span>' : '<span class="req" aria-label="required">*</span>'}</label>
    <div class="wl-valrow"><div class="seg sm" role="group" aria-label="${esc(label)}: how to give it"><button type="button" class="on" aria-pressed="true" data-act="w-src" data-v="typed">Type it</button><button type="button" aria-pressed="false" data-act="w-src" data-v="ref">A reference</button></div>
      <input class="input" type="password" autocomplete="off" id="wv-${esc(k)}" data-src="typed" placeholder="stored write-only"></div><div class="ferr" role="alert"></div></div>`;
  UI.act('w-src', (b) => { const box = b.closest('.wl-val'), inp = box.querySelector('input'); box.querySelectorAll('[data-act="w-src"]').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); });
    inp.value = ''; inp.dataset.src = b.dataset.v; inp.type = b.dataset.v === 'typed' ? 'password' : 'text'; inp.classList.toggle('mono', b.dataset.v === 'ref'); inp.placeholder = b.dataset.v === 'typed' ? 'stored write-only' : '{{ vault "op" "Me/Home/email" }}'; inp.focus(); });
  const REF = /^(\{\{\s*vault\s+"[\w-]+"\s+"[^"]+"\s*\}\}|\$\{(env|secret)\.[A-Z_][A-Z0-9_]*\})$/;
  /** Read the value fields: { field: {kind, source} }, clearing what was typed. Returns null (and marks the bad ones) on an error. */
  const readValues = (root) => { const out = {}; let bad = 0;
    root.querySelectorAll('.wl-val').forEach((box) => { const inp = box.querySelector('input'), v = inp.value.trim(), err = box.querySelector('.ferr'); err.textContent = ''; box.classList.remove('bad'); if (!v) return;
      if (inp.dataset.src === 'ref' && !REF.test(v)) { err.textContent = 'A reference such as {{ vault "op" "Me/Home/email" }}, ${env.NAME} or ${secret.NAME}.'; box.classList.add('bad'); bad++; return; }
      out[box.dataset.wv] = inp.dataset.src === 'ref' ? { kind: /vault/.test(v) ? 'vault' : /env\./.test(v) ? 'env' : 'secret', source: v } : { kind: 'stored', source: '' }; });
    if (bad) return null; root.querySelectorAll('.wl-val input[data-src="typed"]').forEach((i) => { i.value = ''; }); return out; };
  UI.act('w-field-set', (b) => { const p = profile(b.dataset.p), f = b.dataset.f;
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">write-only</div><h2>Replace ${esc(fieldWord(f).toLowerCase())}</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-set">${valueField(f, fieldWord(f), false)}<div class="hint">The page will say set, never the value.</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-set-go">Save</button></div>`);
    UI.$('#w-set-go', s).addEventListener('click', () => { const v = readValues(UI.$('#w-set', s)); if (!v) return; if (!v[f]) { const box = UI.$('.wl-val', s); box.classList.add('bad'); box.querySelector('.ferr').textContent = 'Required.'; return; }
      editP(p.name, (x) => { x.fields[f] = v[f]; }); audit('browser_profile_set', `profile ${p.name} · ${f} · value not recorded`); UI.closeModal(); done('Saved', `${esc(fieldWord(f))} is set`); }); });
  const FIELDS_ALL = ['name', 'given_name', 'family_name', 'email', 'phone', 'company', 'birthday', 'address.line1', 'address.line2', 'address.city', 'address.region', 'address.postal_code', 'address.country', 'billing.line1', 'billing.line2', 'billing.city', 'billing.region', 'billing.postal_code', 'billing.country'];
  UI.act('w-field-add', (b) => { const p = profile(b.dataset.p), free = FIELDS_ALL.filter((f) => !(p.fields || {})[f]);
    const s = UI.modal(`<div class="modal-head"><div><div class="caps">${esc(p.name)}</div><h2>Add a detail</h2></div><button class="icon-btn" data-close aria-label="Close">${icon('x')}</button></div>
      <div class="modal-body stack" id="w-fa"><div class="field"><label for="w-fa-f">Which detail <span class="req" aria-label="required">*</span></label><select class="select" id="w-fa-f">${free.map((f) => `<option value="${f}">${esc(fieldWord(f))}</option>`).join('')}<option value="custom">Something else…</option></select></div>
        <div class="field hide" id="w-fa-cbox"><label for="w-fa-c">Its name</label><input class="input mono" id="w-fa-c" placeholder="loyalty_number"><div class="hint">Letters, digits and _; the bot asks for custom.&lt;name&gt;.</div><div class="ferr" role="alert"></div></div>
        ${valueField('v', 'Value', false)}<div class="hint">Adding a detail doesn’t fill it anywhere yet: add it to a site below.</div></div>
      <div class="modal-foot"><button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="w-fa-go">Add</button></div>`);
    const sel = UI.$('#w-fa-f', s); sel.addEventListener('change', () => UI.$('#w-fa-cbox', s).classList.toggle('hide', sel.value !== 'custom'));
    UI.$('#w-fa-go', s).addEventListener('click', () => { let f = sel.value; if (f === 'custom') { const c = UI.$('#w-fa-c', s).value.trim(); if (!/^[a-z0-9_]{1,40}$/.test(c)) { UI.$('#w-fa-cbox', s).classList.add('bad'); UI.$('#w-fa-cbox .ferr', s).textContent = 'Letters, digits and _, up to 40.'; return; } f = 'custom.' + c; }
      const v = readValues(UI.$('#w-fa', s)); if (!v) return; if (!v.v) { const box = UI.$('.wl-val', s); box.classList.add('bad'); box.querySelector('.ferr').textContent = 'Required.'; return; }
      editP(p.name, (x) => { x.fields[f] = v.v; }); audit('browser_profile_set', `profile ${p.name} · ${f} · value not recorded`); UI.closeModal(); done('Added', `${esc(fieldWord(f))} is set`); }); });
  const ORIGIN = /^https?:\/\/[a-z0-9.-]+(:\d{1,5})?$/i;
  const originErr = (v) => (ORIGIN.test(v || '') ? '' : 'The exact address: https:// and the host, with a port if it has one; no path.');
  const fieldChoices = (p) => { const ks = Object.keys(p.fields || {}), out = []; ks.forEach((k) => { const top = /^(address|billing)\./.test(k) ? k.split('.')[0] : k; if (!out.includes(top)) out.push(top); }); return out; };
  UI.act('w-site-add', (b) => { const p = profile(b.dataset.p), fc = fieldChoices(p);
    UI.form({ caps: esc(p.name), title: 'Add a site', intro: 'Where these details may be filled, and which of them.', fields: [{ k: 'origin', type: 'string', req: true, label: 'Site (its exact address)', ph: 'https://shop.example.com', validate: originErr },
      ...fc.map((f) => ({ k: 'f_' + f.replace('.', '_'), type: 'bool', label: f === 'address' ? 'The address' : f === 'billing' ? 'The billing address' : fieldWord(f), on: 'fill it here' }))],
      note: 'The browser must already be allowed to reach this site.', submit: 'Add with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { const fields = fc.filter((f) => v['f_' + f.replace('.', '_')]); if (!fields.length) return 'Pick at least one detail to fill there.'; const origin = v.origin.replace(/\/$/, '');
        UI.closeModal(); gate(true, `Fill ${esc(p.name)} on ${esc(host(origin))}`, `{ profile: <b>${esc(p.name)}</b>, add_site: <b>${esc(origin)}</b>, fields: <b>${esc(fields.join(', '))}</b> }`, () => { editP(p.name, (x) => { const cur = (x.sites || []).find((s) => s.origin === origin); if (cur) cur.fields = [...new Set(cur.fields.concat(fields))]; else (x.sites = x.sites || []).push({ origin, fields }); }); audit('browser_profile_bind', `profile ${p.name} · site ${origin} · widening`); done('Added', `${esc(p.name)} may be filled on ${esc(host(origin))}`); }, 'Add with passkey'); return false; } }); });
  UI.act('w-site-rm', (b) => UI.confirm({ title: `Stop filling ${esc(b.dataset.p)} on ${esc(host(b.dataset.o))}?`, body: 'Nothing from these details is filled there any more. This takes effect at once.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; editP(b.dataset.p, (p) => { p.sites = p.sites.filter((s) => s.origin !== b.dataset.o); }); audit('browser_profile_bind', `profile ${b.dataset.p} · removed site ${b.dataset.o}`); done('Removed', `not filled on ${esc(host(b.dataset.o))} any more`); }));
  // bots and teams, by name (a team covers the bots on it)
  const idOpts = (have) => S.view().bots.map((b) => ['bot:' + b.name, `${UI.botName(b.name)} (${b.name})${b.cfg && b.cfg.team ? ` · team ${b.cfg.team}` : ''}`]).concat(S.view().teams.map((t) => ['team:' + t.name, `Team ${t.name}: every bot on it`])).filter(([v]) => !have.includes(v));
  UI.act('w-who-add', (b) => { const isCard = b.dataset.kind === 'card', x = isCard ? card(b.dataset.n) : profile(b.dataset.n);
    UI.form({ caps: esc(x.name), title: isCard ? 'Let a bot or team use this card' : 'Let a bot or team use these details', fields: [{ k: 'id', type: 'enum', req: true, label: 'Who', options: idOpts(x.identities || []) }],
      note: isCard ? 'It can propose purchases with the card; every one still waits for your approval.' : 'Only on work you asked for.', submit: 'Allow with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { UI.closeModal(); gate(true, `Let ${esc(who(v.id))} use ${esc(x.name)}`, `{ ${isCard ? 'card' : 'profile'}: <b>${esc(x.name)}</b>, bind: <b>${esc(v.id)}</b> }`, () => { x.identities = (x.identities || []).concat([v.id]); x.updated = now(); audit(`browser_${isCard ? 'card' : 'profile'}_bind`, `${isCard ? 'card' : 'profile'} ${x.name} · bind ${v.id} · widening`); done('Allowed', `${esc(who(v.id))} may use ${esc(x.name)}`); }, 'Allow with passkey'); return false; } }); });
  UI.act('w-who-rm', (b) => { const isCard = b.dataset.kind === 'card', x = isCard ? card(b.dataset.n) : profile(b.dataset.n);
    UI.confirm({ title: `Stop ${esc(who(b.dataset.id))} using ${esc(x.name)}?`, body: isCard ? 'It can no longer propose purchases with this card. Purchases already approved for it lapse unused.' : 'It can no longer fill these details.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; x.identities = x.identities.filter((i) => i !== b.dataset.id); if (isCard && x.identity_limits) delete x.identity_limits[b.dataset.id]; x.updated = now(); audit(`browser_${isCard ? 'card' : 'profile'}_bind`, `${isCard ? 'card' : 'profile'} ${x.name} · unbind ${b.dataset.id}`); done('Removed', `${esc(who(b.dataset.id))} can’t use ${esc(x.name)} any more`); }); });

  /* ---------- Add personal details ---------- */
  const NEWF = ['name', 'email', 'phone', 'address.line1', 'address.city', 'address.region', 'address.postal_code', 'address.country'];
  function newProfilePage() {
    return `<div class="page dl-page wl-page">${UI.head('Add personal details', { crumbs: [['Browsers', '#/browser'], ['Personal details', '#/browser/details'], ['Add personal details']] })}
      <form class="wz-card wl-form" id="w-np" novalidate>
        <div class="field" data-field="name"><label for="w-np-name">Name for this set <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-np-name" placeholder="home" autocomplete="off"><div class="hint">What a bot asks for: <span class="mono">--profile home</span>. Letters, digits, - and _.</div><div class="ferr" role="alert"></div></div>
        <h3 class="wl-h">The details</h3><p class="dl-sub">Type a value once (it’s stored write-only) or give a reference to a vault, an environment variable or a secret. Leave out what you don’t need.</p>
        <div class="wl-grid2">${NEWF.map((f) => valueField(f, fieldWord(f))).join('')}</div>
        <h3 class="wl-h">Where, and which details</h3>
        <div class="field" data-field="origin"><label for="w-np-origin">Site (its exact address) <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-np-origin" placeholder="https://shop.example.com"><div class="hint">The browser must already be allowed to reach it. Add more sites afterwards.</div><div class="ferr" role="alert"></div></div>
        <div class="field" data-field="sitef"><span class="flabel">Fill there</span><div class="wl-checks">${[['name', 'Name'], ['email', 'Email'], ['phone', 'Phone'], ['address', 'The address']].map(([k, l]) => `<label class="row small"><input type="checkbox" class="cbx" data-sf="${k}" checked> ${l}</label>`).join('')}</div><div class="ferr" role="alert"></div></div>
        <h3 class="wl-h">Who may use it</h3>
        <div class="field" data-field="who"><label for="w-np-who">Bot or team <span class="req" aria-label="required">*</span></label><select class="select" id="w-np-who"><option value="">Choose…</option>${idOpts([]).map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`).join('')}</select><div class="hint">Nobody can use it until you choose. Only on work you asked for.</div><div class="ferr" role="alert"></div></div>
        <h3 class="wl-h">What a bot may learn</h3><p class="dl-sub">Nothing, unless you tick it. A fact helps a bot reason without the value.</p>
        <div class="wl-checks">${[['email', 'The email’s domain'], ['address.country', 'The country'], ['phone', 'The phone’s country code']].map(([k, l]) => `<label class="row small"><input type="checkbox" class="cbx" data-vf="${k}"> ${l}</label>`).join('')}</div>
        <div class="form-err callout danger small hide" role="alert" id="w-np-err"></div>
        <div class="wizard-foot"><a class="btn ghost" href="#/browser/details">Cancel</a><span class="spacer"></span><button type="button" class="btn primary" data-act="w-np-save">${icon('passkey')}Save with passkey</button></div>
      </form></div>`;
  }
  const bad = (root, k, msg) => { const box = root.querySelector(`[data-field="${k}"]`); box.classList.toggle('bad', !!msg); box.querySelector('.ferr').textContent = msg || ''; return !!msg; };
  UI.act('w-np-save', () => { const f = UI.$('#w-np'), name = UI.$('#w-np-name').value.trim(), origin = UI.$('#w-np-origin').value.trim().replace(/\/$/, ''), id = UI.$('#w-np-who').value;
    let n = 0; n += bad(f, 'name', !/^[A-Za-z0-9_-]{1,40}$/.test(name) ? 'Letters, digits, - and _, up to 40.' : profile(name) ? 'Personal details by that name exist.' : '') ? 1 : 0;
    n += bad(f, 'origin', originErr(origin)) ? 1 : 0; n += bad(f, 'who', id ? '' : 'Choose who may use it.') ? 1 : 0;
    const sitef = [...f.querySelectorAll('[data-sf]:checked')].map((x) => x.dataset.sf); n += bad(f, 'sitef', sitef.length ? '' : 'Pick at least one.') ? 1 : 0;
    if (n) { const first = f.querySelector('.field.bad input, .field.bad select'); if (first) first.focus(); return; }
    const err = UI.$('#w-np-err'), vals = readValues(f); if (!vals) return; if (!Object.keys(vals).length) { err.textContent = 'Give at least one detail.'; err.classList.remove('hide'); return; } err.classList.add('hide');
    const visible = {}; f.querySelectorAll('[data-vf]:checked').forEach((x) => { if (vals[x.dataset.vf]) visible[x.dataset.vf] = 'fact'; });
    gate(true, `Add personal details: ${esc(name)}`, `{ profile: <b>${esc(name)}</b>, fields: <b>${esc(Object.keys(vals).join(', '))}</b>, site: <b>${esc(origin)}</b>, bind: <b>${esc(id)}</b>, values: <b>write-only, not shown</b> }`, () => {
      D.wallet.profiles.push({ name, method: 'stored', editable: true, added: now(), updated: now(), identities: [id], fields: vals, sites: [{ origin, fields: sitef }], visible });
      audit('browser_profile_add', `profile ${name} · ${id} · values not recorded · widening`); S.save(); UI.go(`#/browser/details/${name}`); UI.toast('Saved', `${esc(name)}: values stored write-only`, 'ok'); }, 'Save with passkey'); });

  /* ---------- Payment cards ---------- */
  /** validate's reminders about a card (WalletNotes), in words. */
  const notes = (c) => [c.kind === 'physical' || !c.kind ? 'A physical card: prefer a virtual or single-use card with its own provider limits. Conductor’s checks are the first layer; a limited card is the second.' : '', !(c.merchants || []).length ? 'No shop is allowed yet, so nothing can be bought with it.' : ''].filter(Boolean);
  const frozenWord = (c) => (c.frozen ? `Frozen${c.frozen_at ? ` ${UI.agoText(c.frozen_at)}` : ' in the config'}` : '');
  const cardRow = (c) => { const L = c.limits, cur = L.currency || 'USD', s = spent(c.name), ns = notes(c), att = !c.frozen && ns.length;
    return `<li class="dl-conn wl-card-row${att ? ' attn' : ''}" data-s="${c.frozen ? 'idle' : 'wait'}" data-card="${esc(c.name)}"><span class="dl-ng">${c.frozen ? `<span class="wl-ice" role="img" aria-label="Frozen">${icon('freeze')}</span>` : ns.length ? UI.mark('wait', { lg: true, label: 'Needs a look' }) : icon('obj-card')}</span>
      <span class="dl-nt"><b><a class="dl-link-q" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> <span class="tgt">${esc(cardName(c))}</span></b>
        <span class="${att || c.frozen ? 'reason' : 'dl-why'}">${c.frozen ? `${frozenWord(c)}: nothing can be paid with it` : ns.length ? esc(ns[ns.length - 1]) : `${money(s.day, cur)} of ${money(L.per_day, cur)} in 24 hours · ${money(s.month, cur)} of ${money(L.per_month, cur)} in 30 days`}</span>
        ${UI.factsLine([{ icon: 'obj-card', value: KIND[c.kind || ''], pri: 2, label: 'Kind' }, { icon: 'users', value: list((c.identities || []).map(who)) || 'Nobody yet', pri: 1, label: 'Who may use it' }, { icon: 'store', value: `${(c.merchants || []).length} shop${(c.merchants || []).length === 1 ? '' : 's'}`, pri: 2, label: 'Shops it may pay' }, c.method === 'config' ? { icon: 'obj-config', value: 'In the config', pri: 3, label: 'Where it’s defined' } : null])}</span>
      <span class="dl-na">${freezeBtn(c, 'sm')}<a class="btn sm secondary" href="#/browser/cards/${esc(c.name)}">Open</a></span></li>`; };
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
        <p class="dl-sub wl-foot">${icon('shield')} ${ap.length ? `${esc(list(ap))} approve${ap.length === 1 ? 's' : ''} purchases, each with a passkey. An approval is used once, within ${esc(UI.humanSpan((D.payments || {}).approval_ttl || '1h'))}.` : '<b>Nobody can approve a purchase yet</b> (no operator has a key), so every purchase will wait.'} Pause all automation also stops every payment.</p></section>
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
      <section class="dl-card wl-gap"><header class="dl-ch"><h2>The card’s details</h2><span class="dl-sub">never shown, never sent to a bot</span></header><dl class="dl-facts wl-cfacts">${['number', 'expiry', 'cvc', 'name'].map((k) => { const f = (c.fields || {})[k]; return `<dt>${{ number: 'Number', expiry: 'Expiry', cvc: 'Security code', name: 'Name on the card' }[k]}</dt><dd>${f ? `<span class="wl-masked">${k === 'number' ? `•••• ${esc(c.last4 || '')}` : '••••'}</span> <span class="dl-sub">set · ${esc(srcWord(f))}</span>` : '<span class="dl-sub">not set</span>'}</dd>`; }).join('')}</dl></section></div>`;
  }
  UI.act('w-card-more', (b) => { const c = card(b.dataset.c); UI.menu(b, [['See it in the audit log', 'logs', 'w-go', 'data-h="#/logs/audit"'], ['-'], ['Remove this card…', 'trash', 'w-card-rm', c.editable === false ? UI.off('Defined in the config: remove it there') : `data-c="${esc(c.name)}"`]]); });
  UI.act('w-card-rm', (b) => UI.confirm({ title: `Remove ${esc(b.dataset.c)}?`, body: 'Its stored values are deleted and nothing more can be bought with it. Approved purchases lapse unused. Receipts stay.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; D.wallet.cards = W().cards.filter((x) => x.name !== b.dataset.c); audit('browser_card_remove', `card ${b.dataset.c}`); S.save(); UI.go('#/browser/cards'); UI.toast('Removed', `${esc(b.dataset.c)} is gone`, 'ok'); }));
  // the kill switch: freezing is one click (it narrows); unfreezing widens, so it asks for your passkey
  UI.act('w-freeze', (b) => { const c = card(b.dataset.c); c.frozen = true; c.frozen_at = now(); audit('browser_card_freeze', `card ${c.name}`); S.save(); UI.rerender(); UI.toast(`${esc(c.name)} is frozen`, 'nothing is paid with it until you unfreeze it', 'ok'); });
  UI.act('w-unfreeze', (b) => { const c = card(b.dataset.c); gate(true, `Unfreeze ${esc(c.name)}`, `{ card: <b>${esc(c.name)}</b>, unfreeze: <b>true</b> }`, () => { c.frozen = false; delete c.frozen_at; audit('browser_card_unfreeze', `card ${c.name} · widening`); done(`${esc(c.name)} is in use again`, 'purchases with it can be proposed again'); }, 'Unfreeze with passkey'); });
  UI.act('w-merch-add', (b) => { const c = card(b.dataset.c);
    UI.form({ caps: esc(c.name), title: 'Add a shop', intro: 'The card is filled only on this exact address, and only for a purchase you approve.', fields: [{ k: 'origin', type: 'string', req: true, label: 'The shop’s address', ph: 'https://shop.example.com', validate: (v) => originErr(v) || ((c.merchants || []).includes(String(v).replace(/\/$/, '')) ? 'Already on the list.' : '') }],
      note: 'The browser must already be allowed to reach it. A checkout inside another site’s frame (hosted payment fields) isn’t filled yet.', submit: 'Add with passkey', submitIcon: 'passkey',
      onSubmit: (v) => { const o = v.origin.replace(/\/$/, ''); UI.closeModal(); gate(true, `Let ${esc(c.name)} pay ${esc(host(o))}`, `{ card: <b>${esc(c.name)}</b>, add_merchant: <b>${esc(o)}</b> }`, () => { c.merchants = (c.merchants || []).concat([o]); c.updated = now(); audit('browser_card_bind', `card ${c.name} · merchant ${o} · widening`); done('Added', `${esc(c.name)} may pay ${esc(host(o))}`); }, 'Add with passkey'); return false; } }); });
  UI.act('w-merch-rm', (b) => UI.confirm({ title: `Stop ${esc(b.dataset.c)} paying ${esc(host(b.dataset.o))}?`, body: 'Nothing more is bought there with this card. Takes effect at once.', ok: 'Remove', danger: true }).then((ok) => { if (!ok) return; const c = card(b.dataset.c); c.merchants = c.merchants.filter((x) => x !== b.dataset.o); audit('browser_card_bind', `card ${c.name} · removed merchant ${b.dataset.o}`); done('Removed', `${esc(c.name)} won’t pay ${esc(host(b.dataset.o))} any more`); }));
  const LIMF = [['per_purchase', 'A purchase'], ['per_day', 'A day (24 hours)'], ['per_month', '30 days']];
  UI.act('w-limits', (b) => { const c = card(b.dataset.c), cur = c.limits.currency || 'USD';
    UI.form({ caps: esc(c.name), title: 'Change the limits', intro: `In ${esc(cur)}. Raising one asks for your passkey; lowering one doesn’t.`, edit: true, values: Object.assign({}, c.limits),
      fields: LIMF.map(([k, l]) => ({ k, type: 'number', req: true, label: l, validate: (v) => (typeof v === 'number' && v > 0 ? '' : 'An amount above 0.') })), submit: 'Save', submitIcon: 'check',
      onSubmit: (v) => { if (v.per_purchase > v.per_day || v.per_day > v.per_month) return 'A purchase’s limit fits inside a day’s, and a day’s inside 30 days’.'; const up = LIMF.some(([k]) => v[k] > c.limits[k]); UI.closeModal();
        gate(up, `Raise ${esc(c.name)}’s limits`, `{ card: <b>${esc(c.name)}</b>, limits: <b>${LIMF.map(([k]) => `${k} ${c.limits[k]} → ${v[k]}`).join(', ')}</b> }`, () => { LIMF.forEach(([k]) => { c.limits[k] = v[k]; }); c.updated = now(); audit('browser_card_bind', `card ${c.name} · limits${up ? ' · widening' : ''}`); done('Saved', 'the new limits apply to the next purchase'); }, 'Save with passkey'); return false; } }); });

  /* ---------- Add a card ---------- */
  function newCardPage() {
    return `<div class="page dl-page wl-page">${UI.head('Add a card', { crumbs: [['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], ['Add a card']] })}
      <form class="wz-card wl-form" id="w-nc" novalidate>
        <div class="callout small wl-tip">${icon('shield')}<span><b>Use a limited card.</b> A virtual or single-use card with its own limits (a masked card from 1Password, Privacy.com, Stripe Issuing) is the second layer behind conductor’s checks.</span></div>
        <div class="field" data-field="name"><label for="w-nc-name">Name for this card <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-nc-name" placeholder="household" autocomplete="off"><div class="hint">What a bot proposes a purchase with: <span class="mono">--card household</span>.</div><div class="ferr" role="alert"></div></div>
        <div class="field" data-field="kind"><span class="flabel">Kind</span><div class="seg" role="group" aria-label="Kind" id="w-nc-kind">${[['virtual', 'Virtual'], ['single-use', 'Single-use'], ['physical', 'Physical']].map(([k, l], i) => `<button type="button" data-act="w-kind" data-v="${k}" class="${i ? '' : 'on'}" aria-pressed="${!i}">${l}</button>`).join('')}</div><div class="hint" id="w-nc-kh">A virtual card with its own limits: good.</div></div>
        <h3 class="wl-h">The card</h3><p class="dl-sub">Type each once (stored write-only) or give a reference. The page will only ever show the last four digits.</p>
        <div class="wl-grid2">${valueField('number', 'Card number', false)}${valueField('expiry', 'Expiry (MM/YY)', false)}${valueField('cvc', 'Security code')}${valueField('cardname', 'Name on the card')}</div>
        <h3 class="wl-h">Limits</h3><p class="dl-sub">Required. In US dollars; checked when a purchase is proposed, approved and paid.</p>
        <div class="wl-grid3">${LIMF.map(([k, l]) => `<div class="field" data-field="${k}"><label for="w-nc-${k}">${l} <span class="req" aria-label="required">*</span></label><input class="input mono" id="w-nc-${k}" inputmode="decimal" placeholder="${{ per_purchase: 50, per_day: 100, per_month: 300 }[k]}"><div class="ferr" role="alert"></div></div>`).join('')}</div>
        <h3 class="wl-h">Where, and who</h3>
        <div class="field" data-field="merchant"><label for="w-nc-merchant">A shop it may pay <span class="opt">optional</span></label><input class="input mono" id="w-nc-merchant" placeholder="https://shop.example.com"><div class="hint">None by default: until you add one, nothing can be bought with it.</div><div class="ferr" role="alert"></div></div>
        <div class="field" data-field="who"><label for="w-nc-who">Bot or team <span class="req" aria-label="required">*</span></label><select class="select" id="w-nc-who"><option value="">Choose…</option>${idOpts([]).map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`).join('')}</select><div class="hint">It may propose purchases; each one waits for your approval.</div><div class="ferr" role="alert"></div></div>
        <div class="form-err callout danger small hide" role="alert" id="w-nc-err"></div>
        <div class="wizard-foot"><a class="btn ghost" href="#/browser/cards">Cancel</a><span class="spacer"></span><button type="button" class="btn primary" data-act="w-nc-save">${icon('passkey')}Save with passkey</button></div>
      </form></div>`;
  }
  UI.act('w-kind', (b) => { b.parentElement.querySelectorAll('button').forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); UI.$('#w-nc-kh').textContent = b.dataset.v === 'physical' ? 'Conductor will remind you to prefer a virtual or single-use card.' : b.dataset.v === 'single-use' ? 'A single-use card: good for one purchase.' : 'A virtual card with its own limits: good.'; });
  UI.act('w-nc-save', () => { const f = UI.$('#w-nc'), name = UI.$('#w-nc-name').value.trim(), merchant = UI.$('#w-nc-merchant').value.trim().replace(/\/$/, ''), id = UI.$('#w-nc-who').value, kind = (f.querySelector('#w-nc-kind .on') || {}).dataset.v || 'virtual';
    let n = 0; n += bad(f, 'name', !/^[A-Za-z0-9_-]{1,40}$/.test(name) ? 'Letters, digits, - and _, up to 40.' : card(name) ? 'A card by that name exists.' : '') ? 1 : 0;
    const lim = {}; LIMF.forEach(([k]) => { const v = Number(UI.$('#w-nc-' + k).value.trim()); lim[k] = v; n += bad(f, k, v > 0 ? '' : 'An amount above 0.') ? 1 : 0; });
    if (!n && (lim.per_purchase > lim.per_day || lim.per_day > lim.per_month)) n += bad(f, 'per_purchase', 'A purchase fits inside a day, and a day inside 30 days.') ? 1 : 0;
    n += bad(f, 'merchant', merchant ? originErr(merchant) : '') ? 1 : 0; n += bad(f, 'who', id ? '' : 'Choose who may use it.') ? 1 : 0;
    const vals = readValues(f); if (!vals) n++;
    const err = UI.$('#w-nc-err'); if (vals && (!vals.number || !vals.expiry)) { err.textContent = 'The card number and expiry are required.'; err.classList.remove('hide'); n++; } else err.classList.add('hide');
    if (n) { const first = f.querySelector('.field.bad input, .field.bad select'); if (first) first.focus(); return; }
    gate(true, `Add a card: ${esc(name)}`, `{ card: <b>${esc(name)}</b>, kind: <b>${kind}</b>, bind: <b>${esc(id)}</b>, merchants: <b>${esc(merchant || 'none')}</b>, limits: <b>${lim.per_purchase} / ${lim.per_day} / ${lim.per_month} USD</b>, values: <b>write-only, not shown</b> }`, () => {
      const fields = {}; [['number', 'number'], ['expiry', 'expiry'], ['cvc', 'cvc'], ['cardname', 'name']].forEach(([a, k]) => { if (vals[a]) fields[k] = vals[a]; });
      // NEW: the last four digits and the brand come from the daemon when it stores the number (a typed one is never echoed)
      D.wallet.cards.push({ name, method: 'stored', editable: true, kind, brand: 'Visa', last4: '4242', added: now(), updated: now(), fields, merchants: merchant ? [merchant] : [], identities: [id], limits: Object.assign({ currency: 'USD' }, lim), identity_limits: {}, frozen: false });
      audit('browser_card_add', `card ${name} · ${id} · values not recorded · widening`); S.save(); UI.go(`#/browser/cards/${name}`); UI.toast('Card added', `${esc(name)}: values stored write-only`, 'ok'); }, 'Save with passkey'); });

  /* ---------- a purchase ---------- */
  function purchasePage(p) {
    const st = UI.puStatus(p), [s, word] = pst(p), pend = st === 'pending', c = card(p.card) || {};
    const ops = (D.payments || {}).approvers || [];
    const ask = `${esc(byWord(p))} wants to buy this with the ${esc(p.card)} card. You approve exactly these terms; ${p.bot ? 'it' : 'the agent'} can then pay once, on ${esc(host(p.merchant))} only.`;
    const origin = /^operator:/.test(p.requester || '') ? `${icon('users')}Asked for by ${p.requester === 'operator:ops' ? 'you' : esc(p.requester.slice(9))}${/^dir:/.test(p.identity) ? `, in an agent you started in <span class="mono">${esc(p.identity.slice(4))}</span>` : ''}.` : `${icon('warn')}Asked for by <span class="mono">${esc(p.requester || 'someone else')}</span>.`;
    const ticket = p.case && (D.cases || []).find((x) => x.id === p.case);
    const auditD = `<details class="dl-audit-d"><summary>${icon('chev')}For audit: the purchase, its identity and the digest your passkey signs</summary><dl class="dl-facts"><dt>Purchase</dt><dd class="mono">${esc(p.id)}</dd><dt>Card</dt><dd class="mono">${esc(p.card)}</dd><dt>Merchant</dt><dd class="mono">${esc(p.merchant)}</dd><dt>Identity</dt><dd class="mono">${esc(p.identity)}</dd>${p.bot_proposal ? `<dt>Bot proposal</dt><dd class="mono">${esc(p.bot)} ${esc(p.bot_proposal)}</dd>` : ''}${p.supersedes ? `<dt>Replaces</dt><dd class="mono">${esc(p.supersedes)}</dd>` : ''}<dt>Digest</dt><dd class="mono">${esc(p.digest)}</dd><dt>Signed by</dt><dd>${p.approver ? esc(p.approver) : 'not yet: your passkey signs this digest'}</dd></dl></details>`;
    const card2 = `<article class="dl-card dl-proposal wl-pu">${pend ? `<div class="dl-kick">${UI.mark('ember')}Needs you · <span class="nowrap">asked ${UI.agoText(p.created)}</span>${p.expires ? ` · <span class="nowrap">expires in ${UI.humanSpan(Math.max(1, Math.round((Date.parse(p.expires) - Date.now()) / 60000)) + 'm')}</span>` : ''}</div>` : `<div class="dl-kick dl-sub">${UI.statusMark(s, word, { sm: true })} · ${UI.agoText(p.created)}</div>`}
      <h2 class="dl-ptitle">${esc(UI.purchaseTitle(p))}</h2>${pend ? `<p class="dl-ask">${ask}</p>` : ''}
      <div class="prop-body">${UI.purchaseWhat(p)}
        ${p.why ? `<div class="dl-pwhy"><span class="dl-lbl">Why</span><p>${UI.taskWords(p.why, true)}</p></div>` : ''}
        <p class="dl-origin">${origin}</p>
        ${pend ? '' : `<p class="dl-decided">${UI.statusMark(s, word)} ${decidedWords(p)}</p>`}${receiptHTML(p)}
        ${ticket ? `<p class="dl-sub prop-can-link"><a class="dl-link" href="#/tickets/${ticket.id}">The ticket: ${esc(ticket.title)} →</a></p>` : ''}${auditD}</div>
      ${pend ? `<div class="dl-decide prop-decide"><button class="btn lg primary" data-act="w-pu-decide" data-p="${esc(p.id)}" data-d="approve">${icon('passkey')}Approve with passkey</button><button class="btn lg secondary" data-act="w-pu-decide" data-p="${esc(p.id)}" data-d="deny">Deny…</button><span class="hint">${ops.length ? `Only ${esc(ops.join(' or '))} can approve.` : 'No operator can approve yet.'}${c.frozen ? ' The card is frozen: unfreeze it first.' : ''}</span></div>` : ''}</article>`;
    return `<div class="page dl-page wl-page">${UI.crumbs([['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], [UI.purchaseTitle(p)]])}<div class="prop-single">${card2}</div></div>`;
  }
  // approve or deny: signed with your passkey (the CLI signs with the operator's SSH key; the dashboard with a passkey)
  UI.act('w-pu-decide', (b) => { const p = (D.purchases || []).find((x) => x.id === b.dataset.p), deny = b.dataset.d === 'deny', c = card(p.card) || {};
    if (!deny && c.frozen) return UI.toast(`${esc(p.card)} is frozen`, 'unfreeze it before approving a purchase with it', 'fail');
    const sign = (reason) => UI.stepup({ title: `${deny ? 'Deny' : 'Approve'}: ${esc(UI.purchaseTitle(p))}`, purpose: 'purchase decision', ok: deny ? 'Deny with passkey' : 'Approve with passkey', danger: deny,
      what: `{ purchase: <b>${esc(p.id)}</b>, decision: <b>${deny ? 'deny' : 'approve'}</b>, card: <b>${esc(p.card)}</b>, merchant: <b>${esc(p.merchant)}</b>, total: <b>${p.total.toFixed(2)} ${esc(p.currency)}</b>, recurring: <b>${p.recurring ? p.interval : 'no'}</b>, digest: <b>${esc(p.digest.replace('sha256:', '').slice(0, 16))}…</b> }` }).then((ok) => {
      if (!ok) return; Object.assign(p, { status: deny ? 'denied' : 'approved', approver: 'ops', decided: now() }); if (reason) p.reason = reason; if (!deny) p.expires = new Date(Date.now() + 36e5).toISOString();
      audit('browser_purchase_' + (deny ? 'deny' : 'approve'), `purchase ${p.id} · ${p.total.toFixed(2)} ${p.currency} at ${host(p.merchant)}`);
      D.attention = D.attention.filter((a) => !String(a.href).includes(p.id)); S.save(); UI.rerender(); UI.refreshChrome();
      UI.toast(deny ? 'Denied' : 'Approved', deny ? 'nothing will be paid' : `it may pay once, within ${esc(UI.humanSpan((D.payments || {}).approval_ttl || '1h'))}`, deny ? 'fail' : 'ok'); });
    if (!deny) return sign('');
    UI.form({ caps: 'deny', title: 'Deny this purchase', fields: [{ k: 'reason', type: 'text', label: 'Why (the agent is told)', ph: 'Not this month.' }], submit: 'Next', onSubmit: (v) => { UI.closeModal(); setTimeout(() => sign((v.reason || '').trim()), 0); return false; } }); });

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

  /* ---------- a bot's What it can do: Can buy, buying without asking, the details it can fill ---------- */
  const forBot = (ids, m) => (ids || []).some((i) => i === 'bot:' + m.name || (m.cfg && m.cfg.team && i === 'team:' + m.cfg.team));
  const UNV = 'browser.purchase';
  UI.botWalletHTML = (m) => { const cfg = S.staged('bots', m.name) || m.cfg || {}, cards = W().cards.filter((c) => forBot(c.identities, m)), profs = W().profiles.filter((p) => forBot(p.identities, m));
    const verbs = S.getPath(cfg, 'skill.verbs') || [], canVerb = (Array.isArray(verbs) ? verbs : Object.keys(verbs)).includes(UNV);
    if (!cards.length && !profs.length && !canVerb) return '';
    const un = (S.getPath(cfg, 'autonomy.unattended') || {})[UNV];
    const shadow = S.getPath(cfg, 'autonomy.shadow');
    const cardLine = (c) => { const L = c.limits, cur = L.currency || 'USD', il = (c.identity_limits || {})['bot:' + m.name] || (m.cfg && m.cfg.team && (c.identity_limits || {})['team:' + m.cfg.team]), cap = il ? Math.min(il.per_purchase, L.per_purchase) : L.per_purchase, s = spent(c.name, 'bot:' + m.name), mo = il ? il.per_month : L.per_month;
      return `<li class="wl-can"><p><b>Can buy:</b> ${c.frozen ? `nothing with <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> while it’s frozen.` : (c.merchants || []).length ? `asks you first, up to ${money(cap, cur)} a purchase, at ${esc(list(c.merchants.map(host)))}, with <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> (${esc(cardName(c))}).` : `nothing yet: <a class="dl-link" href="#/browser/cards/${esc(c.name)}">${esc(c.name)}</a> has no shop.`}</p>
        <div class="wl-lrow"><span class="wl-ll">Last 30 days</span><span class="wl-lv"><b>${money(s.month, cur)}</b> of ${money(mo, cur)}</span>${meter(s.month, mo, cur)}</div></li>`; };
    const unLine = un ? `<p><b>Without asking:</b> up to ${money(un.per_action)} a purchase and ${money(un.per_period)} every ${esc(UI.humanSpan(un.period || '24h'))}, at ${esc(list((un.merchants || []).map(host)))}; only for your requests or its own, never a subscription.</p><button class="btn sm secondary" data-act="w-un-off" data-m="${esc(m.name)}">Always ask me</button>`
      : `<p><b>Without asking:</b> never. Every purchase waits for your approval.</p><button class="btn sm secondary" ${!canVerb ? UI.off(`${m.name} can’t buy at all yet`) : shadow ? UI.off(`${m.name} is in practice mode: nothing it does runs`) : `data-act="w-un-on" data-m="${esc(m.name)}"`}>Let it buy small things without asking…</button>`;
    return `<section class="dl-card wl-bot" id="wl-bot"><header class="dl-ch"><h2>Buying, and your details</h2></header>
      ${canVerb || cards.length ? `<ul class="wl-cans">${cards.map(cardLine).join('') || `<li class="wl-can"><p><b>Can buy:</b> nothing yet: no card names ${esc(m.name)}. <a class="dl-link" href="#/browser/cards">Payment cards</a></p></li>`}</ul>
        <div class="wl-un${un ? ' on' : ''}">${unLine}</div>` : ''}
      ${profs.length ? `<div class="wl-fills"><p><b>Fills your details:</b> ${profs.map((p) => `<a class="dl-link" href="#/browser/details/${esc(p.name)}">${esc(p.name)}</a> on ${esc(list(p.sites.map((s) => `${host(s.origin)} (${list(s.fields.map(siteFieldWord))})`)))}`).join('; ')}. It never sees them${profs.some((p) => learns(p).length) ? `; it may learn ${esc(list([...new Set(profs.flatMap(learns))]))}` : ''}.</p></div>` : ''}
      <p class="dl-sub">The card never reaches it: conductor fills it, only on a shop’s page, only for a purchase you approved.</p></section>`; };
  // buying without asking: bots only, opt-in, a widening; under its spend cap, at named shops (it goes through Review)
  UI.act('w-un-on', (b) => { const m = S.view().bots.find((x) => x.name === b.dataset.m), cfg = JSON.parse(JSON.stringify(S.staged('bots', m.name) || m.cfg)), cap = S.getPath(cfg, 'autonomy.spend_cap') || {};
    const shops = [...new Set(W().cards.filter((c) => forBot(c.identities, m) && !c.frozen).flatMap((c) => c.merchants || []))], team = cfg.team && S.view().teams.find((t) => t.name === cfg.team);
    if (team && S.getPath(team.cfg, 'autonomy.ceiling') !== 'commit_money') return UI.toast('Not on this team', `team ${esc(team.name)}’s ceiling keeps every purchase an approval`, 'fail');
    UI.form({ caps: `bot ${esc(m.name)} · a widening`, title: 'Buy small things without asking', intro: `${esc(UI.botName(m.name))} may buy without your approval only under these caps, at the shops you tick, and only when you or it asked (never someone else, an event or a check). A subscription always asks.`,
      fields: [{ k: 'per_action', type: 'number', req: true, label: 'Up to, a purchase (USD)', ph: '10', validate: (v) => (!(v > 0) ? 'An amount above 0.' : cap.per_action != null && v > cap.per_action ? `It must fit inside its spend cap: ${money(cap.per_action)} an action.` : '') },
        { k: 'per_period', type: 'number', req: true, label: 'Up to, in total (USD)', ph: '30', validate: (v, all) => (!(v > 0) ? 'An amount above 0.' : v < all.per_action ? 'At least one purchase’s worth.' : cap.per_period != null && v > cap.per_period ? `It must fit inside its spend cap: ${money(cap.per_period)} a period.` : '') },
        { k: 'period', type: 'seg', label: 'Every', options: [['24h', '24 hours'], ['168h', '7 days'], ['720h', '30 days']], def: '24h' },
        ...shops.map((o, i) => ({ k: 'shop' + i, type: 'bool', label: host(o), on: 'it may buy here without asking' }))],
      note: 'Each purchase still counts against the card’s own limits and shops, and against its spend cap. You review the change before it applies.', submit: 'Review change', submitIcon: 'check',
      onSubmit: (v) => { const merchants = shops.filter((o, i) => v['shop' + i]); if (!merchants.length) return 'Tick at least one shop.';
        const A = (cfg.autonomy = cfg.autonomy || {}); A.unattended = A.unattended || {}; A.unattended[UNV] = { per_action: v.per_action, per_period: v.per_period, period: v.period || '24h', merchants };
        UI.staged(S.stage('bots', m.name, cfg, `let ${m.name} buy up to ${money(v.per_action)} without asking`)); } }); });
  UI.act('w-un-off', (b) => { const m = S.view().bots.find((x) => x.name === b.dataset.m), cfg = JSON.parse(JSON.stringify(S.staged('bots', m.name) || m.cfg));
    if (cfg.autonomy && cfg.autonomy.unattended) { delete cfg.autonomy.unattended[UNV]; if (!Object.keys(cfg.autonomy.unattended).length) delete cfg.autonomy.unattended; }
    UI.staged(S.stage('bots', m.name, cfg, `${m.name} asks before every purchase`)); });
  /** browser.purchase in a bot's lists, in words. */
  UI.builtinVerbWords = (v) => ({ 'browser.purchase': 'buy things with a card you allow' }[v] || null);
})();
