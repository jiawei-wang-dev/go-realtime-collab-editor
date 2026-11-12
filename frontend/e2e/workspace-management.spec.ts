import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

async function signIn(page: Page, username: string, password: string) {
  await page.goto('/auth')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.locator('form').getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Workspace', exact: true })).toBeVisible()
}

async function createAccount(request: APIRequestContext, username: string, password: string) {
  const data = { username, password }
  const registration = await request.post('/api/auth/register', { data })
  expect(registration.ok()).toBeTruthy()
  const user = await registration.json()
  const login = await request.post('/api/auth/login', { data })
  expect(login.ok()).toBeTruthy()
  const tokens = await login.json()
  return { id: user.id as string, headers: { Authorization: `Bearer ${tokens.access_token}` } }
}

test('switch files and enforce folder access for two browser sessions', async ({ browser, request, baseURL }) => {
  const suffix = Date.now().toString(36)
  const ownerName = `workspace-owner-${suffix}`
  const peerName = `workspace-peer-${suffix}`
  const password = 'Workspace-preview-135!'
  const owner = await createAccount(request, ownerName, password)
  const peer = await createAccount(request, peerName, password)
  const options = { headers: owner.headers }
  const folderResponse = await request.post('/api/folders', { ...options, data: { name: 'Shared project' } })
  expect(folderResponse.ok()).toBeTruthy()
  const folder = await folderResponse.json()
  const childResponse = await request.post('/api/folders', { ...options, data: { name: 'Source', parent_id: folder.id } })
  expect(childResponse.ok()).toBeTruthy()
  const child = await childResponse.json()
  const targetResponse = await request.post('/api/folders', { ...options, data: { name: 'Destination' } })
  expect(targetResponse.ok()).toBeTruthy()
  const target = await targetResponse.json()
  const sharePath = `/api/folders/${folder.id}/permissions/${peer.id}`
  expect((await request.put(sharePath, { ...options, data: { role: 'write' } })).ok()).toBeTruthy()

  const firstResponse = await request.post('/api/documents', { ...options, data: { id: 'first.go', folder_id: child.id } })
  expect(firstResponse.ok()).toBeTruthy()
  const first = await firstResponse.json()
  const secondResponse = await request.post('/api/documents', { ...options, data: { id: 'second.go', folder_id: child.id, content: 'package second' } })
  expect(secondResponse.ok()).toBeTruthy()
  const second = await secondResponse.json()
  const fileURL = `/documents/${encodeURIComponent(first.id)}`

  const ownerPage = await browser.newPage({ baseURL })
  const peerPage = await browser.newPage({ baseURL })
  try {
    await signIn(ownerPage, ownerName, password)
    await signIn(peerPage, peerName, password)
    await ownerPage.goto(fileURL)
    await peerPage.goto(fileURL)
    const editor = ownerPage.locator('.monaco-editor textarea').first()
    await expect(editor).toBeAttached()
    await expect(ownerPage.locator('.editor-statusbar')).toContainText('connected')
    await expect(peerPage.locator('.editor-statusbar')).toContainText('connected')
    const tree = ownerPage.getByRole('navigation', { name: 'Editor files' })
    await ownerPage.evaluate(() => window.dispatchEvent(new Event('offline')))
    await editor.click({ force: true })
    await ownerPage.keyboard.insertText('package first')
    await expect(ownerPage.locator('.editor-statusbar')).toContainText('1 pending')
    await tree.getByRole('button', { name: 'second.go', exact: true }).click()
    await expect(ownerPage.locator('.monaco-editor .view-lines')).toContainText(/package\s+second/)
    await tree.getByRole('button', { name: 'first.go', exact: true }).click()
    await expect(ownerPage.locator('.monaco-editor .view-lines')).toContainText(/package\s+first/)
    await expect(ownerPage.locator('.editor-statusbar')).toContainText('0 pending')

    await ownerPage.keyboard.press('ControlOrMeta+p')
    const quickOpen = ownerPage.getByRole('dialog', { name: 'Quick Open' })
    await quickOpen.getByRole('textbox').fill('Source/second.go')
    await expect(quickOpen.getByRole('button', { name: 'Shared project/Source/second.go' })).toBeVisible()
    await quickOpen.getByRole('textbox').press('Enter')
    await expect(ownerPage.locator('.monaco-editor .view-lines')).toContainText(/package\s+second/)
    await tree.getByRole('button', { name: 'first.go', exact: true }).click()
    await expect(ownerPage.locator('.editor-statusbar')).toContainText('connected')
    await ownerPage.getByRole('button', { name: 'History', exact: true }).click()
    const history = ownerPage.getByRole('dialog', { name: 'Version history' })
    await history.getByRole('button', { name: 'Version 0', exact: true }).click()
    await expect(history.locator('section').first().locator('pre')).toBeEmpty()
    await expect(history.locator('section').last().locator('pre')).toHaveText('package first')
    await history.getByRole('button', { name: 'Done' }).click()

    const inherited = await request.get(`/api/folders/${child.id}/permissions`, options)
    expect(inherited.ok()).toBeTruthy()
    expect(await inherited.json()).toEqual(expect.arrayContaining([
      expect.objectContaining({ username: peerName, folder_name: 'Shared project', role: 'write' }),
    ]))
    expect((await request.put(sharePath, { ...options, data: { role: 'read' } })).ok()).toBeTruthy()
    await expect(peerPage.locator('.collaboration-info')).toContainText('read')
    await expect(peerPage.locator('.monaco-editor textarea').first()).not.toBeEditable()
    const directShare = `/api/documents/${encodeURIComponent(first.id)}/permissions/${peer.id}`
    expect((await request.put(directShare, { ...options, data: { role: 'write' } })).ok()).toBeTruthy()
    await expect(peerPage.locator('.collaboration-info')).toContainText('write')
    await ownerPage.getByRole('button', { name: 'Share', exact: true }).click()
    const access = ownerPage.getByRole('dialog', { name: 'Share first.go' })
    await expect(access.getByText('From Shared project')).toBeVisible()
    await access.getByRole('combobox', { name: `Access for ${peerName}` }).selectOption('revoke')
    await expect(peerPage.locator('.collaboration-info')).toContainText('read')
    await expect(access.getByRole('combobox', { name: `Access for ${peerName}` })).toHaveCount(0)
    await access.getByRole('button', { name: 'Done' }).click()
    expect((await request.delete(sharePath, options)).ok()).toBeTruthy()
    await expect(peerPage.getByText('You no longer have access to this file.')).toBeVisible()
    await peerPage.reload()
    await expect(peerPage.getByRole('alert')).toContainText(/read permission|access/i)
    expect((await request.put(sharePath, { ...options, data: { role: 'read' } })).ok()).toBeTruthy()

    const move = `/api/folders/${child.id}/parent`
    const preview = await request.get(move, { ...options, params: { parent_id: target.id } })
    expect(preview.ok()).toBeTruthy()
    expect(await preview.json()).toEqual(expect.arrayContaining([
      expect.objectContaining({ username: peerName, before: 'read', after: '' }),
    ]))
    expect((await request.put(`/api/folders/${folder.id}/parent`, { ...options, data: { parent_id: child.id } })).status()).toBe(409)
    expect((await request.delete(`/api/folders/${child.id}`, options)).status()).toBe(409)
    expect((await request.put(move, { ...options, data: { parent_id: target.id } })).ok()).toBeTruthy()
    const files = await request.get('/api/documents', { headers: peer.headers })
    expect((await files.json()).documents).not.toEqual(expect.arrayContaining([expect.objectContaining({ id: first.id })]))
  } finally {
    await ownerPage.close()
    await peerPage.close()
    for (const id of [first.id, second.id]) {
      await request.delete(`/api/documents/${encodeURIComponent(id)}`, options)
    }
    for (const id of [child.id, folder.id, target.id]) {
      await request.delete(`/api/folders/${id}`, options)
    }
  }
})
