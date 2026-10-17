// Status-mark check. Loads each page in every engine and OS colour scheme, and fails on:
//   1. a [data-mark] whose state isn't in status/marks.js;
//   2. a mark whose rendered colour isn't its state's token (probed in the mark's own theme scope);
//   3. a mark inside a chart (an <svg>) with no legend, or whose state is missing from the legend
//      of its [data-legend-scope]: every mark on a chart must appear in its legend, unless it is
//      labelled directly on the chart (data-labelled, as in the calm lanes);
//   4. a legacy status glyph that bypasses the vocabulary (old .gl/.sh/.lgd/.c-dot marks);
//   5. a needs-attention row whose leading edge is a different colour from its status mark.
//
//   node tools/mark-check.mjs [engine,engine]   (serve design/brand over HTTP; BRAND_BASE=url)
import { available, context, closeAll } from './browsers.mjs';

const BASE = process.env.BRAND_BASE || 'http://localhost:8768';
const PAGES = ['system.html', 'system.html?theme=dark', 'status-review.html', 'explore/status-column.html'];
for (const s of ['overview', 'run', 'approval', 'history', 'builder', 'activity', 'bot']) for (const t of ['light', 'dark']) PAGES.push(`v2/mock.html?d=daylight&s=${s}&t=${t}`, `v2/mock.html?d=daylight&s=${s}&t=${t}&v=before`);
for (const t of ['light', 'dark']) PAGES.push(`v2/mock.html?d=daylight&s=overview&t=${t}&day=quiet`);
// The scannable proposal: its review page and its five proposed screens.
PAGES.push('scannable-review.html');
for (const s of ['overview', 'history', 'approval', 'perms', 'picker']) for (const t of ['light', 'dark']) PAGES.push(`v2/mock.html?d=daylight&v=scan&s=${s}&t=${t}`);
const LEGACY = '.lgd, .c-dot, svg.gl, svg.sh, .ty-state > svg:not([data-mark]), .c-status > svg:not([data-mark]), .c-badge[data-s] > svg:not([data-mark])';

function audit({ base, legacy }) {
  return import(`${base}/status/marks.js`).then(({ STATES, CHANGE }) => {
    const ALL = { ...STATES, ...CHANGE }, out = { marks: 0, legends: 0, problems: [] };
    const where = (el) => (el.closest('[id]')?.id || el.closest('[class]')?.getAttribute('class') || '').toString().slice(0, 40);
    const rgb = (el) => getComputedStyle(el).color;
    for (const el of document.querySelectorAll('[data-mark]')) {
      out.marks++;
      const st = el.dataset.mark, S = ALL[st];
      if (!S) { out.problems.push(`unknown state “${st}” at ${where(el)}`); continue; }
      const host = el.closest('svg:not([data-mark])')?.parentElement ?? el.parentElement;
      const probe = document.createElement('span'); probe.style.color = `var(${S.mark})`; host.appendChild(probe);
      if (rgb(el) !== rgb(probe)) out.problems.push(`${st} mark is ${rgb(el)}, token ${S.mark} is ${rgb(probe)} at ${where(el)}`);
      probe.remove();
      const inChart = !!el.parentElement.closest('svg');
      const scope = el.closest('[data-legend-scope]');
      if (inChart && el.dataset.labelled) continue;
      if (inChart && !scope) { out.problems.push(`chart mark ${st} has no legend at ${where(el)}`); continue; }
      if (scope) {
        const lg = scope.querySelector('[data-legend]');
        if (!lg) out.problems.push(`legend scope without a legend at ${where(el)}`);
        else if (!el.closest('[data-legend]') && !lg.dataset.legend.split(' ').includes(st)) out.problems.push(`${st} mark not in its legend (${lg.dataset.legend}) at ${where(el)}`);
      }
    }
    // 5. A row that needs someone: its leading edge must be its mark's colour.
    for (const row of document.querySelectorAll('tr.attn, li.attn')) {
      const mk = row.querySelector('[data-mark]'); if (!mk) continue;
      const edgeOf = (el) => (getComputedStyle(el).boxShadow.match(/rgba?\([^)]+\)/) || [])[0];
      const edge = edgeOf(row) || edgeOf(row.querySelector('td') || row);
      if (edge && edge.replace(/\s/g, '') !== rgb(mk).replace(/\s/g, '')) out.problems.push(`row edge ${edge} differs from its ${mk.dataset.mark} mark ${rgb(mk)} at ${where(row)}`);
    }
    out.legends = document.querySelectorAll('[data-legend]').length;
    for (const el of document.querySelectorAll(legacy)) out.problems.push(`legacy status glyph (${el.tagName.toLowerCase()}.${el.getAttribute('class') || ''}) at ${where(el)}`);
    return out;
  });
}

let failing = 0, marks = 0;
for (const engine of available(process.argv[2])) {
  for (const os of engine.osModes ?? ['light', 'dark']) {
    const ctx = await context(engine, os); const page = await ctx.newPage();
    for (const p of PAGES) {
      await page.goto(`${BASE}/${p}`); await page.waitForTimeout(700);
      const r = await page.evaluate(audit, { base: BASE, legacy: LEGACY });
      marks += r.marks;
      if (r.problems.length) { failing++; console.log(`FAIL ${engine.id} · OS ${os} · ${p}: ${r.problems.length} problems`); for (const x of [...new Set(r.problems)].slice(0, 8)) console.log('   ', x); }
    }
    await ctx.close();
  }
}
await closeAll();
console.log(`${marks} marks checked · ${failing} page renders failing`);
process.exit(failing ? 1 : 0);
