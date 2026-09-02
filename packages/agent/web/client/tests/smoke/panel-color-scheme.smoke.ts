import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// The panel's light/dark/auto control (src/scheme.ts + the header button).
//
// The unit suite (src/scheme.test.ts) covers the preference and asserts the
// SHAPE of the CSS selector against the source. It cannot cover the thing that
// matters, because happy-dom applies no stylesheet and resolves no media query:
// there, `light` on a dark OS and `light` losing to a dark OS look identical.
// The whole point of the feature is that an explicit choice BEATS the OS, so
// that is measured here, against the built bundle, with the OS emulated.
//
// The palette literals are repeated below on purpose. Deriving them from the
// sheet would make this pass against any two colours that merely differ,
// including a half-applied palette; naming them means the test knows what dark
// IS. They are styles.css --c-bg-light / --c-bg-dark.
const LIGHT = 'rgb(255, 255, 255)'
const DARK = 'rgb(10, 10, 9)'

// styles.css --accent (birch tar) and the dark palette's --user. The glyph is
// coloured by WHICH mode is active, not by whether the default is overridden,
// so these two must differ from each other and from the neutral auto state.
const BIRCH_TAR = 'rgb(181, 101, 29)'
const USER_BLUE_DARK = 'rgb(59, 130, 246)'
const FG_DARK = 'rgb(233, 233, 236)'
const FG_LIGHT = 'rgb(22, 23, 26)'

const bg = (page: import('@playwright/test').Page) =>
  page.evaluate(() => getComputedStyle(document.body).backgroundColor)

const attr = (page: import('@playwright/test').Page) =>
  page.evaluate(() => document.documentElement.getAttribute('data-scheme'))

test('panel: an explicit light/dark choice overrides the OS, and auto returns to it', async ({ page }) => {
  await installMockBackend(page)
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/')

  // Fixture guard, and a regression in its own right: this button was nearly
  // gated on there being a session, which would have hidden it on the landing —
  // the first screen anyone sees, and the one where a panel in the wrong colour
  // is most likely to be noticed. Assert it is HERE before measuring anything.
  await expect(page.locator('.landing')).toBeVisible()
  const button = page.locator('button[title^="Theme:"]')
  await expect(button).toBeVisible()

  // Auto, dark OS: the behaviour that existed before the control did.
  expect(await attr(page)).toBe('auto')
  expect(await bg(page), 'auto on a dark OS must be dark').toBe(DARK)
  await expect(button).toHaveAttribute('title', /Auto/)

  // Auto → light. The OS is still dark; the choice must win.
  await button.click()
  expect(await attr(page)).toBe('light')
  expect(await bg(page), 'a chosen light must beat a dark OS').toBe(LIGHT)
  expect(
    await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme),
    'color-scheme must follow too, or form controls stay dark',
  ).toBe('light')

  // Light → dark, now with a LIGHT OS, so the mirror case is measured and not
  // assumed: chosen dark must beat a light OS just as chosen light beat a dark.
  await page.emulateMedia({ colorScheme: 'light' })
  expect(await bg(page), 'the OS must not override an explicit light either').toBe(LIGHT)
  await button.click()
  expect(await attr(page)).toBe('dark')
  expect(await bg(page), 'a chosen dark must beat a light OS').toBe(DARK)
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe('dark')

  // Dark → auto, closing the cycle. Auto is not a third palette: it is the
  // absence of an override, so it must TRACK the OS, changing when the OS does.
  await button.click()
  expect(await attr(page)).toBe('auto')
  expect(await bg(page), 'auto on a light OS must be light').toBe(LIGHT)
  await page.emulateMedia({ colorScheme: 'dark' })
  expect(await bg(page), 'auto must follow the OS when it changes, with no click').toBe(DARK)
})

test('panel: the glyph is coloured by which mode is active', async ({ page }) => {
  await installMockBackend(page)
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/')
  await expect(page.locator('.landing')).toBeVisible()

  const button = page.locator('button[title^="Theme:"]')
  await expect(button).toBeVisible()
  const colour = () => button.evaluate((el) => getComputedStyle(el).color)

  // Auto is the neutral state and must NOT be tinted: the colour is what says a
  // choice has been made, so tinting auto would claim one that has not.
  await expect(button).toHaveClass(/scheme-auto/)
  expect(await colour(), 'auto must inherit the plain --fg').toBe(FG_DARK)

  // Every assertion below runs while the pointer still rests on the button from
  // the click, so .icon:hover is live. That is deliberate. The hover rule sets
  // color: var(--fg) at the SAME specificity as these rules, so only their being
  // later in the sheet keeps the glyph coloured. Move them above .icon:hover and
  // this test fails, which is the point.
  await button.click()
  await expect(button).toHaveClass(/scheme-light/)
  expect(await colour(), 'the sun must be birch tar, and survive hover').toBe(BIRCH_TAR)

  await button.click()
  await expect(button).toHaveClass(/scheme-dark/)
  expect(await colour(), 'the moon must be the --user blue, and survive hover').toBe(USER_BLUE_DARK)

  // The two modes must be told apart by colour, not merely both be tinted.
  expect(BIRCH_TAR).not.toBe(USER_BLUE_DARK)

  await button.click()
  await page.emulateMedia({ colorScheme: 'light' })
  await expect(button).toHaveClass(/scheme-auto/)
  expect(await colour(), 'auto must go back to neutral, tracking the OS').toBe(FG_LIGHT)
})

test('panel: the choice survives a reload', async ({ page }) => {
  await installMockBackend(page)
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/')

  const button = page.locator('button[title^="Theme:"]')
  await expect(button).toBeVisible()
  await button.click()
  expect(await attr(page)).toBe('light')

  // A stored choice is only useful if it is applied before the first render.
  // The OS is dark, so a reload that forgot — or that applied the choice late —
  // shows dark here.
  await page.reload()
  await expect(page.locator('.landing')).toBeVisible()
  expect(await attr(page), 'the stored choice must be re-applied on boot').toBe('light')
  expect(await bg(page)).toBe(LIGHT)
  await expect(button, 'the button must show the state it booted into').toHaveAttribute('title', /Light/)
})
