// Run after smoke-api.ps1. Uses a fresh browser profile and only the demo URL.
import { chromium } from '../../client/node_modules/playwright-core/index.mjs'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import assert from 'node:assert/strict'
const output = fileURLToPath(new URL('./screenshots/', import.meta.url))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe' })
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: 'zh-CN' })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('http://localhost:55173', { waitUntil: 'networkidle' })
  assert.match(await page.title(), /GameShelf/)
  assert.doesNotMatch(await page.locator('body').innerText(), /元发|元發|YanfaChatBI/i)
  await page.screenshot({ path: output + '01-login.png', fullPage: true })
  await page.locator('input[name="usernameOrEmail"]').fill('game-demo-presenter')
  await page.locator('input[name="password"]').fill('synthetic-coursework-only')
  await page.locator('button[type="submit"]').click()
  await page.locator('input[name="usernameOrEmail"]').waitFor({ state: 'hidden' })
  await page.getByText('6021 · 合成游戏网店 Demo', { exact: true }).first().click()
  const card = page.locator('.assistant-result-card').first()
  await card.waitFor({ state: 'visible', timeout: 20000 })
  await page.getByText('已连接', { exact: true }).first().waitFor({ state: 'visible', timeout: 20000 })
  await page.waitForTimeout(1800)
  await page.locator('[data-chat-container]').evaluate(container => {
    const report = container.querySelector('.assistant-result-card')
    container.style.scrollBehavior = 'auto'
    container.scrollTop += report.getBoundingClientRect().top - container.getBoundingClientRect().top - 24
  })
  await page.waitForTimeout(300)
  const text = await page.locator('body').innerText()
  assert.doesNotMatch(text, /元发|元發|YanfaChatBI/i)
  assert.match((await card.innerText()).replaceAll(',', ''), /12000/)
  assert.match((await card.innerText()).replaceAll(',', ''), /4800/)
  assert.equal(errors.length, 0, errors.join('\n'))
  await page.screenshot({ path: output + '02-report-page.png', fullPage: true })
  await card.locator('table').screenshot({ path: output + '03-metrics-table.png' })
  await page.getByRole('button', { name: '对话选项', exact: true }).first().click()
  await page.getByRole('button', { name: '编辑知识库说明', exact: true }).click()
  const knowledge = page.getByRole('dialog', { name: '知识库说明', exact: true })
  await knowledge.waitFor({ state: 'visible' })
  await page.waitForTimeout(700)
  assert.doesNotMatch(await knowledge.innerText(), /元发|元發|YanfaChatBI/i)
  await page.screenshot({ path: output + '04-knowledge.png', fullPage: true })
  await page.getByRole('button', { name: '关闭知识库', exact: true }).click()
  const previousCards = await page.locator('.assistant-result-card').count()
  await page.getByRole('button', { name: '工作流', exact: true }).click()
  await page.getByRole('menuitemradio', { name: '商品跨店分析', exact: true }).click()
  const createdResponse = page.waitForResponse(response => response.request().method() === 'POST' && /\/assistant\/runs$/.test(response.url()))
  await page.getByRole('button', { name: '开始分析', exact: true }).click()
  const created = await createdResponse
  assert.equal(created.status(), 202)
  await page.waitForFunction(count => document.querySelectorAll('.assistant-result-card').length > count, previousCards)
  const newReport = page.locator('.assistant-result-card').last()
  assert.match((await newReport.innerText()).replaceAll(',', ''), /12000/)
  assert.match((await newReport.innerText()).replaceAll(',', ''), /4800/)
  assert.doesNotMatch(await page.locator('body').innerText(), /元发|元發|YanfaChatBI/i)
  assert.equal(errors.length, 0, errors.join('\n'))
  await writeFile(output + 'ui-check.txt', `PASS: neutral login/title/body, stored PostgreSQL report, knowledge editor, new UI-created DB report, totals 12000/4800, no page errors.\n${text}`, 'utf8')
  console.log('PASS: GameShelf login -> stored DB report -> knowledge editor -> create a new real DB report in the UI; neutral visible brand and no page errors.')
} finally { await browser.close() }
