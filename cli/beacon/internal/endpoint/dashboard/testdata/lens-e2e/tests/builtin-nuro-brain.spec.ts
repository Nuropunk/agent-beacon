import { expect, test } from '@playwright/test';

test('nuro brain: regions light from the trace, and an inferred region is dashed', async ({ page }) => {
  await page.goto('/session.html?id=gated&lens=nuro-brain');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();
  await expect(frame.locator('#root > p').first()).toContainText("Each region lights only from this trace's events");

  // One prompt, three commands and one approval Beacon inferred; the other nine regions stay quiet.
  await expect(frame.locator('.node:not(.quiet)')).toHaveCount(3);
  await expect(frame.locator('.node.quiet')).toHaveCount(9);
  await expect(frame.locator('.node.inferred')).toHaveCount(1);
  await expect(frame.locator('.raster .row')).toHaveCount(3);

  const regions = frame.locator('table').nth(0).locator('tr');
  await expect(regions).toHaveCount(4);
  await expect(regions.nth(1)).toHaveText(/Prompt\s*1\s*0\s*0/);
  await expect(regions.nth(2)).toHaveText(/Approvals\s*1\s*0\s*1/);
  await expect(regions.nth(3)).toHaveText(/Commands\s*3\s*3\s*0/);

  const pathways = frame.locator('table').nth(1).locator('tr');
  await expect(pathways).toHaveCount(4);
  await expect(pathways.nth(1)).toHaveText(/Approvals\s*Commands\s*1/);
});

test('nuro brain: a short trace draws one pathway', async ({ page }) => {
  await page.goto('/session.html?id=s1&lens=nuro-brain');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();
  await expect(frame.locator('.node:not(.quiet)')).toHaveCount(2);
  await expect(frame.locator('.edge')).toHaveCount(1);
});
