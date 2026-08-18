import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend } from './support'

// Saved user personas (rough-edges #5/#7): the steering drawer's "You (in this
// story)" block lists reusable identities (chips to swap between, a star for the
// default), and can save the current name+description as a persona or as the new
// default. Driven against a mock user-persona library.
test('stage: saved user personas in the steering drawer', async ({ page }) => {
  const PERSONAS = [
    { ref: 'kira', name: 'Kira', description: 'A wary courier.', default: true },
    { ref: 'rook', name: 'Rook', description: 'A blunt merc.' },
  ]
  let bound: { ref?: string; name?: string } | null = null
  let saved: { name?: string; description?: string } | null = null
  let defaulted: { ref?: string } | null = null

  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' } }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'surface.get') return { id: 'lore', title: 'Lore', kind: 'lore', lore: { entries: [] } }
      if (method === 'userpersonas.list') return { personas: PERSONAS }
      if (method === 'user.bind') {
        bound = params as typeof bound
        return {}
      }
      if (method === 'userpersonas.save') {
        saved = params as typeof saved
        return { ref: 'nova', name: (params as { name: string }).name, description: (params as { description?: string }).description }
      }
      if (method === 'userpersonas.set_default') {
        defaulted = params as typeof defaulted
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: 'Hi.' }] }],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  // Since the S10 tab split, "You (in this story)" lives behind the You tab.
  await page.locator('.stage-drawer__tab', { hasText: 'You' }).click()

  // The saved persona chips render; the default is starred.
  await expect(page.locator('.stage-userpersona')).toHaveCount(2)
  await expect(page.locator('.stage-userpersona--default')).toContainText('Kira')

  // Clicking a chip binds that persona by ref.
  await page.locator('.stage-userpersona__pick', { hasText: 'Rook' }).click()
  await expect.poll(() => bound).toEqual({ ref: 'rook' })

  // Typing a name + "Save persona" saves it (identity fields ride along since
  // the persona gender/pronouns work, empty when unset).
  await page.locator('.stage-user-name').fill('Nova')
  await page.locator('.stage-userpersona-save', { hasText: 'Save persona' }).click()
  await expect.poll(() => saved).toEqual({ name: 'Nova', description: '', gender: '', pronouns: '' })

  // "Save as default" saves and marks it the default.
  await page.locator('.stage-userpersona-save', { hasText: 'Save as default' }).click()
  await expect.poll(() => defaulted).toEqual({ ref: 'nova' })
  if (process.env.UP_SHOT) await page.screenshot({ path: `${process.env.UP_SHOT}.png` })

  // The ✕ on a chip is wired to delete without crashing the drawer.
  await page.locator('.stage-userpersona', { hasText: 'Rook' }).locator('.stage-userpersona__del').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
})
