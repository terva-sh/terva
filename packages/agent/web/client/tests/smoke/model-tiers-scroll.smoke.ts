import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The sub-agent tiers panel is the second form that renders as the whole body
// of a .modal, so it had the same clip as the model settings form: .modal caps
// at 82vh and hides its overflow, and nothing inside scrolled. Measured before
// the fix, the Done button's bottom edge sat 15px past the modal at 900x620 and
// 114px past it at 900x500, with the panel scrolling 0px.
//
// The ladder is four rungs (weak, medium, strong, cheap, from swarmTierNames in
// packages/agent/tools/swarm_tier.go), so this is the panel the daemon actually
// sends, not a worst case built to fail.
//
// 900x500 rather than 620 on purpose. At 620 the clipped button is still inside
// the VIEWPORT rectangle, so `toBeInViewport` passes while the button is cut off
// by the modal's overflow:hidden. The assertion that catches this at any height
// is the button's box against the MODAL's box, which is what this test makes.

const MODELS = Array.from({ length: 12 }, (_, i) => ({
  id: `model-${i}`,
  provider: 'anthropic',
  context_window: 200000,
  current: i === 0,
}))

const TIERS = {
  provider: 'anthropic',
  rungs: [
    { rung: 'weak', model: 'model-1', label: 'model-1', source: 'built-in' },
    { rung: 'medium', model: 'model-3', pinned: 'model-3', reasoning: 'medium', source: 'override' },
    { rung: 'strong', model: 'model-5', pinned: 'model-5', source: 'override' },
    { rung: 'cheap', model: '', source: '' },
  ],
}

test('the sub-agent tiers panel scrolls and keeps Done inside the modal', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 500 })

  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'models.list') return { models: MODELS }
      if (method === 'models.tiers') return TIERS
      return undefined
    },
  })
  await page.goto(panelSessionURL)

  await page.locator('button.model-btn').click()
  await page.locator('button[title="Sub-agent tiers"]').first().click()

  const modal = page.locator('.modal')
  const form = page.locator('.modal > .prov-flow')
  await expect(form).toBeVisible()

  // Guard the fixture: all four rungs have to be on the page before any claim
  // about what sits below them means anything.
  await expect(form.locator('.prov-flow-title')).toContainText('anthropic')
  await expect(form.locator('.tier-row')).toHaveCount(4)

  // Guard that the arm binds: the panel must be taller than the modal here, or
  // this is a test about a form that fits.
  const formHeight = await form.evaluate((el) => el.scrollHeight)
  const modalHeight = await modal.evaluate((el) => el.clientHeight)
  expect(formHeight).toBeGreaterThan(modalHeight + 40)

  // The point: Done is inside the modal's visible box, not below its clipped
  // edge, and pressing it closes the panel.
  const done = form.locator('.prov-actions .btn')
  const doneBox = await done.boundingBox()
  const modalBox = await modal.boundingBox()
  expect(doneBox).not.toBeNull()
  expect(modalBox).not.toBeNull()
  expect(doneBox!.y + doneBox!.height).toBeLessThanOrEqual(modalBox!.y + modalBox!.height + 1)

  await done.click({ timeout: 3000 })
  await expect(form).toHaveCount(0)
})
