/* Connector attributes: the one place the dashboard learns what a connector's events and items carry.
 *
 * Core and the screens know nothing about any one connector: not repos, pull requests, channels or services. Each
 * connector declares its attributes in its schema (SCHEMA.connectors[use].attrs: label, icon, search, display, where
 * the value comes from in an event's fields, how it reads, where it links, and whether it's a filter worth its own
 * menu by default: `search` publishes a filter, `menu` puts it beside Kind/Bot/Workflow, the rest go under More
 * filters). The dashboard API serves these for every
 * configured connection (GET /api/v1/connectors/attrs, docs/design/dashboard.md), so a third-party connector's
 * attributes are searchable and shown with nothing added to the dashboard. This file plays that API in the prototype.
 *
 *   UI.connAttrs()        every attribute of every configured connection: { key: 'github.repo', alias: 'repo' (when no
 *                         other connector has that name), conn: 'github', label, group: 'GitHub', icon, search, display }
 *   UI.itemAttrs(run)     { 'github.repo': 'acme/web', 'github.pr': 'acme/web#1288', … } for a run (from its event)
 *   UI.itemFacts(run)     the display attributes as [{ key, label, icon, value, href }]
 *   UI.runTarget(run)     what the run's event is about, in the connector's own words ("acme/web#1288"), or ''
 *   UI.itemLinks(run)     [{ label, href }]: where the connector says its items open (the pull request on GitHub)
 *   UI.eventFields(run)   the event's own fields, for a retry to carry over
 *   UI.attrQualifiers(items) the filters the connectors publish, for the filter bar: a menu each for the primary ones,
 *                         the rest under More filters, and every one a search qualifier; only those with a value
 *                         in the items in view (no GitHub connection or nothing from GitHub here: no Repo menu) */
(function () {
  const view = () => (window.S && S.view ? S.view() : window.D);
  const schemaOf = (use) => (window.SCHEMA && SCHEMA.connectors[use]) || null;
  const useOf = (c) => String(((c.cfg || {}).use) || c.use || '').split('@')[0];
  const getPath = (o, p) => String(p).split('.').reduce((a, k) => (a == null ? a : a[k]), o);
  const fill = (fmt, ev) => { let ok = true; const out = String(fmt).replace(/\{([\w.]+)\}/g, (_, k) => { const v = getPath(ev, k); if (v == null || v === '' || v === 0) ok = false; return v == null ? '' : v; }); return ok ? out : ''; };

  /** The configured connections' attributes, namespaced by their connector; a bare alias when the name is unique. */
  UI.connAttrs = () => { const seen = new Set(), out = [];
    (view().connectors || []).forEach((c) => { const use = useOf(c), sc = schemaOf(use); if (!sc || !sc.attrs || seen.has(use)) return; seen.add(use);
      Object.entries(sc.attrs).forEach(([name, a]) => out.push({ key: `${use}.${name}`, name, conn: use, group: sc.label || use, label: a.label || name, icon: a.icon || 'dot', search: !!a.search, display: !!a.display, a })); });
    const count = {}; out.forEach((x) => (count[x.name] = (count[x.name] || 0) + 1));
    out.forEach((x) => { if (count[x.name] === 1) x.alias = x.name; });
    return out; };

  /** The connection a run's event came from (its trigger names the connection instance), and that event's fields. */
  const eventOf = (r) => { if (!r) return null; const inst = String(r.trigger || '').split(/[.\s]/)[0], c = (view().connectors || []).find((x) => x.name === inst);
    if (!c) return null; const use = useOf(c), sc = schemaOf(use); if (!sc) return null;
    return { use, sc, fields: r.event || r }; };
  UI.eventFields = (r) => { const e = eventOf(r); if (!e) return {}; const keys = new Set(); Object.values(e.sc.events || {}).forEach((ev) => Object.keys(ev.fields || {}).forEach((k) => keys.add(k)));
    const out = {}; keys.forEach((k) => { if (e.fields[k] != null) out[k] = e.fields[k]; }); return out; };
  UI.itemAttrs = (r) => { const e = eventOf(r), out = {}; if (!e) return out;
    Object.entries(e.sc.attrs || {}).forEach(([name, a]) => { const v = a.format ? fill(a.format, e.fields) : getPath(e.fields, a.from || name); if (v != null && v !== '' && v !== 0 && !(Array.isArray(v) && !v.length)) out[`${e.use}.${name}`] = Array.isArray(v) ? v.join(', ') : String(v); });
    return out; };
  UI.itemFacts = (r) => { const e = eventOf(r); if (!e) return []; const vals = UI.itemAttrs(r);
    return Object.entries(e.sc.attrs || {}).filter(([name, a]) => a.display && vals[`${e.use}.${name}`]).map(([name, a]) => ({ key: `${e.use}.${name}`, label: a.label || name, icon: a.icon || 'dot', value: vals[`${e.use}.${name}`], href: a.link ? fill(a.link, e.fields) : '' }))
      // a fact that another one already says (the repository inside "acme/web#1288") is left out
      .filter((f, i, all) => !all.some((g, j) => j !== i && g.value !== f.value && g.value.startsWith(f.value + '#'))); };
  /** The connector's own words for the event that started a run ("A check failed"), from its schema. */
  UI.eventLabel = (r) => { const e = eventOf(r), ev = String((r && r.trigger) || '').split(/[.\s]/)[1]; return e && ev && e.sc.events && e.sc.events[ev] ? e.sc.events[ev].label : ''; };
  UI.runTarget = (r) => { const e = eventOf(r); if (!e) return ''; const t = e.sc.target ? fill(e.sc.target, e.fields) : ''; if (t) return t; const f = UI.itemFacts(r)[0]; return f ? f.value : ''; };
  UI.itemLinks = (r) => { const e = eventOf(r); if (!e) return []; return Object.values(e.sc.attrs || {}).filter((a) => a.link).map((a) => ({ label: a.linkLabel || `Open in ${e.sc.label}`, href: fill(a.link, e.fields) })).filter((x) => x.href); };

  /** A verb's display facts from the options it carries (the channel a post goes to), by the connection it runs on. */
  UI.verbFacts = (verb, options) => { const inst = String(verb || '').split('.')[0], c = (view().connectors || []).find((x) => x.name === inst); if (!c) return [];
    const use = useOf(c), sc = schemaOf(use); if (!sc || !sc.attrs) return [];
    return Object.entries(sc.attrs).filter(([, a]) => a.display).map(([name, a]) => ({ icon: a.icon || 'dot', label: a.label || name, value: a.format ? fill(a.format, options || {}) : getPath(options || {}, a.from || name), href: a.link ? fill(a.link, options || {}) : '', pri: 1 })).filter((f) => f.value); };
  /** The filter bar's connector qualifiers: search-only (no menu), grouped by connector, values from the items in view. */
  UI.attrQualifiers = (items, attrsOf = UI.itemAttrs) => UI.connAttrs().filter((a) => a.search).map((a) => {
    const m = new Map(); items.forEach((it) => { const v = attrsOf(it)[a.key]; if (v) m.set(v, (m.get(v) || 0) + 1); });
    return { k: a.key, alias: a.alias, label: a.label, group: a.group, more: !a.a.menu, multi: a.a.multi !== false, hint: `${a.label} · ${a.group}`, plural: `${a.label.toLowerCase()}s`, n: m.size, options: () => [...m].map(([v, n]) => ({ v, label: v, n })) }; })
    .filter((f) => f.n > 0);
  /** Whether an item passes every connector qualifier that's set in a filter state. */
  UI.attrsMatch = (it, state, attrsOf = UI.itemAttrs) => UI.connAttrs().every((a) => { const vs = state[a.key]; return !vs || !vs.length || vs.includes(attrsOf(it)[a.key]); });
  /** Every attribute value of an item, for free-text search. */
  UI.attrText = (it, attrsOf = UI.itemAttrs) => Object.values(attrsOf(it)).join(' ');
})();
