import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend } from './support'

// The suggest sheet quotes the line you are answering — at phone height, where
// the problem is.
//
// The sheet is a bottom sheet over a dimmed backdrop: while you draft, the
// message you are drafting against is covered by the sheet and greyed by the
// scrim. Quoting it inside the sheet fixes that, but only if the quote is
// BOUNDED — a long scene beat that grows to its natural height would push the
// sketch box and its actions off the bottom, trading one hidden thing for
// another.
//
// These are the assertions a unit test cannot make: real layout, real viewport,
// a real scroll container.
const LONG_SCENE = Array.from(
  { length: 40 },
  (_, i) => `Beat ${i + 1}: she turns the glass a quarter-turn and says nothing at all about the door.`,
).join('\n\n')

test('stage: the suggest sheet quotes the scene line, bounded and scrollable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 })
  const session = { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' }
  const mock = await installStageBackend(page, {
    respond: (method) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session,
        epoch: 1,
        base: 0,
        total: 1,
        busy: false,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: LONG_SCENE }] }],
      },
    },
    SMOKE_SESSION,
  )

  await page.locator('.stage-suggest-btn').click()
  const sheet = page.locator('.stage-suggest')
  await expect(sheet).toBeVisible()

  const quote = page.locator('.stage-suggest__quote-body')
  await expect(quote).toBeVisible()
  await expect(quote).toContainText('Beat 1:')
  if (process.env.SUGGEST_SHOT) await page.screenshot({ path: `${process.env.SUGGEST_SHOT}-suggest.png` })

  // It opens at the END, the way a chat does: you are answering the last thing
  // said, and for a long beat that is the bottom of it. Opening at the top would
  // show the part read several seconds ago and make you scroll to reach what you
  // are actually responding to.
  const at = await quote.evaluate((el) => ({ top: el.scrollTop, max: el.scrollHeight - el.clientHeight }))
  // Without this the assertion below passes vacuously on content that does not
  // overflow at all (0 >= -2), proving nothing about where it opened.
  expect(at.max, 'the fixture must overflow, or the next assertion proves nothing').toBeGreaterThan(20)
  expect(at.top, 'the quote must open scrolled to its end').toBeGreaterThanOrEqual(at.max - 2)

  // Whether it SCROLLS or merely grew is the whole question, and
  // scrollHeight > clientHeight does not answer it — that is equally true of a
  // box painting its overflow outside itself. Only a scroll container moves.
  const scrolls = (el: Element) => {
    el.scrollTop = 9999
    const moved = el.scrollTop > 0
    el.scrollTop = 0
    return moved
  }
  expect(await quote.evaluate(scrolls), 'the quote must scroll inside its box').toBe(true)

  // …and it stayed bounded rather than growing towards the top of the screen.
  const box = await quote.boundingBox()
  expect(box, 'the quote must have a box').not.toBeNull()
  expect(box!.height, 'the quote must be capped well under the viewport').toBeLessThan(780 * 0.35)

  // The sheet itself must still leave the scene visible above it. Growing to
  // the top is the failure the cap exists to prevent.
  const sheetBox = await sheet.boundingBox()
  expect(sheetBox!.y, 'the sheet must not reach the top of the screen').toBeGreaterThan(40)

  // The thing you came to do is still reachable without scrolling the sheet:
  // the sketch box and its button are on screen under the quote.
  const go = page.locator('.stage-suggest__go')
  await expect(go).toBeVisible()
  const goBox = await go.boundingBox()
  expect(goBox!.y + goBox!.height, 'the draft button must be on screen').toBeLessThanOrEqual(780)

  // Selecting inside the quote and releasing outside the sheet must NOT dismiss
  // it. That click lands on the BACKDROP — press and release have no closer
  // common ancestor — and closing there would drop the selection mid-copy,
  // which is the point of quoting the line at all.
  //
  // It has to be a real drag. A plain click on the backdrop collapses the
  // selection on mousedown, before any click handler runs, so simulating it
  // that way tests something else entirely (and is the case below).
  const qb = (await quote.boundingBox())!
  await page.mouse.move(qb.x + 12, qb.y + 10)
  await page.mouse.down()
  await page.mouse.move(qb.x + qb.width - 12, qb.y + 30, { steps: 8 })
  await page.mouse.move(195, 20, { steps: 8 }) // out over the backdrop
  await page.mouse.up()
  await expect(sheet, 'a selection drag ending outside must not close the sheet').toBeVisible()
  expect(
    await page.evaluate(() => window.getSelection()?.toString().length ?? 0),
    'the selection must survive the drag',
  ).toBeGreaterThan(0)

  // …but a deliberate click on the backdrop still dismisses. The browser
  // collapses the selection on that mousedown, so the guard above cannot wedge
  // the sheet open.
  await page.mouse.click(195, 20)
  await expect(sheet, 'a plain click outside still closes').toBeHidden()
})
