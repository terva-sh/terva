import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The toast's placement and its colour, measured in a real browser.
//
// The unit suite (src/features/interactions/Toast.test.tsx) covers the lifetime
// rules and asserts the SHAPE of the stylesheet against its source. It cannot
// cover either of the things here: happy-dom applies no stylesheet, does no
// layout, and returns 0 for every box — so the old `bottom: 80px` and a
// correctly measured offset are indistinguishable there, and so are three kinds
// that all resolve to the same red. Both need pixels.
//
// styles.css --danger. Named rather than derived from the sheet: a test that
// reads the colour it then asserts passes against any two values that merely
// differ, including a palette applied by halves.
const DANGER = 'rgb(209, 69, 59)'

const box = async (page: import('@playwright/test').Page, selector: string) => {
  const b = await page.locator(selector).boundingBox()
  if (!b) throw new Error(`no bounding box for ${selector}`)
  return b
}

// The reported bug: a toast raised while the composer is tall was drawn over
// the input, because its offset was a constant standing in for a height that
// changes.
test('toast: clears the composer however tall the composer has grown', async ({ page }) => {
  const mock = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await mock.subscribed

  const composer = page.locator('footer.composer')
  await expect(composer).toBeVisible()

  // An empty composer first, to measure what "grew" is relative to. This is
  // also the only state the old constant was ever right for.
  const empty = await box(page, 'footer.composer')

  // Grow it the way the screenshot did: a long multi-line draft. The textarea
  // autogrows to 40% of the viewport, so this is the composer at close to its
  // full height.
  const ta = page.locator('footer.composer textarea')
  await ta.fill(
    Array.from({ length: 14 }, (_, i) => `line ${i + 1}: a paragraph of a draft that is still being written`).join(
      '\n',
    ),
  )

  // FIXTURE GUARD. Everything below is vacuous if the composer did not actually
  // grow — an assertion that a toast clears a 60px composer is satisfied by the
  // very bug it is meant to catch. It has to be taller than the 80px constant
  // that used to stand in for it, by a margin no rounding can close.
  //
  // Polled: fill() resolves before the autogrow effect has re-laid the textarea
  // out, so reading the height straight after it measures the composer as it was
  // a frame ago. Read once, this guard failed roughly one run in five — and a
  // FLAKY guard is worse than none, because the runs it passes are the ones
  // where it measured nothing.
  const composerHeight = async () => (await box(page, 'footer.composer')).height
  await expect
    .poll(composerHeight, { message: 'the composer did not grow; the assertions below prove nothing' })
    .toBeGreaterThan(Math.max(empty.height + 100, 200))
  const grown = await box(page, 'footer.composer')

  // An error toast, because it has no clock and cannot expire mid-measurement.
  mock.pushEvent({ type: 'error', error: 'the provider refused the request' })
  const toast = page.locator('.toast')
  await expect(toast).toBeVisible()

  // Polled, not read once. The offset is published by a ResizeObserver, which
  // fires a frame AFTER the composer resizes — a single immediate read races it
  // and fails about one run in four under parallel workers. Polling waits for
  // the layout to settle instead of asserting into the middle of it.
  const overlap = async () => {
    const t = await box(page, '.toast')
    const c = await box(page, 'footer.composer')
    return t.y + t.height - c.y
  }

  // THE regression, in one line. Against `bottom: 80px` the toast's lower edge
  // landed roughly 200px INSIDE the composer, so this number was large and
  // positive; it must be at or below zero.
  await expect
    .poll(overlap, { message: 'the toast must sit above the composer, not over it' })
    .toBeLessThanOrEqual(0)

  // And it must still be on screen: an offset that tracks the composer could
  // just as easily push the toast off the top of a short viewport.
  const raised = await box(page, '.toast')
  expect(raised.y).toBeGreaterThan(0)

  // The composer shrinking must bring the toast back down with it, or the fix
  // is one-way and leaves a gap the size of the largest draft of the session.
  await ta.fill('short')
  await expect.poll(composerHeight).toBeLessThan(grown.height - 100)
  await expect
    .poll(async () => (await box(page, '.toast')).y, {
      message: 'the toast should have followed the composer back down',
    })
    .toBeGreaterThan(raised.y + 100)
  // Still clear of it at the new height, so following it down did not overshoot
  // into the composer from the other direction.
  await expect.poll(overlap).toBeLessThanOrEqual(0)
})

test('toast: colour says which kind it is, and only an error waits to be dismissed', async ({ page }) => {
  const mock = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await mock.subscribed

  const toast = page.locator('.toast')
  const bg = () => toast.evaluate((el) => getComputedStyle(el).backgroundColor)

  // A hint, raised by a slash command used wrongly. Before this change it
  // arrived in the same red box as a failed request — which is what the
  // screenshot in the bug report shows.
  const ta = page.locator('footer.composer textarea')
  await ta.click()
  await ta.pressSequentially('/skill')
  // Escape first: typing a slash opens the command menu, and Enter with that
  // menu open picks a row rather than submitting (see composer.smoke.ts).
  await ta.press('Escape')
  await expect(page.locator('.slash-menu')).toBeHidden()
  await ta.press('Enter')
  await expect(toast).toHaveText('Usage: /skill <name> [task]')
  expect(await bg(), 'a usage hint must not be dressed as a failure').not.toBe(DANGER)
  await expect(toast).toHaveClass(/toast--note/)

  // An error, which is red and stays.
  mock.pushEvent({ type: 'error', error: 'the provider refused the request' })
  await expect(toast).toHaveText('the provider refused the request')
  expect(await bg()).toBe(DANGER)
  await expect(toast).toHaveClass(/toast--error/)

  // Clicking is still how an error goes away.
  await toast.click()
  await expect(toast).toHaveCount(0)

  // The empty dock is still in the DOM — it is the live region — so it must not
  // be eating clicks over the transcript.
  const dock = page.locator('.toast-dock')
  await expect(dock).toHaveCount(1)
  expect(await dock.evaluate((el) => getComputedStyle(el).pointerEvents)).toBe('none')
})

// The lifetime rules — a note expires, an error does not — are NOT tested here.
// They are covered deterministically by fake timers in Toast.test.tsx, and the
// browser adds nothing to them that is worth what it costs to run them here.
//
// The attempt is worth recording, because the failure looks like a product bug
// and is not one. Driving the timeout needs page.clock, or the suite waits
// fifteen real seconds per assertion. But the panel keeps several polling
// intervals running (sessions.list every 4s, among others), so a clock.runFor
// long enough to expire a toast also fires dozens of those, and afterwards the
// clock is left paused — which starves Playwright's OWN polling. The failure
// that produced was `toBeVisible` timing out on an element that the page
// snapshot showed present and correct, which reads as "the error toast
// disappeared" when in fact nothing had disappeared at all.
//
// If this is ever wanted in a browser, the thing to reach for is a shorter
// timeout injected for the test, not a longer clock advance.
