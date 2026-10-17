// The five proposed screens for the scannable review (rendered by v2/mock.html?d=daylight&v=scan&s=…).
// Everything a person uses to understand or decide stays visible; only ids, hashes and raw payloads
// sit behind a disclosure or a hover.
import { KINDS, ACTIONS, STEPS, FLAGS, flagOf, flag, env, kind, lucide, rows, status } from './scan.js';
import { atAGlance, injectCalmCSS } from '../status/calm.js';
import * as S from './data.js';

const chev = lucide('chevron-right', 14);
const n = (xs, f) => xs.filter(f).length;

export function overview() {
  injectCalmCSS();
  const high = S.NEEDS.filter((x) => flagOf(x));
  return `<section class="hello scan-hello"><div><h1>Good afternoon, ops.</h1>
    <p class="lead"><b>${S.NEEDS.length} things need you</b>, ${high.length} of them high-risk: a production apply, an access grant and a $1,800 purchase. ${S.FAILING.length} runs are failing, ${S.RUNNING.length} are running, and 18 of 20 runs in the last 12 hours went fine.</p></div></section>
    ${atAGlance([[S.NEEDS.length, 'Need you'], [S.FAILING.length, 'Failing'], [S.RUNNING.length, 'Running'], ['18 of 20', 'Went fine', '12 h'], ['$8.40', 'Spent', 'today']])}
    <section class="card scan-card">${rows([['needs', S.NEEDS], ['failing', S.FAILING], ['running', S.RUNNING], ['fine', S.FINISHED.slice(0, 4), `<em>4 most recent of ${S.FINISHED.length} · <a class="link">Run history</a></em>`]])}</section>`;
}

export function history() {
  const needs = S.NEEDS.slice(0, 3).map((x) => ({ ...x, kind: 'run', title: `Waiting: ${x.title.charAt(0).toLowerCase()}${x.title.slice(1)}`, why: x.why }));
  const all = [...needs, ...S.FAILING, ...S.RUNNING, ...S.FINISHED];
  return `<div class="phead"><div><h1>Run history</h1><p class="lead"><b>Last 12 hours: ${all.length} runs.</b> ${needs.length} wait for you, ${S.FAILING.length} failed, ${S.RUNNING.length} are running and ${S.FINISHED.length} finished.</p></div></div>
    <div class="filters"><span class="field">${lucide('search')}Search runs, bots, repos</span><span class="chip">${lucide('workflow')}Any workflow</span><span class="chip">${lucide('clock')}Last 12 hours</span></div>
    <section class="card scan-card">${rows([['needs', needs], ['failing', S.FAILING], ['running', S.RUNNING], ['fine', S.FINISHED]])}</section>`;
}

export function approval() {
  const p = S.NEEDS[0];
  const changes = [['+', 'Adds', 1, 'aws_lb_target_group.web_v2', 'A target group for the new image'], ['~', 'Changes', 2, 'aws_lb_listener.web, aws_autoscaling_group.web', 'Points traffic at the new group'], ['±', 'Replaces', 1, 'aws_instance.web', 'Destroyed and recreated with the new image. Not stateful.'], ['−', 'Destroys', 0, '—', 'Nothing']];
  return `<div class="crumbs"><a>Bots</a>${chev}<a>platform-sre</a>${chev}<a>Proposals</a></div>
    <header class="scan-appr" data-item data-s="accent">
      <div class="sa-strip">${kind('approval')}<i>·</i>${kind('infra')}<i>·</i>${flag('prod')}${status('needs')}<span class="sa-age">asked 7 min ago · expires in 23 h 52 min</span></div>
      <h1>Apply the Terraform plan to production?</h1>
      <p class="lead">platform-sre wants to run <b>terraform apply</b> on <b>envs/prod</b> to fix the SEV2 incident “api p99 latency” (case c29). It replaces 1 web server; about 2 minutes of drain.</p>
      <p class="sa-ctx">${kind('bot', { word: false })}<b>platform-sre</b> · team platform · ${kind('incident', { word: false })}started by a PagerDuty incident, not a person</p>
    </header>
    <div class="cols"><div class="col-main">
      <section class="card"><header class="ch"><h2>What it will do</h2></header>
        <table class="tbl scan-chg"><tbody>${changes.map(([sym, w, c, res, note]) => `<tr class="${c ? '' : 'none'}"><td class="sym">${sym}</td><td><b>${w} ${c}</b></td><td class="res"><code>${res}</code></td><td class="sub">${note}</td></tr>`).join('')}</tbody></table>
        <dl class="risk"><dt>Where</dt><dd>${env('production')} AWS account prod · workspace prod</dd><dt>Effect</dt><dd>About 2 minutes of drain while 1 server is replaced</dd><dt>Undo</dt><dd>Re-apply the previous plan (kept 7 days)</dd></dl>
        <div class="decide"><button class="btn primary lg">${lucide('fingerprint')}Approve with passkey</button><button class="btn secondary lg">Deny…</button><span class="hint">Only operators on team platform can approve.</span></div>
        <details class="tech"><summary>${lucide('chevron-right')}Technical details <span class="sub">plan hash, exact command, raw plan</span></summary><dl><dt>Proposal</dt><dd class="mono">p12</dd><dt>Plan</dt><dd class="mono">infra@3f2a9c1d · envs/prod</dd><dt>Plan sha256</dt><dd class="mono">9c1e…4b07</dd></dl></details>
      </section></div>
      <div class="col-side"><section class="card"><header class="ch"><h2>platform-sre can also</h2></header>
        <ul class="scan-mini">${[['read', 'Read metrics, logs and repos'], ['write', 'Acknowledge pages'], ['apply', 'Apply Terraform to staging'], ['spend', 'Buy savings plans', 'Spends up to $2,000']].map(([a, t, w]) => `<li>${lucide(ACTIONS[a].icon)}<span>${t}</span>${w ? flag('money', w) : ''}</li>`).join('')}</ul>
        <a class="link">All of its permissions</a></section></div></div>`;
}

const ORDER = (f) => ['destroy', 'access', 'money', 'prod', null].indexOf(f);
export function perms() {
  const P = S.PERMS;
  const TIERCODE = { observe: 'observe', reversible: 'reversible', propose: 'propose', commit_money: 'commit_money', never: 'never' };
  return `<div class="crumbs"><a>Bots</a>${chev}<a>work / platform</a></div>
    <div class="bothead"><span class="bot-av">${lucide('bot', 24)}</span><div><h1>${P.bot}</h1><p class="lead">${P.role}</p></div>${status('running', 'Working on case c31')}</div>
    <nav class="tabs"><a>Chat</a><a>Cases <em>4</em></a><a>Proposals <em>3</em></a><a>Memory</a><a class="on">Access</a><a>Definition</a></nav>
    
    <section class="card scan-card"><div class="prows">
      <div class="prow phead"><span></span><span>What it may do</span><span>Where</span><span></span></div>
      ${P.groups.map(([tier, title, sub, verbs]) => `<div class="pgroup"><div class="pgh"><b>${title}</b><code>${TIERCODE[tier]}</code>${sub ? `<span>${sub}</span>` : ''}</div>
        ${[...verbs].sort((a, b) => ORDER(flagOf({ action: a[0], tier: a[4], env: a[3] })) - ORDER(flagOf({ action: b[0], tier: b[4], env: b[3] }))).map(([a, words, code, e, t, note, fw]) => { const f0 = flagOf({ action: a, tier: t, env: e }), f = f0 === 'prod' ? null : f0; return `<div class="prow${t === 'never' ? ' never' : ''}"><span class="pa" title="${ACTIONS[a].word}">${lucide(ACTIONS[a].icon)}</span><span class="pv"><b>${words}</b><code>${code}</code>${note ? `<em>${note}</em>` : ''}</span><span>${env(e)}</span><span>${f ? flag(f, fw) : ''}</span></div>`; }).join('')}</div>`).join('')}
    </div></section>`;
}

export function picker() {
  const icon = (type, k) => (type === 'action' ? lucide(ACTIONS[k].icon, 18) : type === 'step' ? lucide(STEPS[k].icon, 18) : lucide(KINDS[k].icon, 18));
  const word = (type, k) => (type === 'action' ? ACTIONS[k].word : type === 'step' ? STEPS[k].word : KINDS[k].word);
  return `<div class="crumbs"><a>Workflows</a>${chev}<a>ci-fixer</a></div>
    <section class="card scan-picker" role="dialog" aria-label="Add a step"><header class="ch"><h2>Add a step</h2><span class="sp"></span>${lucide('x')}</header>
      <span class="field">${lucide('search')}Search steps and connector verbs</span>
      ${S.PICKER.map(([g, items]) => `<h3 class="pk-g">${g}</h3><ul class="pk">${items.map(([type, k, name, sub]) => `<li><span class="pk-i" title="${word(type, k)}">${icon(type, k)}</span><span class="pk-t"><b>${name}</b><span>${sub}</span></span></li>`).join('')}</ul>`).join('')}
    </section>`;
}

export const SCAN_SCREEN_CSS = `
.scan-hello { margin-bottom: 18px; }
.scan-card { padding: 18px 18px 14px; }
.filters .chip .rk { padding: 2px 6px 2px 4px; font-size: 12px; }
.filters .chip em { font-style: normal; font-weight: 700; margin-left: 4px; }
.filters .chip { white-space: nowrap; }
.scan-appr { padding: 6px 0 22px 22px; margin: 8px 0 6px; box-shadow: inset 4px 0 0 var(--s-solid); }
.sa-strip { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.sa-strip > i { font-style: normal; color: var(--color-text-tertiary); }
.sa-age { font-size: 13px; color: var(--color-text-tertiary); }
.scan-appr h1 { margin-top: 12px; max-width: 22em; }
.sa-ctx { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 10px; font-size: 14px; color: var(--color-text-secondary); }
.scan-chg td { padding: 8px 10px; vertical-align: top; border-top: 1px solid var(--color-border-subtle); }
.scan-chg .sym { font: 700 15px var(--font-family-mono); width: 18px; color: var(--color-text-primary); }
.scan-chg tr.none td { color: var(--color-text-tertiary); }
.scan-chg .res code { font: var(--type-code-s, 12.5px var(--font-family-mono)); }
.ch .actn { margin-left: auto; }
.risk dd .env { margin-right: 6px; }
.scan-mini { list-style: none; margin: 0 0 12px; padding: 0; display: grid; gap: 10px; }
.scan-mini li { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; gap: 8px; align-items: center; font-size: 14px; }
.bot-av svg { color: var(--color-text-primary); }
.bothead .mk-status { margin-left: auto; }
.tabs + .scan-card { margin-top: 20px; }
.prows { --pcols: 24px minmax(0, 1fr) 120px 200px; }
.pa { display: grid; place-items: center; color: var(--color-text-primary); }
.prow { display: grid; grid-template-columns: var(--pcols); gap: 14px; align-items: center; min-height: 48px; padding: 6px 10px; border-top: 1px solid var(--color-border-subtle); }
.prow.phead { min-height: 0; border-top: 0; font-size: 12px; font-weight: 600; color: var(--color-text-tertiary); padding-bottom: 6px; }
.prow > span { min-width: 0; }
.pv { display: flex; flex-wrap: wrap; gap: 2px 10px; align-items: baseline; }
.pv b { font-weight: 600; } .pv code { font: 12.5px var(--font-family-mono); color: var(--color-text-tertiary); } .pv em { font-style: normal; font-size: 12.5px; color: var(--color-text-secondary); }
.prow.never .pv b { text-decoration: line-through; text-decoration-thickness: 1.5px; }
.pgroup + .pgroup { margin-top: 16px; }
.pgh { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; padding: 6px 2px 8px; border-bottom: 1px solid var(--color-border-default); }
.pgh b { font-size: 15px; } .pgh code { font: 12.5px var(--font-family-mono); color: var(--color-text-tertiary); } .pgh span { font-size: 13.5px; color: var(--color-text-secondary); }
.scan-picker { max-width: 860px; padding: 20px 22px; box-shadow: var(--shadow-3, var(--shadow-2)); }
.scan-picker .field { margin: 6px 0 4px; width: 100%; }
.pk-g { margin: 18px 0 6px; font-size: 13px; font-weight: 700; color: var(--color-text-secondary); }
.pk { list-style: none; margin: 0; padding: 0; }
.pk li { display: grid; grid-template-columns: 22px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 48px; padding: 6px 4px; border-top: 1px solid var(--color-border-subtle); }
.pk-i { color: var(--color-text-primary); display: grid; place-items: center; }
.pk-t { display: grid; } .pk-t b { font-weight: 600; } .pk-t span { font-size: 13px; color: var(--color-text-tertiary); }
@media (max-width: 600px) {
  .filters { flex-wrap: nowrap; overflow-x: auto; } .filters .field { display: none; }
  .scan-card { padding: 12px 10px; }
  .scan-appr { padding-left: 16px; }
  .prows { --pcols: none; }
  .prow { grid-template-columns: 22px minmax(0, 1fr); grid-template-areas: "a v" ". w" ". r"; gap: 4px 10px; padding: 10px 4px; }
  .prow > span:empty { display: none; }
  .prow.phead { display: none; }
  .prow > span:nth-child(1) { grid-area: a; } .prow > span:nth-child(2) { grid-area: v; } .prow > span:nth-child(3) { grid-area: w; } .prow > span:nth-child(4) { grid-area: r; }
  .scan-picker { padding: 14px 12px; }
}`;
