// The config the UI writes is config conductor's loader accepts (r5 blocker 1): realschema.js mirrors the real
// BotConfig (with its inline identity: avatar, color, display_name, tagline, personality), TeamConfig (avatar) and the
// browser and purchase keys, and every key a screen writes is in it. A new bot, a mascot, an uploaded picture and a
// team's picture all pass the checks and apply; a value from the brand's designed set that conductor's placeholder
// lists don't have is a warning that says so, never a silent pass or a blocking error.
const { test, expect, go, applyStaged } = require('./fixtures');

const keysOutsideRS = (page) => page.evaluate(() => { const bad = [];
  S.view().bots.forEach((b) => { Object.keys(b.cfg || {}).forEach((k) => { if (!RS.bot.has(k)) bad.push(`bots.${b.name}.${k}`); }); Object.keys((b.cfg || {}).avatar || {}).forEach((k) => { if (!RS.avatar.has(k)) bad.push(`bots.${b.name}.avatar.${k}`); }); });
  S.view().teams.forEach((t) => { Object.keys(t.cfg || {}).forEach((k) => { if (!RS.team.has(k)) bad.push(`teams.${t.name}.${k}`); }); Object.keys((t.cfg || {}).avatar || {}).forEach((k) => { if (!RS.avatar.has(k)) bad.push(`teams.${t.name}.avatar.${k}`); }); });
  return bad; });

test('New bot: its config (with its mascot) passes the checks and applies', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"][data-p="incidents"]').click();
  for (let i = 0; i < 4; i++) await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-create"]').click();
  expect(await page.evaluate(() => Object.keys(D.pending.find((c) => c.key === 'incident-bot').after.avatar || {}).sort())).toEqual(['color', 'eyes', 'shape']);
  // the stepper never ticks Validate when it failed; here it passed
  await applyStaged(page);
  expect(await page.evaluate(() => D.bots.some((b) => b.name === 'incident-bot'))).toBe(true);
  expect(await keysOutsideRS(page)).toEqual([]);
});

test('New bot from a community bot (SRE on-call), default mascot: its pack and the bot pass the checks and apply', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-tpl"]').first().click();
  for (let i = 0; i < 6 && await page.locator('[data-act="bw-next"]').count(); i++) await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-create"]').click();
  await applyStaged(page, { expectWidening: await page.locator('.ack input[type=checkbox]').count() > 0 });
  expect(await keysOutsideRS(page)).toEqual([]);
});

test('the reviewer\'s repro: Atlas → Settings → Mascot → Octa → Use this picture → the review passes', async ({ page }) => {
  await go(page, '#/bots/platform-sre/settings', { fresh: true });
  await page.locator('[data-act="bot-mascot"]').click();
  await page.locator('[data-act="mk-shape"][data-v="octa"]').click();
  await page.locator('[data-act="mk-use"]').click();
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('#chk-state')).toContainText('passed');
  await expect(page.locator('#steps .as').nth(1)).toHaveClass(/\bdone\b/);
  await applyStaged(page);
});

test('a new mascot and an uploaded picture pass the checks; the upload is a file beside the config', async ({ page }) => {
  await go(page, '#/bots/platform-sre/settings', { fresh: true });
  await page.locator('[data-act="bot-mascot"]').click();
  await page.locator('[data-act="mk-shape"][data-v="capsule"]').click(); await page.locator('[data-act="mk-eyes"][data-v="happy"]').click(); await page.locator('[data-act="mk-use"]').click();
  let v = await page.evaluate(() => S.validate());
  expect(v.errors).toEqual([]);
  expect(v.warnings.join('\n'), 'a brand name off conductor\'s placeholder list is said, as a warning').toMatch(/avatar\.shape: "capsule" is the brand's mascot shape; conductor's list \(placeholders/);
  await page.locator('[data-act="bot-mascot"]').click();
  await page.locator('[data-act="mk-mode"][data-v="upload"]').click();
  const png = await page.evaluate(() => { const c = document.createElement('canvas'); c.width = 64; c.height = 64; const x = c.getContext('2d'); x.fillStyle = '#3a8'; x.fillRect(0, 0, 64, 64); return c.toDataURL('image/png').split(',')[1]; });
  await page.setInputFiles('#cr-file', { name: 'me.png', mimeType: 'image/png', buffer: Buffer.from(png, 'base64') });
  await page.locator('[data-act="mk-use"]').click();
  expect(await page.evaluate(() => D.pending.at(-1).after.avatar.image)).toMatch(/^avatars\/platform-sre\.(png|webp|jpg)$/);
  await expect(page.locator('.mk-field .bav.img img').first()).toHaveAttribute('src', /^data:image\//);
  v = await page.evaluate(() => S.validate()); expect(v.errors).toEqual([]);
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await applyStaged(page);
  expect(await keysOutsideRS(page)).toEqual([]);
});

test('a team\'s picture: an icon from conductor\'s list, an upload, or initials (no avatar at all) all pass', async ({ page }) => {
  await go(page, '#/teams/platform', { fresh: true });
  const pick = async (mode, then) => { await page.locator('[data-act="team-more"]').first().click(); await page.locator('#pop-menu [data-act="team-pic"]').click(); await page.locator(`[data-act="tp-mode"][data-v="${mode}"]`).click(); if (then) await then(); await page.locator('[data-act="tp-use"]').click(); };
  await pick('icon', async () => { await expect(page.locator('[data-act="tp-icon"]')).toHaveCount(15); await page.locator('[data-act="tp-icon"][data-v="radar"]').click(); });
  expect(await page.evaluate(() => JSON.stringify(S.staged('teams', 'platform').avatar))).toMatch(/"icon":"radar"/);
  expect((await page.evaluate(() => S.validate())).errors).toEqual([]);
  await pick('initials');
  expect(await page.evaluate(() => 'avatar' in S.staged('teams', 'platform'))).toBe(false);
  expect((await page.evaluate(() => S.validate())).errors).toEqual([]);
  expect(await keysOutsideRS(page)).toEqual([]);
});

test('the checks refuse what conductor refuses: unknown avatar keys, a face on a team, an icon without a colour', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const errs = await page.evaluate(() => { const out = {};
    const tryBot = (av) => { const c = JSON.parse(JSON.stringify(S.staged('bots', 'platform-sre'))); c.avatar = av; S.stage('bots', 'platform-sre', c); const e = S.validate().errors.filter((x) => /avatar/.test(x)); D.pending = []; return e; };
    const tryTeam = (av) => { const c = JSON.parse(JSON.stringify(S.staged('teams', 'platform'))); c.avatar = av; S.stage('teams', 'platform', c); const e = S.validate().errors.filter((x) => /avatar/.test(x)); D.pending = []; return e; };
    out.initials = tryTeam({ initials: true }); out.faceOnTeam = tryTeam({ shape: 'hex', eyes: 'dots' }); out.iconNoColour = tryTeam({ icon: 'radar' }); out.faceAndIcon = tryBot({ shape: 'hex', icon: 'radar' }); out.ok = tryBot({ shape: 'hex', eyes: 'dots', color: 'teal' });
    return out; });
  expect(errs.initials.join()).toMatch(/avatar\.initials: not an avatar key/);
  expect(errs.faceOnTeam.join()).toMatch(/faces \(mascots\) are for bots/);
  expect(errs.iconNoColour.join()).toMatch(/an icon needs a color/);
  expect(errs.faceAndIcon.join()).toMatch(/a face \(shape, eyes\) or an icon — not both/);
  expect(errs.ok).toEqual([]);
});

test('a failed check is never ticked: the stepper marks Validate failed and the count reads right', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { const c = JSON.parse(JSON.stringify(S.staged('bots', 'platform-sre'))); c.avatar = { initials: true }; S.stage('bots', 'platform-sre', c); });
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('#chk-state')).toContainText('failed');
  expect(await page.locator('[data-act="apply-go"]').evaluate((b) => b.getAttribute('data-tip') || b.getAttribute('title') || b.getAttribute('aria-description') || '')).toMatch(/^1 error blocks the apply/);
  const st = await page.locator('#steps .as').evaluateAll((xs) => xs.map((x) => [x.querySelector('.as-l').textContent, x.className, x.querySelector('b').textContent]));
  const val = st.find(([l]) => l === 'Validate on a copy');
  expect(val[1], 'Validate is marked failed').toMatch(/\bbad\b/);
  expect(val[2], 'and never ticked').not.toBe('✓');
  expect(st.filter(([, c]) => /\bdone\b/.test(c)).map(([l]) => l)).toEqual(['Edit']);
});
