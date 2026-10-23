const { test, expect } = require('@playwright/test');

const query = process.env.YAGO_UI_QUERY || 'Canada';
const username = process.env.YAGO_UI_USERNAME;
const password = process.env.YAGO_UI_PASSWORD;

test.beforeEach(async ({ page }) => {
  if (!username || !password) return;

  await page.goto('/admin/login');
  await page.getByLabel('Login:').fill(username);
  await page.getByLabel('Password:').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin\/overview/);
});

async function observeSearchResultPage(page) {
  await expect(page.getByRole('heading', { name: `Search for ${query}` })).toBeVisible();
  const status = page.getByRole('status').first();
  await expect(status).toContainText(/result/, { timeout: 15_000 });
  const results = page.getByRole('list').filter({ has: page.getByRole('heading') });
  const resultCount = await results.getByRole('listitem').count();
  if (resultCount > 0) {
    await expect(results.getByRole('listitem').first()).toBeVisible();
    await expect(
      results.getByRole('listitem').first().getByRole('heading').getByRole('link'),
    ).toBeVisible();
  } else if (/partial/i.test(await status.innerText())) {
    await expect(page.getByText('Nothing found.', { exact: true })).toHaveCount(0);
  } else {
    await expect(page.getByText('Nothing found.', { exact: true })).toBeVisible();
  }
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByText(/local search deadline exceeded|search incomplete/i)).toHaveCount(0);

  return {
    complete: !/at least|partial/i.test(await status.innerText()),
    resultCount,
  };
}

test('returns a useful local result and refines the repeated search', async ({ page }, testInfo) => {
  test.setTimeout(75_000);
  await page.goto('/');
  await page.getByRole('searchbox', { name: 'Search terms' }).fill(query);
  await page.getByRole('button', { name: 'Search' }).click();

  await observeSearchResultPage(page);
  // A random partial window may be empty. Require the full-shard completion
  // cache to produce the useful result before ending the scenario.
  await page.screenshot({ path: testInfo.outputPath('first-search.png'), fullPage: true });

  const cacheDeadline = Date.now() + 35_000;
  let complete = false;
  while (!complete && Date.now() < cacheDeadline) {
    await page.getByLabel('Search terms').fill(query);
    await page.getByRole('button', { name: 'Search' }).click();
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
  await page.goto('/');
  await page.getByRole('searchbox', { name: 'Search terms' }).fill(unmatched);
  await page.getByRole('button', { name: 'Search' }).click();

  await expect(page.getByRole('heading', { name: `Search for ${unmatched}` })).toBeVisible();
  const status = page.getByRole('status').first();
  await expect(status).toContainText(/result/);
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByText(/local search deadline exceeded|search incomplete/i)).toHaveCount(0);
  if (await status.innerText().then((text) => /partial search/i.test(text))) {
    await expect(page.getByText('Nothing found.')).toHaveCount(0);
  } else {
    await expect(page.getByText('Nothing found.', { exact: true })).toBeVisible();
  }
  await page.screenshot({ path: testInfo.outputPath('empty-search.png'), fullPage: true });
});
