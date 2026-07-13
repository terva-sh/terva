import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Flow 1: the shell renders at both desktop and narrow widths, and reaches the
// connected state. A blank screen or a shell that only lays out at one width is
// the kind of break happy-dom can't catch.
const viewports = [
  { name: 'desktop', size: { width: 1280, height: 800 } },
  { name: 'narrow', size: { width: 390, height: 780 } },
]

for (const vp of viewports) {
  test(`renders the control-panel shell at ${vp.name} width`, async ({ page }) => {
    await page.setViewportSize(vp.size)
    await installMockBackend(page)
    await page.goto('/')

    await expect(page.locator('.topbar')).toBeVisible()
    await expect(page.locator('footer.composer textarea')).toBeVisible()
    await expect(page.locator('.log')).toBeVisible()
    // The mock hello flips the status dot from connecting (amber) to open (green).
    await expect(page.locator('.topbar .dot.open')).toBeVisible()
  })
}
