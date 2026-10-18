// The current Daylight screens: the approved reference for the dashboard (see qa/SPEC.md).
// Overview, Activity (which holds what was the run history), approvals, a bot. Rows use the fixed
// row anatomy from status/rows.js; runs over time use the run strip from status/calm.js.
// No greetings, no summary sentences that restate the screen, no pills, no lanes, no digests.
import { rows, kind, env, flag, lucide, status, KINDS } from '../status/rows.js';
import { runStrip, byWorkflow, atAGlance } from '../status/calm.js';
import { mark } from '../status/marks.js';
import * as C from './data.js';
import * as S from './rows-data.js';

const chev = lucide('chevron-right', 14);
const fp = lucide('fingerprint');

// ---------- Overview ----------
// Four At a glance tiles · Needs you (one action each) · Running now (capped) · runs per hour with
// the failures named · For your information · Get started (only what remains).
export function overview() {
  const running = S.RUNNING, cap = 3;
  return `<h1 class="cur-title">Overview</h1>
    ${atAGlance([[S.NEEDS.length, 'Need you'], [running.length, 'Running'], [S.FAILING.length, 'Failed', '12 h'], ['$8.40', 'Spent', 'today']])}
    <section class="card cur-card">${rows([['needs', S.NEEDS]])}</section>
    <section class="card cur-card">${rows([['running', running.slice(0, cap), `<em><a class="link">+${running.length - cap} more</a></em>`, running.length]], { head: false })}</section>
    <section class="card cur-card"><header class="ch"><h2>Runs per hour</h2><span class="sub">last 12 hours</span></header>${runStrip(C.HOURLY)}
      <ul class="cur-failed">${S.FAILING.map((f) => `<li>${mark('failed', { size: 12, label: true })}<b>${f.title}</b><span>${f.time} ago</span></li>`).join('')}</ul></section>
    <div class="cols cur-cols"><section class="card"><header class="ch"><h2>For your information</h2></header><ul class="cur-fyi">${C.FYI.map((f) => `<li><b>${f.title}</b><span>${f.why}</span></li>`).join('')}</ul></section>
    <section class="card"><header class="ch"><h2>Get started</h2></header><ul class="cur-fyi">
      <li><b>Connect PagerDuty</b><span>So bots can see and acknowledge incidents.</span><button class="btn secondary sm">Connect</button></li>
      <li><b>Add a second operator</b><span>Approvals need someone else when you’re away.</span><button class="btn secondary sm">Invite</button></li></ul></section></div>`;
}

// ---------- Activity (runs over time, and every run) ----------
export function activity() {
  const A = C.ACTIVITY;
  // The exceptions grouped by workflow are the attention list, so the run list below starts at Running now.
  return `<h1 class="cur-title">Activity</h1>
    <div class="filters"><span class="field">${lucide('search')}Search runs, bots, repos</span><span class="chip">${lucide('workflow')}Any workflow</span><span class="chip">${lucide('clock')}Last 12 hours</span></div>
    <section class="card cur-card">${runStrip(A.hours)}${byWorkflow(A.exceptions, A.healthy)}</section>
    <section class="card cur-card">${rows([['running', S.RUNNING], ['fine', S.FINISHED, `<em>12 of 200 · <a class="link">Show more</a></em>`]])}</section>`;
}

// ---------- Approvals ----------
const PLAN = [
  ['#', '  # aws_instance.web must be replaced'],
  ['-', '-/+ resource "aws_instance" "web" {'],
  ['~', '      ~ ami           = "ami-0a1b2c" -> "ami-0c3d4e"'],
  ['', '        instance_type = "m6i.large"'],
  ['', '    }'],
  ['+', '  + resource "aws_lb_target_group" "web_v2" {'],
  ['+', '      + port     = 8080'],
  ['+', '      + protocol = "HTTP"'],
  ['', '    }'],
  ['~', '  ~ resource "aws_lb_listener" "web" {'],
  ['~', '      ~ default_action.target_group_arn = (known after apply)'],
  ['', '    }'],
  ['', '  Plan: 1 to add, 2 to change, 0 to destroy (1 replaced).'],
];
const plan = () => `<pre class="code cur-plan">${PLAN.map(([k, l]) => `<span class="pl${k === '+' ? ' pl-add' : k === '-' ? ' pl-del' : k === '~' ? ' pl-chg' : ''}">${l.replace(/</g, '&lt;')}</span>`).join('')}</pre>`;
// Approve and Deny are both signed with the passkey; the bar is sticky and never covers the title or why.
const decideBar = (note) => `<div class="cur-decide"><button class="btn primary lg">${fp}Approve</button><button class="btn secondary lg">${fp}Deny…</button><span class="hint">${note}</span></div>`;

export function approval(variant = 'terraform', tab = 'overview') {
  if (variant === 'slack') {
    return `<div class="crumbs"><a>Bots</a>${chev}<a>billing-sre</a>${chev}<a>Proposals</a></div>
    <header class="cur-appr" data-s="accent"><div class="sa-strip">${kind('approval')}<i>·</i>${kind('connector', { word: false })}<span>Slack</span>${status('needs')}<span class="sa-age">asked 22 min ago · expires in 23 h</span></div>
      <h1>Post the monthly forecast to #incidents?</h1>
      <p class="lead">A Slack user who isn’t one of your operators asked billing-sre for it. The message goes to 412 people.</p></header>
    <section class="card cur-card"><header class="ch"><h2>What it will post</h2></header>
      <blockquote class="cur-msg"><b>Monthly cloud forecast</b><br>October is on track: $8,940 projected against a $9,500 budget. Compute is 61% of spend.</blockquote>
      <details class="tech"><summary>${lucide('chevron-right')}Technical details <span class="sub">verb, channel and payload</span></summary><dl><dt>Verb</dt><dd class="mono">slack-ops.post</dd><dt>Channel</dt><dd class="mono">#incidents</dd><dt>Proposal</dt><dd class="mono">p4</dd></dl></details></section>
    ${decideBar('Only operators on team billing can approve.')}`;
  }
  const tabs = `<nav class="tabs cur-tabs"><a class="${tab === 'overview' ? 'on' : ''}">Overview</a><a class="${tab === 'tech' ? 'on' : ''}">Technical details</a></nav>`;
  const body = tab === 'tech'
    ? `<section class="card cur-card"><header class="ch"><h2>The plan</h2><span class="sub">envs/prod · workspace prod</span></header>${plan()}
        <dl class="risk"><dt>Proposal</dt><dd class="mono">p12</dd><dt>Plan</dt><dd class="mono">infra@3f2a9c1d</dd><dt>Plan sha256</dt><dd class="mono">9c1e…4b07</dd><dt>Command</dt><dd class="mono">terraform apply -input=false plan.tfplan</dd></dl></section>`
    : `<section class="card cur-card"><header class="ch"><h2>What it will do</h2></header>
        <table class="tbl scan-chg"><tbody>${[['+', 'Adds 1', 'aws_lb_target_group.web_v2', 'A target group for the new image'], ['~', 'Changes 2', 'aws_lb_listener.web, aws_autoscaling_group.web', 'Points traffic at the new group'], ['±', 'Replaces 1', 'aws_instance.web', 'Destroyed and recreated with the new image. Not stateful.'], ['−', 'Destroys 0', '—', 'Nothing']].map(([sym, w, res, note]) => `<tr><td class="sym">${sym}</td><td><b>${w}</b></td><td class="res"><code>${res}</code></td><td class="sub">${note}</td></tr>`).join('')}</tbody></table>
        <dl class="risk"><dt>Where</dt><dd>${env('production')} AWS account prod · workspace prod</dd><dt>Effect</dt><dd>About 2 minutes of drain while 1 server is replaced</dd><dt>Undo</dt><dd>Re-apply the previous plan (kept 7 days)</dd><dt>Started by</dt><dd>A PagerDuty incident, not a person</dd></dl></section>`;
  return `<div class="crumbs"><a>Bots</a>${chev}<a>platform-sre</a>${chev}<a>Proposals</a></div>
    <header class="cur-appr" data-s="accent"><div class="sa-strip">${kind('approval')}<i>·</i>${kind('infra')}<i>·</i>${flag('prod')}${status('needs')}<span class="sa-age">asked 7 min ago · expires in 23 h 52 min</span></div>
      <h1>Replace the production web server?</h1>
      <p class="lead">platform-sre wants to run <b>terraform apply</b> on envs/prod to fix the SEV2 incident “api p99 latency” (case c29).</p></header>
    ${tabs}${body}
    ${decideBar('Only operators on team platform can approve.')}`;
}

// ---------- A bot (members are now bots, kept in folders) ----------
export function bot() {
  const props = S.NEEDS.filter((x) => x.ctx[0] === 'platform-sre');
  const recent = S.FINISHED.filter((x) => x.ctx[0] === 'platform-sre');
  return `<div class="crumbs"><a>Bots</a>${chev}<a>work / platform</a></div>
    <div class="bothead"><span class="bot-av">${lucide('bot', 24)}</span><div><h1>platform-sre</h1><p class="lead">Keeps production healthy for the platform team</p></div>${status('running', 'Working on case c31')}</div>
    <nav class="tabs"><a>Chat</a><a>Cases <em>4</em></a><a class="on">Proposals <em>${props.length}</em></a><a>Memory</a><a>Access</a><a>Definition</a></nav>
    <section class="card cur-card" style="margin-top:20px">${rows([['needs', props], ['fine', recent, '<em><a class="link">All of its runs</a></em>']])}</section>`;
}

export const CURRENT_CSS = `
.cur-title { margin: 6px 0 18px; }
.at-glance + .card, .cur-card + .cur-card, .cur-card + .cols { margin-top: 18px; }
.at-glance { margin-bottom: 18px; }
.cur-card { padding: 18px 18px 14px; }
.cur-failed { list-style: none; margin: 12px 0 0; padding: 0; display: grid; gap: 6px; }
.cur-failed li { display: flex; gap: 10px; align-items: baseline; font-size: 14px; } .cur-failed li span:last-child { color: var(--color-text-tertiary); font-size: 13px; }
.cur-fyi { list-style: none; margin: 0; padding: 0; display: grid; gap: 12px; }
.cur-fyi li { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 2px 12px; align-items: center; }
.cur-fyi li span { grid-column: 1; font-size: 14px; color: var(--color-text-secondary); } .cur-fyi li .btn { grid-column: 2; grid-row: 1 / span 2; }
.cur-cols { margin-top: 18px; grid-template-columns: 1fr 1fr; }
.cur-appr { padding: 4px 0 18px 22px; margin: 8px 0 4px; box-shadow: inset 4px 0 0 var(--s-solid); }
.cur-appr h1 { margin-top: 10px; max-width: 22em; }
.cur-tabs { margin-top: 8px; margin-bottom: 18px; }
.cur-plan .pl { display: block; white-space: pre; }
.cur-plan .pl-add { color: var(--color-code-string); background: color-mix(in srgb, var(--color-status-success-bg) 60%, transparent); }
.cur-plan .pl-del { color: var(--color-status-danger-fg); background: color-mix(in srgb, var(--color-status-danger-bg) 60%, transparent); }
.cur-plan .pl-chg { color: var(--color-status-warning-fg); }
.cur-msg { margin: 0 0 14px; padding: 12px 14px; border-left: 3px solid var(--color-border-strong, var(--color-border-default)); background: var(--color-bg-sunken); border-radius: 0 8px 8px 0; line-height: 1.5; }
/* the decision bar: sticky at the bottom of the page, after the content, so it never covers the title or why */
.cur-decide { position: sticky; bottom: 0; z-index: 4; display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin: 18px -36px -56px; padding: 14px 36px; background: var(--color-bg-surface); border-top: 1px solid var(--color-border-default); box-shadow: 0 -8px 20px -16px rgb(0 0 0 / .4); }
.cur-decide .hint { font-size: 13px; color: var(--color-text-tertiary); }
@media (max-width: 600px) {
  .cur-cols { grid-template-columns: 1fr; }
  .cur-card { padding: 12px 10px; }
  .cur-decide { bottom: 76px; margin: 14px -16px 0; padding: 10px 16px; } .cur-decide .btn { flex: 1 1 40%; justify-content: center; } .cur-decide .hint { display: none; }
}`;
