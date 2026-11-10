import { expect, test, type BrowserContext, type Page } from '@playwright/test'
import { execFileSync, spawn } from 'node:child_process'
import { appendFileSync, closeSync, openSync } from 'node:fs'

const requiredEnvironment = [
  'E2E_NODE_A_URL',
  'E2E_NODE_B_URL',
  'E2E_NODE_A_PID',
  'E2E_NODE_B_PID',
  'E2E_NODE_A_RESTART_ENV',
  'E2E_NODE_B_RESTART_ENV',
  'E2E_SERVER_BINARY',
  'E2E_PROJECT_ROOT',
  'E2E_ETCD_CONTAINER',
  'E2E_RESTART_PID_FILE',
] as const

interface OwnerRecord {
  node_id: string
  incarnation: string
  state: string
  fencingToken: number
}

function environment(name: typeof requiredEnvironment[number]): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required for distributed WebTransport acceptance.`)
  return value
}

function etcdRecords(prefix: string): Array<{ key: string; value: string; createRevision: number }> {
  const output = execFileSync('docker', [
    'exec', environment('E2E_ETCD_CONTAINER'),
    'etcdctl',
    '--endpoints=https://127.0.0.1:2379',
    '--cacert=/certs/ca.pem',
    '--cert=/certs/client.pem',
    '--key=/certs/client-key.pem',
    'get', prefix, '--prefix', '--write-out=json',
  ], { encoding: 'utf8' })
  const response = JSON.parse(output) as {
    kvs?: Array<{ key: string; value: string; create_revision: string }>
  }
  return (response.kvs ?? []).map((item) => ({
    key: Buffer.from(item.key, 'base64').toString('utf8'),
    value: Buffer.from(item.value, 'base64').toString('utf8'),
    createRevision: Number(item.create_revision),
  }))
}

function ownerKeyDocument(key: string): string {
  const encoded = key.slice('/collab/v1/owners/'.length)
  return Buffer.from(encoded.replaceAll('-', '+').replaceAll('_', '/'), 'base64').toString('utf8')
}

function ownerFor(documentId: string): OwnerRecord | undefined {
  const entry = etcdRecords('/collab/v1/owners/').find((item) => ownerKeyDocument(item.key) === documentId)
  if (!entry) return undefined
  const owner = JSON.parse(entry.value) as Omit<OwnerRecord, 'fencingToken'>
  return { ...owner, fencingToken: entry.createRevision }
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

async function insertAtEnd(page: Page, text: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText(text)
}

async function insertAtStart(page: Page, text: string): Promise<void> {
  await focusEditor(page)
  await page.keyboard.press('ControlOrMeta+Home')
  await page.keyboard.insertText(text)
}

async function installWebTransportDiagnostics(context: BrowserContext): Promise<void> {
  await context.addInitScript(() => {
    const original = globalThis.WebTransport
    const diagnostics: string[] = []
    Object.assign(globalThis, { __phase111WebTransportDiagnostics: diagnostics })
    if (!original) {
      diagnostics.push('WebTransport API is unavailable')
      return
    }
    globalThis.WebTransport = new Proxy(original, {
      construct(target, argumentsList, newTarget) {
        const url = String(argumentsList[0])
        const options = argumentsList[1] as WebTransportOptions | undefined
        const digest = options?.serverCertificateHashes?.[0]?.value
        diagnostics.push(`connect ${url} digestBytes=${digest?.byteLength ?? 0}`)
        const session = Reflect.construct(target, argumentsList, newTarget) as WebTransport
        void session.ready.then(
          () => diagnostics.push(`ready ${url}`),
          (error: unknown) => diagnostics.push(`failed ${url}: ${error instanceof Error ? error.message : String(error)}`),
        )
        return session
      },
    })
  })
}

async function waitForConnected(page: Page): Promise<void> {
  try {
    await expect(page.locator('.connection-connected')).toBeVisible()
  } catch (error) {
    const diagnostics = await page.evaluate(() =>
      (window as unknown as { __phase111WebTransportDiagnostics?: string[] }).__phase111WebTransportDiagnostics ?? [],
    )
    console.log(`WEBTRANSPORT_DIAGNOSTICS=${JSON.stringify(diagnostics)}`)
    throw error
  }
}

async function documentFrom(page: Page, documentId: string): Promise<{ content: string; version: number }> {
  return page.evaluate(async (id) => {
    const refresh = await fetch('/api/browser/auth/refresh', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ request_id: `distributed-inspect-${crypto.randomUUID()}` }),
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

function restartNode(nodeID: string): number {
  const envFile = nodeID === 'node-a' ? environment('E2E_NODE_A_RESTART_ENV') : environment('E2E_NODE_B_RESTART_ENV')
  const logFile = `${envFile}.log`
  const log = openSync(logFile, 'a', 0o600)
  const child = spawn('/bin/bash', ['--noprofile', '--norc', '-c', 'set -a; source "$1"; set +a; exec "$2"', '_', envFile, environment('E2E_SERVER_BINARY')], {
    cwd: environment('E2E_PROJECT_ROOT'),
    detached: true,
    stdio: ['ignore', log, log],
  })
  closeSync(log)
  child.unref()
  if (!child.pid) throw new Error(`failed to restart ${nodeID}`)
  appendFileSync(environment('E2E_RESTART_PID_FILE'), `${child.pid}\n${logFile}\n`, { mode: 0o600 })
  return child.pid
}

test.describe('distributed browser WebTransport', () => {
  test.skip(requiredEnvironment.some((name) => !process.env[name]), 'isolated two-node environment is not configured')

  test('redirects to the owner and resumes after owner failover', async ({ browser }) => {
    const baseA = environment('E2E_NODE_A_URL')
    const baseB = environment('E2E_NODE_B_URL')
    const suffix = Date.now().toString(36)
    const usernameA = `wt-a-${suffix}`
    const usernameB = `wt-b-${suffix}`
    const documentId = `phase111-${suffix}.go`
    const contextA = await browser.newContext()
    const contextB = await browser.newContext()
    await installWebTransportDiagnostics(contextA)
    await installWebTransportDiagnostics(contextB)
    const pageA = await contextA.newPage()
    const pageB = await contextB.newPage()

    try {
      await register(pageA, baseA, usernameA)
      await register(pageB, baseB, usernameB)

      await pageA.getByRole('button', { name: /New document/i }).click()
      await pageA.getByPlaceholder('project/main.go').fill(documentId)
      await pageA.getByRole('button', { name: 'Create and open' }).click()
      await waitForConnected(pageA)
      await expect(pageA.locator('.editor-statusbar')).toContainText('webtransport')
      await insertAtEnd(pageA, 'package main\n')
      await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')

      await pageA.getByRole('button', { name: 'Share' }).click()
      const share = pageA.getByRole('dialog')
      await share.getByPlaceholder('username').fill(usernameB)
      await share.getByRole('button', { name: 'Share', exact: true }).click()
      await expect(share).toContainText(usernameB)
      await share.getByRole('button', { name: 'Done' }).click()

      await pageB.reload()
      await expect(pageB.getByText(documentId, { exact: true })).toBeVisible()
      await pageB.getByText(documentId, { exact: true }).click()
      await waitForConnected(pageB)
      await expect(pageB.locator('.editor-statusbar')).toContainText('webtransport')
      await expect(pageA.locator('.participant')).toHaveCount(2)
      await expect(pageB.locator('.participant')).toHaveCount(2)

      const initialOwner = await expect.poll(() => ownerFor(documentId), { timeout: 20_000 }).toMatchObject({ state: 'ACTIVE' }).then(() => ownerFor(documentId))
      expect(initialOwner).toBeDefined()
      expect(['node-a', 'node-b']).toContain(initialOwner!.node_id)

      await Promise.all([insertAtStart(pageA, 'A'), insertAtStart(pageB, 'B')])
      await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
      await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
      const beforeFailover = await documentFrom(initialOwner!.node_id === 'node-a' ? pageB : pageA, documentId)

      await focusEditor(pageA)
      await pageA.keyboard.press('ControlOrMeta+Home')
      await expect(pageB.locator('.remote-cursor-label')).toContainText(usernameA)

      await pageA.evaluate(() => {
        const target = document.querySelector('.editor-statusbar')
        const history: string[] = [target?.textContent ?? '']
        Object.assign(window, { __phase111ConnectionHistory: history })
        new MutationObserver(() => history.push(target?.textContent ?? '')).observe(target!, { subtree: true, childList: true, characterData: true })
      })

      const ownerPID = Number(environment(initialOwner!.node_id === 'node-a' ? 'E2E_NODE_A_PID' : 'E2E_NODE_B_PID'))
      process.kill(ownerPID, 'SIGKILL')
      await expect.poll(() => pageA.evaluate(() => {
        const history = (window as unknown as { __phase111ConnectionHistory?: string[] }).__phase111ConnectionHistory ?? []
        return history.some((entry) => entry.includes('reconnecting'))
      }), { timeout: 15_000 }).toBe(true)
      await insertAtEnd(pageA, ' pending-after-crash')
      await expect(pageA.locator('.editor-statusbar')).not.toContainText('0 pending')

      await expect.poll(() => {
        const owner = ownerFor(documentId)
        return owner?.state === 'ACTIVE'
          && owner.node_id !== initialOwner!.node_id
          && owner.fencingToken > initialOwner!.fencingToken
      }, { timeout: 30_000 }).toBe(true)
      const recoveredOwner = ownerFor(documentId)
      expect(recoveredOwner).toBeDefined()
      expect(recoveredOwner!.node_id).not.toBe(initialOwner!.node_id)
      expect(recoveredOwner!.fencingToken).toBeGreaterThan(initialOwner!.fencingToken)
      await expect(pageA.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })
      await expect(pageB.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })
      await expect(pageA.locator('.editor-statusbar')).toContainText('0 pending')
      await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')

      const recoveredDocument = await expect.poll(
        () => documentFrom(recoveredOwner!.node_id === 'node-a' ? pageA : pageB, documentId),
        { timeout: 20_000 },
      ).toMatchObject({ content: expect.stringContaining('pending-after-crash') }).then(
        () => documentFrom(recoveredOwner!.node_id === 'node-a' ? pageA : pageB, documentId),
      )
      expect(recoveredDocument.version).toBeGreaterThan(beforeFailover.version)

      await insertAtEnd(pageB, ' after-failover')
      await expect(pageB.locator('.editor-statusbar')).toContainText('0 pending')
      await expect(pageA.locator('.view-lines')).toContainText('after-failover')

      const restartedPID = restartNode(initialOwner!.node_id)
      expect(restartedPID).toBeGreaterThan(0)
      await expect.poll(() => etcdRecords('/collab/v1/nodes/').length, { timeout: 20_000 }).toBe(2)
      await expect.poll(() => ownerFor(documentId), { timeout: 10_000 }).toMatchObject({
        node_id: recoveredOwner!.node_id,
        incarnation: recoveredOwner!.incarnation,
        fencingToken: recoveredOwner!.fencingToken,
        state: 'ACTIVE',
      })

      await focusEditor(pageB)
      await pageB.keyboard.press('ControlOrMeta+End')
      await expect(pageA.locator('.remote-cursor-label')).toContainText(usernameB)
    } finally {
      await contextA.close().catch(() => undefined)
      await contextB.close().catch(() => undefined)
    }
  })
})
