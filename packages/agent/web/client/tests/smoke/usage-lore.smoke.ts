import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The Usage/context pane renders the lore activation trace (ContextBreakdown.
// lore_fired) — the panel's home for the 4c trace (the Stage drawer is the other).
const CTX = {
  window: 200000,
  system_bytes: 1200,
  ext_guidance_bytes: 0,
  tool_bytes: 0,
  tool_count: 0,
  ext_bytes: 340,
  transcript_bytes: 900,
  total_bytes: 2440,
  messages: [{ index: 0, kind: 'assistant', bytes: 900 }],
  cumulative: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 },
  lore_fired: [
    { name: 'The Pass', source: 'pass.md', keys: ['pass'] },
    { name: 'The Sea', source: 'sea.md', keys: ['sea'], dropped: true },
    { name: 'Guardian oath', source: 'oath.md', constant: true },
  ],
}

test('usage pane shows the lore activation trace', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'surfaces.list') return { surfaces: [{ id: 'context', title: 'Context', kind: 'context' }] }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'context')
        return { surface: { id: 'context', title: 'Context', kind: 'context', context: CTX } }
      return undefined
    },
  })
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()

  // Open the panes host and select the Context (usage) pane.
  await page.locator('.topbar button[title="Panes (usage, settings, extensions)"]').click()
  await page.locator('.pane-tab[title="Context"]').click()
  await expect(page.locator('.ctx-lore')).toBeVisible()

  // All three fired entries render; the matched key shows; the budget-dropped one
  // is flagged; the constant entry reads "always on".
  await expect(page.locator('.ctx-lore-row')).toHaveCount(3)
  await expect(page.locator('.ctx-lore-keys').first()).toHaveText('pass')
  await expect(page.locator('.ctx-lore-tag.dropped')).toBeVisible()
  await expect(page.locator('.ctx-lore-tag', { hasText: 'always on' })).toBeVisible()
  if (process.env.USAGE_SHOT) await page.screenshot({ path: `${process.env.USAGE_SHOT}.png`, fullPage: true })
})
