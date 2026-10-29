// On a phone every page opens on its own content: the nav folds behind a menu
// button in the top bar, opens on a tap, and closes again once you pick a page.
import { test, expect } from '../fixtures'

const ROUTES = ['', 'chat', 'activity', 'workflows', 'bots', 'tickets', 'browser', 'config', 'secrets', 'packs', 'connections', 'system', 'audit', 'security']

test.beforeEach(() => {
  test.skip(!test.info().project.use.isMobile, 'phone layout only')
})

for (const r of ROUTES) {
  test(`#/${r} opens on page content, not the nav`, async ({ page }) => {
    await page.goto('/#/' + r)
    const main = page.locator('main.page')
    await expect(main).toBeVisible()
    await expect(page.locator('#primary-nav')).toBeHidden()
    await expect(page.getByTestId('menu-button')).toBeVisible()
    // The page's first content starts near the top of the first screen.
    const vh = page.viewportSize()!.height
    const box = (await main.boundingBox())!
    expect(box.y, 'page content starts on the first screen').toBeLessThan(vh * 0.25)
    await expect(main.locator(':scope > *').first()).toBeInViewport()
  })
}

test('the menu opens the nav and closes on navigation', async ({ page }) => {
  await page.goto('/#/')
  const btn = page.getByTestId('menu-button')
  const box = (await btn.boundingBox())!
  expect(box.height, 'menu button is a 44px tap target').toBeGreaterThanOrEqual(44)
  await expect(btn).toHaveAttribute('aria-expanded', 'false')
  await btn.click()
  await expect(btn).toHaveAttribute('aria-expanded', 'true')
  await expect(page.locator('#primary-nav')).toBeVisible()
  await page.locator('a.nav-item', { hasText: 'Bots' }).click()
  await expect(page).toHaveURL(/#\/bots/)
  await expect(page.locator('#primary-nav')).toBeHidden()
  await expect(btn).toHaveAttribute('aria-expanded', 'false')
})
