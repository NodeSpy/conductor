// Wizard headers in every state: walk each wizard through its steps and check its stepper at each one. Shared by
// wizard.spec.js (Chromium, desktop and phone) and iphone.spec.js (WebKit, iPhone 15).
const { go } = require('./fixtures');

/** In-page: every visible stepper fits (no clipped steps), no two of its parts overlap, and its text isn't cut. */
const stepperAudit = () => {
  const out = [];
  document.querySelectorAll('.stepper').forEach((st) => {
    const r = st.getBoundingClientRect(); if (!r.width) return;
    if (st.scrollWidth > st.clientWidth + 1) out.push(`stepper clipped: ${st.scrollWidth} > ${st.clientWidth}`);
    if (r.right > innerWidth + 1) out.push(`stepper wider than the window: right=${Math.round(r.right)}`);
    const parts = [...st.querySelectorAll('.as b, .as .as-l, .as, .step-compact .sc-t, .step-bar')].filter((x) => x.getBoundingClientRect().width && x.checkVisibility());
    const boxes = parts.map((x) => ({ x, b: x.getBoundingClientRect() }));
    for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
      const a = boxes[i], c = boxes[j]; if (a.x.contains(c.x) || c.x.contains(a.x)) continue;
      const ov = Math.min(a.b.right, c.b.right) - Math.max(a.b.left, c.b.left), ovy = Math.min(a.b.bottom, c.b.bottom) - Math.max(a.b.top, c.b.top);
      if (ov > 1 && ovy > 1) out.push(`overlap: "${a.x.textContent.trim().slice(0, 20)}" × "${c.x.textContent.trim().slice(0, 20)}"`);
    }
    // a connector line must not run through a label: each step's ::before ends before its own circle
    st.querySelectorAll('.as').forEach((s) => { const lbl = s.querySelector('.as-l'); if (lbl && lbl.checkVisibility() && lbl.scrollWidth > lbl.clientWidth + 1) out.push(`label cut: ${lbl.textContent}`); });
    const t = st.querySelector('.step-compact .sc-t'); if (t && t.checkVisibility() && t.scrollWidth > t.clientWidth + 1) out.push(`compact label cut: ${t.textContent}`);
  });
  return out;
};

/** The wizards and how to move each one forward: [name, route, steps (async actions taking it to the next state)]. */
const WIZARDS = [
  ['New bot', '#/bots/new', [
    (p) => p.locator('[data-act="bw-purpose"][data-p="incidents"]').click(),
    (p) => p.locator('[data-act="bw-next"]').click(),
    async (p) => { await p.locator('[data-act="bw-next"]').click(); },
    async (p) => { await p.locator('[data-act="bw-tell"][data-v="slack"]').click(); await p.fill('#bw-channel', '#incidents'); await p.locator('[data-act="bw-next"]').click(); },
    async (p) => { await p.locator('[data-act="bw-preset"][data-v="read"]').click(); await p.locator('[data-act="bw-next"]').click(); },
  ]],
  ['Add a login', '#/browser/add', [(p) => p.locator('[data-act="la-method"][data-m="password"]').click()]],
  ['Config apply', '#/bots/platform-sre/settings', [async (p) => { await p.evaluate(() => { const c = S.staged('bots', 'platform-sre'); c.description = 'Platform SRE, edited'; S.stage('bots', 'platform-sre', c); location.hash = '#/config/apply'; }); await p.waitForTimeout(300); }]],
  ['Seed a login', '#/browser/takeover', []],
  ['Register a passkey', '#/browser/passkey', []],
  ['Enrol a passkey', '#/enroll', []],
];

/** Walk every wizard; return the problems found, per wizard and state. */
async function walkWizards(page) {
  const problems = [];
  for (const [name, route, steps] of WIZARDS) {
    await go(page, route, { fresh: true });
    for (let i = 0; i <= steps.length; i++) {
      await page.waitForTimeout(120);
      (await page.evaluate(stepperAudit)).forEach((x) => problems.push(`${name}, state ${i + 1}: ${x}`));
      if (i < steps.length) await steps[i](page);
    }
  }
  return problems;
}

module.exports = { stepperAudit, walkWizards, WIZARDS };
