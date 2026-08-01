import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// A renamed model changes what the picker ROW looks like — name in the slot the
// id used to hold, id demoted into the meta line — and vitest/happy-dom can
// assert the text but not whether the result fits or reads. Hence a screenshot.
const LONG_ID = 'hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL'

const models = [
  {
    id: LONG_ID,
    provider: 'ollama',
    display_name: 'Qwen Coder',
    renamed: true,
    context_window: 262144,
    current: true,
  },
  // The control: the daemon sends a display name here too, and it must NOT
  // displace the id — catalog names are longer than the ids they'd replace.
  {
    id: 'claude-sonnet-4-5',
    provider: 'anthropic',
    display_name: 'Claude Sonnet 4.5 (latest)',
    context_window: 200000,
    auth: 'oauth',
  },
  { id: 'llama3.1:70b-instruct-q8_0', provider: 'ollama', context_window: 131072 },
]

test('a renamed model leads with its name and keeps its id', async ({ page }, testInfo) => {
  await installMockBackend(page, {
    respond: (method) => (method === 'models.list' ? { models } : undefined),
  })
  await page.goto(panelSessionURL)

  // The header button is the web's status bar: it must show the short name.
  const openBtn = page.locator('button.model-btn')
  await expect(openBtn).toContainText('Qwen Coder')
  await expect(openBtn).not.toContainText('hf.co')

  await openBtn.click()
  const renamedRow = page.locator('.pick-row', { hasText: 'Qwen Coder' })
  await expect(renamedRow.locator('.pick-id')).toHaveText('Qwen Coder')
  await expect(renamedRow.locator('.pick-meta')).toContainText(LONG_ID)

  const catalogRow = page.locator('.pick-row', { hasText: 'claude-sonnet-4-5' })
  await expect(catalogRow.locator('.pick-id')).toHaveText('claude-sonnet-4-5')

  // The row must not overflow its own container now that it carries both.
  const overflow = await renamedRow.evaluate((el) => el.scrollWidth - el.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)

  // …and it must not stay inside the row by CRUSHING the name instead, which
  // is what it did first: .pick-meta was flex:none, so the long id took the
  // width it wanted and the name rendered as one clipped glyph. Every text
  // assertion above still passed — the name was in the DOM, just invisible.
  const nameWidth = await renamedRow.locator('.pick-id').evaluate((el) => el.getBoundingClientRect().width)
  expect(nameWidth).toBeGreaterThan(60)

  await testInfo.attach('model-picker-renamed.png', {
    body: await page.locator('.modal').screenshot(),
    contentType: 'image/png',
  })
})
