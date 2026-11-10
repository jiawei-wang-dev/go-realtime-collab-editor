import { expect, test, type BrowserContext, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import path from 'node:path'

const screenshotDirectory = path.resolve('../docs/frontend/screenshots/phase-10.4')

interface BrowserDiagnostics {
  consoleErrors: string[]
  pageErrors: string[]
  unexpectedRequestFailures: string[]
  offline: boolean
}

function collectDiagnostics(page: Page): BrowserDiagnostics {
  const diagnostics: BrowserDiagnostics = { consoleErrors: [], pageErrors: [], unexpectedRequestFailures: [], offline: false }
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    const location = message.location().url
    const expectedAnonymousProbe = location.endsWith('/api/browser/auth/refresh') && message.text().includes('401')
    if (!expectedAnonymousProbe) diagnostics.consoleErrors.push(`${message.text()} (${location})`)
  })
  page.on('pageerror', (error) => diagnostics.pageErrors.push(error.message))
  page.on('requestfailed', (request) => {
    if (!diagnostics.offline) diagnostics.unexpectedRequestFailures.push(`${request.method()} ${request.url()}: ${request.failure()?.errorText}`)
  })
  return diagnostics
}

async function register(page: Page, username: string): Promise<void> {
  await page.goto('/auth')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill('CorrectHorseBattery1!')
  await page.locator('form').getByRole('button', { name: 'Create account', exact: true }).click()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: new RegExp(username) })).toBeVisible()
}

async function focusEditor(page: Page): Promise<void> {
  const input = page.locator('.monaco-editor textarea').first()
  await expect(input).toBeAttached()
  await input.click({ force: true })
}

async function replaceEditor(page: Page, content: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.insertText(content)
}

async function insertAtStart(page: Page, content: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+Home')
  await page.keyboard.insertText(content)
}

async function insertAtEnd(page: Page, content: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText(content)
}

async function serverDocument(page: Page, documentId: string): Promise<{ content: string; version: number }> {
  return page.evaluate(async (id) => {
    const refresh = await fetch('/api/browser/auth/refresh', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ request_id: `e2e-inspect-${crypto.randomUUID()}` }),
    })
    if (!refresh.ok) throw new Error(`refresh failed with ${refresh.status}`)
    const token = await refresh.json() as { access_token: string }
    const response = await fetch(`/api/documents/${encodeURIComponent(id)}`, {
      headers: { Authorization: `Bearer ${token.access_token}` },
    })
    if (!response.ok) throw new Error(`document read failed with ${response.status}`)
    return response.json() as Promise<{ content: string; version: number }>
  }, documentId)
}

async function pendingRecords(page: Page, userId: string, documentId: string): Promise<Array<{ requestId: string; operation: unknown }>> {
  return page.evaluate(async ({ user, document }) => new Promise((resolve, reject) => {
    const open = indexedDB.open('collab-editor', 2)
    open.onerror = () => reject(open.error)
    open.onsuccess = () => {
      const database = open.result
      const transaction = database.transaction('pending-edits', 'readonly')
      const request = transaction.objectStore('pending-edits').index('user-document').getAll([user, document])
      request.onerror = () => reject(request.error)
      request.onsuccess = () => {
        resolve(request.result as Array<{ requestId: string; operation: unknown }>)
        database.close()
      }
    }
  }), { user: userId, document: documentId })
}

async function userId(page: Page): Promise<string> {
  return page.locator('.settings-profile p').textContent().then((value) => value?.trim() ?? '')
}

async function screenshot(page: Page, name: string, width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height })
  await page.screenshot({ path: path.join(screenshotDirectory, name), fullPage: true })
}

async function closeContext(context: BrowserContext): Promise<void> {
  await context.close().catch(() => undefined)
}

test('two real browser contexts collaborate, recover pending edits, refresh, and enforce permission changes', async ({ browser }) => {
  await mkdir(screenshotDirectory, { recursive: true })
  const suffix = Date.now().toString(36)
  const usernameA = `e2e-a-${suffix}`
  const usernameB = `e2e-b-${suffix}`
  const documentId = `phase104-${suffix}.go`
  const contextA = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const contextB = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const pageA = await contextA.newPage()
  const pageB = await contextB.newPage()
  const diagnosticsA = collectDiagnostics(pageA)
  const diagnosticsB = collectDiagnostics(pageB)

  try {
    await pageA.goto('/auth')
    await screenshot(pageA, 'login-1440x900.png', 1440, 900)
    await pageA.getByRole('button', { name: 'Create account' }).click()
    await screenshot(pageA, 'register-390x844.png', 390, 844)

    await register(pageB, usernameB)
    await register(pageA, usernameA)
    await screenshot(pageA, 'dashboard-1920x1080.png', 1920, 1080)
    await screenshot(pageA, 'dashboard-768x1024.png', 768, 1024)

    await pageA.getByRole('button', { name: /New document/i }).click()
    await pageA.getByPlaceholder('project/main.go').fill(documentId)
    await pageA.getByRole('button', { name: 'Create and open' }).click()
    await expect(pageA.locator('.connection-connected')).toBeVisible()
    if (process.env.E2E_EXPECTED_TRANSPORT) {
      await expect(pageA.locator('.editor-statusbar')).toContainText(process.env.E2E_EXPECTED_TRANSPORT)
    }
    await replaceEditor(pageA, 'package main\n')
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')

    await pageA.getByRole('button', { name: 'Share' }).click()
    const shareDialog = pageA.getByRole('dialog')
    await expect(shareDialog).toBeVisible()
    await screenshot(pageA, 'permission-dialog-1280x720.png', 1280, 720)
    await shareDialog.getByPlaceholder('username').fill(usernameB)
    await shareDialog.getByRole('button', { name: 'Share', exact: true }).click()
    await expect(shareDialog).toContainText(usernameB)
    await shareDialog.getByRole('button', { name: 'Done' }).click()

    await pageB.reload()
    await expect(pageB.getByText(documentId, { exact: true })).toBeVisible()
    await pageB.getByText(documentId, { exact: true }).click()
    await expect(pageB.locator('.connection-connected')).toBeVisible()
    await expect(pageA.locator('.participant')).toHaveCount(2)
    await expect(pageB.locator('.participant')).toHaveCount(2)
    await expect(pageB.locator('.view-lines')).toContainText('package main')
    await screenshot(pageA, 'editor-1440x900.png', 1440, 900)
    await screenshot(pageA, 'editor-390x844.png', 390, 844)
    await pageA.setViewportSize({ width: 1440, height: 900 })

    // Cursor and selection presence is ephemeral: it must not change content or version.
    const beforeCursorVersion = await pageA.locator('.file-panel-meta code').textContent()
    await focusEditor(pageA)
    await pageA.keyboard.press('ControlOrMeta+Home')
    await pageA.keyboard.down('Shift')
    for (let index = 0; index < 7; index += 1) await pageA.keyboard.press('ArrowRight')
    await pageA.keyboard.up('Shift')
    await expect(pageB.locator('.remote-cursor-label')).toContainText(usernameA)
    await expect(pageB.locator('.remote-selection')).toHaveCount(1)
    await focusEditor(pageB)
    await pageB.keyboard.press('ControlOrMeta+End')
    await expect(pageA.locator('.remote-cursor-label')).toContainText(usernameB)
    await expect(pageA.locator('.file-panel-meta code')).toHaveText(beforeCursorVersion ?? '')

    // Leaving the document removes its cursor; reopening publishes a fresh one.
    await pageA.goto('/')
    await expect(pageB.locator('.remote-cursor-label')).toHaveCount(0)
    await pageA.getByText(documentId, { exact: true }).click()
    await expect(pageA.locator('.connection-connected')).toBeVisible()
    await focusEditor(pageA)
    await pageA.keyboard.press('ControlOrMeta+Home')
    await expect(pageB.locator('.remote-cursor-label')).toContainText(usernameA)

    // Same-position insert, different-position insert, and interleaved insert/delete.
    await Promise.all([insertAtStart(pageA, 'A'), insertAtStart(pageB, 'B')])
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
    await Promise.all([insertAtStart(pageA, '<'), insertAtEnd(pageB, '>')])
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
    await focusEditor(pageA)
    await pageA.keyboard.press('ControlOrMeta+Home')
    await Promise.all([pageA.keyboard.press('Delete'), insertAtEnd(pageB, '!')])
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
    await Promise.all([insertAtEnd(pageA, ' rapid-A'), insertAtEnd(pageB, ' rapid-B')])
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')

    const beforeOffline = await serverDocument(pageB, documentId)
    const accountPage = await contextA.newPage()
    await accountPage.goto('/settings')
    const ownerUserId = await userId(accountPage)
    await accountPage.close()

    // Monaco loads its icon font lazily. Finish loading static resources before
    // the test intentionally removes network access so only recovery traffic is
    // evaluated during the offline phase.
    await pageA.evaluate(async () => {
      await document.fonts.load('16px codicon')
      await document.fonts.ready
    })
    diagnosticsA.offline = true
    await contextA.setOffline(true)
    await expect(pageA.locator('.connection-reconnecting')).toBeVisible()
    await insertAtEnd(pageA, ' offline-A')
    await expect(pageA.locator('.editor-statusbar')).not.toContainText('0 pending')
    const offlinePending = await pendingRecords(pageA, ownerUserId, documentId)
    expect(offlinePending).toHaveLength(1)
    const originalRequestId = offlinePending[0].requestId

    await insertAtEnd(pageB, ' remote-B')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
    diagnosticsA.offline = false
    await contextA.setOffline(false)
    await expect(pageA.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    expect(await pendingRecords(pageA, ownerUserId, documentId)).toEqual([])
    const afterRecovery = await serverDocument(pageB, documentId)
    expect(afterRecovery.version).toBeGreaterThan(beforeOffline.version)
    expect(afterRecovery.content).toContain('offline-A')
    expect(afterRecovery.content).toContain('remote-B')
    expect(originalRequestId).toMatch(/^edit-/)
    await expect(pageA.locator('.view-lines')).toContainText('offline-A')
    await expect(pageB.locator('.view-lines')).toContainText('offline-A')

    // Refresh proves cookie bootstrap restores the in-memory access token.
    await pageA.reload()
    await expect(pageA.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })
    expect(await pageA.evaluate(() => Object.keys(localStorage))).toEqual([])
    expect(await pageA.evaluate(() => Object.keys(sessionStorage))).toEqual([])

    // Downgrade the writer and verify the live editor becomes read-only.
    await pageA.getByRole('button', { name: 'Share' }).click()
    await shareDialog.getByPlaceholder('username').fill(usernameB)
    await shareDialog.locator('.share-form select').selectOption('read')
    await shareDialog.getByRole('button', { name: 'Share', exact: true }).click()
    await expect(pageB.locator('.collaboration-info')).toContainText('read')
    const beforeReadOnlyAttempt = await serverDocument(pageA, documentId)
    await insertAtEnd(pageB, ' should-not-apply')
    const afterReadOnlyAttempt = await serverDocument(pageA, documentId)
    expect(afterReadOnlyAttempt).toEqual(beforeReadOnlyAttempt)
    await shareDialog.getByRole('button', { name: 'Done' }).click()

    // A second tab logs out the same session; BroadcastChannel clears the editor tab.
    const logoutPage = await contextA.newPage()
    await logoutPage.goto('/settings')
    await logoutPage.getByRole('button', { name: /Sign out of this session/i }).click()
    await expect(pageA).toHaveURL(/\/auth$/)
    await expect(logoutPage).toHaveURL(/\/auth$/)
    await expect(pageB.locator('.remote-cursor-label')).toHaveCount(0)

    expect(diagnosticsA.pageErrors).toEqual([])
    expect(diagnosticsB.pageErrors).toEqual([])
    expect(diagnosticsA.consoleErrors).toEqual([])
    expect(diagnosticsB.consoleErrors).toEqual([])
    expect(diagnosticsA.unexpectedRequestFailures).toEqual([])
    expect(diagnosticsB.unexpectedRequestFailures).toEqual([])
  } finally {
    if (test.info().status !== test.info().expectedStatus) {
      await test.info().attach('browser-diagnostics.json', {
        body: JSON.stringify({ diagnosticsA, diagnosticsB }, null, 2),
        contentType: 'application/json',
      })
    }
    await closeContext(contextA)
    await closeContext(contextB)
  }
})
