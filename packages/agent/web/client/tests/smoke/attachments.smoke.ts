import { test, expect } from '@playwright/test'
import { PNG_1x1_BASE64, SMOKE_SESSION, installMockBackend, panelSessionURL } from './support'

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
  await page.goto(panelSessionURL)
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dispatchImage(page, 'drop')

  await expect(page.locator('.composer-chip')).toHaveCount(1)
  await expect(page.locator('.composer-chip img')).toBeVisible()
})

test('pasting an image adds an attachment chip', async ({ page }) => {
  await installMockBackend(page)
  await page.goto(panelSessionURL)
  const ta = page.locator('footer.composer textarea')
  await ta.click()

  await dispatchImage(page, 'paste')

  await expect(page.locator('.composer-chip')).toHaveCount(1)
})

// Flow 4b: a NON-image reaching the composer. The DataTransfer plumbing is the
// same as above, but this path also crosses fetch() to the upload route, so the
// mock has to serve /upload as well as the socket.
//
// It is here rather than in the vitest suite for the reason that suite exists:
// happy-dom renders no layout, and the file chip is the one piece of this
// feature whose failure mode is visual — a name long enough to push the row, a
// × sitting on top of the text. The width assertion below is what a component
// test cannot make.
async function dropFile(page: import('@playwright/test').Page, name: string, type: string) {
  await page.evaluate(
    ({ name, type }) => {
      const file = new File([new Uint8Array(64)], name, { type })
      const dt = new DataTransfer()
      dt.items.add(file)
      const footer = document.querySelector('footer.composer') as HTMLElement
      footer.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }))
      footer.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }))
    },
    { name, type },
  )
}

test('dropping a document uploads it and chips it by name', async ({ page }) => {
  await installMockBackend(page, { features: ['attachments'], maxAttachmentBytes: 100 << 20 })
  const uploads: string[] = []
  await page.route('**/upload*', async (route) => {
    uploads.push(new URL(route.request().url()).searchParams.get('sess') ?? '')
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: 'att_1', name: 'filters.xml', mime: 'application/xml', kind: 'document', size: 64 }),
    })
  })
  await page.goto(panelSessionURL)
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dropFile(page, 'filters.xml', 'application/xml')

  const chip = page.locator('.composer-chip--file')
  await expect(chip).toHaveCount(1)
  await expect(chip.locator('.chip-name')).toHaveText('filters.xml')
  // The upload is addressed to the session the composer is on — not the blank
  // "current session" shorthand, which resolves daemon-side to something the
  // staged file would not be found under.
  expect(uploads).toEqual([SMOKE_SESSION])
})

// The visual guard. A file chip sizes to its label, so an unbounded name would
// push the composer row wide and scroll the page sideways — the class of defect
// that has shipped past a green component suite before.
test('a long attachment name truncates instead of widening the composer', async ({ page }) => {
  await installMockBackend(page, { features: ['attachments'], maxAttachmentBytes: 100 << 20 })
  const longName = 'quarterly-mailbox-filter-export-with-a-very-long-name-indeed-2026.xml'
  await page.route('**/upload*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: 'att_1', name: longName, mime: 'application/xml', kind: 'document', size: 64 }),
    }),
  )
  await page.goto(panelSessionURL)
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dropFile(page, longName, 'application/xml')
  await expect(page.locator('.composer-chip--file')).toHaveCount(1)

  const composer = page.locator('footer.composer')
  const chipWidth = (await page.locator('.composer-chip--file').boundingBox())!.width
  const composerWidth = (await composer.boundingBox())!.width
  expect(chipWidth).toBeLessThanOrEqual(composerWidth)
  // And the page itself must not have gained a horizontal scrollbar.
  const overflows = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  )
  expect(overflows).toBe(false)
})

// The remove button is positioned for the 56px SQUARE image chip (top corner),
// which on a chip only as tall as one line of text left it riding 6px above
// centre and sitting on top of the size text. Both were invisible to every
// assertion in this file and to the whole component suite — they were found by
// screenshotting it. Measure them, so they cannot come back.
test('the remove button is centred on a file chip and clear of its text', async ({ page }) => {
  await installMockBackend(page, { features: ['attachments'], maxAttachmentBytes: 100 << 20 })
  await page.route('**/upload*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: 'att_1', name: 'debug.log', mime: 'text/plain', kind: 'document', size: 402 }),
    }),
  )
  await page.goto(panelSessionURL)
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dropFile(page, 'debug.log', 'text/plain')
  const chip = page.locator('.composer-chip--file')
  await expect(chip).toHaveCount(1)

  const chipBox = (await chip.boundingBox())!
  const closeBox = (await chip.locator('.chip-x').boundingBox())!
  const sizeBox = (await chip.locator('.chip-size').boundingBox())!

  const chipCentre = chipBox.y + chipBox.height / 2
  const closeCentre = closeBox.y + closeBox.height / 2
  expect(Math.abs(closeCentre - chipCentre)).toBeLessThanOrEqual(1)
  // The label must end before the button starts — an overlap is unreadable text
  // and an unclickable corner, which is what a 24px right padding produced.
  expect(closeBox.x).toBeGreaterThan(sizeBox.x + sizeBox.width)
})

// Silently discarding the file is the behavior this feature removes, so a daemon
// that never advertised an upload route must say so rather than reintroduce it.
test('a daemon without the upload route refuses the drop out loud', async ({ page }) => {
  await installMockBackend(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('footer.composer textarea')).toBeVisible()

  await dropFile(page, 'notes.txt', 'text/plain')

  await expect(page.getByText('This daemon cannot take file attachments')).toBeVisible()
  await expect(page.locator('.composer-chip')).toHaveCount(0)
})

// The transcript half. A message carrying attachments arrives with the host's
// preamble as its first text block — machine prose ending in an absolute staging
// path, which the model needs and which wrapped to nine lines above the user's
// own two on a phone. The panel renders labels instead.
const PREAMBLE =
  '(the user attached files, saved locally — read them with your tools if relevant:\n' +
  '  /Users/u/Library/Application Support/terva/attachments/20260728-140233-a1b2c3d4/att_bf2c-mail-filters-export.xml — document, application/xml, 12403 bytes\n' +
  ')\n'

async function seedAttachedMessage(page: import('@playwright/test').Page, backend: Awaited<ReturnType<typeof installMockBackend>>) {
  await backend.subscribed
  backend.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'mail', experience: 'code' },
        epoch: 1, base: 0, total: 2,
        messages: [
          {
            role: 'user',
            content: [{ type: 'text', text: PREAMBLE }, { type: 'text', text: 'Can you check these filters?' }],
            attachments: [{ name: 'mail-filters-export.xml', kind: 'document', mime: 'application/xml', size: 12403 }],
            // The daemon's own signal that content[0] is its prose and not the
            // user's. Separate from the list above, because a message can carry
            // the preamble with nothing left to label — see the expired case.
            preamble: true,
          },
          { role: 'assistant', content: [{ type: 'text', text: 'Read it — three rules, all archive-only.' }] },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('text=archive-only').waitFor()
}

test('the transcript labels the attachment and hides the staging path', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await seedAttachedMessage(page, backend)

  await expect(page.locator('.msg-file__name')).toHaveText('mail-filters-export.xml')
  await expect(page.locator('.msg-file__size')).toHaveText('12.1 KB')
  await expect(page.locator('.msg.user')).toContainText('Can you check these filters?')
  // The path is the model's copy, not the reader's.
  await expect(page.locator('.msg.user')).not.toContainText('/Library/Application Support/terva/attachments/')
  await expect(page.locator('.msg.user')).not.toContainText('read them with your tools')
})

// Inert by contract: the staged bytes are swept on a TTL, so an affordance here
// would fail more often than it worked. Sending files the other way is a
// separate flow and must not quietly land on this element.
test('the attachment label is not interactive', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await seedAttachedMessage(page, backend)

  const label = page.locator('.msg-file')
  await expect(label).toHaveCount(1)
  await expect(label.locator('a, button')).toHaveCount(0)
  await expect(label.evaluate((e) => e.tagName)).resolves.toBe('SPAN')
})

// The copy button floats over the bubble's top-right, directly above this row.
// Before the row reserved its width the size text rendered underneath the icon —
// a 6.8px overlap, invisible to every assertion above.
test('the attachment label clears the copy button', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await seedAttachedMessage(page, backend)

  const size = (await page.locator('.msg-file__size').boundingBox())!
  const copy = (await page.locator('.user-wrap button').first().boundingBox())!
  expect(size.x + size.width).toBeLessThanOrEqual(copy.x)
})

// A staged file is allowed to lapse out from under the message that named it —
// a 24-hour TTL against a composer left open on a phone is all it takes. The
// daemon still sends the preamble, because the MODEL has to be told it is not
// getting the files, so a panel that keys suppression off the label list drops
// nothing and prints the manifest as the user's own words.
//
// The visual claim, which no unit assertion reaches: the bubble reads as the
// question with a lapsed-files note, not as machine prose.
const EXPIRED_PREAMBLE =
  '(the user attached files, saved locally — read them with your tools if relevant:\n' +
  '  (2 further attachment(s) are no longer on disk — ask the user to re-attach them if you need them)\n' +
  ')\n'

test('a message whose attachments all expired says so instead of printing the manifest', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await backend.subscribed
  backend.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'mail', experience: 'code' },
        epoch: 1, base: 0, total: 2,
        messages: [
          {
            role: 'user',
            content: [{ type: 'text', text: EXPIRED_PREAMBLE }, { type: 'text', text: 'Can you check these filters?' }],
            preamble: true,
            attachments_missing: 2,
          },
          { role: 'assistant', content: [{ type: 'text', text: 'I cannot see them — re-attach?' }] },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('text=re-attach?').waitFor()

  const bubble = page.locator('.msg.user')
  await expect(bubble).toContainText('Can you check these filters?')
  await expect(bubble).not.toContainText('read them with your tools')
  await expect(bubble).not.toContainText('no longer on disk')
  // Said out loud, not silently omitted: a message showing nothing would leave
  // the user comparing the answer against files they believe they sent.
  await expect(page.locator('.msg-file--gone')).toHaveText('2 attachments had expired')
  await expect(page.locator('.msg-file__name')).toHaveCount(0)
})
