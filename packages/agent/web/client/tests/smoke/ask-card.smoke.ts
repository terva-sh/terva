import { test, expect, type Page } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The mid-turn ask card, at the size that made it a problem. A set is capped at
// eight (core.MaxAskQuestions), and eight questions with five options each is
// what the card has to survive — it used to render every question expanded,
// which pushed the transcript off the top and the send control off the bottom.
//
// This is the browser-level guard the component tests cannot give: happy-dom
// has no layout, so "the card is bounded" and "send is reachable" are exactly
// the two claims that were unverifiable until this file existed.
//
// The card is a SIBLING of the transcript and the composer, not transcript
// content, so any height it takes is taken from them. That is why the bound is
// asserted against the viewport rather than against the card's own scroller.

const VIEWPORTS = [
  { name: 'phone', width: 390, height: 780 },
  { name: 'tablet-portrait', width: 744, height: 1000 },
  { name: 'desktop', width: 1280, height: 800 },
]

const OPTIONS = ['Postgres', 'SQLite', 'MySQL', 'DuckDB', 'none of these']

// n questions, five options each, with a slug on some but not all — the strip
// names what the model named and leaves the rest as bare numbers, so a mixed
// set is the realistic one to render.
function askSet(n: number) {
  return {
    ask_id: 'smoke-ask-1',
    question: 'Question 1?',
    questions: Array.from({ length: n }, (_, i) => ({
      question: `Question ${i + 1}? This one is deliberately long enough to wrap on a phone.`,
      slug: i % 2 === 0 ? `topic-${i + 1}` : undefined,
      options: OPTIONS,
    })),
  }
}

async function openAsk(page: Page, n: number) {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()
  await backend.subscribed
  // `type`, not `kind`: app.tsx switches on ev.type. An envelope with the
  // wrong discriminant is silently ignored rather than rejected, so the only
  // symptom is a card that never appears.
  backend.pushEvent({ type: 'ask_request', ask: askSet(n) })
  await page.locator('.ask').waitFor()
  return backend
}

// Everything the layout has to be true of, measured in one pass so a failure
// names which part broke rather than which assertion ran first.
async function measure(page: Page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth
    const vh = document.documentElement.clientHeight
    const card = document.querySelector<HTMLElement>('.ask')!
    // Found by ROLE, not by .ask-send: that class only exists on the bounded
    // card, so keying on it would make this fixture crash on an older layout
    // instead of reporting what is wrong with it. A test that dies with
    // "cannot read getBoundingClientRect of null" teaches nobody anything.
    const send = document.querySelector<HTMLElement>('.ask button[type="submit"]')!
    const body = document.querySelector<HTMLElement>('.ask-body')

    // Anything sticking out sideways, ignoring the card's own inner scroller
    // the way the pane sweep ignores .wg-table-wrap.
    const spill: string[] = []
    document.querySelectorAll<HTMLElement>('.ask, .ask *').forEach((el) => {
      const r = el.getBoundingClientRect()
      if (r.right > vw + 0.5 || r.left < -0.5)
        spill.push(`${el.tagName}.${[...el.classList].join('.')}`)
    })

    // A forced horizontal pan must clamp back to 0.
    const doc = document.scrollingElement as HTMLElement
    doc.scrollLeft = 120
    const pan = doc.scrollLeft
    doc.scrollLeft = 0

    const cardRect = card.getBoundingClientRect()
    const sendRect = send.getBoundingClientRect()
    return {
      vw,
      vh,
      cardHeight: cardRect.height,
      cardBottom: cardRect.bottom,
      sendBottom: sendRect.bottom,
      sendTop: sendRect.top,
      sendVisible: sendRect.width > 0 && sendRect.height > 0,
      pan,
      spill,
      // The open question renders a .card-head; a stub renders .ask-summary.
      // Scoped to .ask rather than .ask-body for the same reason as `send` —
      // the older card had no body wrapper, and counting inside one that does
      // not exist would report zero open questions on a card showing eight.
      openQuestions: document.querySelectorAll('.ask .card-head').length,
      stubs: document.querySelectorAll('.ask .ask-summary').length,
      chips: document.querySelectorAll('.ask .ask-chip').length,
      // Does the body actually scroll rather than the card growing?
      bodyScrolls: body ? body.scrollHeight > body.clientHeight + 0.5 : false,
      // How much is actually out of sight. `bodyScrolls` answers whether the
      // card is willing to scroll; this answers how badly, which is what
      // separates "too small for the display" from "clipped by a constant".
      bodyHidden: body ? body.scrollHeight - body.clientHeight : 0,
    }
  })
}

for (const vp of VIEWPORTS) {
  test(`the ask card stays bounded and sendable at eight questions (${vp.name})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    await openAsk(page, 8)

    const m = await measure(page)

    // 1. The card cannot eat the screen. It is capped at 58vh in CSS; the
    //    assertion allows the card's own border/padding on top of that but
    //    nothing like a full page.
    expect(m.cardHeight, `card height at ${vp.name}`).toBeLessThan(m.vh * 0.7)

    // 2. Send is ON SCREEN. This is the one that used to fail: at eight
    //    questions the control that ends the interruption sat below the fold.
    expect(m.sendVisible, 'send button rendered').toBe(true)
    expect(m.sendBottom, `send bottom vs viewport at ${vp.name}`).toBeLessThanOrEqual(m.vh + 0.5)
    expect(m.sendTop, 'send top is on screen').toBeGreaterThanOrEqual(-0.5)

    // 3. One axis only, the same rule the pane sweep enforces.
    expect(m.spill, `horizontal spill at ${vp.name}`).toEqual([])
    expect(m.pan, 'document pans horizontally').toBe(0)

    // 4. The bound must not be met by DROPPING questions. Without this, a card
    //    that rendered three of the eight would satisfy every assertion above.
    //
    //    Note this is not "the body scrolls": at tablet-portrait 58vh is 580px,
    //    which fits one open question and seven stubs outright. Whether it
    //    scrolls is a property of the viewport; whether the set survives is a
    //    property of the card, and only the second is worth asserting here.
    expect(m.openQuestions + m.stubs, `all 8 questions present at ${vp.name}`).toBe(8)
    expect(m.openQuestions, `exactly one open at ${vp.name}`).toBe(1)
  })
}

test('one question is open, the rest are stubs, and the strip names them all', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await openAsk(page, 8)

  const m = await measure(page)
  // Progressive disclosure, asserted where it actually matters — a real layout
  // engine, not a DOM with no geometry.
  expect(m.openQuestions, 'exactly one question expanded').toBe(1)
  expect(m.stubs, 'the other seven are stubs').toBe(7)
  // Every question gets a chip, plus the review chip.
  expect(m.chips, 'eight question chips plus review').toBe(9)
})

// The OTHER arm of the cap, which nothing above reaches.
//
// max-height is min(58vh, Npx). Every other test here runs at a height where
// 58vh is the smaller term, so the absolute arm went unexercised — and it was
// wrong: 640px sat under the height a full set actually needs, so on a tall
// display the last stub was clipped and the scroll hint stuck on permanently,
// with no viewport at which it ever cleared.
//
// The width matters more than it looks. Below the 640px layout breakpoint a
// full card needs ~669px; above it, ~571px, which clears either cap and makes
// the test vacuous. The first version of this test ran at 1280 wide and passed
// against the bug it was written to catch, for exactly that reason. So: narrow
// enough that the content is tall, tall enough that 58vh (812px) does not bind.
//
// Every other assertion in this file asks whether the card is small ENOUGH, and
// a card too small to show its content passes all of them. This one asks the
// opposite: given a display with room to spare, does the full set actually fit?
test('a full set is not clipped by the absolute cap on a tall display', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 1400 })
  await openAsk(page, 8)

  const m = await measure(page)

  expect(m.openQuestions + m.stubs, 'all 8 questions present').toBe(8)
  expect(m.bodyHidden, 'pixels of the card hidden below its own fold').toBeLessThanOrEqual(0.5)
  expect(m.bodyScrolls, 'a full set fits outright when the display allows it').toBe(false)
})

test('the card grows with the set but stays within its bound', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 })

  const heights: number[] = []
  const scrolls: boolean[] = []
  for (const n of [2, 5, 8]) {
    const ctx = await page.context().newPage()
    await ctx.setViewportSize({ width: 390, height: 780 })
    await openAsk(ctx, n)
    const m = await measure(ctx)
    heights.push(m.cardHeight)
    scrolls.push(m.bodyScrolls)
    // A screenshot per size, for a human (or me) to look at rather than infer.
    await ctx.screenshot({ path: `test-results/ask-card-${n}q.png`, fullPage: false })
    expect(m.sendBottom, `send reachable with ${n} questions`).toBeLessThanOrEqual(m.vh + 0.5)
    expect(m.cardHeight, `bounded with ${n} questions`).toBeLessThan(m.vh * 0.7)
    await ctx.close()
  }

  // Two questions must not cost the same room as eight — a bound that is also a
  // floor would make every small ask look like a big one.
  expect(heights[0], 'a 2-question card is smaller than an 8-question one').toBeLessThan(
    heights[2],
  )
  // ...and a small set must not scroll at all: scrolling is the price of a set
  // too big for the bound, not the normal state of the card.
  expect(scrolls[0], 'a 2-question card does not scroll').toBe(false)
  expect(scrolls[2], 'an 8-question card does').toBe(true)
})
