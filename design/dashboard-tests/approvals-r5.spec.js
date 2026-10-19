// r5 blocker 4: p12's tiles said 0 Adds while the plan ('Plan: 1 to add, 2 to change, 0 to destroy') and the seal
// (+1 ~2 −0) said one. The tiles now say what the plan's own summary says; the add that is the replacement says so.
const { test, expect, go } = require('./fixtures');

test('p12: the tiles agree with the plan summary and the seal; the replacement is named as one of the adds', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  const tiles = await page.locator('.dl-changes > div').evaluateAll((xs) => xs.map((x) => [x.querySelector('span').textContent, +x.querySelector('b').textContent, (x.querySelector('small') || {}).textContent || '']));
  expect(tiles).toEqual([['Adds', 1, 'the replacement'], ['Changes', 2, ''], ['Replaces', 1, ''], ['Destroys', 0, '']]);
  const tf = await page.evaluate(() => D.proposals.find((p) => p.id === 'p12').tf);
  expect([tf.add, tf.change, tf.destroy]).toEqual([tiles[0][1], tiles[1][1], tiles[3][1]]);
  expect(await page.evaluate(() => D.tfPlan)).toContain(`Plan: ${tiles[0][1]} to add, ${tiles[1][1]} to change, ${tiles[3][1]} to destroy`);
});
