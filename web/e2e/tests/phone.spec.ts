// On a phone the rail is replaced entirely by the bottom tab bar (design: the
// approved shell's phone layout, Shell.tsx phase 1) — there is no fold-out nav
// drawer any more. Every route still opens on its own content, not on chrome.
import { test, expect } from '../fixtures'

const ROUTES = ['', 'chat', 'activity', 'workflows', 'bots', 'tickets', 'browser', 'config', 'secrets', 'packs', 'connections', 'system', 'audit', 'security', 'attention', 'more']

test.beforeEach(() => {
  test.skip(!test.info().project.use.isMobile, 'phone layout only')
})

for (const r of ROUTES) {
  test(`#/${r} opens on page content, with the tab bar, not the rail`, async ({ page }) => {
    await page.goto('/#/' + r)
    const main = page.locator('main.main')
    await expect(main).toBeVisible()
    await expect(page.locator('.rail')).toBeHidden()
    await expect(page.locator('nav.tabbar')).toBeVisible()
    // The page's first content starts near the top of the first screen.
    const vh = page.viewportSize()!.height
    const box = (await main.boundingBox())!
    expect(box.y, 'page content starts on the first screen').toBeLessThan(vh * 0.25)
    await expect(main.locator(':scope .page > *').first()).toBeInViewport()
  })
}

test('the tab bar has five items and navigates without a fold-out menu', async ({ page }) => {
  await page.goto('/#/')
  const tabbar = page.locator('nav.tabbar')
  await expect(tabbar).toBeVisible()
  const tabs = tabbar.locator('a')
  await expect(tabs).toHaveCount(5)
  // Each tab is a real 44px-plus tap target (design: comfortable density on a phone).
  for (const t of await tabs.all()) {
    const box = (await t.boundingBox())!
    expect(box.height, 'tab bar item is a 44px tap target').toBeGreaterThanOrEqual(44)
  }
  await page.locator('nav.tabbar a[data-nav="activity"]').click()
  await expect(page).toHaveURL(/#\/activity/)
  await expect(page.locator('nav.tabbar a[data-nav="activity"]')).toHaveClass(/on/)
})

// More (the tab bar's fifth item) carries Settings on a phone, since the rail
// — where Settings lives on a wider screen — is hidden entirely.
test('More carries Settings on a phone', async ({ page }) => {
  await page.goto('/#/')
  await page.locator('nav.tabbar a[data-nav="more"]').click()
  await expect(page).toHaveURL(/#\/more/)
  await expect(page.locator('.page', { hasText: 'More' })).toBeVisible()
  await expect(page.locator('a', { hasText: 'System' })).toBeVisible()
  await expect(page.locator('a', { hasText: 'Secrets' })).toBeVisible()
})

test('a wide screen shows the rail, not the tab bar', async ({ page }) => {
  await page.setViewportSize({ width: 1200, height: 800 })
  await page.goto('/#/')
  await expect(page.locator('.rail')).toBeVisible()
  await expect(page.locator('nav.tabbar')).toBeHidden()
})
