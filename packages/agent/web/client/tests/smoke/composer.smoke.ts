import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// Flow 3: the slash-command autocomplete preserves focus and keyboard behavior.
// Focus stealing and swallowed keystrokes are classic real-browser regressions
// that a DOM-only test misses.
test('slash autocomplete: keeps focus, navigates with keys, Escape dismisses', async ({ page }) => {
  await installMockBackend(page)
  await page.goto(panelSessionURL)

  const ta = page.locator('footer.composer textarea')
  await ta.click()
  await ta.pressSequentially('/')

  const menu = page.locator('.slash-menu[role="listbox"]')
  await expect(menu).toBeVisible()
  await expect(page.locator('.slash-item[role="option"]').first()).toBeVisible()

  // Typing into the composer must not have moved focus off the textarea.
  await expect(ta).toBeFocused()

  // ArrowDown selects a row (the selected option carries the `sel` class).
  await ta.press('ArrowDown')
  await expect(page.locator('.slash-item.sel')).toHaveCount(1)

  // Escape closes the menu but keeps the typed text and focus.
  await ta.press('Escape')
  await expect(menu).toBeHidden()
  await expect(ta).toBeFocused()
  await expect(ta).toHaveValue('/')
})
