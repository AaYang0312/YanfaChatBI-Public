import { expect, test } from '@playwright/test'

test('browser smoke renders runner-ok', async ({ page }) => {
  await page.setContent('<!doctype html><title>runner-ok</title><p>runner-ok</p>')
  const title = await page.evaluate(() => document.title)
  expect(title).toBe('runner-ok')
})
