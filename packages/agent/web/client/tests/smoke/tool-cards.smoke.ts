import { test, expect, type Page } from '@playwright/test'
import { SMOKE_SESSION, installMockBackend, panelSessionURL } from './support'

// Stage 1 of the tool card (docs/proposals/web-tool-cards.md).
//
// These live here rather than in vitest because every property below is a
// layout property, and happy-dom has no layout: it reports zero for every box,
// so a card squeezed to nothing and a card rendered correctly are the same
// object there. The unit tests next to the component cover what the DOM can
// answer (the clamp count, the mask, the subject derivation); these cover what
// only a real box can.

// PANEL is the width the panel is actually read at, and the width at which
// every eliding decision in the header either works or does not.
const PANEL = { width: 460, height: 620 }

async function seed(
  page: Page,
  backend: Awaited<ReturnType<typeof installMockBackend>>,
  call: { name: string; args: unknown },
  result: string,
  opts: { filler?: number } = {},
) {
  await backend.subscribed
  // Rows ahead of the card, so the transcript's flex column runs out of room.
  // A short transcript has slack for every row and hides a shrink bug whole.
  const filler = []
  for (let i = 0; i < (opts.filler ?? 0); i++) {
    filler.push({ role: 'user', content: [{ type: 'text', text: `earlier question ${i + 1}` }] })
    filler.push({ role: 'assistant', content: [{ type: 'text', text: `earlier answer ${i + 1}` }] })
  }
  const messages = [
    ...filler,
    { role: 'user', content: [{ type: 'text', text: 'have a look' }] },
    { role: 'assistant', content: [{ type: 'tool_call', id: 'c_1', name: call.name, args: call.args }] },
    { role: 'tool', content: [{ type: 'tool_result', call_id: 'c_1', content: [{ type: 'text', text: result }] }] },
    { role: 'assistant', content: [{ type: 'text', text: 'that is what I found.' }] },
  ]
  backend.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'cards', experience: 'code' },
        epoch: 1,
        base: 0,
        total: messages.length,
        messages,
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('text=that is what I found').waitFor()
  await page.locator('.tool-card').waitFor()
}

// The card sets overflow:hidden to clip its header strip to the rounded
// corners, which zeroes a flex item's content-based minimum height. Without
// flex:none it is then the row a full transcript squeezes away, exactly as
// .shared-file was. Reverting `flex: none` in styles.css must fail this.
test('a tool card keeps its height in a transcript that overflows', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  await seed(page, backend, { name: 'bash', args: { command: 'just test' } }, 'ok\nfine\ndone', { filler: 12 })

  // Guard the fixture: the pressure has to be real, or this passes by
  // rendering a transcript that never had to shrink anything.
  const overflowing = await page.locator('.log').evaluate((el) => el.scrollHeight > el.clientHeight + 1)
  expect(overflowing, 'the transcript must actually overflow, or this asserts nothing').toBe(true)

  const size = await page.locator('.tool-card').evaluate((el) => ({
    rendered: Math.round(el.getBoundingClientRect().height),
    content: el.scrollHeight,
  }))
  expect(size.rendered).toBeGreaterThanOrEqual(size.content)
})

// The point of replacing max-height with a line clamp. A nested scroll region
// steals the wheel: scrolling over a tool result moved the result, not the
// conversation, and the transcript appeared stuck.
test('a clamped result has no scroll well of its own', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  const long = Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join('\n')
  await seed(page, backend, { name: 'bash', args: { command: 'just build' } }, long)

  const body = page.locator('.tool-card__body')
  await expect(body).toBeVisible()
  const scrolls = await body.evaluate((el) => ({
    overflowY: getComputedStyle(el).overflowY,
    // A scrollable box is one whose content is taller than its own frame.
    trapped: el.scrollHeight > el.clientHeight + 1,
  }))
  expect(scrolls.overflowY).toBe('visible')
  expect(scrolls.trapped, 'the body must not be its own scroll region').toBe(false)
})

// The head/tail split, which is why the subject is two spans rather than one
// string cut to a guessed character budget. CSS elides the head at whatever
// the real width turns out to be; the basename is pinned beside it.
test('a deep path keeps its basename at panel width', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  const path = 'packages/agent/web/client/src/features/conversation/MessageContent.tsx'
  await seed(page, backend, { name: 'read', args: { path } }, 'ok')

  const head = page.locator('.tool-card__subject-head')
  const tail = page.locator('.tool-card__subject-tail')

  // Guard that the arm under test is the one that binds. If the header is wide
  // enough to show the whole path, nothing is being elided and the assertion
  // below passes without exercising the split at all.
  const elided = await head.evaluate((el) => el.scrollWidth > el.clientWidth + 1)
  expect(elided, 'the head must actually be eliding at this width').toBe(true)

  // The basename is whole, and inside the subject's box rather than clipped past it.
  await expect(tail).toHaveText('MessageContent.tsx')
  const boxes = await page.locator('.tool-card__subject').evaluate((sub) => {
    const t = sub.querySelector('.tool-card__subject-tail')!.getBoundingClientRect()
    const s = sub.getBoundingClientRect()
    return { tailRight: t.right, tailWidth: t.width, subRight: s.right }
  })
  expect(boxes.tailWidth).toBeGreaterThan(0)
  expect(boxes.tailRight).toBeLessThanOrEqual(boxes.subRight + 1)
})

// The header is a flex row and the subject is the only part allowed to grow.
// A subject that pushed the outcome off the card would hide the one field that
// says whether the call worked.
test('a long subject does not push the outcome chip off the card', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  await seed(page, backend, { name: 'bash', args: { command: 'x'.repeat(300) } }, 'ok')

  const card = (await page.locator('.tool-card').boundingBox())!
  const chip = (await page.locator('.tool-card__chip').boundingBox())!
  expect(chip.width).toBeGreaterThan(0)
  // Measured against the CARD, which is the box that clips, and not against the
  // viewport: an element can sit inside the viewport and still be cut off by an
  // ancestor's overflow:hidden.
  expect(chip.x + chip.width).toBeLessThanOrEqual(card.x + card.width + 1)
  expect(chip.x).toBeGreaterThanOrEqual(card.x)
})

// ---------------------------------------------------------------------------
// stage 2: the per-tool renderers

// The point of the gutter is that the pipeline's shape reads down the left edge
// before any of the words do, and that only works if the connectors line up.
// Alignment is a rendered-box property: the DOM says nothing about it.
test('a chained command lines its operators up in a gutter', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  await seed(
    page,
    backend,
    { name: 'bash', args: { command: 'cd /home/dev/workspace/terva && just build | tee log' } },
    'built\n[exit 0]  Took 1.2s',
  )

  const rows = page.locator('.tc-cmd-row')
  await expect(rows).toHaveCount(3)

  const geom = await page.evaluate(() => {
    const ops = [...document.querySelectorAll('.tc-cmd-op')]
    return ops.map((el) => {
      const r = el.getBoundingClientRect()
      return { right: Math.round(r.right), width: Math.round(r.width) }
    })
  })
  expect(geom).toHaveLength(3)
  // Every connector ends at the same x, so the commands after them start at the
  // same x too. One shared gutter, not three independent indents.
  expect(new Set(geom.map((g) => g.right)).size).toBe(1)
  expect(new Set(geom.map((g) => g.width)).size).toBe(1)

  // The exit code was lifted out of the transcript and into the header.
  await expect(page.locator('.tool-card__chip')).toHaveText('exit 0')
  await expect(page.locator('.tool-card__body')).not.toContainText('[exit 0]')
})

// A structured body is built from nodes rather than a string, so it reaches the
// clamp by a different path than plain text does. It has to arrive at the same
// place: twelve lines, no scroll well of its own.
test('a long diff clamps like plain text and traps no scroll', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.setViewportSize(PANEL)
  await page.goto(panelSessionURL)
  const diff = ['--- a/x.go', '+++ b/x.go', '@@ -1,400 +1,400 @@']
    .concat(Array.from({ length: 397 }, (_, i) => (i % 2 ? `+added ${i}` : `-removed ${i}`)))
    .join('\n')
  await seed(page, backend, { name: 'edit', args: { path: 'pkg/x.go', edits: [{}, {}] } }, diff)

  // Twelve rendered lines, counted as elements rather than inferred from height.
  await expect(page.locator('.tc-diff')).toHaveCount(12)
  await expect(page.locator('.tool-card__more')).toContainText('400')
  await expect(page.locator('.tool-card__meta')).toHaveText('2 edits')
  // The chip is derived from the diff text, so it survives a reload; the wire's
  // lines_added never reaches a replayed transcript. 397 body lines alternate
  // starting on a removal, so 199 removed and 198 added, and the ---/+++ file
  // headers count towards neither.
  await expect(page.locator('.tool-card__chip')).toHaveText('+198 −199')

  const body = page.locator('.tool-card__body')
  const trapped = await body.evaluate((el) => el.scrollHeight > el.clientHeight + 1)
  expect(trapped, 'a structured body must not become its own scroll region').toBe(false)

  await page.locator('.tool-card__more').click()
  await expect(page.locator('.tc-diff')).toHaveCount(400)
})
