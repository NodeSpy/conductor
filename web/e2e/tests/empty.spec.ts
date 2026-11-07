// A first visit to a fresh box (no workflows, runs, bots, teams, tickets, packs
// or secrets) reads as empty states, never errors: every screen says what it is
// for and what to do next, and the API answers JSON on every path the screens
// read. Runs on Chromium desktop and WebKit iPhone, OS dark and light; with
// DASH_SHOTS set, each empty state is saved there as <screen>--<project>.png.
// The create-first-bot path runs end to end on its own daemon.
import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect, type Page } from '../fixtures'
import { startEmpty, stop, type Runtime } from '../lib/daemon'

test.describe.configure({ mode: 'serial' })

const SHOTS = process.env.DASH_SHOTS || ''
const BASE_PORT = Number(process.env.DASH_E2E_PORT || 48766)

let rt: Runtime

test.beforeAll(async () => {
  rt = await startEmpty(BASE_PORT + 11)
})
test.afterAll(() => {
  if (rt) stop(rt)
})

const RAW = /SyntaxError|JSON\.parse|JSON Parse|Unexpected token|is not valid JSON|Failed to fetch|Load failed|\b(404 Not Found|500 Internal Server Error|503 Service Unavailable)\b/

async function shot(page: Page, name: string): Promise<void> {
  if (!SHOTS) return
  mkdirSync(SHOTS, { recursive: true })
  await page.screenshot({ path: path.join(SHOTS, `${name}--${test.info().project.name}.png`), fullPage: true })
}

// noErrors: no error banner and no raw parser/HTTP text anywhere on the page.
async function noErrors(page: Page): Promise<void> {
  await expect(page.locator('.banner.err')).toHaveCount(0)
  const text = await page.locator('.page').innerText()
  expect(text).not.toMatch(RAW)
}

const SCREENS: { route: string; name: string; testid: string; heading: RegExp }[] = [
  { route: 'activity', name: 'activity', testid: 'activity-empty', heading: /No runs yet/ },
  { route: 'workflows', name: 'workflows', testid: 'workflows-empty', heading: /No workflows yet/ },
  { route: 'bots', name: 'bots', testid: 'bots-empty', heading: /Meet your first bot/ },
  { route: 'chat', name: 'chat', testid: 'chat-empty', heading: /No one to talk to yet/ },
  { route: 'tickets', name: 'tickets', testid: 'tickets-empty', heading: /Tickets need a bot/ },
  { route: 'secrets', name: 'secrets', testid: 'secrets-empty', heading: /No secrets yet/ },
  { route: 'packs', name: 'packs', testid: 'packs-empty', heading: /No packs installed/ },
  { route: 'audit', name: 'audit', testid: 'audit-empty', heading: /Nothing recorded yet/ },
  { route: 'security', name: 'access', testid: 'access-empty', heading: /Nobody signs in here yet/ },
]

for (const s of SCREENS) {
  test(`#/${s.route} on a fresh box is an empty state, not an error`, async ({ page, collector }) => {
    await page.goto(`${rt.baseURL}/#/${s.route}`)
    const empty = page.getByTestId(s.testid)
    await expect(empty).toBeVisible()
    await expect(empty.getByRole('heading', { level: 2 })).toHaveText(s.heading)
    await noErrors(page)
    // A failed request is only fine where the screen expects one: Access asks
    // who is signed in, and an open dashboard has nobody (401).
    const failed = collector.failed.filter((f) => !(s.name === 'access' && /^401 .*\/api\/v1\/access$/.test(f)))
    expect(failed, 'no failed requests').toEqual([])
    expect(collector.consoleErrors.filter((e) => !/401/.test(e)), 'no console errors').toEqual([])
    await shot(page, s.name)
  })
}

// Home on a fresh box (design review note): the layout stays up — the tiles,
// Needs you, Running now and the runs strip — rather than collapsing to one
// centred message. Only the panels that have nothing say so, each on its own.
test('#/ on a fresh box keeps the Home layout, empty only where there is nothing', async ({ page, collector }) => {
  await page.goto(`${rt.baseURL}/#/`)
  await expect(page.locator('.dl-glance .dl-tile')).toHaveCount(4)
  await expect(page.locator('.dl-glance .dl-tile b')).toHaveText(['0', '0', '0', '0'])
  const need = page.getByTestId('needs-you-empty')
  await expect(need).toBeVisible()
  await expect(need.getByRole('heading', { level: 2 })).toHaveText(/Nothing needs you/)
  await expect(page.locator('.dl-running')).toHaveCount(0)
  await expect(page.locator('.dl-hours')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Get started' })).toBeVisible()
  await noErrors(page)
  expect(collector.failed, 'no failed requests').toEqual([])
  expect(collector.consoleErrors.filter((e) => !/401/.test(e)), 'no console errors').toEqual([])
  await shot(page, 'overview')
})

// Screens that were already fine at zero still show no error and no raw text.
test('Config names its switches in plain words', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/config`)
  await expect(page.locator('label.field[data-path="dashboard.enabled"] .cfg-name')).toHaveText('Dashboard on')
  await expect(page.locator('label.field[data-path="store.state_file"] .cfg-name')).toHaveText('State file')
})

for (const r of ['browser', 'browser/cards', 'browser/logins', 'browser/details', 'connections', 'system', 'config']) {
  test(`#/${r} on a fresh box shows no error`, async ({ page }) => {
    await page.goto(`${rt.baseURL}/#/${r}`)
    await expect(page.locator('.page h1').first()).toBeVisible()
    await expect(page.getByText('Loading', { exact: false })).toHaveCount(0)
    await noErrors(page)
    await shot(page, r.replace('/', '-'))
  })
}

test('the Bots welcome has one big way in, and no controls with nothing to act on', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/bots`)
  const empty = page.getByTestId('bots-empty')
  await expect(empty).toBeVisible()
  const cta = page.getByTestId('create-first-bot')
  await expect(cta).toBeVisible()
  const box = (await cta.boundingBox())!
  expect(box.height, 'the create button is a big tap target').toBeGreaterThanOrEqual(44)
  await expect(page.locator('.filter-bar')).toHaveCount(0)
  await expect(page.getByText('Group by')).toHaveCount(0)
  await expect(page.getByText(/Each team is a section/)).toHaveCount(0)
  await expect(page.getByTestId('new-bot')).toHaveCount(0)
  await cta.click()
  await expect(page.getByRole('dialog', { name: 'New bot' })).toBeVisible()
  // No teams yet: the sheet doesn't offer a team picker with only "Solo" in it.
  await expect(page.getByRole('dialog').getByText('Team', { exact: true })).toHaveCount(0)
  await shot(page, 'bots-new-sheet')
})

// A box with no agent runtime can't run a bot yet: the welcome says that comes
// first instead of offering a create that can't validate.
test('the Bots welcome on a box with no agent runtime says what comes first', async ({ page }) => {
  const bare = await startEmpty(BASE_PORT + 13, { runtime: false })
  try {
    await page.goto(`${bare.baseURL}/#/bots`)
    await expect(page.getByTestId('bots-empty')).toBeVisible()
    await expect(page.getByTestId('bots-need-runtime')).toBeVisible()
    await expect(page.getByTestId('create-first-bot')).toHaveCount(0)
    await expect(page.getByTestId('bots-empty-runtime')).toHaveAttribute('href', '#/config')
    await noErrors(page)
    await shot(page, 'bots-no-runtime')
  } finally {
    stop(bare)
  }
})

// The empty Hosts card has a way to add one: it stages through the review.
test('a host can be added from the Hosts card', async ({ page }) => {
  await page.goto(`${rt.baseURL}/#/connections`)
  await page.getByTestId('add-host').click()
  const form = page.getByTestId('add-host-form')
  await form.getByRole('textbox', { name: 'Name' }).fill('build-box')
  await form.getByRole('textbox', { name: 'Address' }).fill('build01.example.com')
  await shot(page, 'connections-add-host')
  await form.getByTestId('stage-host').click()
  await expect(form).toBeHidden()
  await page.getByRole('button', { name: 'Review changes' }).click()
  await expect(page.locator('.effects')).toContainText('hosts.build-box')
  await noErrors(page)
  // Leave the fresh box as it was for the screens that follow.
  await page.request.post(`${rt.baseURL}/api/v1/config/clear`, { data: {} })
})

test('the bot API answers JSON with nothing configured', async ({ request }) => {
  for (const [p, key] of [
    ['/api/v1/bots', 'bots'],
    ['/api/v1/teams', 'teams'],
    ['/api/v1/conversations', 'conversations'],
    ['/api/v1/tickets', 'tickets'],
    ['/api/v1/proposals', 'proposals'],
  ] as const) {
    const res = await request.get(rt.baseURL + p)
    expect(res.status(), p).toBe(200)
    expect(res.headers()['content-type'], p).toMatch(/^application\/json/)
    const body = await res.json()
    expect(body[key], p).toEqual([])
    expect(body.configured, p).toBe(false)
  }
  const roster = await (await request.get(rt.baseURL + '/api/v1/bots')).json()
  expect(roster.editable).toBe(true)
  for (const p of ['/api/v1/bots/atlas', '/api/v1/nope']) {
    const res = await request.get(rt.baseURL + p)
    expect(res.status(), p).toBe(404)
    expect(res.headers()['content-type'], p).toMatch(/^application\/json/)
    expect((await res.json()).error?.message, p).toBeTruthy()
  }
})

// The whole create path from the welcome, on a daemon of its own (it applies a
// config change and the daemon reloads): stage the bot, review, apply, and the
// roster shows it.
test('create the first bot from the welcome', async ({ page }) => {
  test.setTimeout(120_000)
  const own = await startEmpty(BASE_PORT + 12)
  try {
    await page.goto(`${own.baseURL}/#/bots`)
    await page.getByTestId('create-first-bot').click()
    const sheet = page.getByRole('dialog', { name: 'New bot' })
    await sheet.getByRole('textbox', { name: 'Name', exact: true }).fill('scout')
    await sheet.getByRole('textbox', { name: 'Display name' }).fill('Scout')
    await sheet.getByRole('textbox', { name: "What it's for" }).fill('Keeps an eye on the build')
    await sheet.getByTestId('create-bot').click()
    await expect(sheet).toBeHidden()

    // The staged card's own primary button is the next step: review, then apply.
    const card = page.getByTestId('bots-staged')
    await expect(card).toBeVisible()
    await noErrors(page)
    await shot(page, 'bots-staged')
    await expect(card.getByTestId('bot-summary')).toContainText('Scout')
    await expect(card.getByTestId('bot-summary')).toContainText('Avatar')
    await card.getByTestId('bots-review').click()
    await expect(card.getByTestId('bots-apply')).toBeVisible()
    // One apply action, the card's: the panel below only shows what changes.
    await expect(page.getByRole('button', { name: /apply/i })).toHaveCount(1)
    await expect(page.getByRole('button', { name: /Review changes|Review again|Check it first/ })).toHaveCount(0)
    await expect(page.locator('.diff')).toContainText('scout:')
    // On a phone the change wraps instead of running off the edge.
    const overflow = await page.locator('.diff').evaluate((el) => el.scrollWidth - el.clientWidth)
    if (test.info().project.use.isMobile) expect(overflow, 'the diff fits the screen').toBeLessThanOrEqual(1)
    // No step-up on an open dashboard, so no note about one.
    await expect(page.getByTestId('review-guarded')).toHaveCount(0)
    await shot(page, 'bots-staged-reviewed')
    await card.getByTestId('bots-apply').click()

    // The daemon reloads; the roster then shows the new bot, in practice mode.
    const row = page.locator('li.bot-row[data-bot="scout"]')
    await expect(row).toBeVisible({ timeout: 60_000 })
    await expect(row).toContainText('Scout')
    await expect(row).toContainText('practice mode')
    await expect(page.getByTestId('bots-empty')).toHaveCount(0)
    await expect(page.getByTestId('new-bot')).toBeVisible()
    await expect(page.locator('h1 .count')).toHaveText('1 bot')
    // The success note is plain words; the config and backup ids wait in Details.
    const head = page.locator('.banner.ok .applied-head')
    await expect(head).toContainText('Applied')
    await expect(head).not.toContainText(/[0-9a-f]{12}|\d{8}T/)
    await expect(page.locator('.banner.ok details')).not.toHaveAttribute('open', '')
    // The live stream drops while the daemon reloads, then comes back: the
    // top-bar connection cell only renders while it isn't live (Shell.tsx's
    // ThemeCell), so "live again" is its absence.
    await expect(page.getByTestId('conn-bar')).toBeHidden({ timeout: 30_000 })
    await noErrors(page)
    await shot(page, 'bots-first-bot')
  } finally {
    stop(own)
  }
})
