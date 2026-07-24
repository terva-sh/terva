import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// The right rail on the landing, where there is no session for it to be about.
//
// It used to open onto "loading…" and stay there: the rail switches over a
// session's surfaces, the landing has no session, and the panel is right not to
// ask (an empty address mints one). Now the same control opens the workspace
// instead — the daemon's own facts, fetched with verbs that ignore the session
// address on purpose.
const PROVIDERS = {
  can_login: true,
  providers: [
    { id: 'anthropic', label: 'Anthropic', method: 'oauth' },
    { id: 'openai', label: 'OpenAI' },
    { id: 'openai-codex', label: 'ChatGPT (Codex)' },
  ],
}

test('panel: the landing opens the workspace drawer, not an empty rail', async ({ page }) => {
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'auth.providers') return PROVIDERS
      // No session in the list: this is a panel that has never started one, the
      // state in which the drawer matters most.
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })
  void mock

  await page.goto('/')
  await expect(page.locator('.topbar .dot.open')).toBeVisible()

  // The control names what it opens — the same button means two things.
  const panes = page.locator('button[title="Workspace (providers, about)"]')
  await expect(panes).toBeVisible()
  await panes.click()

  const rail = page.locator('.pane-rail')
  await expect(rail).toBeVisible()
  await expect(rail).toContainText('Anthropic')
  await expect(rail).not.toContainText('loading…')
  if (process.env.WS_SHOT) await page.screenshot({ path: `${process.env.WS_SHOT}-providers.png` })

  // The other tab: what this daemon is.
  await rail.locator('.pane-tab', { hasText: 'About' }).click()
  await expect(rail.locator('.ws-about')).toBeVisible()
  if (process.env.WS_SHOT) await page.screenshot({ path: `${process.env.WS_SHOT}-about.png` })

  // On a phone the rail is a full sheet, the same as the session panes.
  await page.setViewportSize({ width: 390, height: 780 })
  await expect(rail).toBeVisible()
  if (process.env.WS_SHOT) await page.screenshot({ path: `${process.env.WS_SHOT}-phone.png` })
})
