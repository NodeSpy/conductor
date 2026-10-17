// Privacy check: nothing under design/ may name a real person, or leak a private host or home path.
// This branch is public. Fails (exit 1) on any hit of `git grep -i <name>` in design/.
//
//   node tools/privacy-check.mjs          (also run by `node tools/tokens.mjs`)
//
// The names are stored base64-encoded so this file doesn't match its own search.
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const NAMES = ['ZGFuaWVs', 'YmFsZHdpbg=='].map((b) => Buffer.from(b, 'base64').toString());
const PATTERNS = [
  ...NAMES.map((n) => ['-i', '-e', n]),
  ['-E', '-e', '/home/[a-z]'], // a local home directory
  ['-E', '-e', '\\b100\\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\\.[0-9]+\\.[0-9]+\\b'], // a tailnet (CGNAT) address
];

export function privacyCheck() {
  const hits = [];
  for (const args of PATTERNS) {
    const r = spawnSync('git', ['grep', '-n', '-I', ...args, '--', 'design/'], { cwd: path.resolve(here, '../../..'), encoding: 'utf8' });
    if (r.status === 0) hits.push(...r.stdout.trim().split('\n'));
    else if (r.status !== 1) throw new Error(`git grep failed: ${r.stderr}`);
  }
  return hits;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const hits = privacyCheck();
  for (const h of hits) console.log('PRIVATE', h.slice(0, 160));
  console.log(hits.length ? `${hits.length} privacy hits under design/` : 'privacy: 0 hits under design/');
  process.exit(hits.length ? 1 : 0);
}
