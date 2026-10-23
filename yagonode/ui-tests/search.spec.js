const { test, expect } = require('@playwright/test');

const query = process.env.YAGO_UI_QUERY || 'Canada';
const searchPage = '/yacysearch.html?resource=local&contentdom=text';

async function observeSearchResultPage(page) {
  await expect(page.getByRole('heading', { name: `Search for ${query}` })).toBeVisible();
  const status = page.locator('.result-count');
  await expect(status).toContainText(/result|searching/i, { timeout: 15_000 });
  const results = page.locator('#search-results > li');
  const resultCount = await results.count();
  if (resultCount > 0) {
    await expect(results.first()).toBeVisible();
    await expect(results.first().getByRole('heading').getByRole('link')).toBeVisible();
  } else if (/partial|searching/i.test(await status.innerText())) {
    await expect(page.locator('#nothing-found')).toHaveCount(0);
  } else {
    await expect(page.locator('#nothing-found')).toBeVisible();
  }
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByText(/local search deadline exceeded|search incomplete/i)).toHaveCount(0);

  return {
    complete: !/at least|partial|searching/i.test(await status.innerText()),
    resultCount,
  };
}

test('returns a useful local result and refines the repeated search', async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  await page.goto(searchPage);
  await page.locator('#query').fill(query);
  await page.getByRole('button', { name: 'search' }).click();
  await expect(page).toHaveURL(/progressive=1/);

  await observeSearchResultPage(page);
  // A random partial window may be empty. Require the full-shard completion
  // cache to produce the useful result before ending the scenario.
  await page.screenshot({ path: testInfo.outputPath('first-search.png'), fullPage: true });

  const cacheDeadline = Date.now() + 35_000;
  let complete = false;
  while (!complete && Date.now() < cacheDeadline) {
    await page.locator('#query').fill(query);
    await page.getByRole('button', { name: 'search' }).click();
    await expect(page).toHaveURL(/progressive=1/);
    const state = await observeSearchResultPage(page);
    complete = state.complete && state.resultCount > 0;
    if (!complete) await page.waitForTimeout(1_000);
  }
  expect(
    complete,
    'the bounded full-shard pass should populate the complete result cache',
  ).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('repeated-search.png'), fullPage: true });
});

test('does not claim an incomplete empty search is complete', async ({ page }, testInfo) => {
  const unmatched = `yagoui${Date.now()}qzxv`;
  await page.goto(searchPage);
  await page.locator('#query').fill(unmatched);
  await page.getByRole('button', { name: 'search' }).click();
  await expect(page).toHaveURL(/progressive=1/);

  await expect(page.getByRole('heading', { name: `Search for ${unmatched}` })).toBeVisible();
  const status = page.locator('.result-count');
  await expect(status).toContainText(/result|searching/i);
  await expect(status).not.toContainText(/searching/i, { timeout: 20_000 });
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByText(/local search deadline exceeded|search incomplete/i)).toHaveCount(0);
  if (await status.innerText().then((text) => /partial search/i.test(text))) {
    await expect(page.locator('#nothing-found')).toHaveCount(0);
  } else {
    await expect(page.locator('#nothing-found')).toBeVisible();
  }
  await page.screenshot({ path: testInfo.outputPath('empty-search.png'), fullPage: true });
});
