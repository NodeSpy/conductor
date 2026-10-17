// The design work is public: no real person's name appears in any file under design/ (the prototype, its tests, the
// handoff and audit notes, review pages, fake data). Fake data stays neutral (ops, platform-sre, acme/…).
// The same check as `git grep -i <name> -- design` (see check-privacy.sh); the name is assembled so this file
// doesn't match itself.
const fs = require('fs');
const path = require('path');
const { test, expect } = require('@playwright/test');

const NAMES = [['dan', 'iel']].map((p) => new RegExp(p.join(''), 'i'));
const ROOT = path.join(__dirname, '..');
const TEXT = /\.(html|js|css|md|sh|json|txt|webmanifest|svg|yaml|yml)$/;
const walk = (d) => fs.readdirSync(d, { withFileTypes: true }).flatMap((e) => (e.name === 'node_modules' || e.name.startsWith('.') ? [] : e.isDirectory() ? walk(path.join(d, e.name)) : TEXT.test(e.name) ? [path.join(d, e.name)] : []));

test('no personal names anywhere under design/', () => {
  const hits = [];
  for (const f of walk(ROOT)) fs.readFileSync(f, 'utf8').split('\n').forEach((line, i) => { if (NAMES.some((rx) => rx.test(line))) hits.push(`${path.relative(ROOT, f)}:${i + 1}`); });
  expect(hits).toEqual([]);
});
