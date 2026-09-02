import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// /nextstep in a real browser: the command reaches the daemon, the answer comes
// back as a strip above the composer, and Tab moves it into the textarea
// without sending anything.
//
// The component tests cover the composer's half in isolation. What only a
// browser can show is the whole path joined up -- that typing the command
// actually dispatches it, that the on_demand flag rides the request, and that
// the strip lands above the composer rather than somewhere off-screen.

const OFFER = 'run the failing test again'

async function openPanel(page: import('@playwright/test').Page, line = OFFER) {
  const seen: unknown[] = []
  const backend = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'suggest.next_step') {
        seen.push(params)
        return { line }
      }
      // Everything else falls through to the harness's own defaults.
      return undefined
    },
  })
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()
  await backend.subscribed
  return { backend, seen }
}

const composer = (page: import('@playwright/test').Page) => page.locator('.composer textarea')

async function runNextStep(page: import('@playwright/test').Page) {
  const box = composer(page)
  await box.fill('/nextstep')
  await box.press('Enter')
}

test('/nextstep offers a line above the composer and Tab takes it', async ({ page }) => {
  const { seen } = await openPanel(page)

  await runNextStep(page)

  await page.locator('.composer-offer').waitFor()
  await expect(page.locator('.composer-offer__line')).toHaveText(OFFER)

  // The flag exists so the daemon frames the question honestly: the idle prompt
  // tells the model the user "has not asked you for anything", which on this
  // path is false. If it were dropped in transit nothing would look broken.
  expect(seen).toContainEqual(expect.objectContaining({ on_demand: true }))

  await composer(page).press('Tab')

  await expect(composer(page)).toHaveValue(OFFER)
  // Taken, so the offer is gone -- and nothing was sent: the line is sitting in
  // the composer waiting for the user to decide.
  await expect(page.locator('.composer-offer')).toHaveCount(0)
  await expect(page.locator('.msg.user')).toHaveCount(0)
})

test('Escape discards the offer and leaves the composer alone', async ({ page }) => {
  await openPanel(page)

  await runNextStep(page)
  await page.locator('.composer-offer').waitFor()

  await composer(page).press('Escape')

  await expect(page.locator('.composer-offer')).toHaveCount(0)
  await expect(composer(page)).toHaveValue('')
})

test('an empty answer offers nothing rather than an empty strip', async ({ page }) => {
  // The daemon invites the model to stay quiet when no next step is obvious,
  // and an empty line is an ordinary answer rather than a failure. A strip with
  // nothing in it would be a worse outcome than silence.
  const { seen } = await openPanel(page, '   ')

  await runNextStep(page)

  // Assert the ASK happened first. Without this the test passes on a client
  // that has no /nextstep at all -- "no strip appeared" is trivially true when
  // nothing was ever requested, and an absence-only assertion cannot tell the
  // two apart. Checked: reverting the feature makes this line fail, where the
  // count assertion below still passed.
  await expect.poll(() => seen.length).toBeGreaterThan(0)

  await expect(page.locator('.composer-offer')).toHaveCount(0)
})
