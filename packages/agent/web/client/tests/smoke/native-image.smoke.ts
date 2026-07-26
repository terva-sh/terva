import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// Flow: an image the ASSISTANT drew inline (native image output) renders in the
// transcript. Agent-generated images ride the finalized assistant_message, not
// the text deltas (store.applyEvent, case 'assistant_message'), so this covers a
// path the composer-attachment smoke does not — and one only a real browser can,
// since it needs the data: URL to actually decode to a raster.
test('assistant-emitted inline image renders in the transcript', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('.topbar .dot.open')).toBeVisible()
  await backend.subscribed

  // Draw a visible test image in-browser and hand its base64 to the mock event,
  // exactly as an agent-generated image arrives on a finalized message.
  const dataB64 = await page.evaluate(() => {
    const c = document.createElement('canvas')
    c.width = 240
    c.height = 240
    const ctx = c.getContext('2d')!
    ctx.fillStyle = '#f5f5f5'
    ctx.fillRect(0, 0, 240, 240)
    ctx.fillStyle = '#ee0000'
    ctx.beginPath()
    ctx.arc(120, 120, 96, 0, Math.PI * 2)
    ctx.fill()
    return c.toDataURL('image/png').split(',')[1]
  })

  backend.pushEvent({
    type: 'assistant_message',
    message: {
      role: 'assistant',
      content: [
        { type: 'text', text: 'Here is your red circle:' },
        { type: 'image', mime_type: 'image/png', data: dataB64, bytes: 0 },
      ],
    },
  })

  const img = page.locator('.msg-image')
  await expect(img).toBeVisible()
  // naturalWidth > 0 proves the data: URL actually decoded to a raster.
  await expect.poll(() => img.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
})
