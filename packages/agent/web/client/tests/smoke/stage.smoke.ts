import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The Stage app end to end over a mocked control plane, zero backend/spend: the
// library renders the Phase-2 stores, tapping a card starts a chat, and the
// immersive chat renders the transcript (avatar rows, scene background, swipe
// arrows) over the shared conversation store.
test('stage: library renders and a card opens an immersive chat', async ({ page }) => {
  // Serve the media routes (avatar + background) the chat references, so the
  // scene renders — the mock backend only intercepts /ws.
  await page.route('**/media/**', (route) => {
    const bg = route.request().url().includes('/backgrounds/')
    const svg = bg
      ? '<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#3a4a55"/><stop offset="1" stop-color="#1a1410"/></linearGradient></defs><rect width="400" height="300" fill="url(#g)"/></svg>'
      : '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#7a5a8a"/></svg>'
    void route.fulfill({ contentType: 'image/svg+xml', body: svg })
  })

  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list')
        return {
          cards: [
            { id: 'seraphina-1', name: 'Seraphina', creator: 'anon', greetings: 3, book_entries: 5, has_phi: true, avatar_url: '/media/cards/seraphina-1' },
            { id: 'nova-2', name: 'Nova', greetings: 1 },
            { id: 'kael-3', name: 'Kael', creator: 'someone', greetings: 2 },
          ],
        }
      if (method === 'personas.list')
        return {
          personas: [
            { name: 'Mieli', ref: 'mieli', origin: 'built-in', emoji: '🧭' },
            { name: 'Kertoja', ref: 'kertoja', origin: 'built-in', emoji: '📖' },
            { name: 'Bard', ref: 'bard', origin: 'user', emoji: '🎵', editable: true },
          ],
        }
      if (method === 'sessions.create')
        return { session: { id: SMOKE_SESSION, title: 'Seraphina', experience: 'chat', card: 'seraphina-1', background: 'dusk-1' } }
      if (method === 'cards.get')
        return { id: 'seraphina-1', name: 'Seraphina', greetings: 3, avatar_url: '/media/cards/seraphina-1', raw: {} }
      if (method === 'backgrounds.list')
        return {
          backgrounds: [
            { id: 'dusk-1', url: '/media/backgrounds/dusk-1' },
            { id: 'tavern-2', url: '/media/backgrounds/tavern-2' },
          ],
        }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'lore')
        return {
          id: 'lore',
          kind: 'lore',
          lore: {
            entries: [
              { name: 'The Pass', keys: ['pass', 'mountain'], content: 'A narrow mountain pass, snowbound after dusk.', fired: true, matched_keys: ['pass'] },
              { name: 'Guardian oath', constant: true, content: 'She swore to hold the pass until the last traveler is through.' },
              { name: 'The Sea', keys: ['sea'], content: 'A cold grey sea beyond the range.', fired: true, dropped_for_budget: true },
            ],
          },
        }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(3)
  if (process.env.STAGE_SHOT) await page.screenshot({ path: `${process.env.STAGE_SHOT}-library.png`, fullPage: true })

  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: {
          id: SMOKE_SESSION,
          title: 'Seraphina',
          experience: 'chat',
          card: 'seraphina-1',
          background: 'dusk-1',
          note: 'It is dusk on the pass.',
          user_name: 'Kira',
          user_description: 'A wary courier who trusts no one.',
          supports_continue: true,
        },
        epoch: 1,
        base: 0,
        total: 3,
        messages: [
          { role: 'assistant', content: [{ type: 'text', text: '*She looks up from the map.* "You’re late, traveler. The pass won’t stay open past dusk."' }] },
          { role: 'user', content: [{ type: 'text', text: '"I ran into trouble on the road."' }] },
          { role: 'assistant', content: [{ type: 'text', text: '*She sheathes her dagger and studies you.* "Trouble follows some people like a shadow. Sit — tell me what you saw."' }] },
        ],
        busy: false,
        tail: { span_start: 2, variants: 2, active: 1 },
      },
    },
    SMOKE_SESSION,
  )

  await expect(page.locator('.stage-row--assistant')).toHaveCount(2)
  await expect(page.locator('.stage-avatar').first()).toBeVisible()
  await expect(page.locator('.stage-swipe')).toBeVisible() // swipe arrows on the tail (2 takes)
  // Both regenerate affordances: the plain one (↻) and the guided twin (↻✎)
  // that 43e986fb added beside it. Asserting each SPECIFICALLY is the point —
  // a bare '.stage-regen' matched one element when this was written and two
  // afterwards, which Playwright's strict mode fails rather than silently
  // passing on the wrong one.
  await expect(page.locator('.stage-regen:not(.stage-regen--guided)')).toBeVisible()
  await expect(page.locator('.stage-regen--guided')).toBeVisible()
  // The continue affordance shows because the snapshot advertises supports_continue.
  await expect(page.locator('.stage-continue')).toBeVisible()
  if (process.env.STAGE_SHOT) await page.screenshot({ path: `${process.env.STAGE_SHOT}-chat.png`, fullPage: true })

  // Open the steering drawer. Since the S10 tab split it lands on Session; the
  // user persona lives in You, and the note/scene/lorebook in Scene.
  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  // The user persona — name + description — seeds from the session snapshot.
  await page.locator('.stage-drawer__tab', { hasText: 'You' }).click()
  await expect(page.locator('.stage-user-name')).toHaveValue('Kira')
  await expect(page.locator('.stage-user-desc')).toHaveValue('A wary courier who trusts no one.')
  // The author's note seeds from the snapshot too (SessionInfo.note).
  await page.locator('.stage-drawer__tab', { hasText: 'Scene' }).click()
  await expect(page.locator('.stage-note')).toHaveValue('It is dusk on the pass.')
  await expect(page.locator('.stage-bg-tile')).toHaveCount(3) // None + 2 backgrounds
  await expect(page.locator('.stage-lore__entry')).toHaveCount(3)
  // The activation trace: one entry fired (its matched key highlighted), one fired
  // but was dropped for budget.
  await expect(page.locator('.stage-lore__key--hit')).toHaveText('pass')
  await expect(page.locator('.stage-lore__badge--fired')).toBeVisible()
  await expect(page.locator('.stage-lore__badge--dropped')).toBeVisible()
  if (process.env.STAGE_SHOT) await page.screenshot({ path: `${process.env.STAGE_SHOT}-drawer.png`, fullPage: true })

  // Mobile-first: at a phone width nothing overflows horizontally (the chat is
  // the surface where phone is the primary form factor).
  await page.locator('.stage-drawer__close').click()
  await page.setViewportSize({ width: 390, height: 844 })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
  if (process.env.STAGE_SHOT) await page.screenshot({ path: `${process.env.STAGE_SHOT}-mobile.png` })
})
