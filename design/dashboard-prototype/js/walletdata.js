/* Personal details and payment cards (the conductor side: branch feat/agent-browser, docs/wiki/Browser.md, "Personal
 * details (profiles)" and "Payment cards and purchases"). The shapes are its control-socket answers:
 *   browser-wallet     profiles and cards, sources only (a field's kind and reference, never a value), method config |
 *                      stored, editable (stored only: a config one changes in the config), frozen and frozen_at
 *   browser-purchases  the purchase records: card, merchant (an exact origin), items, total, currency, recurring and
 *                      interval, why, identity, bot and its proposal, ticket (case), status (pending | approved | denied |
 *                      expired | used | refused | completed), approver, unattended, expires, digest, supersedes, reason,
 *                      receipt { order_number, total, currency, at, note }
 *   spend              from the ledger's charges: a rolling 24 hours and 30 days, per card and per identity
 * NEW (the dashboard needs it, the backend doesn't have it yet; docs/design/dashboard.md §4.5.1): a card's brand and
 * last four digits (`brand`, `last4`), and a profile's or card's use history beyond the audit.
 * Loaded after data.js and before scenario.js, so the demo days and the seed include it. Neutral fake data only: test
 * card numbers (4242…), example.com shops, no person's name anywhere (values are never shown anyway). */
(function () {
  const D = window.D, now = Date.now(), m = (min) => new Date(now - min * 60000).toISOString(), at = (min) => new Date(now + min * 60000).toISOString();
  let demo = 'calm';
  try { demo = localStorage.getItem('cd-demo') || 'calm'; } catch (e) {}
  D.walletVer = 1;
  const V = (key) => ({ kind: 'vault', source: `{{ vault "op" "${key}" }}` }), E = (name) => ({ kind: 'env', source: `\${env.${name}}` }), ST = { kind: 'stored', source: '' };

  D.wallet = {
    profiles: [
      { name: 'home', method: 'stored', editable: true, added: m(9000), updated: m(2900),
        identities: ['bot:homelab'],
        fields: { name: ST, email: V('Me/Home/email'), phone: E('HOME_PHONE'), 'address.line1': V('Me/Home/street'), 'address.city': V('Me/Home/city'), 'address.region': V('Me/Home/region'), 'address.postal_code': V('Me/Home/postcode'), 'address.country': V('Me/Home/country') },
        sites: [{ origin: 'https://shop.example.com', fields: ['name', 'email', 'phone', 'address'] }, { origin: 'https://parts.example.net', fields: ['name', 'address'] }],
        visible: { email: 'fact', 'address.country': 'fact' } },
      { name: 'work', method: 'config', editable: false, added: m(40000), updated: m(40000),
        identities: ['team:finance'],
        fields: { name: V('Me/Work/name'), email: V('Me/Work/email'), company: { kind: 'vault', source: '{{ vault "house" "work/company" }}' }, phone: E('WORK_PHONE') },
        sites: [{ origin: 'https://billing.example.org', fields: ['name', 'email', 'company'] }],
        visible: { email: 'fact', company: 'value' } },
    ],
    cards: [
      { name: 'household', method: 'stored', editable: true, kind: 'virtual', brand: 'Visa', last4: '4242', added: m(9000), updated: m(2900),
        fields: { number: V('Cards/Household/number'), expiry: V('Cards/Household/expiry'), cvc: V('Cards/Household/cvc'), name: V('Cards/Household/name') },
        merchants: ['https://shop.example.com', 'https://parts.example.net', 'https://backup.example.com'],
        identities: ['bot:homelab', 'dir:/srv/lab'],
        limits: { per_purchase: 150, per_day: 200, per_month: 400, currency: 'USD' },
        identity_limits: { 'bot:homelab': { per_purchase: 120, per_day: 150, per_month: 300 } }, frozen: false },
      { name: 'software', method: 'stored', editable: true, kind: 'virtual', brand: 'Mastercard', last4: '4444', added: m(30000), updated: m(11600),
        fields: { number: V('Cards/Software/number'), expiry: V('Cards/Software/expiry'), cvc: V('Cards/Software/cvc') },
        merchants: ['https://tools.example.org'], identities: ['team:finance'],
        limits: { per_purchase: 50, per_day: 50, per_month: 120, currency: 'USD' }, identity_limits: {}, frozen: true, frozen_at: m(11500) },
      { name: 'spare', method: 'config', editable: false, kind: 'physical', brand: 'Visa', last4: '5556', added: m(40000), updated: m(40000),
        fields: { number: { kind: 'secret', source: '${secret.SPARE_CARD_NUMBER}' }, expiry: { kind: 'secret', source: '${secret.SPARE_CARD_EXPIRY}' } },
        merchants: [], identities: [], limits: { per_purchase: 25, per_day: 25, per_month: 50, currency: 'USD' }, identity_limits: {}, frozen: false },
    ],
  };
  // browser.payments: who may approve (operators with a key), and how long an approval may be used (single-use either way)
  D.payments = { approvers: ['ops'], approval_ttl: '1h' };

  /* Purchases: the ledger behind Needs you, a card's receipts and a ticket's. A bot's purchase is also its proposal
   * (verb browser.purchase, tier commit_money): you approve the proposal and that approves the purchase. */
  const P = (o) => Object.assign({ currency: 'USD', recurring: false, digest: 'sha256:' + Math.abs(o.id.split('').reduce((a, c) => (a * 31 + c.charCodeAt(0)) | 0, 7)).toString(16).padEnd(64, '0').slice(0, 64) }, o);
  D.purchases = [
    P({ id: 'pu7', card: 'household', merchant: 'https://shop.example.com', items: [{ name: '4 TB NAS hard drive', qty: 1, amount: 89.99 }], total: 89.99,
      why: 'Disk 2 in nas-01 reports reallocated sectors, and you asked for a replacement (task c39).', identity: 'bot:homelab', bot: 'homelab', case: 'c39', requester: 'operator:ops',
      status: 'pending', created: m(12), expires: at(48), bot_proposal: 'p14', supersedes: 'pu6' }),
    P({ id: 'pu6', card: 'household', merchant: 'https://shop.example.com', items: [{ name: '4 TB NAS hard drive', qty: 1, amount: 84.99 }], total: 84.99,
      why: 'Disk 2 in nas-01 reports reallocated sectors, and you asked for a replacement (task c39).', identity: 'bot:homelab', bot: 'homelab', case: 'c39', requester: 'operator:ops',
      status: 'refused', created: m(40), decided: m(33), approver: 'ops', bot_proposal: 'p13b', reason: 'At checkout the page showed 89.99 USD, more than the approved 84.99 USD. Nothing was paid; a new purchase for what the page shows was proposed.' }),
    P({ id: 'pu5', card: 'household', merchant: 'https://parts.example.net', items: [{ name: 'SATA cable, 2-pack', qty: 1, amount: 12.5 }], total: 12.5,
      why: 'The replacement disk needs a spare cable (task c39).', identity: 'bot:homelab', bot: 'homelab', case: 'c39', requester: 'operator:ops',
      status: 'completed', created: m(38), decided: m(33), approver: 'ops', bot_proposal: 'p13a', receipt: { order_number: 'PX-20418', total: 12.5, currency: 'USD', at: m(25) } }),
    P({ id: 'pu8', card: 'household', merchant: 'https://backup.example.com', items: [{ name: 'Off-site backup, 2 TB plan', qty: 1, amount: 6 }], total: 6, recurring: true, interval: 'month',
      why: 'Keep an off-site copy of nas-01 while disk 2 is replaced.', identity: 'dir:/srv/lab', requester: 'operator:ops', status: 'pending', created: m(6), expires: at(54) }),
    P({ id: 'pu2', card: 'household', merchant: 'https://shop.example.com', items: [{ name: 'Label tape, 12 mm', qty: 2, amount: 3.75 }], total: 7.5,
      why: 'Label the new drive bays.', identity: 'bot:homelab', bot: 'homelab', requester: 'operator:ops',
      status: 'completed', created: m(17400), decided: m(17390), approver: 'ops', receipt: { order_number: 'SH-11873', total: 7.5, currency: 'USD', at: m(17380) } }),
    P({ id: 'pu1', card: 'software', merchant: 'https://tools.example.org', items: [{ name: 'Diagram editor, 1 seat', qty: 1, amount: 24 }], total: 24, recurring: true, interval: 'month',
      why: 'The forecast deck needs a diagram editor (task c7).', identity: 'bot:billing-sre', bot: 'billing-sre', requester: 'operator:ops',
      status: 'completed', created: m(11700), decided: m(11690), approver: 'ops', receipt: { order_number: 'TL-5521', total: 26.4, currency: 'USD', at: m(11680), note: 'The confirmation charged 26.40 USD, more than the approved 24.00 USD.' } }),
  ];

  // the audit: every fill names the profile, the details, the site, who and the ticket, never a value
  D.audit.push(
    { ts: m(24), event: 'browser_profile_fill', who: 'bot:homelab', detail: 'profile home · name, address on https://parts.example.net · ticket c39 · values not recorded', words: 'Hearth filled the name and address on parts.example.net, for the ticket “Replace the failing disk in nas-01”', run: '' },
    { ts: m(14), event: 'browser_profile_fill', who: 'bot:homelab', detail: 'profile home · name, email, phone, address on https://shop.example.com · ticket c39 · values not recorded', words: 'Hearth filled the name, email, phone and address on shop.example.com, for the ticket “Replace the failing disk in nas-01”', run: '' },
    { ts: m(11500), event: 'browser_card_freeze', who: 'operator:ops', detail: 'card software · after a charge above the approval (TL-5521)', run: '' });
  D.audit.sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));

  // homelab ("Hearth") may buy, asking first: the card names it, and its gate classes browser.purchase as commit_money
  const hl = D.bots.find((b) => b.name === 'homelab');
  if (hl) {
    hl.cfg.skill.verbs = (hl.cfg.skill.verbs || []).concat(['browser.purchase']);
    hl.cfg.autonomy.tiers.commit_money = ['browser.purchase'];
    hl.cfg.autonomy.amounts = { 'browser.purchase': 'total' };
    hl.cfg.autonomy.spend_cap = { per_action: 150, per_period: 400, period: '720h', currency: 'USD' };
    hl.open = 2; hl.pending = 1;
    D.charters.homelab = 'Keep an eye on the home lab. Report missed backups and expiring certificates. Buy replacement parts only through a purchase you approve; never change anything else.\n';
  }
  D.cases.push({ id: 'c39', bot: 'homelab', title: 'Replace the failing disk in nas-01', status: 'open', requester: 'operator:ops', cls: 'operator', opened: m(45), workers: 0, proposals: 1,
    priority: 'high', desc: 'Disk 2 reports reallocated sectors. Order a matching 4 TB drive and a spare cable, shipped home.', routed: { by: 'direct', to: 'homelab', requester_kept: true },
    notes: [{ at: m(12), who: 'bot:homelab', text: 'The shop raised the drive to $89.99 at checkout, so conductor stopped and asked you again. The cable is ordered (PX-20418).' }] });
  // the bot's proposal for pu7 (its gate's side of the same purchase)
  D.proposals.push({ id: 'p14', bot: 'homelab', kind: 'purchase', verb: 'browser.purchase', tier: 'commit_money', status: 'pending', case: 'c39', requester: 'operator:ops', cls: 'operator', at: m(12), expires: '48m',
    title: 'Buy a 4 TB NAS hard drive for $89.99', summary: 'browser.purchase on shop.example.com: 89.99 USD with card household', purchase: 'pu7',
    ask: 'Hearth wants to buy this with the household card. You approve exactly these terms; it can then pay once, on shop.example.com only.',
    why: 'Disk 2 in nas-01 reports reallocated sectors, and you asked for a replacement (task c39).', impact: 'Charges 89.99 USD to the household card, once.',
    options: { card: 'household', merchant: 'https://shop.example.com', items: '[{"name":"4 TB NAS hard drive","qty":1,"amount":89.99}]', total: '89.99', currency: 'USD', recurring: 'false' },
    digest: 'sha256:7a1c3e5f9b2d4f6a8c0e2b4d6f8a1c3e5b7d9f1a3c5e7b9d2f4a6c8e0b2d4f6a' });

  // the other demo days: a calm day keeps the drive waiting on you (the backup plan was approved yesterday); a quiet
  // day has nothing waiting at all
  if (demo !== 'full') {
    const pu8 = D.purchases.find((x) => x.id === 'pu8');
    Object.assign(pu8, { status: 'completed', created: m(1500), decided: m(1490), approver: 'ops', expires: undefined, receipt: { order_number: 'BK-7781', total: 6, currency: 'USD', at: m(1480) } });
  }
  if (demo === 'quiet') {
    Object.assign(D.proposals.find((x) => x.id === 'p14'), { status: 'executed', approver: 'ops (passkey · laptop)' });
    Object.assign(D.purchases.find((x) => x.id === 'pu7'), { status: 'completed', decided: m(10), approver: 'ops', expires: undefined, receipt: { order_number: 'SH-48213', total: 89.99, currency: 'USD', at: m(6) } });
    hl.pending = 0;
  }
})();
