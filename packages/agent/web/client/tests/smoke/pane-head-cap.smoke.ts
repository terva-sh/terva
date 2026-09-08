import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The pane head must not eat the pane.
//
// .pane-tabs wraps, and the head had no cap, so every extra surface added a row
// that came straight out of .pane-body: the body is the flex:1 item and
// min-height:0 lets it shrink to nothing. Measured at 900x620 against a 561px
// rail, the body was 92% of the rail at 1 tab, 47% at 9, 21% at 13, and 0% at
// 19 -- a pane rendering its surface at zero height. The head also spilled 98px
// past the rail, putting the last three tabs off screen with nothing able to
// scroll to them.
//
// Nineteen surfaces is a workspace with a handful of extensions, and the
// degradation is monotonic, so the milder counts are the common case: half the
// pane is already gone at nine.
//
// Both assertions below flip on the fix. The body height is the visible
// consequence; the click on the last tab is the reachability one, and it is
// there because scroll ROOM is not scrollability -- the head reports room in
// both states, and only one of them lets anyone move it.

const EXT_COUNT = 18

const CTX = {
  window: 200000,
  system_bytes: 12400,
  tool_bytes: 18900,
  tool_count: 22,
  transcript_bytes: 402000,
  total_bytes: 434400,
  messages: Array.from({ length: 30 }, (_, i) => ({ index: i, kind: 'assistant', bytes: 13400 })),
  context_tokens: 108000,
  cumulative: { input: 42000, output: 31000, cache_read: 1940000, cache_write: 96000, cost_usd: 3.42 },
}

const SURFACES = [
  { id: 'context', title: 'Context', kind: 'context' },
  ...Array.from({ length: EXT_COUNT }, (_, i) => ({
    id: `ext-${i}`,
    title: `Extension surface ${i}`,
    kind: 'panel',
  })),
]

test('the pane head stays bounded so the surface keeps its room', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 620 })

  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'surfaces.list') return { surfaces: SURFACES }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'context')
        return { surface: { id: 'context', title: 'Context', kind: 'context', context: CTX } }
      if (method === 'surface.get')
        return { surface: { id: 'ext-0', title: 'x', kind: 'panel', panel: { rows: [] } } }
      return undefined
    },
  })
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()
  await page.locator('.topbar button[title="Panes (usage, settings, extensions)"]').click()

  // Guard the fixture: the tabs and the surface must both have rendered.
  // Measuring the "loading…" placeholder void'd a probe run of this exact test.
  await expect(page.locator('.ctx-body')).toBeVisible()
  await expect(page.locator('.pane-tab')).toHaveCount(EXT_COUNT + 1)

  // Guard that the arm binds: the tab strip's natural height must exceed the
  // cap, or there was never a squeeze to survive. Phrased on scrollHeight,
  // which reads the same 660px content in BOTH states, so this still runs
  // against the unfixed build instead of failing on something the fix adds.
  const head = page.locator('.pane-head')
  const natural = await head.evaluate((el) => el.scrollHeight)
  expect(natural, 'the tab strip must overflow the cap, or nothing is being tested').toBeGreaterThan(400)

  // The visible consequence: the surface keeps the majority of the rail.
  const { bodyH, railH } = await page.evaluate(() => ({
    bodyH: (document.querySelector('.pane-body') as HTMLElement).getBoundingClientRect().height,
    railH: (document.querySelector('.pane-rail') as HTMLElement).getBoundingClientRect().height,
  }))
  expect(railH, 'the rail must be short enough to force the squeeze').toBeLessThan(600)
  expect(bodyH / railH, 'the surface must keep at least 40% of the rail').toBeGreaterThan(0.4)

  // The reachability one: every tab can still be pressed. Playwright scrolls the
  // target into view the way a reader does and fails when nothing can.
  await page.locator('.pane-tab').last().click({ timeout: 3000 })
})
