import { expect, test, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'

const baseA = process.env.E2E_BASE_URL_A
const baseB = process.env.E2E_BASE_URL_B
const mysqlContainer = process.env.E2E_MYSQL_CONTAINER
const mysqlPassword = process.env.E2E_MYSQL_PASSWORD
const nodeAPID = Number(process.env.E2E_NODE_A_PID)
const nodeBPID = Number(process.env.E2E_NODE_B_PID)

test.skip(!baseA || !baseB || !mysqlContainer || !mysqlPassword || !nodeAPID || !nodeBPID, 'isolated multi-node environment is required')

function queryMySQL(sql: string): string {
  return execFileSync('docker', [
    'exec', mysqlContainer!, 'mysql', '-ucollab', `-p${mysqlPassword}`, '-Nse', sql, 'collab_editor',
  ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim()
}

async function register(page: Page, baseURL: string, username: string): Promise<void> {
  await page.goto(`${baseURL}/auth`)
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill('CorrectHorseBattery1!')
  await page.locator('form').getByRole('button', { name: 'Create account', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`${baseURL.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/?$`))
}

async function focusEditor(page: Page): Promise<void> {
  const input = page.locator('.monaco-editor textarea').first()
  await expect(input).toBeAttached()
  await input.click({ force: true })
}

async function insertAtEnd(page: Page, content: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText(content)
}

test('two browser entry nodes redirect to one owner and recover after owner loss', async ({ browser }) => {
  const suffix = Date.now().toString(36)
  const documentID = `phase104-cluster-${suffix}.go`
  const usernameA = `cluster-a-${suffix}`
  const usernameB = `cluster-b-${suffix}`
  const contextA = await browser.newContext()
  const contextB = await browser.newContext()
  const pageA = await contextA.newPage()
  const pageB = await contextB.newPage()
  const socketsA: string[] = []
  const socketsB: string[] = []
  const errors: string[] = []
  pageA.on('websocket', (socket) => socketsA.push(socket.url()))
  pageB.on('websocket', (socket) => socketsB.push(socket.url()))
  for (const page of [pageA, pageB]) {
    page.on('pageerror', (error) => errors.push(error.message))
  }

  try {
    await register(pageA, baseA!, usernameA)
    await register(pageB, baseB!, usernameB)

    await pageA.getByRole('button', { name: /New document/i }).click()
    await pageA.getByPlaceholder('project/main.go').fill(documentID)
    await pageA.getByRole('button', { name: 'Create and open' }).click()
    await expect(pageA.locator('.connection-connected')).toBeVisible({ timeout: 45_000 })

    await pageA.getByRole('button', { name: 'Share' }).click()
    const dialog = pageA.getByRole('dialog')
    await dialog.getByPlaceholder('username').fill(usernameB)
    await dialog.getByRole('button', { name: 'Share', exact: true }).click()
    await expect(dialog).toContainText(usernameB)
    await dialog.getByRole('button', { name: 'Done' }).click()

    await pageB.reload()
    await expect(pageB.getByText(documentID, { exact: true })).toBeVisible()
    await pageB.getByText(documentID, { exact: true }).click()
    await expect(pageB.locator('.connection-connected')).toBeVisible({ timeout: 45_000 })
    await expect(pageA.locator('.participant')).toHaveCount(2)

    await insertAtEnd(pageA, 'seed')
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.view-lines')).toContainText('seed')
    const cursorVersion = queryMySQL(`SELECT version FROM documents WHERE id='${documentID}'`)
    const cursorOperations = queryMySQL(`SELECT COUNT(*) FROM operation_logs WHERE document_id='${documentID}'`)
    await focusEditor(pageA)
    await pageA.keyboard.press('ControlOrMeta+Home')
    await pageA.keyboard.press('ArrowRight')
    await expect(pageB.locator('.remote-cursor-label')).toContainText(usernameA)
    expect(queryMySQL(`SELECT version FROM documents WHERE id='${documentID}'`)).toBe(cursorVersion)
    expect(queryMySQL(`SELECT COUNT(*) FROM operation_logs WHERE document_id='${documentID}'`)).toBe(cursorOperations)

    await Promise.all([insertAtEnd(pageA, ' node-a'), insertAtEnd(pageB, ' node-b')])
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
    await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')

    let ownerNode = ''
    await expect.poll(() => {
      ownerNode = queryMySQL(`SELECT owner_node_id FROM documents WHERE id='${documentID}'`)
      return ownerNode
    }, { timeout: 30_000 }).toMatch(/^node-[ab]$/)
    const oldToken = Number(queryMySQL(`SELECT fencing_token FROM documents WHERE id='${documentID}'`))
    process.kill(ownerNode === 'node-a' ? nodeAPID : nodeBPID, 'SIGTERM')

    await expect(pageA.locator('.connection-connected')).toBeVisible({ timeout: 60_000 })
    await expect(pageB.locator('.connection-connected')).toBeVisible({ timeout: 60_000 })
    await focusEditor(pageB)
    await pageB.keyboard.press('ControlOrMeta+End')
    await expect(pageA.locator('.remote-cursor-label')).toContainText(usernameB)
    await insertAtEnd(pageA, ' after-failover')
    await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending', { timeout: 30_000 })
    await expect(pageB.locator('.view-lines')).toContainText('after-failover', { timeout: 30_000 })

    await expect.poll(() => Number(queryMySQL(`SELECT fencing_token FROM documents WHERE id='${documentID}'`)), { timeout: 30_000 })
      .toBeGreaterThan(oldToken)
    expect(queryMySQL(`SELECT COUNT(*) FROM operation_logs WHERE document_id='${documentID}'`))
      .toBe(queryMySQL(`SELECT version FROM documents WHERE id='${documentID}'`))
    expect(queryMySQL(`SELECT COUNT(*)-COUNT(DISTINCT request_id) FROM operation_logs WHERE document_id='${documentID}'`)).toBe('0')
    expect(socketsA.some((url) => url.includes('/api/realtime'))).toBe(true)
    expect(socketsB.some((url) => url.includes('/api/realtime'))).toBe(true)
    expect(errors).toEqual([])
  } finally {
    await contextA.close()
    await contextB.close()
  }
})
