import { chromium, expect, firefox, test, webkit, type BrowserType, type Page } from '@playwright/test'

const baseURL = process.env.E2E_BASE_URL

test.skip(!baseURL, 'isolated browser test environment is required')

async function register(page: Page, username: string): Promise<void> {
  await page.goto('/auth')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill('CorrectHorseBattery1!')
  await page.locator('form').getByRole('button', { name: 'Create account', exact: true }).click()
  await expect(page).toHaveURL(/\/$/)
}

async function focusEditor(page: Page): Promise<void> {
  const editor = page.locator('.monaco-editor').first()
  await expect(editor).toBeVisible()
  await editor.click({ position: { x: 160, y: 32 } })
}

async function insertAtEnd(page: Page, text: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText(text)
  await expect(page.locator('.editor-statusbar')).toContainText('0 pending')
}

const combinations: Array<[string, BrowserType, string, BrowserType]> = [
  ['Chromium + Chromium', chromium, 'chromium', chromium],
  ['Chromium + Firefox', chromium, 'firefox', firefox],
  ['Chromium + WebKit', chromium, 'webkit', webkit],
  ['Firefox + WebKit', firefox, 'webkit', webkit],
]

for (const [name, firstType, secondName, secondType] of combinations) {
  test(`${name} collaborate, synchronize cursors, reconnect, refresh, and logout`, async () => {
    const firstBrowser = await firstType.launch({ headless: true })
    const secondBrowser = await secondType.launch({ headless: true })
    const firstContext = await firstBrowser.newContext({ baseURL })
    const secondContext = await secondBrowser.newContext({ baseURL })
    const first = await firstContext.newPage()
    const second = await secondContext.newPage()
    const suffix = `${secondName}-${Date.now().toString(36)}`
    const firstUsername = `cross-a-${suffix}`.slice(0, 32)
    const secondUsername = `cross-b-${suffix}`.slice(0, 32)
    const documentID = `cross-${suffix}.go`

    try {
      await register(first, firstUsername)
      await register(second, secondUsername)
      await first.getByRole('button', { name: /New document/i }).click()
      await first.getByPlaceholder('project/main.go').fill(documentID)
      await first.getByRole('button', { name: 'Create and open' }).click()
      await expect(first.locator('.connection-connected')).toBeVisible()
      await insertAtEnd(first, 'package main\n')

      await first.getByRole('button', { name: 'Share' }).click()
      const shareDialog = first.getByRole('dialog')
      await shareDialog.getByPlaceholder('username').fill(secondUsername)
      await shareDialog.getByRole('button', { name: 'Share', exact: true }).click()
      await expect(shareDialog).toContainText(secondUsername)
      await shareDialog.getByRole('button', { name: 'Done' }).click()

      await second.reload()
      await second.getByText(documentID, { exact: true }).click()
      await expect(second.locator('.connection-connected')).toBeVisible()
      await expect(first.locator('.participant')).toHaveCount(2)
      await expect(second.locator('.view-lines')).toContainText('package main')

      await focusEditor(first)
      await first.keyboard.press('ControlOrMeta+Home')
      await first.keyboard.down('Shift')
      await first.keyboard.press('ArrowRight')
      await first.keyboard.press('ArrowRight')
      await first.keyboard.up('Shift')
      await expect(second.locator('.remote-cursor-label')).toContainText(firstUsername)
      await expect.poll(() => second.locator('.remote-selection').count()).toBeGreaterThan(0)

      await insertAtEnd(second, ` // ${secondName}`)
      await expect(first.locator('.view-lines')).toContainText(secondName)
      await first.reload()
      await expect(first.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })
      await expect(first.locator('.view-lines')).toContainText(secondName)

      await firstContext.setOffline(true)
      await expect(first.locator('.connection-reconnecting')).toBeVisible()
      await firstContext.setOffline(false)
      await expect(first.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })

      const logoutPage = await firstContext.newPage()
      await logoutPage.goto('/settings')
      await logoutPage.getByRole('button', { name: /Sign out of this session/i }).click()
      await expect(first).toHaveURL(/\/auth$/)
      await expect(logoutPage).toHaveURL(/\/auth$/)
      await expect(second.locator('.remote-cursor-label')).toHaveCount(0)
    } finally {
      await firstContext.close().catch(() => undefined)
      await secondContext.close().catch(() => undefined)
      await firstBrowser.close().catch(() => undefined)
      await secondBrowser.close().catch(() => undefined)
    }
  })
}
