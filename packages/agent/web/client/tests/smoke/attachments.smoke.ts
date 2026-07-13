import { test, expect } from '@playwright/test'
import { installMockBackend, PNG_1x1_BASE64 } from './support'

// Flow 4: an image reaching the composer via drop (and paste) is accepted and
// rendered as an attachment chip. Both paths funnel through the same addFiles →
// fileToAttachment callback; DataTransfer/ClipboardEvent plumbing only really
// exists in a browser, so this can't be exercised under happy-dom.

async function dispatchImage(page: import('@playwright/test').Page, kind: 'drop' | 'paste') {
  await page.evaluate(
    ({ b64, kind }) => {
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
      const file = new File([bytes], 'pixel.png', { type: 'image/png' })
      const dt = new DataTransfer()
      dt.items.add(file)
      if (kind === 'drop') {
        const footer = document.querySelector('footer.composer') as HTMLElement
        footer.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }))
        footer.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }))
      } else {
        const ta = document.querySelector('footer.composer textarea') as HTMLTextAreaElement
        ta.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
      }
    },
    { b64: PNG_1x1_BASE64, kind },
  )
}

test('dropping an image adds an attachment chip', async ({ page }) => {
  await installMockBackend(page)
  await page.goto('/')
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dispatchImage(page, 'drop')

  await expect(page.locator('.composer-chip')).toHaveCount(1)
  await expect(page.locator('.composer-chip img')).toBeVisible()
})

test('pasting an image adds an attachment chip', async ({ page }) => {
  await installMockBackend(page)
  await page.goto('/')
  const ta = page.locator('footer.composer textarea')
  await ta.click()

  await dispatchImage(page, 'paste')

  await expect(page.locator('.composer-chip')).toHaveCount(1)
})
