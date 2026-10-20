// Config semantics follow the real loader (strict decode): what the builder
// emits and what "valid · checked on a copy" accepts is what conductor's
// loader accepts. Covers Riley B1/B2, M5/M7 and the minors.
const { test, expect, go, applyStaged, isPhone } = require('./fixtures');

const yamlTa = (page) => page.locator('#wf-yaml .ced-ta');
const issues = (page) => page.evaluate(() => (window.__wfIssues ? window.__wfIssues() : []));
async function newWorkflow(page, name) {
  await go(page, '#/workflows', { fresh: true });
  await page.locator('[data-act="wf-new"]').click();
  await page.fill('#f-name', name); await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(new RegExp('#/workflows/' + name));
}
const pasteYaml = async (page, text) => { await yamlTa(page).fill(text); await page.waitForTimeout(500); };
const validate = (page, yaml, reusable) => page.evaluate(([y, r]) => WF.validate(y, { reusable: r }).filter((i) => i.level === 'error').map((i) => i.msg), [yaml, !!reusable]);

test('strict decode: unknown keys, budget keys, typed filters, isolation rules, optional prompt', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  const base = 'name: t\non: gh.pull_request\n';
  expect((await validate(page, base + 'policy: { budget: { max_runs: 40 } }\nsteps:\n  - { id: a, uses: gh.label, options: { repo: x, number: 1, labels: [a] } }\n')).join('\n')).toMatch(/max_runs: not a budget key[\s\S]*needs max_cost_usd and\/or max_tokens/);
  expect(await validate(page, base + 'policy: { budget: { window: 24h, max_tokens: 500k } }\nsteps:\n  - { id: a, uses: gh.label, options: { repo: x, number: 1, labels: [a] } }\n')).toEqual([]);
  expect((await validate(page, base + 'filter: { draft: "false" }\nsteps: []\n')).join()).toMatch(/draft: want true or false, got the string "false"/);
  expect(await validate(page, base + 'filter: { not_draft: true, expr: "!contains(title, \'Release\')" }\nsteps: []\n')).toEqual([]);
  expect(await validate(page, base + 'filter:\n  - { author: [dependabot], not_draft: true }\n  - "!is_draft"\nsteps: []\n')).toEqual([]);
  expect((await validate(page, base + 'steps:\n  - { id: a, bogus_key: 1, uses: gh.label, options: { repo: x, number: 1, labels: [a] } }\n')).join()).toMatch(/field bogus_key not found in type config\.Step/);
  // isolation: not on a paseo runtime; container needs an image; no "none" mode
  expect((await validate(page, base + 'steps:\n  - { id: a, type: agent, runtime: paseo, isolation: { mode: namespace } }\n')).join()).toMatch(/paseo runtime/);
  expect((await validate(page, base + 'steps:\n  - { id: a, type: agent, runtime: claude-cli, isolation: { mode: container } }\n')).join()).toMatch(/container\.image: required/);
  expect((await validate(page, base + 'steps:\n  - { id: a, type: agent, runtime: claude-cli, isolation: { mode: none } }\n')).join()).toMatch(/not user, namespace or container/);
  expect(await validate(page, base + 'steps:\n  - { id: a, type: agent, runtime: claude-cli, isolation: { mode: container, container: { image: ghcr.io/acme/agents:1 }, network: { egress: ["api.github.com:443"] } } }\n')).toEqual([]);
  // prompt is optional: omit it and the agent gets the event
  expect(await validate(page, base + 'steps:\n  - { id: a, type: agent, runtime: claude-cli }\n')).toEqual([]);
  // verbs are <instance>.<verb>; broker grants are <vault>/<key> with secrets_via: broker
  expect((await validate(page, base + 'steps:\n  - { id: a, type: agent, skill: { verbs: [terraform.apply] } }\n')).join()).toMatch(/not <instance>\.<verb>/);
  expect((await validate(page, base + 'steps:\n  - { id: a, type: agent, skill: { verbs: [tf-apply.apply], allow_secrets: [house/gh-app-key] } }\n')).join()).toMatch(/needs secrets_via: broker/);
  expect(await validate(page, base + 'steps:\n  - { id: a, type: agent, skill: { verbs: [tf-apply.apply], secrets_via: broker, allow_secrets: [house/gh-app-key] } }\n')).toEqual([]);
  // the bot step takes name or team, inside bot:
  expect((await validate(page, base + 'steps:\n  - { id: a, bot: { name: platform-sre, team: platform } }\n')).join()).toMatch(/exactly one of name/);
});

test('the builder emits real keys: budget window/max_tokens, typed filter values, no not_not_ keys', async ({ page }) => {
  await newWorkflow(page, 'guard');
  await page.locator('[data-act="wf-trig-pick"]').click();
  await page.fill('#pk-q', 'pull request'); await page.locator('.pk-item[data-key="on:gh.pull_request"]').click();
  const keys = await page.locator('#flt-new option').allTextContents();
  expect(keys.some((k) => k.startsWith('not_not_'))).toBe(false);
  expect(keys).toContain('not_draft'); expect(keys).toContain('expr');
  await page.selectOption('#flt-new', 'not_draft');
  await page.selectOption('#flt-val', 'true');
  await page.locator('[data-act="wf-flt-add"]').click();
  await expect.poll(() => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toMatch(/not_draft: true/);
  expect(await page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).not.toMatch(/"true"/);
  await page.locator('details[data-adv="$trigger"] summary').click();
  await expect(page.locator('[data-wf-path="policy.budget.max_runs"]')).toHaveCount(0);
  await page.locator('[data-wf-path="policy.budget.max_tokens"]').fill('500k');
  await expect.poll(() => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toMatch(/budget: \{ max_tokens: 500k \}|max_tokens: 500k/);
});

test('isolation in the agent form: unavailable on paseo, full editor elsewhere, container asks for an image', async ({ page }) => {
  await newWorkflow(page, 'iso');
  await pasteYaml(page, 'name: iso\non: gh.pull_request\nsteps:\n  - id: review\n    type: agent\n    runtime: paseo\n');
  await page.locator('[data-act="wf-toggle"][data-id="review"]').click().catch(() => {});
  await expect(page.locator('[data-card="review"]')).toContainText("paseo runtime");
  await page.locator('[data-card="review"] select[data-wf-path="runtime"]').selectOption('claude-cli');
  await page.locator('[data-card="review"] [data-act="wf-seg"][data-path="isolation.mode"][data-v="container"]').click();
  await expect(page.locator('[data-card="review"]')).toContainText('container.image: required');
  await page.locator('[data-card="review"] [data-wf-path="isolation.container.image"]').fill('ghcr.io/acme/agents:1');
  await page.locator('[data-card="review"] [data-wf-path="isolation.network.egress"] .list-add').fill('api.github.com:443');
  await page.locator('[data-card="review"] [data-wf-path="isolation.network.egress"] .list-add').press('Enter');
  await expect.poll(() => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toMatch(/image: ghcr\.io\/acme\/agents:1/);
  expect(await page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toMatch(/egress: \[api\.github\.com:443\]|egress:\n\s+- api\.github\.com:443/);
  await expect(page.locator('[data-card="review"]')).not.toContainText('container.image: required');
});

test('skill.verbs in map form (scoped grants) renders and edits, and never crashes the list', async ({ page }) => {
  await newWorkflow(page, 'scoped');
  await pasteYaml(page, 'name: scoped\non: gh.pull_request\nsteps:\n  - id: review\n    type: agent\n    runtime: claude-cli\n    skill:\n      verbs:\n        gh.pr_diff: {}\n        slack-ops.post: { channel: ["#reviews"] }\n');
  await expect(page.locator('.sticky-err')).toHaveCount(0);
  await page.locator('[data-act="wf-toggle"][data-id="review"]').click().catch(() => {});
  const g = page.locator('[data-card="review"] .grants');
  await expect(g).toContainText('slack-ops.post');
  await expect(g).toContainText('channel: #reviews');
  await page.locator('[data-card="review"] [data-scope-val="slack-ops.post"]').fill('#ops');
  await page.locator('[data-card="review"] [data-act="wf-scope-add"][data-p="slack-ops.post"]').click();
  await expect.poll(() => page.evaluate(() => document.querySelector('#wf-yaml .ced-ta').value)).toMatch(/#reviews.*#ops|"#ops"/s);
  // a resource the verb doesn't declare is a load error
  await pasteYaml(page, 'name: scoped\non: gh.pull_request\nsteps:\n  - id: review\n    type: agent\n    runtime: claude-cli\n    skill:\n      verbs:\n        slack-ops.post: { repo: [acme/api] }\n');
  expect(await page.evaluate(() => WF.validate(document.querySelector('#wf-yaml .ced-ta').value).map((i) => i.msg).join('\n'))).toContain('declares no resource "repo"');
});

test('pack overrides: a key that matches nothing is an error; Checks show the real validate output', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { const c = JSON.parse(JSON.stringify(S.staged('packs', 'review'))); c.steps['nope/missing'] = { model: 'light' }; S.stage('packs', 'review', c); });
  await go(page, '#/config/apply');
  await expect(page.locator('#chk-state')).toContainText(/passed|failed|error/, { timeout: 8000 });
  await expect(page.locator('#validate-out')).toContainText('$ conductor validate');
  await expect(page.locator('#validate-out')).toContainText('nope/missing');
  await expect(page.locator('#validate-out')).toContainText('exit status 1');
  await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
});

test('one widening classifier: egress, grants and repos are caught; an official pack is not flagged', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const w = await page.evaluate(() => {
    const before = D.workflows.find((x) => x.name === 'ci-fixer').yaml;
    const after = before.replace('filter: { repo: [acme/api, acme/web]', 'filter: { repo: [acme/api, acme/web, acme/infra]').replace(/^( *)runtime: claude-cli\n/m, (m0, ind) => `${ind}runtime: claude-cli\n${ind}isolation: { mode: namespace, network: { egress: ["api.github.com:443"] } }\n${ind}skill: { verbs: [gh.comment] }\n`);
    return { wf: S.classify('workflows', before, after), pack: S.classify('packs', null, { use: 'conductor-packs/dep-hygiene' }), third: S.classify('packs', null, { use: 'acme/conductor-packs/kit' }) };
  });
  expect(w.wf.join('\n')).toMatch(/egress: adds api\.github\.com:443/);
  expect(w.wf.join('\n')).toMatch(/skill\.verbs: grants gh\.comment/);
  expect(w.wf.join('\n')).toMatch(/filter\.repo: also runs on acme\/infra/);
  expect(w.pack).toEqual([]);
  expect(w.third.join()).toMatch(/third-party pack source/);
});

test('Safety: every bot in words (on its own / asks you first / never), linked from the sandbox chip', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await go(page, '#/safety'); // a System tab now
  await expect(page.locator('.dl-subnav a[aria-current="page"]')).toHaveText('Safety');
  await expect(page.locator('.dl-phead h1')).toHaveText(/Safety/);
  await expect(page.locator('.tier-ladder')).toHaveCount(0); // no risk ladder: each bot's reach is said in words below
  // each bot's three groups, as the approved "What it can do": in plain words
  const sre = page.locator('.panel', { hasText: 'Atlas' }).first(); // a bot by its display name (r5-9)
  await expect(sre.locator('.cang[data-s="ok"]')).toContainText('Post a message');
  await expect(sre.locator('.cang[data-s="ember"]')).toContainText('Apply a signed plan');
  await expect(sre.locator('.cang[data-s="fail"]')).toContainText('Delete in GitHub');
  await go(page, '#/bots/platform-sre/access');
  await expect(page.locator('.dl-can .cang[data-s="ok"]')).toContainText('Post a message');
});
