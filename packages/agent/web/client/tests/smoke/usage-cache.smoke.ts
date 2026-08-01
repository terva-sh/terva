import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The Usage/context pane's prompt-cache reading (ContextBreakdown.cache): the
// session hit-rate gauge, the last request's read/write/full-price split, and
// the per-request strip.
//
// A smoke rather than another unit test because the strip is the part that can
// only fail visually: happy-dom asserts the inline heights the component wrote
// and has no layout, so a strip whose bars hang from the top of the row, or
// whose 32 columns overflow a narrow pane, passes every assertion in
// app-cache.test.tsx.

const usage = (o: Record<string, number> = {}) => ({
  input: 0,
  output: 0,
  cache_read: 0,
  cache_write: 0,
  cost_usd: 0,
  ...o,
})

// A conversation with one visible break in it: steady 90-something percent, one
// request where the prefix changed, then recovery.
const RATES = [0.94, 0.96, 0.97, 0.95, 0.02, 0.71, 0.93, 0.96, 0.97, 0.97, 0.98, 0.96]

const CTX = {
  window: 200000,
  system_bytes: 12_400,
  ext_guidance_bytes: 0,
  tool_bytes: 18_900,
  tool_count: 22,
  ext_bytes: 1_100,
  transcript_bytes: 402_000,
  total_bytes: 434_400,
  messages: [{ index: 0, kind: 'assistant', bytes: 402_000 }],
  context_tokens: 108_000,
  cumulative: usage({ input: 42_000, output: 31_000, cache_read: 1_940_000, cache_write: 96_000, cost_usd: 3.42 }),
  cache: {
    supported: true,
    session: usage({
      input: 42_000,
      cache_read: 1_940_000,
      cache_write: 96_000,
      cost_usd: 3.42,
      cache_saved_usd: 4.87,
    }),
    last_request: usage({ input: 2_100, cache_read: 104_000, cache_write: 1_800 }),
    recent: RATES.map((r) => ({
      hit_rate: r,
      prompt_tokens: 108_000,
      saved_usd: r * 0.3,
    })),
  },
}

async function openContextPane(page: import('@playwright/test').Page) {
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()
  await page.locator('.topbar button[title="Panes (usage, settings, extensions)"]').click()
  await page.locator('.pane-tab[title="Context"]').click()
}

function mount(ctx: unknown) {
  return {
    respond: (method: string, params: unknown) => {
      if (method === 'surfaces.list') return { surfaces: [{ id: 'context', title: 'Context', kind: 'context' }] }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'context')
        return { surface: { id: 'context', title: 'Context', kind: 'context', context: ctx } }
      return undefined
    },
  }
}

test('the usage pane draws the prompt-cache reading', async ({ page }) => {
  await installMockBackend(page, mount(CTX))
  await openContextPane(page)

  await expect(page.locator('.ctx-cache')).toBeVisible()
  await expect(page.locator('.ctx-cache-bar')).toHaveCount(RATES.length)

  // The strip's bars must sit on a shared baseline, or it is not a chart. Take
  // the bottom edge of the tallest and the shortest and require them to agree:
  // a missing align-items:flex-end floats the short one and this diverges by
  // most of the row height.
  const tall = await page.locator('.ctx-cache-fill').first().boundingBox()
  const short = await page.locator('.ctx-cache-fill').nth(4).boundingBox() // the 2% one
  expect(tall).not.toBeNull()
  expect(short).not.toBeNull()
  expect(Math.abs(tall!.y + tall!.height - (short!.y + short!.height))).toBeLessThan(2)
  // And the short bar must be genuinely shorter, or the break is invisible.
  expect(short!.height).toBeLessThan(tall!.height / 2)

  // Nothing may push the pane into a horizontal scroll — the strip is the widest
  // thing on it and the most likely to.
  const body = page.locator('.ctx-body')
  const overflow = await body.evaluate((el) => el.scrollWidth - el.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)

  if (process.env.CACHE_SHOT) await page.screenshot({ path: `${process.env.CACHE_SHOT}.png`, fullPage: true })
})

// A full ring on a phone-width pane is the crowding case: 32 columns in the
// narrowest layout the app supports.
test('a full strip survives a narrow pane', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 })
  const many = Array.from({ length: 32 }, (_, i) => ({
    hit_rate: i === 17 ? 0 : 0.9 + (i % 5) * 0.02,
    prompt_tokens: 90_000,
  }))
  await installMockBackend(page, mount({ ...CTX, cache: { ...CTX.cache, recent: many } }))
  await openContextPane(page)

  await expect(page.locator('.ctx-cache-bar')).toHaveCount(32)
  const body = page.locator('.ctx-body')
  expect(await body.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  // Every bar keeps a nonzero width; flex:1 1 0 with min-width:0 shrinks rather
  // than overflowing, but zero-width columns would be a strip nobody can read.
  const widths = await page.locator('.ctx-cache-bar').evaluateAll((els) =>
    els.map((e) => (e as HTMLElement).getBoundingClientRect().width),
  )
  expect(Math.min(...widths)).toBeGreaterThan(1)

  if (process.env.CACHE_SHOT) await page.screenshot({ path: `${process.env.CACHE_SHOT}-narrow.png`, fullPage: true })
})

// A provider with no prefix cache must not read as a broken one.
test('an uncached provider says so instead of showing 0%', async ({ page }) => {
  await installMockBackend(
    page,
    mount({
      ...CTX,
      cache: { supported: false, session: usage({ input: 90_000, output: 4_000 }), last_request: usage() },
    }),
  )
  await openContextPane(page)

  await expect(page.locator('.ctx-cache-none')).toContainText('no cache activity')
  await expect(page.locator('.ctx-cache-strip')).toHaveCount(0)
})
