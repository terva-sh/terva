import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The provider-switch offer, from the daemon's report to the session it opens.
//
// The daemon cannot stop and ask a browser the way the TUI asks a terminal, so
// on a pinned provider it cannot use it starts on whatever works and says so in
// the Providers pane. The component test proves the banner renders and the
// button calls its handler; this proves the handler reaches the WIRE — that
// clicking it sends sessions.create carrying the offered pair, rather than a
// default create that would land straight back on the dead pin.
const PROVIDERS = {
  can_login: true,
  providers: [
    { id: 'anthropic', label: 'Anthropic', method: 'oauth', expired: true },
    { id: 'openai', label: 'OpenAI', method: 'apikey' },
  ],
  switch: {
    from: 'anthropic',
    from_model: 'claude-opus-5',
    to: 'openai',
    to_model: 'gpt-5',
    reason: 'anthropic login expired and could not be refreshed',
    lapsed: true,
  },
}

test('panel: a lapsed pin offers the working provider, and taking it creates that session', async ({ page }) => {
  const created: unknown[] = []
  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'auth.providers') return PROVIDERS
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create') {
        created.push(params)
        return { session: { id: SMOKE_SESSION, title: '', provider: 'openai', model: 'gpt-5' } }
      }
      return undefined
    },
  })

  await page.goto('/')
  await page.locator('button[title="Workspace (providers, about)"]').click()

  const rail = page.locator('.pane-rail')
  await expect(rail).toBeVisible()
  // Both halves: which login lapsed, and what is running instead. Either alone
  // misleads — "anthropic expired" reads as nothing running, "on openai" reads
  // as the user's own choice.
  await expect(rail).toContainText('anthropic')
  await expect(rail).toContainText('openai/gpt-5')
  await expect(rail).toContainText('still your default')
  if (process.env.WS_SHOT) await page.screenshot({ path: `${process.env.WS_SHOT}-provider-switch.png` })

  await rail.getByText(/Start a chat on openai\/gpt-5/i).click()

  // The pair has to be ON the create. A bare sessions.create would be seeded
  // from config.json — the very provider that just failed — so the click would
  // produce the same refusal it was offered to escape.
  await expect.poll(() => created.length).toBeGreaterThan(0)
  expect(created[0]).toMatchObject({ provider: 'openai', model: 'gpt-5' })
})
