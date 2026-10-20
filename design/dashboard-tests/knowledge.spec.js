// Knowledge by kind (conductor's content kinds: knowledge, runbook, watch, log,
// handoff, plus collections declared on a team or bot). New… picks a kind and
// the path follows from kind + title; free paths exist only in a collection
// that declares them. Runbooks have a step editor; proposals and imports are
// checked against the kind's schema. Git underneath: one operator commit per
// save, history, revert, pins, blame, conflicts.
const { test, expect, go, applyStaged } = require('./fixtures');

const ta = (page) => page.locator('#rp-md');
const head = (page, id) => page.evaluate((i) => { const c = R.get(i).log[0]; return { who: c.who, kind: c.kind, msg: c.msg, n: c.changes.length, trailers: c.trailers || '', paths: c.changes.map((x) => x.path) }; }, id);
const files = (page, id) => page.evaluate((i) => R.get(i).files, id);
// A bot's memory on a phone shows the list or the entry (69b): these step to the one the next line taps.
const listOn = async (page) => { const b = page.locator('[data-act="rp-list"]'); if (await b.isVisible()) await b.click(); };
const entryOn = async (page) => { if (!(await page.locator('.repo-ed').first().isVisible())) await page.locator('.kind-entries .entry.on').click(); };
// Evidence is a list of things (71); a note is one of them
const addNote = async (page, text) => { await page.locator('[data-field="evidence"] .ev-add').click(); await page.locator('.menu [data-act="ev-new"][data-k="note"]').click(); await page.fill('#f-note', text); await page.locator('#gen-submit').click(); };
const newKind = async (page, k) => { await page.locator('[data-act="rp-new"]').first().click(); await page.locator(`[data-act="rp-new-pick"][data-k="${k}"]`).click(); };
const addTo = async (page, k, v) => { const i = page.locator(`#ke-form [data-listk="${k}"] input.list-add`); await i.fill(v); await i.press('Enter'); };
const save = (page) => page.locator('[data-act="rp-save"]').click();

test('New… creates each kind, and the path is derived from kind and title (never typed)', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  // the chooser: kinds, not paths; read-only kinds aren't offered to write
  await page.locator('[data-act="rp-new"]').first().click();
  await expect(page.locator('.modal [data-act="rp-new-pick"][data-k="knowledge"]')).toBeVisible();
  await expect(page.locator('.modal [data-act="rp-new-pick"][data-k="runbook"]')).toBeVisible();
  await expect(page.locator('.modal input#f-path, .modal [data-k="path"]')).toHaveCount(0);
  await page.locator('.modal [data-close]').first().click();
  // knowledge
  await newKind(page, 'knowledge');
  await expect(page.locator('#ke-free')).toHaveCount(0);
  await page.fill('#ke-title', 'Cache TTLs');
  await expect(page.locator('#ke-path')).toHaveText('knowledge/cache-ttls.md');
  await addTo(page, 'applies_to', 'acme/api');
  await addNote(page, 'modules/api/cache.tf'); // one note saves as the plain string conductor writes today
  await ta(page).fill('The /search cache TTL is 300s.');
  await page.fill('#rp-msg', 'knowledge: cache TTLs');
  await save(page);
  expect(await head(page, 'team:platform')).toMatchObject({ who: 'operator:ops', kind: 'operator', msg: 'knowledge: cache TTLs', paths: ['knowledge/cache-ttls.md'] });
  expect((await head(page, 'team:platform')).trailers).toContain('Authored-By: operator:ops');
  expect((await files(page, 'team:platform'))['knowledge/cache-ttls.md']).toMatch(/kind: knowledge[\s\S]*applies_to: \[acme\/api\][\s\S]*evidence: modules\/api\/cache.tf[\s\S]*confidence: medium[\s\S]*---\nThe \/search cache TTL is 300s./);
  // renaming is changing the title: one commit moves the file
  await page.fill('#ke-title', 'Search cache TTLs');
  await expect(page.locator('#ke-path')).toHaveText('knowledge/search-cache-ttls.md');
  await save(page);
  const f = await files(page, 'team:platform');
  expect(f['knowledge/search-cache-ttls.md']).toBeTruthy(); expect(f['knowledge/cache-ttls.md']).toBeUndefined();
  // a watch item: its own file in watch/, named from "what to watch"
  await newKind(page, 'watch');
  await page.fill('#ke-what', 'Queue depth on jobs');
  await expect(page.locator('#ke-path')).toHaveText('watch/queue-depth-on-jobs.md');
  await page.fill('#ke-signal', 'sqs ApproximateNumberOfMessages (jobs)'); await page.fill('#ke-threshold', '> 1000 for 10m'); await page.fill('#ke-why', 'Workers fall behind before anyone notices');
  await save(page);
  await expect.poll(async () => (await files(page, 'team:platform'))['watch/queue-depth-on-jobs.md'] || '').toMatch(/kind: watch\nwhat: Queue depth on jobs\nsignal: /);
  // a required field missing: the save is refused with the schema error
  await newKind(page, 'knowledge');
  await page.fill('#ke-title', 'Half a fact');
  await save(page);
  await expect(page.locator('#ke-err')).toContainText('evidence: missing required field');
  await expect(page.locator('[data-field="evidence"].bad')).toBeVisible();
});

test('the runbook step editor: add, a command and a verb per step, reorder, remove', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  await newKind(page, 'runbook');
  await page.fill('#ke-title', 'Restart the worker');
  await expect(page.locator('#ke-path')).toHaveText('runbooks/restart-the-worker.md');
  await page.fill('#ke-when_to_use', 'Jobs queue grows and workers are idle');
  for (let i = 0; i < 3; i++) await page.locator('[data-act="rb-add"]').click();
  const row = (i) => page.locator('.rb-step').nth(i);
  await row(0).locator('[data-sk="do"]').fill('Drain the worker');
  await row(0).locator('[data-sk="type"]').selectOption('run');
  await row(0).locator('[data-sk="arg"]').fill('systemctl stop worker');
  await row(1).locator('[data-sk="do"]').fill('Tell #ops');
  await row(1).locator('[data-sk="type"]').selectOption('verb');
  await row(1).locator('[data-sk="arg"]').fill('slack-ops.post');
  await row(2).locator('[data-sk="do"]').fill('Start it again');
  // move "Tell #ops" to the end, then remove nothing else
  await row(1).locator('[data-act="rb-down"]').click();
  await expect(row(2).locator('[data-sk="do"]')).toHaveValue('Tell #ops');
  await row(0).locator('[data-act="rb-up"]').count();
  await expect(row(0).locator('[data-act="rb-up"]')).toHaveCount(0); // the first step can't move up (disabled with a reason)
  await page.locator('[data-act="rb-add"]').click();
  await page.locator('.rb-step').nth(3).locator('[data-act="rb-del"]').click();
  await expect(page.locator('.rb-step')).toHaveCount(3);
  await page.fill('#ke-verification', 'Queue drains within 10 minutes');
  await page.fill('#ke-owner', 'platform');
  await save(page);
  await expect(page.locator('#ke-err')).toContainText('rollback: missing required field');
  await page.fill('#ke-rollback', 'Nothing to roll back; the worker restarts clean.');
  await save(page);
  // steps are a list of strings, as conductor's runbook kind declares; the command or verb rides in the text
  const t = (await files(page, 'team:platform'))['runbooks/restart-the-worker.md'];
  expect(t).toMatch(/steps:\n\s+- "?Drain the worker \(run: `systemctl stop worker`\)"?\n\s+- Start it again\n\s+- "?Tell #ops \(verb: slack-ops.post\)"?/);
  // knowledge shows what cites it; runbooks show the cases they were used in
  await go(page, '#/bots/platform-sre/memory');
  await listOn(page);
  await page.locator('[data-act="rp-kind"][data-k="knowledge"]').click();
  await page.locator('[data-act="rp-open"][data-key="knowledge/alb.md"]').click();
  await expect(page.locator('.cites')).toContainText('Latency');
  await expect(page.locator('.cites')).toContainText('SEV2 api p99 latency > 2s'); // a ticket by its title (r5-10)
});

test('no free path outside a custom collection; declare one, then create in it with a free path', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  await newKind(page, 'runbook');
  await expect(page.locator('#ke-free')).toHaveCount(0);
  await expect(page.locator('.ke-path')).toContainText('derived from the kind and the title');
  await page.locator('[data-act="rp-discard"]').click();
  // the pack-shipped collection: path still derived
  await newKind(page, 'c:postmortems');
  await expect(page.locator('#ke-free')).toHaveCount(0);
  await page.fill('#ke-title', 'API outage 2026-10-01');
  await expect(page.locator('#ke-path')).toHaveText('postmortems/api-outage-2026-10-01.md');
  await page.locator('[data-act="rp-discard"]').click(); await page.locator('#cf-ok').click();
  // declare a collection that allows free paths: config, through the diff
  await go(page, '#/config/collections');
  await expect(page.locator('[data-coll="team:platform/postmortems"]')).toContainText('from pack sre-bot');
  await page.locator('[data-act="coll-new"]').first().click();
  await page.locator('#f-owner').selectOption('team:platform');
  await page.fill('#f-name', 'vendors'); await page.fill('#f-dir', 'knowledge/vendors');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="dir"] .ferr')).toContainText('built-in kind');
  await page.fill('#f-dir', 'vendors');
  const fl = page.locator('[data-listk="fields"] input.list-add'); await fl.fill('contact!:string'); await fl.press('Enter');
  await page.locator('#f-free_paths').check();
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('teams.platform');
  await expect(page.locator('.diff')).toContainText('vendors:');
  await expect(page.locator('.diff')).toContainText('free_paths: true');
  await expect(page.locator('.diff')).toContainText('required: true');
  await applyStaged(page);
  await go(page, '#/teams/platform/knowledge');
  await newKind(page, 'c:vendors');
  await expect(page.locator('#ke-free')).toBeVisible();
  await page.fill('#ke-title', 'Acme Cloud');
  await page.fill('#ke-free', 'cloud/acme');
  await expect(page.locator('#ke-path')).toHaveText('vendors/cloud/acme.md');
  await page.fill('#ke-contact', 'support@acme.example');
  await save(page);
  expect((await files(page, 'team:platform'))['vendors/cloud/acme.md']).toContain('contact: support@acme.example');
});

test('a malformed runbook proposal shows its schema errors; fixed by approving with changes', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  const k5 = page.locator('.kprop[data-prop="k5"]');
  await expect(k5).toContainText('Runbooks');
  await expect(k5.locator('.schema-errs')).toContainText('steps: item 2: want a non-empty string');
  await expect(k5.locator('.schema-errs')).toContainText('steps: item 3: want a non-empty string');
  await expect(k5.locator('.schema-errs')).toContainText('verification: missing required field');
  await expect(k5.locator('.schema-errs')).toContainText('rollback: missing required field');
  await expect(k5.locator('.schema-errs')).toContainText('owner: missing required field');
  await expect(k5.locator('button', { hasText: 'Approve' }).first()).toHaveAttribute('aria-disabled', 'true');
  await k5.locator('[data-act="kp-edit"]').click();
  await expect(page.locator('.ed-head')).toContainText('approving k5 with changes');
  await expect(page.locator('#ke-err')).toContainText('verification: missing required field');
  await page.locator('.rb-step').nth(1).locator('[data-sk="do"]').fill('Roll back the web deploy');
  await page.fill('#ke-verification', 'checkout 5xx back under 0.5%');
  await page.fill('#ke-rollback', 'Redeploy the previous web release.');
  await page.fill('#ke-owner', 'web-triage');
  await page.locator('[data-act="rp-save"]', { hasText: 'Approve with changes' }).click();
  await page.locator('#su-go').click();
  await expect(page.locator('.kprop[data-prop="k5"]')).toContainText('approved (edited)');
  const h = await head(page, 'team:platform');
  expect(h).toMatchObject({ who: 'bot:web-triage', kind: 'bot', paths: ['runbooks/checkout-errors.md'] });
  expect(h.trailers).toContain('Edited-By: operator:ops');
  expect((await files(page, 'team:platform'))['runbooks/checkout-errors.md']).toContain('Roll back the web deploy (run: `kubectl rollout undo deploy/web`)');
});

test('approve with changes, deny with a reason, bulk approve', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  await page.locator('[data-act="kp-edit"][data-id="k4"]').click();
  await expect(ta(page)).toHaveValue(/deregistration_delay. is \*\*30s\*\*|deregistration_delay is 30s/);
  await page.locator('[data-field="evidence"] [data-act="ev-note"]').first().click(); // its evidence was a string: one note, edited in place
  await page.fill('#f-note', 'modules/web/alb.tf, checked 2026-10-16'); await page.locator('#gen-submit').click();
  await page.locator('[data-act="rp-save"]', { hasText: 'Approve with changes' }).click();
  await page.locator('#su-go').click();
  await expect(page.locator('.kprop[data-prop="k4"]')).toContainText('approved (edited)');
  await page.locator('[data-act="kp-deny"][data-id="k3"]').click();
  await page.fill('#f-reason', 'Covered by the Safari note already.'); await page.locator('#gen-submit').click();
  await expect(page.locator('.kprop[data-prop="k3"]')).toContainText('denied: Covered by the Safari note');
  expect(await page.evaluate(() => D.chats['web-triage'].slice(-1)[0].text)).toContain('Denied knowledge proposal k3');
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  for (const id of ['k4', 'k3']) await page.locator(`.kprop input[data-id="${id}"]`).check();
  await page.locator('[data-act="kp-bulk"][data-d="approve"]').click(); await page.locator('#su-go').click();
  await expect(page.locator('.kprop[data-prop="k3"]')).toContainText('approved');
  expect((await files(page, 'team:platform'))['knowledge/safari-18-flex-gap.md']).toContain('kind: knowledge');
});

test('import maps markdown into a kind: a runbook is detected and confirmed', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge', { fresh: true });
  await page.locator('.repo-side [data-act="rp-new"]').click(); await page.locator('.modal [data-act="rp-upload"]').click();
  await page.locator('[data-act="up-mode"][data-v="paste"]').click();
  await page.fill('#up-text', '# Rotate the TLS cert\n\n## When\nThe cert expires within 14 days.\n\n## Steps\n1. Issue a new cert: `tailscale cert ops-box`\n2. Reload the listener\n3. Check the expiry\n\n## Verify\nopenssl shows the new expiry.\n\n## Rollback\nRestore the previous cert files.\n');
  await page.locator('#up-go').click();
  await expect(page.locator('.im-row')).toContainText('detected: runbook');
  await expect(page.locator('.im-row')).toContainText('runbooks/rotate-the-tls-cert.md');
  await expect(page.locator('.im-row')).toContainText('3 steps');
  await page.locator('#im-go').click();
  const t = (await files(page, 'team:platform'))['runbooks/rotate-the-tls-cert.md'];
  expect(t).toContain('Issue a new cert (run: `tailscale cert ops-box`)'); expect(t).toContain('verification: openssl shows the new expiry.'); expect(t).toContain('rollback: Restore the previous cert files.');
  await expect(page.locator('#ke-path')).toHaveText('runbooks/rotate-the-tls-cert.md');
  // a note mapped as knowledge: the note becomes the body, the evidence says where it came from
  await page.locator('.repo-side [data-act="rp-new"]').click(); await page.locator('.modal [data-act="rp-upload"]').click();
  await page.setInputFiles('#up-file', { name: 'oncall-v2.md', mimeType: 'text/markdown', buffer: Buffer.from('# On-call v2\n\nPrimary rotates weekly.\n') });
  await page.locator('#up-go').click();
  await expect(page.locator('.im-row')).toContainText('detected: knowledge entry');
  await page.locator('#im-go').click();
  const k = (await files(page, 'team:platform'))['knowledge/on-call-v2.md'];
  expect(k).toContain('evidence: imported from oncall-v2.md'); expect(k).toMatch(/---\nPrimary rotates weekly./);
});

test('bot memory: edit, pin, who wrote it; the log is annotated or flagged, never rewritten', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await listOn(page);
  await page.locator('[data-act="rp-open"][data-key="knowledge/db-prod-1.md"]').click();
  await ta(page).fill((await ta(page).inputValue()).replace('No data loss.', 'No data loss. Pool size 200.'));
  await save(page);
  expect(await head(page, 'bot:platform-sre')).toMatchObject({ who: 'operator:ops' });
  await page.locator('[data-act="rp-file-menu"]').click(); await page.locator('#pop-menu [data-act="rp-pin"]').click();
  await listOn(page);
  await expect(page.locator('.kind-entries .entry.on .pin')).toBeVisible();
  await entryOn(page);
  await page.locator('[data-act="rp-blame"]').click();
  await expect(page.locator('.bl-group.bot').first()).toContainText('Atlas');
  await expect(page.locator('.bl-group.operator').first()).toContainText('you');
  await listOn(page);
  await page.locator('[data-act="rp-kind"][data-k="log"]').click();
  await entryOn(page);
  await expect(page.locator('#ke-form')).toHaveCount(0);
  await expect(page.locator('.repo-ed')).toContainText('history isn\'t rewritten');
  const before = (await files(page, 'bot:platform-sre'))['log/2026-10-16-c29.md'];
  const body = before.slice(before.indexOf('\n---\n') + 5).trimEnd();
  await page.locator('[data-act="rp-flag"]').click();
  await page.fill('#f-text', 'The regression started at 14:03, not 14:02.'); await page.locator('#gen-submit').click();
  await expect(page.locator('.repo-ed .callout.danger')).toContainText('14:03');
  await expect(page.locator('.ed-head')).toContainText('flagged as wrong');
  // conductor's operator edit: flagged_wrong is set and a section is appended; the bot's text is untouched
  const after = (await files(page, 'bot:platform-sre'))['log/2026-10-16-c29.md'];
  expect(after).toContain('flagged_wrong: The regression started at 14:03, not 14:02.');
  expect(after).toContain(body);
  expect(after).toMatch(/## Flagged WRONG by the operator — ops, .+\n\nThe regression started at 14:03/);
  await page.locator('[data-act="rp-annotate"]').click();
  await page.fill('#f-text', 'Paged twice; second page was a duplicate.'); await page.locator('#gen-submit').click();
  expect((await files(page, 'bot:platform-sre'))['log/2026-10-16-c29.md']).toMatch(/## Operator note — ops, .+\n\nPaged twice/);
});

test('a bot commits while you edit: the conflict is shown and you rebase explicitly', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  await listOn(page);
  await page.locator('[data-act="rp-kind"][data-k="runbook"]').click();
  await page.locator('[data-act="rp-open"][data-key="runbooks/latency.md"]').click();
  await ta(page).fill('Check the CDN cache hit rate too.\n');
  await page.locator('.repo-ed [data-act="rp-file-menu"]').click(); await page.locator('.menu [data-act="rp-sim"]').click();
  await save(page);
  await expect(page.locator('.conflict')).toContainText('Conflict');
  await page.locator('[data-act="rp-rebase"]').click();
  await expect(ta(page)).toHaveValue(/auto-noted[\s\S]*CDN cache hit rate|CDN cache hit rate[\s\S]*auto-noted/);
  await save(page);
  const f = (await files(page, 'bot:platform-sre'))['runbooks/latency.md'];
  expect(f).toContain('auto-noted'); expect(f).toContain('CDN cache hit rate');
});

test('unsaved changes are guarded; browse by kind with tag filters; search spans team knowledge and bot memory', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  // the shared filter bar: a qualifier narrows to what an entry applies to
  await listOn(page);
  await page.fill('.fb[data-fb="rp"] .fb-in', 'about:nat'); await page.locator('.fb[data-fb="rp"] .fb-in').press('Enter');
  await expect(page.locator('.kind-entries .entry')).toHaveCount(1);
  await expect(page.locator('.kind-entries')).toContainText('Infra map');
  await page.locator('.fb[data-fb="rp"] .fb-pill .fb-px').click();
  await entryOn(page);
  await page.fill('#ke-title', 'changed');
  await page.evaluate(() => { location.hash = '#/overview'; });
  await expect(page.locator('.modal, .cdrawer').first()).toContainText('aren\'t saved');
  await page.locator('#cf-cancel, .modal [data-close]').first().click();
  await expect(page).toHaveURL(/memory/);
  await page.locator('[data-act="rp-discard"]').click(); await page.locator('#cf-ok').click();
  await listOn(page);
  await page.fill('.fb[data-fb="rp"] .fb-in', 'Safari'); await page.locator('.fb[data-fb="rp"] .fb-in').press('Enter');
  await expect(page.locator('.search-res')).toHaveCount(1);
  await go(page, '#/teams/platform/knowledge');
  await page.fill('.fb[data-fb="rp"] .fb-in', 'deregistration'); await page.locator('.fb[data-fb="rp"] .fb-in').press('Enter');
  await page.locator('.search-res [data-act="rp-hit"][data-repo="bot:platform-sre"]').first().click();
  await expect(page).toHaveURL(/#\/bots\/platform-sre\/memory/);
  await expect(page.locator('[data-field="evidence"] .ev-add, #rp-md').first()).toBeVisible();
});
