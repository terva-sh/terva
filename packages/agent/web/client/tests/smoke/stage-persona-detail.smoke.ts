import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Personas interactive (rough-edge #9): the Library's persona roster was
// display-only. Tapping a persona now opens its full PersonaView (personas.get)
// — specialty, summary, introduction, the charter that shapes its identity, and
// the good-for/avoid-for guidance. Driven against a mock persona library.
const PERSONA = {
  name: 'Kertoja',
  ref: 'crew:kertoja',
  namespace: 'crew',
  origin: 'built-in',
  emoji: '📖',
  specialty: 'Narration',
  summary: 'The narrator who weaves a scene from what the cast returns.',
  immersive: true,
}
const VIEW = {
  ...PERSONA,
  introduction: 'I set scenes and speak the world around you.',
  charter: 'You are Kertoja, a narrator. Describe, never decide for the player. Weave the cast’s lines into prose.',
  good_for: ['immersive roleplay', 'multi-character scenes'],
  avoid_for: ['code tasks'],
  recommended_skills: ['lore'],
  pronunciation: 'KER-toy-a',
}

test('stage: inspect a persona from the roster', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'personas.list') return { personas: [PERSONA] }
      if (method === 'personas.get') return VIEW
      return undefined
    },
  })

  await page.goto('/stage.html')
  const pill = page.locator('.stage-persona', { hasText: 'Kertoja' })
  await expect(pill).toBeVisible()

  await pill.click()
  await expect(page.locator('.stage-personasheet')).toBeVisible()
  await expect(page.locator('.stage-personasheet__id h3')).toHaveText('Kertoja')
  await expect(page.locator('.stage-personasheet__meta')).toContainText('Narration')
  await expect(page.locator('.stage-personasheet__immersive')).toHaveText('immersive')
  await expect(page.locator('.stage-personasheet__summary')).toContainText('weaves a scene')

  // The charter (the identity-shaping body) renders.
  const charter = page.locator('.stage-personasheet__field', { has: page.locator('.stage-personasheet__label', { hasText: 'Charter' }) })
  await expect(charter.locator('.stage-personasheet__value')).toContainText('never decide for the player')

  // Good-for / avoid-for guidance renders as toned chips.
  await expect(page.locator('.stage-personasheet__chip--good', { hasText: 'immersive roleplay' })).toBeVisible()
  await expect(page.locator('.stage-personasheet__chip--avoid', { hasText: 'code tasks' })).toBeVisible()
  if (process.env.PERSONA_SHOT) await page.screenshot({ path: `${process.env.PERSONA_SHOT}.png` })

  // Backdrop click dismisses.
  await page.locator('.stage-sheet-backdrop').click({ position: { x: 10, y: 10 } })
  await expect(page.locator('.stage-personasheet')).toHaveCount(0)
})
