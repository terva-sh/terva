import { test, expect } from '@playwright/test'
import { editButtonFor, installMockBackend, SMOKE_SESSION } from './support'

// Selecting text in a message, which used to be impossible.
//
// The bubble opened the inline editor on any click, so dragging across a line to
// copy it turned the message into a textarea mid-gesture. Editing is behind ✎ in
// the row's control bar now, and the bubble is for reading.
//
// happy-dom cannot see this half: it has no layout, so there are no coordinates
// to drag between and no real Selection to inspect. The contracts that CAN be
// unit-tested are in src/apps/stage/Chat.test.tsx ("Chat message editing"); this
// covers the gesture itself.
const SNAPSHOT = {
  type: 'snapshot',
  snapshot: {
    session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' },
    epoch: 3,
    base: 0,
    total: 2,
    busy: false,
    messages: [
      { role: 'user', content: [{ type: 'text', text: 'i step inside and shut the door behind me' }] },
      {
        role: 'assistant',
        content: [{ type: 'text', text: 'The lamp gutters once and steadies. She does not look up from the ledger.' }],
      },
    ],
  },
}

async function scene(page: import('@playwright/test').Page) {
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create')
        return { session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' } }
      return undefined
    },
  })
  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(SNAPSHOT, SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'The lamp gutters' })).toBeVisible()
  return mock
}

test('stage: a message can be selected and copied without becoming an editor', async ({ page }) => {
  await scene(page)

  const bubble = page.locator('.stage-bubble', { hasText: 'The lamp gutters' })
  // hover() first because it is the only step here with an actionability wait:
  // Playwright holds until the element's box is unchanged across two animation
  // frames. mouse.move() has no such check, so measuring before the transcript
  // has settled and then dragging at those coordinates is a race.
  await bubble.hover()

  // Measure the TEXT, not the bubble. The old drag ran along `box.y + 10` — ten
  // pixels below the bubble's top edge, which is inside its padding rather than
  // reliably on a line of prose. It selected the whole line ~98% of the time and
  // one character the rest (1 failure in 45 runs), and which of those you got
  // depended on where the padding happened to end.
  //
  // A Range over the text node gives the real line boxes, so the drag runs
  // through the vertical middle of the FIRST line whatever the padding, the font,
  // or the number of lines the bubble wrapped to.
  const line = await bubble.evaluate((el) => {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!n.textContent?.includes('lamp')) continue
      const r = document.createRange()
      r.selectNodeContents(n)
      const first = r.getClientRects()[0]
      if (first) return { x: first.x, y: first.y, width: first.width, height: first.height }
    }
    return null
  })
  expect(line, 'no rendered line box for the message text — the drag would have nothing to cross').not.toBeNull()
  const midY = line!.y + line!.height / 2

  // A real drag across that line — press, move, move, release. A synthesized
  // click() would collapse the selection on mousedown and prove nothing, the
  // lesson the suggest-sheet backdrop guard already cost once.
  await page.mouse.move(line!.x + 2, midY)
  await page.mouse.down()
  await page.mouse.move(line!.x + line!.width * 0.4, midY, { steps: 8 })
  await page.mouse.move(line!.x + line!.width * 0.8, midY, { steps: 8 })
  await page.mouse.up()

  const selected = await page.evaluate(() => window.getSelection()?.toString() ?? '')
  expect(selected.length, 'the drag must actually select text, or the next assertion proves nothing').toBeGreaterThan(5)
  expect(selected, 'the selection must come from the message').toContain('lamp')
  await expect(page.locator('.stage-edit textarea'), 'the message must not have become an editor').toHaveCount(0)
})

test('stage: ✎ opens the editor, on any message and not only the last', async ({ page }) => {
  await scene(page)

  // Your own message, mid-scene — the case with no generation controls at all,
  // and the one that had no bar before this.
  await editButtonFor(page, 'i step inside').click()
  const box = page.locator('.stage-edit textarea')
  await expect(box).toHaveValue('i step inside and shut the door behind me')

  // The row being edited drops its own ✎ rather than offering to reopen itself.
  await expect(page.locator('.stage-row--user .stage-msgedit')).toHaveCount(0)
  await page.locator('.stage-edit__cancel').click()

  // The last reply keeps the generation controls, with ✎ alongside them.
  const last = page.locator('.stage-row--assistant').last()
  await expect(last.locator('.stage-msgedit')).toBeVisible()
  await expect(last.locator('.stage-regen')).toHaveCount(2)
  await last.locator('.stage-msgedit').click()
  await expect(page.locator('.stage-edit textarea')).toHaveValue(
    'The lamp gutters once and steadies. She does not look up from the ledger.',
  )
})
