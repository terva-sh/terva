import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The browser's extension config form.
//
// Before this the pane advertised `has_config` and offered nothing behind it:
// the schema and saved values were local-disk reads, and a browser has no disk.
// A deployed web-only agent therefore had no way to reach its own settings.
//
// Rendered in a real browser because the failure modes here are visual: a
// secret that shows its stored value, a description clipped to nothing, a form
// that overflows its card. vitest sees none of those.
//
// Set EC_SHOT=<dir> to capture screenshots.
const EXTS = {
  extensions: [
    {
      name: 'jmap-mail',
      version: '0.20.1',
      status: 'running',
      enabled: true,
      scope: 'session',
      language: 'go',
      description: 'Mail over JMAP.',
      tools: 12,
      commands: 3,
      has_config: true,
      config: [
        {
          key: 'api_key',
          label: 'API key',
          type: 'secret',
          secret: true,
          required: true,
          has_saved: true,
          description: 'Credential for the JMAP endpoint. Stored on the host and never sent to this page.',
        },
        {
          key: 'enable_sieve_tools',
          label: 'Enable Sieve tools',
          type: 'bool',
          saved: 'false',
          description: 'Expose server-side filter management as tools.',
        },
        {
          key: 'mailbox',
          label: 'Default mailbox',
          type: 'select',
          options: ['INBOX', 'Archive'],
          default: 'INBOX',
          saved: 'Archive',
        },
      ],
    },
  ],
}

function backend(page: import('@playwright/test').Page, sent: { last?: Record<string, string> }) {
  return installMockBackend(page, {
    respond(method, params) {
      if (method === 'surfaces.list')
        return { surfaces: [{ id: 'extensions', title: 'Extensions', icon: '🧩', kind: 'extensions', actions: true }] }
      if (method === 'surface.get')
        return { surface: { id: 'extensions', title: 'Extensions', kind: 'extensions', extensions: EXTS } }
      if (method === 'surface.action') {
        sent.last = (params as { args?: Record<string, string> })?.args
        return {}
      }
      return undefined
    },
  })
}

async function openExtensions(page: import('@playwright/test').Page) {
  await page.locator('.topbar .dot.open').waitFor()
  await page.locator('button[title="Panes (usage, settings, extensions)"]').click()
  const rail = page.locator('.pane-rail')
  await rail.waitFor()
  await rail.locator('.pane-tab', { hasText: 'Extensions' }).first().click()
  return rail
}

test('an extension declaring a schema can be configured from the browser', async ({ page }) => {
  const sent: { last?: Record<string, string> } = {}
  const mock = await backend(page, sent)
  await page.goto(panelSessionURL)
  await mock.subscribed
  await openExtensions(page)

  await page.getByRole('button', { name: 'Configure' }).click()
  await expect(page.locator('.ext-cfg-field')).toHaveCount(3)

  // A secret seeds EMPTY even though one is stored, and says so rather than
  // looking unset — otherwise the only safe read is "it has no key".
  const secret = page.locator('#cfg-jmap-mail-api_key')
  await expect(secret).toHaveValue('')
  await expect(secret).toHaveAttribute('type', 'password')
  await expect(secret).toHaveAttribute('placeholder', /leave blank to keep/)

  // Non-secret values seed from what is stored.
  await expect(page.locator('#cfg-jmap-mail-mailbox')).toHaveValue('Archive')

  if (process.env.EC_SHOT) {
    await page.screenshot({ path: `${process.env.EC_SHOT}/ext-config.png`, fullPage: true })
  }

  // Flip the boolean that the fleet had to stop a service to change, and save.
  await page.locator('#cfg-jmap-mail-enable_sieve_tools').click()
  await page.getByRole('button', { name: 'Save' }).click()

  await expect.poll(() => sent.last?.name).toBe('jmap-mail')
  const values = JSON.parse(sent.last?.values ?? '{}')
  expect(values.enable_sieve_tools).toBe('true')
  // The blank secret rides as an empty string, which the host reads as
  // "leave it alone". It must never carry a value this page was never given.
  expect(values.api_key).toBe('')
})

test('the config form stays readable on a phone', async ({ page }) => {
  const sent: { last?: Record<string, string> } = {}
  const mock = await backend(page, sent)
  await page.setViewportSize({ width: 390, height: 780 })
  await page.goto(panelSessionURL)
  await mock.subscribed
  await openExtensions(page)
  await page.getByRole('button', { name: 'Configure' }).click()
  await expect(page.locator('.ext-cfg-field')).toHaveCount(3)

  // Field descriptions are the only explanation of what a setting does; a
  // clipped one is worse than none.
  const clipped = await page.evaluate(() => {
    for (const el of Array.from(document.querySelectorAll('.ext-cfg-desc')) as HTMLElement[]) {
      if (el.scrollWidth > el.clientWidth + 1) return el.textContent?.slice(0, 40) ?? 'clipped'
    }
    return ''
  })
  expect(clipped).toBe('')
  const overflowX = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  )
  expect(overflowX).toBe(false)

  if (process.env.EC_SHOT) {
    await page.screenshot({ path: `${process.env.EC_SHOT}/ext-config-phone.png`, fullPage: true })
  }
})
