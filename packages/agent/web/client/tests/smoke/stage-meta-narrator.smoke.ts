import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// The meta-narrator (Worlds W3): a chat World with a roster grows a "Who
// replies" setting in the World tab (world.set), and a line the router handed
// to a roster character renders 🎭-attributed to that character — like a
// directed line, but model-produced. Zero backend.
test('stage: the World tab sets coordination and routed lines are attributed', async ({ page }) => {
  await stubMedia(page)

  const SESSION = {
    id: SMOKE_SESSION,
    title: 'The Lowtown Job',
    experience: 'chat',
    card: 'kob-1',
    cast: { Elira: 'elira-1' },
    coordination: '',
  }
  let coordinationSet: unknown = null
  const mock = await installStageBackend(page, {
    cards: [{ id: 'kob-1', name: 'Kobeni', greetings: 1 }],
    respond: (method, params) => {
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'kob-1', name: 'Kobeni', greetings: 1, avatar_url: '/media/cards/kob-1', raw: {} }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'world.set') {
        coordinationSet = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A routed line in the transcript: the meta-narrator picked Elira, so her
  // line is 🎭-attributed to her, not to the session's main card.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: SESSION,
        epoch: 1,
        base: 0,
        total: 3,
        messages: [
          { role: 'user', content: [{ type: 'text', text: '"Elira — the ledger. Now."' }] },
          { role: 'assistant', content: [{ type: 'text', text: '*Elira slides it across the table.* "Careful who sees you with that."' }], routed: true, actor: 'Elira' },
          { role: 'assistant', content: [{ type: 'text', text: '*Rain starts against the shutters.*' }], routed: true },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  // Routed rows carry their OWN class. They used to share .stage-row--directed
  // with lines the user authored, which made a machine-invented beat and a
  // hand-written one indistinguishable on screen.
  const routedRows = page.locator('.stage-row--routed')
  await expect(page.locator('.stage-row--directed')).toHaveCount(0)
  await expect(routedRows).toHaveCount(2)
  await expect(routedRows.first().locator('.stage-row__name')).toHaveText('🎭 Elira')
  // A routed line with no actor is a narrator beat.
  await expect(routedRows.nth(1).locator('.stage-row__name')).toHaveText('🎭 Narrator')

  // The World tab's "Who replies" select (visible because the roster is
  // non-empty) posts world.set on pick.
  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  const select = page.locator('.stage-coordination__select')
  await expect(select).toBeVisible()
  await select.selectOption('focus:Elira')
  await expect.poll(() => coordinationSet).toEqual({ coordination: 'focus:Elira' })
  await select.selectOption('off')
  await expect.poll(() => coordinationSet).toEqual({ coordination: 'off' })
})
