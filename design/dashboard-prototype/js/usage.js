/* Usage display (conductor's usage.mode): off | subscription | api.
 * off (the default): no usage or spend UI anywhere. Model accounts usually sit
 * behind a proxy or load balancer conductor can't see past, so anything it
 * showed unasked would be partial.
 * subscription: runs and turns conductor observes, per provider; plan-window
 * utilization and tokens only from a configured usage.source. api: dollars,
 * per provider. Never a total across providers or models: Claude and Codex
 * are separate, each in its own units, each figure labelled with its source.
 * Cloud spend (the commit_money tier, the aws-cost connector) is a real-world
 * action, not model usage; it shows in every mode, marked data-cloud-spend. */
(function () {
  const U = UI;
  const hash = (s) => { let h = 0; for (const c of String(s)) h = (h * 31 + c.charCodeAt(0)) >>> 0; return h; };
  U.usageMode = () => (D.prefs && D.prefs.usageMode) || 'off'; // a display preference (this browser), not config
  U.isApi = () => U.usageMode() === 'api';
  U.usageOn = () => U.usageMode() !== 'off';
  U.usageSource = () => (D.usageCfg && D.usageCfg.source) || null;
  U.fmtTok = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1000 ? Math.round(n / 1000) + 'k' : String(n || 0));
  U.provs = () => D.usage.providers;
  U.provLabel = (p) => (D.usage.providers[p] || { label: p }).label;
  U.provOf = (model) => (/gpt|codex|^o\d/.test(String(model)) ? 'codex' : 'claude');
  /** A run's provider and model: one each, so its own figures are never a mix. */
  U.runProv = (r) => r.provider || (hash(r.id) % 4 === 0 ? 'codex' : 'claude');
  U.runModel = (r) => r.model_id || (U.runProv(r) === 'codex' ? 'gpt-5-codex' : ['claude-sonnet-5', 'claude-opus-5', 'claude-haiku-4-5'][hash(r.id) % 3]);
  U.runTurns = (r) => r.turns != null ? r.turns : 2 + (hash(r.id) % 14);
  U.tokOf = (x) => (x && x.tokens) || Math.round(((x && (x.cost != null ? x.cost : x.cost_usd)) || 0) * 72000);
  U.srcLabel = (kind) => { const s = U.usageSource(); return kind === 'observed' || !s ? 'observed by conductor' : `from ${s.name}`; };
  const srcTag = (kind) => `<span class="pv-src" data-src="${kind === 'observed' || !U.usageSource() ? 'observed' : 'source'}">${U.srcLabel(kind)}</span>`;
  /** One provider's figure, in that provider's units. */
  const fig = (p, x) => {
    if (U.isApi()) return `${U.usd(x.usd || 0)}${x.turns != null ? ` · ${x.turns} turns` : ''}`;
    const parts = []; if (x.runs != null) parts.push(`${x.runs} runs`); if (x.turns != null) parts.push(`${x.turns} turns`);
    if (U.usageSource() && x.tokens != null) parts.push(`${U.fmtTok(x.tokens)} tok`);
    return parts.join(' · ');
  };
  /** Per-provider figures side by side, never summed. byProv: { claude: {...}, codex: {...} }. */
  U.provSet = (byProv, opts) => {
    if (!U.usageOn()) return '';
    opts = opts || {};
    const rows = Object.entries(byProv || {}).filter(([, x]) => x && (x.runs || x.turns || x.tokens || x.usd));
    if (!rows.length) return opts.empty === false ? '' : `<span class="pv-set" data-usage><span class="muted">no model use today</span></span>`;
    const kind = U.isApi() || (U.usageSource() && rows.some(([, x]) => x.tokens != null)) ? 'source' : 'observed';
    return `<span class="pv-set${opts.block ? ' block' : ''}" data-usage>${rows.map(([p, x]) => `<span class="pv" data-prov="${p}"><span class="pv-n">${U.provLabel(p)}</span> <b class="mono" data-figure>${fig(p, x)}</b></span>`).join('')}${opts.nosrc ? '' : srcTag(kind)}</span>`;
  };
  /** Today, per provider, for one bot (observed runs/turns; tokens from the source, dollars in api). */
  U.botProv = (name) => { const o = D.usage.bots[name] || {}, out = {}; Object.entries(o).forEach(([p, x]) => { out[p] = Object.assign({}, x, U.usageSource() ? { tokens: x.turns * 4200 } : {}, U.isApi() ? { usd: +(x.turns * 0.07).toFixed(2) } : {}); }); return out; };
  /** Several bots, still per provider: each provider's figures added only to itself. */
  U.botsProv = (names) => { const out = {}; names.forEach((n) => Object.entries(U.botProv(n)).forEach(([p, x]) => { const a = out[p] || (out[p] = { runs: 0, turns: 0 }); a.runs += x.runs || 0; a.turns += x.turns || 0; if (x.tokens != null) a.tokens = (a.tokens || 0) + x.tokens; if (x.usd != null) a.usd = +((a.usd || 0) + x.usd).toFixed(2); })); return out; };
  /** One run: its own provider and model, its own figure. */
  U.runFig = (r) => { if (!U.usageOn()) return ''; const p = U.runProv(r), x = { turns: U.runTurns(r) }; if (U.usageSource()) x.tokens = U.tokOf(r); if (U.isApi()) x.usd = r.cost != null ? r.cost : r.cost_usd; return `<span class="pv one" data-usage data-prov="${p}" title="${U.provLabel(p)} · ${U.runModel(r)} · ${U.srcLabel(U.isApi() || U.usageSource() ? 'source' : 'observed')}"><span class="pv-n">${U.provLabel(p)}</span> <b class="mono" data-figure>${U.isApi() ? U.usd(x.usd || 0) : U.usageSource() ? U.fmtTok(x.tokens) + ' tok' : x.turns + ' turns'}</b></span>`; };
  /** A workflow's recent runs, grouped by the provider each ran on. */
  U.wfProv = (name) => { const out = {}; D.runs.filter((r) => r.workflow === name).forEach((r) => { const p = U.runProv(r), a = out[p] || (out[p] = { runs: 0, turns: 0 }); a.runs++; a.turns += U.runTurns(r); if (U.usageSource()) a.tokens = (a.tokens || 0) + U.tokOf(r); if (U.isApi()) a.usd = +((a.usd || 0) + (r.cost || 0)).toFixed(2); }); return out; };
  U.measHead = () => (U.isApi() ? 'cost' : U.usageSource() ? 'tokens' : 'turns');
  /** A provider's plan windows, only as reported by the configured usage source. */
  const windows = (p) => { const r = D.usage.reported[p]; if (!U.usageSource() || !r) return ''; return r.windows.map((w) => `<div class="win-meter compact" data-usage data-prov="${p}"><div class="row small"><span class="grow"><b data-figure>${w.pct}%</b> of the ${w.span} limit</span><span class="mono tiny muted">resets in ${UI.humanSpan(w.resets)}</span></div><div class="meter"><i style="width:${w.pct}%;background:${w.pct > 85 ? 'var(--color-status-danger-solid)' : w.pct > 65 ? 'var(--color-status-warning-solid)' : 'var(--color-icon-default)'}"></i></div></div>`).join(''); };
  const spark = (vals, fmt, p) => { const max = Math.max(...vals) || 1; return `<div class="cchart sm"><div class="cbars">${vals.map((t, i) => `<div class="cbar${i === vals.length - 1 ? ' today' : ''}" data-tip="<b>${U.provLabel(p)} · ${new Date(Date.now() - (vals.length - 1 - i) * 864e5).toLocaleDateString('en', { month: 'short', day: 'numeric' })}</b><br><span class='mono'>${fmt(t)}</span>"><i style="height:${(t / max) * 100}%"></i></div>`).join('')}</div></div>`; };
  /** The overview panel: one card per provider, side by side, never a combined chart. */
  U.usagePanel = () => {
    const src = U.usageSource(), api = U.isApi();
    const card = (p) => { const d = D.usage.providers[p], o = D.usage.observed[p], r = D.usage.reported[p], $ = D.usage.usd[p];
      return `<div class="prov-card" data-usage data-prov="${p}"><div class="row"><b>${d.label}</b><span class="muted tiny">${d.vendor}</span></div>
        ${windows(p)}
        <div class="row small"><span class="grow muted">today</span><b class="mono" data-figure>${o.runs} runs · ${o.turns} turns</b></div><div class="pv-src tiny" data-src="observed">observed by conductor</div>
        ${api ? `<div class="row small"><span class="grow muted">API spend today</span><b class="mono" data-figure>${U.usd($.today)}</b></div>${spark($.daily, U.usd, p)}<div class="pv-src tiny" data-src="${src ? 'source' : 'observed'}">${src ? 'from ' + U.esc(src.name) : 'observed by conductor · from runtime reports'}</div>`
          : src ? `<div class="row small"><span class="grow muted">tokens today</span><b class="mono" data-figure>${U.fmtTok(r.today)}</b></div>${spark(r.daily, (t) => U.fmtTok(t) + ' tokens', p)}<div class="mono tiny muted">${Object.entries(r.models).map(([m, t]) => `${m} ${U.fmtTok(t)}`).join(' · ')}</div><div class="pv-src tiny" data-src="source">from ${U.esc(src.name)}</div>` : ''}</div>`; };
    return `<div class="panel" data-usage><div class="panel-head"><h3>Model usage</h3><span class="muted small">per provider · ${src ? 'limits from ' + U.esc(src.name) : 'no usage source configured'}</span><div class="tools"><a class="btn sm ghost" href="#/config">Usage settings</a></div></div><div class="panel-body"><div class="prov-grid">${Object.keys(D.usage.providers).map(card).join('')}</div>${src ? '' : `<div class="muted tiny" style="margin-top:8px">Plan limits and tokens come from a usage source (the proxy or load balancer in front of your model accounts). Without one, conductor shows only what it observes.</div>`}</div></div>`;
  };
  /** The overview stat tile: providers listed, not added. */
  U.usageStat = () => `<a class="panel stat" href="#/activity" data-usage><span class="k">Model usage · today</span><span class="prov-lines">${Object.keys(D.usage.providers).map((p) => { const r = D.usage.reported[p], w = U.usageSource() && r && r.windows[0], o = D.usage.observed[p]; return `<span class="row" data-prov="${p}"><span class="grow">${U.provLabel(p)}</span><b class="mono" data-figure>${U.isApi() ? U.usd(D.usage.usd[p].today) : w ? `${w.pct}% of ${w.span}` : `${o.turns} turns`}</b></span>`; }).join('')}</span><span class="d">${U.srcLabel(U.isApi() || U.usageSource() ? 'source' : 'observed')}</span></a>`;
})();
