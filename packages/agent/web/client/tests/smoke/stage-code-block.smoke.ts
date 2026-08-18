import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend } from './support'

// Wide markdown in a scene, at phone width — the width where it goes wrong.
//
// A fenced code block does not wrap, so without being told to scroll it keeps
// its natural width and takes the message with it: the text ran out through the
// side of the bubble and off the screen. A model asked to keep a stat block or
// an inventory emits exactly this, so it is a normal scene, not a corner.
//
// The assertions are the ones a screenshot cannot make on its own: that the
// block IS a scroll container (content wider than its box), and that nothing
// pushed the page itself sideways.
const SCENE = `She looks up as you enter.

\`\`\`
User hit points: 82
User exp: 65/100 (+20, tactical counter-attack against a superior foe)
User level: 1
Blight-Boar hit points: 2 (Near Death — Crippled, bleeding from the flank)
\`\`\`

| Item carried | How many | Condition after the fight | Where you got it |
| --- | --- | --- | --- |
| Iron rations | 3 | Dry but edible | Traded at the crossroads |
| Waterskin | 1 | Half empty | Filled at the last well |

The beast is dying, its violet eyes fading.`

test('stage: wide markdown scrolls inside the bubble on a phone', async ({ page }) => {
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
        messages: [{ role: 'assistant', content: [{ type: 'text', text: SCENE }] }],
      },
    },
    SMOKE_SESSION,
  )

  // Whether the content SCROLLS or merely spills is the whole question, and
  // scrollWidth > clientWidth does not answer it — that is true of an element
  // painting its overflow outside the box too, which is exactly the bug. Only a
  // scroll container can actually be scrolled, so ask it to move.
  const scrolls = (el: Element) => {
    el.scrollLeft = 9999
    const moved = el.scrollLeft > 0
    el.scrollLeft = 0
    return moved
  }

  const pre = page.locator('.stage-bubble pre')
  await expect(pre).toBeVisible()
  // Shot before the assertions, so a failing run still leaves the picture of
  // what went wrong.
  if (process.env.CODE_SHOT) await page.screenshot({ path: `${process.env.CODE_SHOT}-stage.png` })
  expect(await pre.evaluate(scrolls), 'the code block must scroll, not spill out of the bubble').toBe(true)

  // The table gets the same treatment — it has no wrapper to key on, so it is
  // styled separately and can regress on its own.
  const table = page.locator('.stage-bubble table')
  expect(await table.evaluate(scrolls), 'the table must scroll inside the bubble').toBe(true)

  // And nothing dragged the page sideways: the symptom the reporter saw was the
  // scene itself being wider than the phone.
  const page_ = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, win: window.innerWidth }))
  expect(page_.doc).toBeLessThanOrEqual(page_.win)

  if (process.env.CODE_SHOT) await page.screenshot({ path: `${process.env.CODE_SHOT}-stage.png` })
})
