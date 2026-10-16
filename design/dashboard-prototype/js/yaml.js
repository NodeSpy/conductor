/* A small, comment-preserving YAML for the prototype.
 *
 * parse(text) returns { value, meta }. `meta` is keyed by a stable node path
 * (map keys, `[id=…]` for list items that carry an id, indexes otherwise —
 * the same selectors the config-edit pipeline uses) and remembers, per node:
 * the comment lines and blank lines before it, its trailing comment and that
 * comment's column, the exact text it was written with, flow vs block style,
 * and its line range.
 *
 * emit(value, meta) writes the value back. A node whose value is unchanged is
 * written byte-for-byte as it was; a changed node is regenerated in its
 * original style with its trailing comment kept at its column. So
 * emit(parse(t)) === t, and an edit touches only the lines it changes. That is
 * the node-level, byte-preserving splice of design §7.2, in miniature.
 *
 * Supported: block maps and lists (including `- - ` nested lists), flow maps
 * and lists, plain/single/double-quoted scalars, `|` and `>` block scalars,
 * comments, anchors and aliases kept as written. Not supported: multi-doc,
 * tags, complex keys. Good enough for workflows and config sections. */
(function () {
  const Y = (window.YAML = {});
  class YErr extends Error { constructor(msg, line) { super(`line ${line}: ${msg}`); this.line = line; this.msg = msg; } }
  Y.Error = YErr;

  const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v);
  const clone = (v) => (v === undefined ? undefined : JSON.parse(JSON.stringify(v)));
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  Y.clone = clone; Y.eq = eq;

  /* ---------- lexing helpers ---------- */
  function splitComment(s) {
    let q = null;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (q) {
        if (q === '"' && c === '\\') { i++; continue; }
        if (c === q) { if (q === "'" && s[i + 1] === "'") { i++; continue; } q = null; }
        continue;
      }
      if ((c === '"' || c === "'") && (i === 0 || /[\s,[{:]/.test(s[i - 1]))) { q = c; continue; }
      if (c === '#' && (i === 0 || /\s/.test(s[i - 1]))) return { body: s.slice(0, i).replace(/\s+$/, ''), comment: s.slice(i), at: i };
    }
    return { body: s.replace(/\s+$/, ''), comment: '', at: -1 };
  }
  const KEY = /^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|<<|[^\s#'"[\]{},&*!|>%@`-][^#]*?|-[^\s#][^#]*?)\s*:(?=\s|$)/;
  const isSeqLine = (t) => t === '-' || t.startsWith('- ');
  const unq = (k) => (k[0] === '"' ? JSON.parse(k) : k[0] === "'" ? k.slice(1, -1).replace(/''/g, "'") : k);

  function scalar(t, line) {
    t = t.trim();
    if (t === '' || t === '~' || t === 'null') return null;
    if (t[0] === '"') {
      if (t.length < 2 || !t.endsWith('"')) throw new YErr('unterminated "string"', line);
      try { return JSON.parse(t.replace(/\\\//g, '/')); } catch (e) { throw new YErr('bad escape in "string"', line); }
    }
    if (t[0] === "'") { if (t.length < 2 || !t.endsWith("'")) throw new YErr("unterminated 'string'", line); return t.slice(1, -1).replace(/''/g, "'"); }
    if (t[0] === '{' || t[0] === '[') throw new YErr('unbalanced ' + t[0], line);
    if (/^[|>]/.test(t)) throw new YErr('block scalar must start the value', line);
    if (t[0] === '@' || t[0] === '`') throw new YErr(`a plain value can't start with ${t[0]}; quote it`, line);
    if (t === 'true') return true;
    if (t === 'false') return false;
    if (/^-?\d+$/.test(t) && t.length < 16) return parseInt(t, 10);
    if (/^-?\d*\.\d+$/.test(t)) return parseFloat(t);
    if (/: /.test(t)) throw new YErr(`"${t.slice(0, 24)}…" has a ": " inside a plain value; quote it`, line);
    return t;
  }

  function parseFlow(s, line) {
    let i = 0;
    const ws = () => { while (i < s.length && /\s/.test(s[i])) i++; };
    const str = () => {
      const q = s[i]; let j = i + 1;
      while (j < s.length) { if (q === '"' && s[j] === '\\') { j += 2; continue; } if (s[j] === q) { if (q === "'" && s[j + 1] === "'") { j += 2; continue; } break; } j++; }
      if (j >= s.length) throw new YErr('unterminated string in flow', line);
      const t = s.slice(i, j + 1); i = j + 1; return scalar(t, line);
    };
    const plain = (stops) => { const j0 = i; while (i < s.length && !stops.test(s[i])) { if (s[i] === '$' && s[i + 1] === '{') { const e = s.indexOf('}', i); if (e > 0) { i = e + 1; continue; } } if (s[i] === ':' && /[\s,}\]]/.test(s[i + 1] || ' ') && stops.source.includes(':')) break; i++; } return s.slice(j0, i).trim(); };
    const val = () => {
      ws();
      if (s[i] === '{') {
        i++; const o = {};
        for (;;) {
          ws(); if (s[i] === '}') { i++; return o; }
          let k; if (s[i] === '"' || s[i] === "'") k = String(str()); else k = plain(/[:,}]/);
          ws(); if (s[i] !== ':') throw new YErr(`expected ":" after "${k}" in {…}`, line);
          i++; const v = val(); if (k in o) throw new YErr(`duplicate key "${k}"`, line); o[k] = v;
          ws(); if (s[i] === ',') { i++; continue; } if (s[i] === '}') { i++; return o; }
          throw new YErr('expected "," or "}"', line);
        }
      }
      if (s[i] === '[') {
        i++; const a = [];
        for (;;) {
          ws(); if (s[i] === ']') { i++; return a; }
          a.push(val()); ws();
          if (s[i] === ',') { i++; continue; } if (s[i] === ']') { i++; return a; }
          throw new YErr('expected "," or "]"', line);
        }
      }
      if (s[i] === '"' || s[i] === "'") return str();
      const t = plain(/[,}\]]/);
      if (t === '') throw new YErr('empty value in flow', line);
      if (t.startsWith('{{')) throw new YErr('a value starting with {{ must be quoted', line);
      return scalar(t, line);
    };
    const v = val(); ws();
    if (i < s.length) throw new YErr('unexpected text after ' + (Array.isArray(v) ? ']' : '}'), line);
    return v;
  }
  const balanced = (s) => {
    let d = 0, q = null;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (q) { if (q === '"' && c === '\\') { i++; continue; } if (c === q) q = null; continue; }
      if (c === '"' || c === "'") q = c; else if (c === '{' || c === '[') d++; else if (c === '}' || c === ']') d--;
    }
    return d <= 0;
  };

  /* ---------- parse ---------- */
  Y.parse = (text) => {
    const raw = text.replace(/\r\n?/g, '\n').split('\n');
    const L = raw.map((r, k) => {
      if (/\t/.test(r.match(/^\s*/)[0])) throw new YErr('tab in indentation', k + 1);
      const ind = r.search(/\S/);
      return { raw: r, ind: ind < 0 ? -1 : ind, text: ind < 0 ? '' : r.slice(ind), n: k + 1 };
    });
    // a trailing newline yields one empty final line; keep it out of the content
    let nlines = L.length; const trailingNL = raw.length > 1 && raw[raw.length - 1] === '';
    if (trailingNL) nlines--;
    const meta = {}; let i = 0, pending = [], last = 0;
    const blankOrComment = (l) => l.ind < 0 || l.text.startsWith('#');
    const skip = () => { while (i < nlines && blankOrComment(L[i])) pending.push(L[i++].raw); };
    const take = () => { const b = pending; pending = []; return b; };
    const peek = () => { let j = i; while (j < nlines && blankOrComment(L[j])) j++; return j < nlines ? L[j] : null; };
    const M = (p) => (meta[p] = meta[p] || {});
    const join = (p, k) => (p === '' ? k : p + '.' + (/[.[\]]/.test(k) ? `["${k}"]` : k));

    function value(body, absCol, kp, ownInd, line) {
      const m = M(kp);
      let b = body;
      const am = b.match(/^&([\w-]+)\s*/); if (am) { m.anchor = am[1]; b = b.slice(am[0].length); }
      if (b === '') return { nested: true };
      if (/^[|>][-+]?\d*$/.test(b)) {
        const rl = []; let ci = -1;
        while (i < nlines && (L[i].ind < 0 || L[i].ind > ownInd)) { if (ci < 0 && L[i].ind >= 0) ci = L[i].ind; rl.push(L[i].raw); i++; }
        while (rl.length && rl[rl.length - 1].trim() === '') { i--; rl.pop(); }
        last = i ? L[i - 1].n : last;
        const body2 = rl.map((r) => r.slice(ci)).join(b[0] === '>' ? ' ' : '\n');
        m.block = b; m.rawLines = rl;
        return { v: b.includes('-') ? body2 : body2 + '\n' };
      }
      if (b[0] === '{' || b[0] === '[') {
        let s = b, rawTxt = body;
        while (!balanced(s) && i < nlines) { const nx = L[i++]; s += ' ' + splitComment(nx.text).body; rawTxt += '\n' + nx.raw; m.multi = true; last = nx.n; }
        m.flow = true; m.raw = rawTxt;
        return { v: parseFlow(s, line) };
      }
      if (b[0] === '*') { m.raw = body; return { v: b }; }
      if (b.startsWith('{{')) throw new YErr('a value starting with {{ must be quoted', line);
      m.raw = body;
      if (b[0] === '"' || b[0] === "'") m.quote = b[0];
      return { v: scalar(b, line) };
    }

    function block(minInd, path) {
      const l = peek();
      if (!l || l.ind < minInd) return null;
      if (isSeqLine(l.text)) return seq(l.ind, path);
      if (KEY.test(l.text)) return map(l.ind, path);
      skip(); i++; last = l.n;
      const { body, comment, at } = splitComment(l.text);
      const m = M(path); if (comment) { m.comment = comment; m.col = l.ind + at; }
      m.line = l.n; m.end = l.n;
      const r = value(body, l.ind, path, l.ind - 1, l.n);
      if (r.nested) return null;
      m.val = clone(r.v);
      return r.v;
    }

    function map(ind, path) {
      const o = {};
      for (;;) {
        const l = peek();
        if (!l || l.ind < ind) break;
        if (l.ind > ind) throw new YErr('unexpected indentation', l.n);
        if (isSeqLine(l.text)) break;
        const km = l.text.match(KEY);
        if (!km) throw new YErr(`expected "key: value", got "${l.text.slice(0, 30)}"`, l.n);
        skip(); i++; last = l.n;
        const key = unq(km[1]);
        if (Object.prototype.hasOwnProperty.call(o, key)) throw new YErr(`duplicate key "${key}"`, l.n);
        const kp = join(path, key), m = M(kp);
        m.before = take(); m.line = l.n;
        const rest = l.text.slice(km[0].length);
        const { body, comment, at } = splitComment(rest);
        if (comment) { m.comment = comment; m.col = l.ind + km[0].length + at; }
        const r = value(body.trim(), 0, kp, ind, l.n);
        let v;
        if (r.nested) {
          const nx = peek();
          if (nx && nx.ind > ind) { m.ind = nx.ind - ind; if (isSeqLine(nx.text)) m.dash = nx.ind - ind; v = block(ind + 1, kp); }
          else if (nx && nx.ind === ind && isSeqLine(nx.text)) { m.dash = 0; v = seq(ind, kp); }
          else { v = null; m.raw = ''; }
        } else v = r.v;
        o[key] = v; m.val = clone(v); m.end = last;
      }
      return o;
    }

    function renamePrefix(from, to) {
      if (from === to) return;
      Object.keys(meta).forEach((k) => { if (k === from || k.startsWith(from + '.')) { meta[to + k.slice(from.length)] = meta[k]; delete meta[k]; } });
    }

    function seq(ind, path) {
      const a = [];
      for (;;) {
        const l = peek();
        if (!l || l.ind !== ind || !isSeqLine(l.text)) { if (l && l.ind > ind) throw new YErr('unexpected indentation', l.n); break; }
        skip();
        const idx = a.length, tmp = path + '.#' + idx;
        const before = take();
        const content = l.text.replace(/^-\s*/, ''), off = l.text.length - content.length;
        let item;
        const m0 = M(tmp); m0.line = l.n;
        if (content === '' || content.startsWith('#')) {
          if (content) { m0.comment = content; m0.col = l.ind + off; }
          i++; last = l.n; item = block(ind + 1, tmp);
        } else {
          L[i] = { raw: l.raw, ind: ind + off, text: content, n: l.n, virt: true };
          item = block(ind + off, tmp);
        }
        const fin = path + '.' + (isObj(item) && typeof item.id === 'string' && item.id ? `[id=${item.id}]` : idx);
        renamePrefix(tmp, fin);
        const m = M(fin); m.before = before; m.line = l.n; m.end = last; m.off = off;
        if (m.val === undefined) m.val = clone(item);
        a.push(item);
      }
      return a;
    }

    skip();
    let value0 = null;
    const first = peek();
    if (first) {
      if (first.ind !== 0) throw new YErr('the document must start at column 0', first.n);
      value0 = block(0, '');
      skip();
      if (i < nlines) throw new YErr(`unexpected "${L[i].text.slice(0, 30)}"`, L[i].n);
    }
    meta.$end = take(); meta.$nl = trailingNL;
    return { value: value0, meta };
  };

  /* ---------- emit ---------- */
  const RESERVED = /^(true|false|null|~|yes|no|on|off|-?\d[\d.]*)$/i;
  function fmtScalar(v, m) {
    if (v === null || v === undefined) return 'null';
    if (typeof v !== 'string') return String(v);
    const q = m && m.quote;
    const needs = v === '' || /^\s|\s$/.test(v) || /^[[\]{}&*!|>'"%@`#,?]/.test(v) || /^- /.test(v) || /: |\s#/.test(v) || RESERVED.test(v) || v.includes('{{') || v.endsWith(':');
    if (q === "'" ) return "'" + v.replace(/'/g, "''") + "'";
    if (q === '"' || needs) return JSON.stringify(v);
    return v;
  }
  function fmtFlow(v, spaced) {
    if (Array.isArray(v)) return '[' + v.map((x) => fmtFlow(x, spaced)).join(', ') + ']';
    if (isObj(v)) { const ks = Object.keys(v); if (!ks.length) return '{}'; const s = spaced ? ' ' : ''; return '{' + s + ks.map((k) => fmtKey(k) + ': ' + fmtFlow(v[k], spaced)).join(', ') + s + '}'; }
    const t = fmtScalar(v);
    return /[,{}[\]]/.test(t) && !/^["']/.test(t) ? JSON.stringify(v) : t;
  }
  const fmtKey = (k) => (/^[A-Za-z_$<][\w$./*<-]*$/.test(k) || k === '<<' ? k : JSON.stringify(k));
  const simple = (v) => (Array.isArray(v) ? v.every((x) => x === null || typeof x !== 'object') : isObj(v) && Object.values(v).every((x) => x === null || typeof x !== 'object' || (Array.isArray(x) && x.every((y) => typeof y !== 'object'))));
  const wantsFlow = (v) => simple(v) && !JSON.stringify(v).includes('\\n') && fmtFlow(v, true).length <= 64;

  Y.emit = (value, meta = {}) => {
    const out = [];
    const push = (s) => out.push(s);
    const withC = (line, m) => (m && m.comment ? (line.length < m.col ? line.padEnd(m.col) : line + ' ') + m.comment : line);
    const join = (p, k) => (p === '' ? k : p + '.' + (/[.[\]]/.test(k) ? `["${k}"]` : k));
    const ipath = (p, it, j) => p + '.' + (isObj(it) && typeof it.id === 'string' && it.id ? `[id=${it.id}]` : j);

    function val(head, v, kp, ind, m) {
      const same = m.val !== undefined && eq(v, m.val);
      if (typeof v === 'string' && v.includes('\n')) {
        if (same && m.rawLines) { push(withC(head + ' ' + m.block, m)); m.rawLines.forEach(push); return; }
        if (same && m.raw != null) { push(withC(head + ' ' + m.raw, m)); return; }
        const hdr = v.endsWith('\n') ? '|' : '|-', pad = ' '.repeat(ind + 2);
        push(withC(head + ' ' + hdr, m));
        v.replace(/\n$/, '').split('\n').forEach((x) => push(x ? pad + x : ''));
        return;
      }
      if (v === null || typeof v !== 'object') {
        const t = same && m.raw != null ? m.raw : (m.anchor ? '&' + m.anchor + ' ' : '') + fmtScalar(v, m);
        push(withC(head + (t === '' ? '' : ' ' + t), m));
        return;
      }
      const empty = Array.isArray(v) ? !v.length : !Object.keys(v).length;
      if (m.flow || empty || (m.val === undefined && wantsFlow(v))) {
        const spaced = m.raw ? /^\{\s/.test(m.raw) : true;
        if (same && m.raw != null && m.flow) { const rl = m.raw.split('\n'); push(withC(head + ' ' + rl[0], rl.length > 1 ? null : m)); rl.slice(1).forEach((x, k) => push(k === rl.length - 2 ? withC(x, m) : x)); return; }
        push(withC(head + ' ' + (m.anchor ? '&' + m.anchor + ' ' : '') + fmtFlow(v, spaced), m));
        return;
      }
      push(withC(head + (m.anchor ? ' &' + m.anchor : ''), m));
      if (Array.isArray(v)) seq(v, kp, ind + (m.dash ?? 2));
      else map(v, kp, ind + (m.ind ?? 2));
    }
    function map(o, path, ind, first) {
      Object.keys(o).forEach((k, j) => {
        const kp = join(path, k), m = meta[kp] || {};
        if (j > 0 || first == null) (m.before || []).forEach(push);
        const pre = j === 0 && first != null ? first : ' '.repeat(ind);
        val(pre + fmtKey(k) + ':', o[k], kp, ind, m);
      });
    }
    function seq(a, path, ind, first) {
      a.forEach((it, j) => {
        const ip = ipath(path, it, j), m = meta[ip] || {};
        if (j > 0 || first == null) (m.before || []).forEach(push);
        const off = m.off || 2;
        const dash = (j === 0 && first != null ? first : ' '.repeat(ind)) + '-' + ' '.repeat(off - 1);
        const same = m.val !== undefined && eq(it, m.val);
        if (isObj(it) && Object.keys(it).length && !(m.flow && (same || wantsFlow(it)))) {
          if (m.flow) { push(withC(dash + fmtFlow(it, /^\{\s/.test(m.raw || '{ ')), m)); return; }
          map(it, ip, ind + off, dash);
        } else if (Array.isArray(it) && it.length && !m.flow) {
          seq(it, ip, ind + off, dash);
        } else {
          let t;
          if (same && m.raw != null) t = m.raw;
          else if (it !== null && typeof it === 'object') t = fmtFlow(it, true);
          else if (typeof it === 'string' && it.includes('\n')) t = JSON.stringify(it);
          else t = fmtScalar(it, m);
          push(withC(dash + t, m));
        }
      });
    }
    if (isObj(value)) map(value, '', 0);
    else if (Array.isArray(value)) seq(value, '', 0);
    else if (value != null) push(fmtScalar(value));
    (meta.$end || []).forEach(push);
    return out.join('\n') + (meta.$nl ? '\n' : '');
  };

  /** Plain stringify for nodes with no history (new config entries, diffs). */
  Y.stringify = (v, indent = 0) => {
    const t = Y.emit(v, {});
    return indent ? t.split('\n').map((l) => (l ? ' '.repeat(indent) + l : l)).join('\n') : t;
  };

  /** Line diff (LCS). Returns [{t:' '|'+'|'-', a, b, s}] with 1-based line numbers. */
  Y.diff = (A, B) => {
    const a = A.split('\n'), b = B.split('\n'), n = a.length, m = b.length;
    const dp = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
    for (let x = n - 1; x >= 0; x--) for (let y = m - 1; y >= 0; y--) dp[x][y] = a[x] === b[y] ? dp[x + 1][y + 1] + 1 : Math.max(dp[x + 1][y], dp[x][y + 1]);
    const out = []; let x = 0, y = 0;
    while (x < n && y < m) {
      if (a[x] === b[y]) { out.push({ t: ' ', a: x + 1, b: y + 1, s: a[x] }); x++; y++; }
      else if (dp[x + 1][y] >= dp[x][y + 1]) { out.push({ t: '-', a: x + 1, s: a[x] }); x++; }
      else { out.push({ t: '+', b: y + 1, s: b[y] }); y++; }
    }
    while (x < n) { out.push({ t: '-', a: x + 1, s: a[x] }); x++; }
    while (y < m) { out.push({ t: '+', b: y + 1, s: b[y] }); y++; }
    return out;
  };
  /** Collapse a diff into hunks with `ctx` lines of context. */
  Y.hunks = (d, ctx = 2) => {
    const keep = d.map(() => false);
    d.forEach((r, k) => { if (r.t !== ' ') for (let j = Math.max(0, k - ctx); j <= Math.min(d.length - 1, k + ctx); j++) keep[j] = true; });
    const hs = []; let cur = null;
    d.forEach((r, k) => { if (keep[k]) { if (!cur) hs.push((cur = [])); cur.push(r); } else cur = null; });
    return hs;
  };
})();
