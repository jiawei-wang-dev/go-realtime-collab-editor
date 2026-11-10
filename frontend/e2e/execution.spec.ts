import { expect, test, type Page } from '@playwright/test'

async function register(page: Page, username: string): Promise<void> {
  await page.goto('/auth')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill('CorrectHorseBattery1!')
  await page.locator('form').getByRole('button', { name: 'Create account', exact: true }).click()
  await expect(page).toHaveURL(/\/$/)
}

async function replaceEditor(page: Page, source: string): Promise<void> {
  const input = page.locator('.monaco-editor textarea').first()
  await expect(input).toBeAttached({ timeout: 30_000 })
  await input.click({ force: true })
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.insertText(source)
  await expect(page.locator('.editor-statusbar')).toContainText('0 pending')
}

test('runs the live Monaco Go source and renders stdout, errors, and metadata', async ({ page }) => {
  const suffix = Date.now().toString(36)
  const documentID = `phase109-browser-${suffix}.go`
  await register(page, `run-${suffix}`)
  await page.getByRole('button', { name: /New document/i }).click()
  await page.getByPlaceholder('project/main.go').fill(documentID)
  await page.getByRole('button', { name: 'Create and open' }).click()
  await expect(page.locator('.connection-connected')).toBeVisible({ timeout: 30_000 })

  await replaceEditor(page, 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("Hello from Monaco")\n}\n')
  const versionBeforeRun = await page.locator('.file-panel-meta code').textContent()
  await page.getByRole('button', { name: '▶ Run' }).click()
  await expect(page.getByRole('region', { name: 'Program output' })).toContainText('Running…')
  await expect(page.locator('.run-stdout')).toContainText('Hello from Monaco', { timeout: 25_000 })
  await expect(page.getByRole('region', { name: 'Program output' })).toContainText('Exited 0')
  await expect(page.locator('.file-panel-meta code')).toHaveText(versionBeforeRun ?? '')

  await replaceEditor(page, 'package main\nfunc main() { fmt.Println("missing import") }\n')
  await page.getByRole('button', { name: '▶ Run' }).click()
  await expect(page.locator('.run-stderr')).toContainText('undefined: fmt', { timeout: 25_000 })
  await expect(page.getByRole('region', { name: 'Program output' })).toContainText(/Exited [1-9]/)

  await page.getByRole('button', { name: 'Clear output' }).click()
  await expect(page.getByRole('region', { name: 'Program output' })).toHaveCount(0)
})
