import { test, expect, type Page } from '@playwright/test'
import { PNG_1x1_BASE64, SMOKE_SESSION, installMockBackend, panelSessionURL } from './support'

// The outbound half of the file flow: an agent published a file, and the panel
// has to make it something the user can actually get at.
//
// This lives here rather than in vitest for the reason that suite exists —
// happy-dom renders no layout, and every failure mode of this feature is
// visual. A card can be present in the DOM and still be: buried inside a
// collapsed tool group, clipped past the transcript's width, or overlapping the
// message beside it. None of that is assertable without a real box.

// seedShared puts one shared file in the transcript, attached to a tool call.
// The shape is exactly what the daemon sends: the record rides the tool-role
// MESSAGE (core.MetaShared), naming the call it came from.
async function seedShared(
  page: Page,
  backend: Awaited<ReturnType<typeof installMockBackend>>,
  file: Record<string, unknown>,
  opts: { extraTools?: number; filler?: number } = {},
) {
  await backend.subscribed
  const calls = [{ type: 'tool_call', id: 'c_share', name: 'share_file', args: {} }]
  for (let i = 0; i < (opts.extraTools ?? 0); i++) {
    calls.unshift({ type: 'tool_call', id: 'c_' + i, name: 'read', args: {} })
  }
  const results = calls.map((c) => ({
    type: 'tool_result',
    call_id: c.id,
    content: [{ type: 'text', text: 'ok' }],
  }))
  // Rows ahead of the card, to put the transcript's flex column under real
  // height pressure. A short transcript has slack for every row and hides the
  // whole class of shrink bugs.
  const filler = []
  for (let i = 0; i < (opts.filler ?? 0); i++) {
    filler.push({ role: 'user', content: [{ type: 'text', text: `earlier question ${i + 1}` }] })
    filler.push({ role: 'assistant', content: [{ type: 'text', text: `earlier answer ${i + 1}` }] })
  }
  const messages = [
    ...filler,
    { role: 'user', content: [{ type: 'text', text: 'Pull last week together for me.' }] },
    { role: 'assistant', content: calls },
    { role: 'tool', content: results, shared: [{ ...file, call_id: 'c_share' }] },
    { role: 'assistant', content: [{ type: 'text', text: 'Here is the weekly rollup.' }] },
  ]
  backend.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'report', experience: 'code' },
        epoch: 1,
        base: 0,
        total: messages.length,
        messages,
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('text=weekly rollup').waitFor()
}

const doc = { id: 'shr_abc', name: 'weekly-rollup.pdf', kind: 'document', mime: 'application/pdf', size: 248_000 }

// The mock backend serves the socket, not the download route, so an <img> or an
// <audio> pointing at /shared/ would 404 — and a broken <img> has a very
// different box from a real one, which is precisely what these tests measure.
// Serve something real from the route instead.
async function serveSharedRoute(page: Page) {
  await page.route('**/shared/**', (route) =>
    route.fulfill({ status: 200, contentType: 'image/png', body: Buffer.from(PNG_1x1_BASE64, 'base64') }),
  )
}

test('a shared file is a real download, scoped to the session', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await page.goto(panelSessionURL)
  await seedShared(page, backend, doc)

  const link = page.locator('a.shared-file__row')
  await expect(link).toHaveCount(1)
  await expect(link).toHaveAttribute('href', `/shared/${SMOKE_SESSION}/shr_abc`)
  await expect(link).toHaveAttribute('download', 'weekly-rollup.pdf')
  await expect(link).toContainText('weekly-rollup.pdf')
  await expect(link).toContainText('242.2 KB')
})

// The reason the card is lifted out of its tool row at all. ToolGroup renders
// collapsed, so a card left inside one is invisible until the user thinks to
// expand a row of chrome they have every reason to skip.
test('the card is visible without expanding the tool group that made it', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  // The panel defaults to 'full' (every tool row rendered), where nothing is
  // hidden and this proves nothing. Grouped mode is the setting the lift exists
  // for — and the one a user who finds tool rows noisy will be sitting in.
  await page.addInitScript(() => localStorage.setItem('terva_toolview', 'grouped'))
  await page.goto(panelSessionURL)
  await seedShared(page, backend, doc, { extraTools: 2 })

  // The group is collapsed: its body is not rendered at all.
  await expect(page.locator('.tool-group-head')).toHaveCount(1)
  await expect(page.locator('.tool-group-body')).toHaveCount(0)
  // ...and the card is on screen anyway.
  await expect(page.locator('.shared-file')).toBeVisible()

  const card = (await page.locator('.shared-file').boundingBox())!
  const head = (await page.locator('.tool-group-head').boundingBox())!
  expect(card.y).toBeGreaterThan(head.y)
})

// A transcript longer than its own column is the ordinary case, not an edge
// one, and it is where this card used to disappear completely.
//
// `.log` is a flex COLUMN, so every transcript row is a flex item. A flex item
// normally cannot be shrunk below its content, because min-height:auto resolves
// to a content-based minimum, but that only holds while the item's overflow is
// visible. `.shared-file` sets overflow:hidden, to clip its image to the card's
// rounded corners, and that zeroes the minimum. So it is the one row in the
// transcript that can be squeezed to nothing, and being overflow:hidden it then
// clips away its own filename, size and download link. The route still serves
// the bytes, which is what makes the failure so quiet: nothing errors, the user
// simply cannot see or click the file.
//
// Measured against the card's OWN content height. A collapsed card still has a
// box, still reports itself visible, and still sits inside the viewport, so
// none of those catch it. The rendered height against scrollHeight does.
test('a shared card is not squeezed to nothing by a long transcript', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await serveSharedRoute(page)
  // Small enough that the rows genuinely compete for the column's height.
  await page.setViewportSize({ width: 460, height: 620 })
  await page.goto(panelSessionURL)
  await seedShared(
    page,
    backend,
    { id: 'shr_img', name: 'latency.png', kind: 'image', mime: 'image/png', size: 4096 },
    { filler: 12 },
  )

  // Guard the fixture before trusting the assertion: the pressure has to be
  // real, or this passes by rendering a transcript that never had to shrink
  // anything and proves nothing at all.
  const overflowing = await page.locator('.log').evaluate((el) => el.scrollHeight > el.clientHeight + 1)
  expect(overflowing, 'the transcript must actually overflow, or this test asserts nothing').toBe(true)

  const card = page.locator('.shared-file')
  await expect(card).toHaveCount(1)
  const size = await card.evaluate((el) => ({
    rendered: Math.round(el.getBoundingClientRect().height),
    content: el.scrollHeight,
  }))
  expect(size.rendered).toBeGreaterThanOrEqual(size.content)

  // ...and the row the user clicks is inside the card's clip, not past its edge.
  const cardBox = (await card.boundingBox())!
  const rowBox = (await page.locator('a.shared-file__row').boundingBox())!
  expect(rowBox.y + rowBox.height).toBeLessThanOrEqual(cardBox.y + cardBox.height + 1)
  await expect(page.locator('.shared-file__name')).toContainText('latency.png')
})

test('a shared image renders inline and stays inside the transcript', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await serveSharedRoute(page)
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { id: 'shr_img', name: 'latency.png', kind: 'image', mime: 'image/png', size: 4096 })

  const img = page.locator('.shared-file__media img')
  await expect(img).toHaveAttribute('src', `/shared/${SMOKE_SESSION}/shr_img?inline=1`)
  // It actually loaded — a 404'd <img> is still in the DOM and still has a box.
  await expect(img.evaluate((e: HTMLImageElement) => e.naturalWidth)).resolves.toBeGreaterThan(0)

  const card = (await page.locator('.shared-file').boundingBox())!
  const log = (await page.locator('.log').boundingBox())!
  expect(card.x).toBeGreaterThanOrEqual(log.x - 1)
  expect(card.x + card.width).toBeLessThanOrEqual(log.x + log.width + 1)
})

// An audio player with unreachable controls is an audio player that does not
// work, and it looks identical to a working one in a DOM assertion.
test('a shared audio file gets a usable player', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await serveSharedRoute(page)
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { id: 'shr_mp3', name: 'standup.mp3', kind: 'audio', mime: 'audio/mpeg', size: 900_000 })

  const player = page.locator('.shared-file__media')
  await expect(player).toBeVisible()
  const box = (await player.boundingBox())!
  // A native <audio> control strip is ~54px tall and needs real width for the
  // scrubber; a collapsed or hairline box means the layout ate it.
  expect(box.height).toBeGreaterThan(20)
  expect(box.width).toBeGreaterThan(200)

  const card = (await page.locator('.shared-file').boundingBox())!
  expect(box.x + box.width).toBeLessThanOrEqual(card.x + card.width + 1)
})

// A long filename must ellipsize, not push the size out of the card — the same
// failure the composer chip had, in the other direction.
test('a long filename does not push the size off the card', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await page.goto(panelSessionURL)
  await seedShared(page, backend, {
    id: 'shr_long',
    name: 'quarterly-infrastructure-cost-attribution-and-forecast-2026-q3-final-v4.pdf',
    kind: 'document',
    mime: 'application/pdf',
    size: 1_048_576,
  })

  const card = (await page.locator('.shared-file').boundingBox())!
  const size = (await page.locator('.shared-file__size').boundingBox())!
  const name = (await page.locator('.shared-file__name').boundingBox())!

  await expect(page.locator('.shared-file__size')).toHaveText('1.0 MB')
  expect(size.x + size.width).toBeLessThanOrEqual(card.x + card.width)
  // The name yields to the size rather than overrunning it.
  expect(name.x + name.width).toBeLessThanOrEqual(size.x + 1)
})

test('a caption renders under the file, not on top of it', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { ...doc, caption: 'week 32 — p99 up 40ms after the migration' })

  const row = (await page.locator('.shared-file__row').boundingBox())!
  const caption = (await page.locator('.shared-file__caption').boundingBox())!
  await expect(page.locator('.shared-file__caption')).toContainText('p99 up 40ms')
  expect(caption.y).toBeGreaterThanOrEqual(row.y + row.height - 1)
})

// The inbound and outbound shapes are deliberately different components, and
// this is the assertion that keeps them from converging by accident: an
// attachment label promises nothing, a shared card promises a file.
test('a share is interactive where an attachment label is not', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await page.goto(panelSessionURL)
  await seedShared(page, backend, doc)

  await expect(page.locator('.shared-file a')).toHaveCount(1)
  await expect(page.locator('.msg-file')).toHaveCount(0)
})

// Where every share ends up: swept on its TTL, with the transcript still naming
// it. The route 404s, and the card has to stop offering a download it cannot
// honour — this is the one state the panel will spend most of its life in for
// any given file, so it gets a real-browser test rather than a synthetic event.
test('a swept share stops offering a download instead of showing a broken image', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await page.route('**/shared/**', (route) => route.fulfill({ status: 404, body: 'not found' }))
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { id: 'shr_old', name: 'last-month.png', kind: 'image', mime: 'image/png', size: 4096 })

  await expect(page.locator('.shared-file--inert')).toBeVisible()
  await expect(page.locator('.shared-file img')).toHaveCount(0)
  await expect(page.locator('.shared-file a')).toHaveCount(0)
  // It still says what was shared. The record outlives the bytes on purpose.
  await expect(page.locator('.shared-file')).toContainText('last-month.png')
})

// Guard against the image path regressing to a data: URL or an <img> with no
// source at all — both of which render as nothing and pass a count assertion.
test('the image source points at the route, not at inline data', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await serveSharedRoute(page)
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { id: 'shr_p', name: 'p.png', kind: 'image', mime: 'image/png', size: 70 })

  const src = await page.locator('.shared-file__media img').getAttribute('src')
  expect(src).toBe(`/shared/${SMOKE_SESSION}/shr_p?inline=1`)
  expect(src).not.toContain('base64')
})

// The gap expires_at closes, measured the way it was found: before it, only an
// <img> failing to load could degrade a card, so a document — the kind
// share_file's own description leads with — kept a live ↓ link for bytes the
// sweeper had taken. A 404 on click is not a transcript telling you the truth.
//
// Every kind, because the old mechanism covered exactly one of them.
for (const f of [
  { id: 'shr_d', name: 'report.pdf', kind: 'document', mime: 'application/pdf', size: 4096 },
  { id: 'shr_a', name: 'voice.mp3', kind: 'audio', mime: 'audio/mpeg', size: 4096 },
  { id: 'shr_v', name: 'clip.mp4', kind: 'video', mime: 'video/mp4', size: 4096 },
  { id: 'shr_i', name: 'chart.png', kind: 'image', mime: 'image/png', size: 4096 },
]) {
  test(`an expired ${f.kind} share offers no download`, async ({ page }) => {
    const backend = await installMockBackend(page, { features: ['shared-files'] })
    // Every byte request 404s, as it would once the file is swept. If the card
    // still offers a link, this is what the user's click gets.
    await page.route('**/shared/**', (route) => route.fulfill({ status: 404, body: 'gone' }))
    await page.goto(panelSessionURL)
    await seedShared(page, backend, { ...f, expires_at: new Date(Date.now() - 3600_000).toISOString() })

    await expect(page.locator('.shared-file--inert')).toBeVisible()
    await expect(page.locator('.shared-file a')).toHaveCount(0)
    await expect(page.locator('.shared-file')).toContainText(f.name)
    await expect(page.locator('.shared-file')).toContainText('no longer available')
  })
}

// …and an unexpired one is untouched: the deadline must not withdraw a download
// that still works, which is the failure the other direction.
test('an unexpired share still offers its download', async ({ page }) => {
  const backend = await installMockBackend(page, { features: ['shared-files'] })
  await serveSharedRoute(page)
  await page.goto(panelSessionURL)
  await seedShared(page, backend, { ...doc, expires_at: new Date(Date.now() + 3600_000).toISOString() })

  await expect(page.locator('a.shared-file__row')).toHaveCount(1)
  await expect(page.locator('.shared-file--inert')).toHaveCount(0)
})
