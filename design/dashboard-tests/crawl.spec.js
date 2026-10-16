// Click every interactive element on every route, at desktop and phone sizes.
//
// A control passes if its click changes the DOM (outside toasts, tooltips and
// regions marked data-volatile, which stream on their own), the URL, saved
// state, or starts a download. A control may instead be visibly disabled:
// aria-disabled="true" (or disabled) with a reason in data-reason or title.
// Anything else is a dead control and fails the test.
//
// The crawl is breadth-first: every control on the loaded page is clicked;
// controls that appear after a click (a modal, a menu, an expanded step) are
// queued with the path of clicks that revealed them, up to depth 2, and
// replayed from a fresh load when needed. Revealed controls that repeat one
// template (same data-act) are sampled, SAMPLE per handler per route.
const { test, expect, go } = require('./fixtures');

const ROUTES = [
  '#/overview', '#/live', '#/attention', '#/runs', '#/runs/r-7ef0',
  '#/workflows', '#/workflows/ci-fixer', '#/workflows/pr-review', '#/workflows/assess-and-post', '#/workflows/incident-intake', '#/workflows/bot:platform-sre',
  '#/bots', '#/folders/work', '#/folders/work%2Fplatform', '#/bots/homelab/memory', '#/teams/platform', '#/teams/platform/routing', '#/teams/platform/defaults', '#/teams/platform/knowledge', '#/teams/finance',
  '#/bots/platform-sre/chat', '#/bots/platform-sre/cases', '#/bots/web-triage/cases/c34', '#/bots/platform-sre/cases/c29', '#/bots/web-triage/cases/c36', '#/bots/platform-data/cases/c35', '#/bots/release-scribe/cases', '#/bots/platform-sre/proposals', '#/bots/platform-sre/proposals/p12',
  '#/bots/platform-sre/memory', '#/bots/platform-sre/workers', '#/bots/platform-sre/access', '#/bots/platform-sre/settings', '#/bots/new/settings',
  '#/browser', '#/browser/b-02', '#/browser/seed', '#/browser/passkey',
  '#/config', '#/config/raw', '#/config/apply', '#/secrets',
  '#/extensions/packs', '#/extensions/plugins', '#/extensions/browse',
  '#/connectors/connectors', '#/connectors/runtimes', '#/connectors/hosts', '#/connectors/isolation',
  '#/updates', '#/logs/daemon', '#/logs/audit', '#/security', '#/signin', '#/enroll', '#/more',
];
const MAX_DEPTH = 2;
const SAMPLE = 3; // revealed (depth ≥ 1) instances per data-act; everything on the loaded page is clicked

/* In-page helpers, injected once per load. */
async function inject(page) {
  await page.evaluate(() => {
    if (window.__crawl) return;
    const INTERACTIVE = 'a[href], button, [role="button"], [role="tab"], [role="switch"], [role="menuitem"], [data-act], summary';
    const IGNORE_MUT = '.toasts, .tooltip, .reason-flash, [data-volatile], #wf-ac';
    const visible = (el) => { const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0 && el.checkVisibility({ checkVisibilityCSS: true, contentVisibilityAuto: true }) && !el.closest('.hidden, .hide, [hidden], details:not([open]) > :not(summary)'); };
    const esc = (v) => CSS.escape(v);
    // A selector stable across re-renders: data-act + data-* args, else href, else a DOM path.
    const keyOf = (el) => {
      if (el.matches('input, select, textarea, option, label')) return null;
      const tag = el.tagName.toLowerCase();
      let sel;
      if (el.dataset.act) sel = `${tag}[data-act="${esc(el.dataset.act)}"]` + Object.entries(el.dataset).filter(([k]) => !['act', 'tip', 'reason', 'onInput', 'onChange'].includes(k)).map(([k, v]) => `[data-${k.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())}="${esc(v)}"]`).join('');
      else if (tag === 'a' && el.getAttribute('href')) sel = `a[href="${esc(el.getAttribute('href'))}"]`;
      else {
        const parts = []; let x = el;
        while (x && x !== document.body && parts.length < 6) { let p = x.tagName.toLowerCase(); if (x.id && !/^\d/.test(x.id)) { parts.unshift(`#${esc(x.id)}`); break; } const sib = [...x.parentElement.children].filter((c) => c.tagName === x.tagName); if (sib.length > 1) p += `:nth-of-type(${sib.indexOf(x) + 1})`; parts.unshift(p); x = x.parentElement; }
        sel = parts.join(' > ');
      }
      const all = [...document.querySelectorAll(sel)];
      const i = all.indexOf(el);
      return i > 0 ? `${sel} >> nth=${i}` : sel;
    };
    window.__crawl = {
      list(scopeSel, includeChrome) {
        const root = scopeSel ? document.querySelector(scopeSel) : document;
        if (!root) return [];
        const out = [], seenHref = new Set();
        root.querySelectorAll(INTERACTIVE).forEach((el) => {
          if (!visible(el)) return;
          if (!includeChrome && el.closest('.strip, .rail, .tabbar')) return;
          if (el.closest('[data-volatile]') && !el.dataset.act) return;
          if (el.parentElement && el.parentElement.closest('a[href], button')) return; // nested inside another control
          const href = el.tagName === 'A' ? el.getAttribute('href') : null;
          if (href) { if (seenHref.has(href)) return; seenHref.add(href); }
          const k = keyOf(el); if (k) out.push({ sel: k, text: (el.getAttribute('aria-label') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 50) });
        });
        return out;
      },
      state(sel) {
        const el = document.querySelector(sel.split(' >> ')[0]);
        return el;
      },
      arm() {
        window.__muts = 0;
        if (window.__obs) window.__obs.disconnect();
        window.__obs = new MutationObserver((ms) => { ms.forEach((m) => { const t = m.target.nodeType === 1 ? m.target : m.target.parentElement; if (!t || t.closest(IGNORE_MUT)) return; if (m.type === 'childList') { const nodes = [...m.addedNodes, ...m.removedNodes].filter((n) => !(n.nodeType === 1 && n.matches(IGNORE_MUT))); if (!nodes.length) return; } window.__muts++; }); });
        window.__obs.observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true, attributeFilter: ['class', 'aria-pressed', 'aria-checked', 'aria-expanded', 'aria-disabled', 'style', 'open', 'value', 'hidden'] });
        return { url: location.href, ls: localStorage.getItem('cd-proto-state-v4') || '', theme: localStorage.getItem('cd-theme') };
      },
      overlay() { const d = document.querySelectorAll('.drawer-scrim'); if (d.length) return '.drawer-scrim .cdrawer'; const o = document.querySelector('#pop-menu, #scrim, .palette'); return o ? (o.id ? '#' + o.id : '.palette') : null; },
    };
  });
}

async function info(page, sel) {
  const loc = page.locator(sel).first();
  if (!(await loc.count())) return null;
  return loc.evaluate((el) => {
    const disabled = el.disabled || el.getAttribute('aria-disabled') === 'true';
    const reason = el.dataset.reason || el.getAttribute('title') || el.dataset.tip || '';
    const current = el.getAttribute('aria-current') === 'page' || el.getAttribute('aria-pressed') === 'true' || el.getAttribute('aria-selected') === 'true' || (el.tagName === 'A' && el.getAttribute('href') === location.hash) || (el.tagName === 'A' && ('#' + el.getAttribute('href').split('#')[1]) === location.hash && el.getAttribute('href').startsWith('#'));
    const r = el.getBoundingClientRect();
    return { disabled, reason, current, visible: r.width > 0 && r.height > 0 && el.checkVisibility({ checkVisibilityCSS: true }) && !el.closest('details:not([open]) > :not(summary)') };
  });
}

async function crawlRoute(page, testInfo, route, { afterLoad } = {}) {
    const chrome = route === '#/overview';
    const report = { route, project: testInfo.project.name, clicked: 0, disabled: 0, current: 0, dead: [], broken: [], skipped: 0, sampledOut: 0 };
    const sampled = {};
    let downloads = 0;
    page.on('download', () => downloads++);
    const fresh = async () => { await go(page, route, { fresh: true }); if (afterLoad) await afterLoad(page); await inject(page); };
    await fresh();
    const initial = await page.evaluate((c) => window.__crawl.list(null, c), chrome);
    const queue = initial.map((x) => ({ ...x, path: [] }));
    const seen = new Set(initial.map((x) => x.sel));
    let dirty = false;
    while (queue.length) {
      const item = queue.shift();
      let st = await info(page, item.sel);
      if (dirty || !st || !st.visible) {
        await fresh(); dirty = false;
        for (const p of item.path) { const loc = page.locator(p).first(); if (await loc.count()) { await loc.click({ timeout: 2500 }).catch(() => {}); await page.waitForTimeout(60); } }
        st = await info(page, item.sel);
      }
      if (!st || !st.visible) { report.skipped++; continue; }
      if (st.disabled) { report.disabled++; if (!st.reason) report.broken.push(`${item.sel} "${item.text}": disabled with no reason`); continue; }
      if (st.current) { report.current++; continue; }
      await inject(page);
      const before = await page.evaluate(() => window.__crawl.arm());
      const dl0 = downloads;
      try { await page.locator(item.sel).first().click({ timeout: 2500 }); }
      catch (e) {
        const onTop = await page.locator(item.sel).first().evaluate((el) => { el.scrollIntoView({ block: 'center' }); const r = el.getBoundingClientRect(); const t = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2); return t ? (t === el || el.contains(t) ? 'itself' : t.closest('[id],[class]').outerHTML.slice(0, 120)) : 'nothing'; }).catch(() => 'gone');
        report.broken.push(`${item.sel} "${item.text}": could not be clicked (${e.message.split('\n')[0].slice(0, 60)}; on top: ${onTop}; path ${item.path.join(' → ')})`); dirty = true; continue;
      }
      await page.waitForTimeout(140);
      const after = await page.evaluate(() => ({ url: location.href, ls: localStorage.getItem('cd-proto-state-v4') || '', theme: localStorage.getItem('cd-theme'), muts: window.__muts || 0, overlay: window.__crawl ? window.__crawl.overlay() : false })).catch(() => ({ url: 'navigated', ls: '', muts: 1 }));
      report.clicked++;
      const changed = after.url !== before.url || after.ls !== before.ls || after.theme !== before.theme || after.muts > 0 || downloads > dl0;
      if (!changed) { report.dead.push(`${item.sel} "${item.text}"${item.path.length ? ` (after ${item.path.join(' → ')})` : ''}`); continue; }
      if (after.url !== before.url || after.ls !== before.ls) dirty = true;
      if (item.path.length < MAX_DEPTH && after.url === before.url) {
        const scope = after.overlay || null;
        await inject(page);
        const now = await page.evaluate(([s, c]) => window.__crawl.list(s, c), [scope, chrome]);
        now.forEach((x) => {
          if (seen.has(x.sel)) return; seen.add(x.sel);
          // Revealed controls rendered from one template (same data-act, different args:
          // a reference picker's leaves, every step's Move/Duplicate…) are sampled.
          const act = (x.sel.match(/data-act="([^"]+)"/) || [])[1];
          if (act) { sampled[act] = (sampled[act] || 0) + 1; if (sampled[act] > SAMPLE) { report.sampledOut++; return; } }
          queue.push({ ...x, path: item.path.concat([item.sel]) });
        });
      }
      if (after.overlay) dirty = true;
    }
    return report;
}

for (const route of ROUTES) {
  test(`every control works · ${route}`, async ({ page }, testInfo) => {
    const report = await crawlRoute(page, testInfo, route);
    testInfo.annotations.push({ type: 'crawl', description: `${report.project} ${route}: clicked ${report.clicked}, disabled-with-reason ${report.disabled}, current ${report.current}, skipped ${report.skipped}` });
    console.log(`[crawl] ${report.project.padEnd(7)} ${route.padEnd(38)} clicked ${String(report.clicked).padStart(3)} · disabled ${String(report.disabled).padStart(2)} · current ${String(report.current).padStart(2)} · dead ${report.dead.length} · broken ${report.broken.length} · sampled-out ${report.sampledOut}`);
    expect(report.dead, `dead controls on ${route}`).toEqual([]);
    expect(report.broken, `broken controls on ${route}`).toEqual([]);
  });
}

// The crawler must catch what it exists to catch: a button that does nothing,
// and a disabled control that doesn't say why.
test('crawler self-test: reports a dead button and an unexplained disabled one', async ({ page }, testInfo) => {
  const report = await crawlRoute(page, testInfo, '#/updates', { afterLoad: (p) => p.evaluate(() => {
    const h = document.querySelector('.page-head .actions');
    h.insertAdjacentHTML('beforeend', '<button class="btn" id="planted-dead">Does nothing</button><button class="btn" id="planted-mute" aria-disabled="true">Off, no reason</button>');
  }) });
  expect(report.dead.some((d) => d.includes('planted-dead'))).toBe(true);
  expect(report.broken.some((d) => d.includes('planted-mute') && d.includes('no reason'))).toBe(true);
  expect(report.dead.length).toBe(1);
});
