import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Character inspection (rough-edge #8): a card's ⋯ opens a detail sheet showing
// the WHOLE card — every CCv2 field, not just the greeting — with quick-start
// controls kept at the top. Empty fields render "—"; the card author's text
// (including a malformed macro) is shown verbatim, since this is an inspector.
const RAW = {
  spec: 'chara_card_v2',
  spec_version: '2.0',
  data: {
    name: 'Kobeni',
    description: 'A shut-in isekai heroine. {{char}} will never speak for {{user)} — a malformed macro.',
    personality: '',
    scenario: 'An isekai fantasy world.',
    first_mes: '*Kobeni squints.* "...NPC or love interest?"',
    mes_example: '',
    system_prompt: '',
    post_history_instructions: 'Stay in character.',
    creator_notes: 'A middling test card.',
    alternate_greetings: ['*She trips.* "Tutorial level."'],
    character_book: { name: 'lore', entries: [{ name: 'Otome games', keys: ['otome'], constant: false }, { name: 'World', constant: true }] },
  },
}
const SUMMARY = {
  id: 'card-1',
  name: 'Kobeni',
  creator: 'someone',
  character_version: '1.0',
  spec_version: '2.0',
  tags: ['isekai', 'comedy'],
  avatar_url: '/media/cards/card-1',
  greetings: 2,
  book_entries: 2,
  has_phi: true,
}

test('stage: inspect a character from the ⋯ detail sheet', async ({ page }) => {
  await stubMedia(page)
  let created = false
  await installStageBackend(page, {
    cards: [SUMMARY],
    respond: (method) => {
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'cards.get') return { ...SUMMARY, raw: RAW }
      if (method === 'cards.lint')
        return {
          findings: [
            { rule: 'malformed-macro', severity: 'warn', field: 'description', message: 'A macro is malformed and will be sent to the model literally instead of expanding.', detail: '{{user)}' },
            { rule: 'empty-personality', severity: 'info', field: 'personality', message: 'Personality is empty; the card relies on the description alone.' },
            { rule: 'no-example-dialogue', severity: 'info', field: 'mes_example', message: 'No example dialogue — the model has less grounding for the character’s voice.' },
          ],
        }
      if (method === 'sessions.create') {
        created = true
        return { session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1' } }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card').first()).toBeVisible()

  // ⋯ opens the detail sheet with the full card.
  await page.locator('.stage-card__more').first().click()
  await expect(page.locator('.stage-sheet--detail')).toBeVisible()
  await expect(page.locator('.stage-cardsheet__id h3')).toHaveText('Kobeni')
  await expect(page.locator('.stage-cardsheet__meta')).toContainText('spec 2.0')
  await expect(page.locator('.stage-cardsheet__facts')).toContainText('2 greetings')

  // The deterministic lint findings surface above the fields: a warn for the
  // malformed macro (with the offending snippet) plus info-level facts.
  await expect(page.locator('.stage-lint')).toBeVisible()
  await expect(page.locator('.stage-lint__count--warn')).toContainText('1')
  const warn = page.locator('.stage-lint__item--warn')
  await expect(warn).toHaveCount(1)
  await expect(warn.locator('.stage-lint__detail')).toHaveText('{{user)}')
  await expect(warn.locator('.stage-lint__field')).toHaveText('description')
  if (process.env.LINT_SHOT) await page.screenshot({ path: `${process.env.LINT_SHOT}.png` })

  // Empty personality → "—".
  const personality = page.locator('.stage-cardfield', { has: page.locator('.stage-cardfield__label', { hasText: 'Personality' }) })
  await expect(personality.locator('.stage-cardfield__value--empty')).toHaveText('—')

  // Malformed macro is shown verbatim (the inspector shows the card as authored).
  await expect(page.locator('.stage-cardfield__value').filter({ hasText: '{{user)}' })).toBeVisible()

  // Lorebook entries render (a keyed entry + an always-on entry).
  await expect(page.locator('.stage-cardsheet__book-key', { hasText: 'otome' })).toBeVisible()
  await expect(page.locator('.stage-cardsheet__book-tag', { hasText: 'always on' })).toBeVisible()

  // The greeting picker previews an alternate as the "First message".
  const firstMes = page.locator('.stage-cardfield', { has: page.locator('.stage-cardfield__label', { hasText: 'First message' }) })
  await expect(firstMes.locator('.stage-cardfield__value')).toContainText('NPC or love interest')
  await page.locator('.stage-cardsheet__start .stage-greeting-pick button').last().click()
  await expect(firstMes.locator('.stage-cardfield__value')).toContainText('Tutorial level')

  // Start chat still creates the session (the quick-start path is preserved).
  await page.locator('.stage-cardsheet__start .stage-sheet__start').click()
  await expect.poll(() => created).toBe(true)
})
