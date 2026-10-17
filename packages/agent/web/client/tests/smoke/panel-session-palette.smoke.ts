import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installMockBackend, panelSessionURL } from './support'

// The session quick switcher (features/sessions/SessionPalette.tsx).
//
// The behaviour is covered in happy-dom (SessionPalette.test.tsx). Two things
// are not visible there. A real browser decides whether Ctrl+K reaches the page
// at all, and from inside the composer too. happy-dom also has no layout, so a
// palette wider than a phone looks the same as one that fits.

const SESSIONS = [
  { id: SMOKE_SESSION, current: true, title: 'the session you are in', model: 'gpt-5.6-sol', messages: 58 },
  { id: 'flaky', title: 'Fix the flaky procgroup test', live: true, busy: true, waiting: 'approval', model: 'gpt-5.6-sol', messages: 18 },
  { id: 'kickoff', title: 'Talkoot kickoff race on Windows', live: true, model: 'gpt-5.6-sol', messages: 61 },
  { id: 'neot/20261011-090000-1a2b3c4d', origin: 'neot', title: 'Disk usage report, fleet sweep', model: 'gpt-5.6-sol', messages: 7 },
]

const mock = (page: import('@playwright/test').Page) =>
  installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') return { sessions: SESSIONS }
      if (method === 'sessiongroups.list') return { groups: [] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })

test('panel: Ctrl+K from the composer jumps to another session', async ({ page }) => {
  await mock(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('.topbar .sess-title')).toHaveText('the session you are in')

  // From the composer, where the person is typing when the thought arrives.
  await page.locator('footer.composer textarea').focus()
  await page.keyboard.press('Control+k')
  const dialog = page.getByRole('dialog', { name: 'Jump to a session' })
  await expect(dialog).toBeVisible()
  const input = dialog.getByRole('combobox')
  await expect(input).toBeFocused()

  // The waiting session is first and starts selected.
  await expect(dialog.getByRole('option').first()).toContainText('Fix the flaky')
  await expect(dialog.getByRole('option').first()).toHaveAttribute('aria-selected', 'true')
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-desktop.png` })

  await input.fill('kick')
  await expect(dialog.getByRole('option')).toHaveCount(1)
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page.locator('.topbar .sess-title')).toHaveText('Talkoot kickoff race on Windows')
})

test('panel: Escape closes the quick switcher and gives focus back', async ({ page }) => {
  await mock(page)
  await page.goto(panelSessionURL)
  const ta = page.locator('footer.composer textarea')
  await ta.focus()
  await page.keyboard.press('Control+k')
  await expect(page.getByRole('dialog', { name: 'Jump to a session' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Jump to a session' })).toBeHidden()
  await expect(ta).toBeFocused()
  await expect(page.locator('.topbar .sess-title')).toHaveText('the session you are in')
})

test('panel: Enter on the focused board button opens the board, not a session', async ({ page }) => {
  await mock(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('.topbar .sess-title')).toHaveText('the session you are in')
  await page.keyboard.press('Control+k')
  const dialog = page.getByRole('dialog', { name: 'Jump to a session' })
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(dialog.getByRole('button', { name: 'Open the board' })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page.locator('.board-view')).toBeVisible()
  await expect(page.locator('.topbar .sess-title')).toHaveText('the session you are in')
})

test('panel: on a phone the header control opens a quick switcher that fits', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mock(page)
  await page.goto(panelSessionURL)
  // ⚠️ The phone topbar settles a few pixels lower once the session's title
  // and model arrive. A click aimed before that lands on the wordmark above,
  // which leaves for the landing, so wait for the row to fill first.
  await expect(page.locator('.topbar .sess-title')).toHaveText('the session you are in')
  await expect(page.locator('.topbar .model-btn')).toBeVisible()
  await page.getByRole('button', { name: 'Jump to a session' }).click()
  const dialog = page.getByRole('dialog', { name: 'Jump to a session' })
  await expect(dialog).toBeVisible()

  const box = (await dialog.boundingBox())!
  expect(box.x, 'the palette starts on screen').toBeGreaterThanOrEqual(0)
  expect(box.x + box.width, 'the palette ends on screen').toBeLessThanOrEqual(390)
  // A row carries chips; none may push past the palette's edge.
  const over = await dialog
    .locator('.spal-row')
    .evaluateAll((rows, right) => rows.map((r) => r.getBoundingClientRect().right - right).filter((d) => d > 0.5), box.x + box.width)
  expect(over, 'a row must fit the palette').toEqual([])
  const scroll = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(scroll, 'the page must not scroll sideways').toBeLessThanOrEqual(390)

  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-phone.png` })
})
