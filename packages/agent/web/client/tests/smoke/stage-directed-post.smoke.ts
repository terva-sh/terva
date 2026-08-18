import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Directed authorship (Phase 6): the ✨ modal gains a target — Me / Character /
// Narrator. A "Me" draft still fills the composer; a Character or Narrator draft
// is APPROVED, then POSTED into the transcript as an attributed line (post.line).
// This smoke drives the real client flow (pick Character → name → draft → post)
// against a mock that echoes the draft and records the post — proving the target
// threads through suggest.reply and the approved line is committed with its
// actor. Zero backend.
test('stage: directed authorship — draft a character line and post it', async ({ page }) => {
  await stubMedia(page)

  const drafts: Array<{ target?: string; target_name?: string; target_voice?: string }> = []
  let posted: { actor?: string; text?: string } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'suggest.reply') {
        const p = params as { target?: string; target_name?: string; target_voice?: string }
        drafts.push({ target: p.target, target_name: p.target_name, target_voice: p.target_voice })
        return { draft: `${p.target}:${p.target_name || '-'} steps in.` }
      }
      if (method === 'post.line') {
        posted = params as { actor?: string; text?: string }
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A scene so the chat isn't empty.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: 'The dock is quiet.' }] }],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-bubble', { hasText: 'The dock is quiet' })).toBeVisible()

  // Open ✨ and switch to the Character target.
  await page.locator('.stage-suggest-btn').click()
  await expect(page.locator('.stage-suggest')).toBeVisible()
  await page.locator('.stage-suggest__target button', { hasText: 'Character' }).click()

  // Draft is gated until the character is named.
  await expect(page.locator('.stage-suggest__go')).toBeDisabled()
  await page.locator('.stage-suggest__actor-name').fill('Kael')
  await page.locator('.stage-suggest__actor-voice').fill('a gruff dockmaster')
  await expect(page.locator('.stage-suggest__go')).toBeEnabled()

  await page.locator('.stage-suggest__go').click()
  await expect(page.locator('.stage-suggest__draft')).toHaveValue('actor:Kael steps in.')
  // The target + walk-on identity threaded through suggest.reply.
  await expect.poll(() => drafts.length).toBe(1)
  expect(drafts[0]).toEqual({ target: 'actor', target_name: 'Kael', target_voice: 'a gruff dockmaster' })

  // Post it → post.line commits the approved line attributed to Kael, and the
  // modal closes (nothing lands in the composer).
  await page.locator('.stage-suggest__use', { hasText: 'Post to scene' }).click()
  await expect(page.locator('.stage-suggest')).toHaveCount(0)
  await expect.poll(() => posted).not.toBeNull()
  expect(posted!.actor).toBe('Kael')
  expect(posted!.text).toBe('actor:Kael steps in.')
  await expect(page.locator('.stage-composer textarea')).toHaveValue('')
})

// A narrator beat posts with no actor, and a directed message renders with 🎭
// attribution rather than as a plain assistant bubble.
test('stage: directed authorship — a narrator beat, and directed rows are attributed', async ({ page }) => {
  await stubMedia(page)

  let posted: { actor?: string; text?: string } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'suggest.reply') return { draft: 'Night falls over the harbour.' }
      if (method === 'post.line') {
        posted = params as { actor?: string; text?: string }
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Draft + post a narrator beat.
  await page.locator('.stage-suggest-btn').click()
  await page.locator('.stage-suggest__target button', { hasText: 'Narrator' }).click()
  await page.locator('.stage-suggest__go').click()
  await expect(page.locator('.stage-suggest__draft')).toHaveValue('Night falls over the harbour.')
  await page.locator('.stage-suggest__use', { hasText: 'Post to scene' }).click()
  await expect.poll(() => posted).not.toBeNull()
  expect(posted!.actor).toBe('') // a narrator beat has no actor

  // A snapshot carrying directed lines renders them with 🎭 attribution.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 2,
        messages: [
          { role: 'assistant', content: [{ type: 'text', text: 'Night falls over the harbour.' }], directed: true },
          { role: 'assistant', content: [{ type: 'text', text: `"You're late."` }], directed: true, actor: 'Kael' },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-row--directed')).toHaveCount(2)
  await expect(page.locator('.stage-row--directed .stage-row__name', { hasText: 'Narrator' })).toBeVisible()
  await expect(page.locator('.stage-row--directed .stage-row__name', { hasText: 'Kael' })).toBeVisible()
})

// Direct (Phase 6b): type an out-of-character direction → the model runs a steered
// turn (direct.turn). The [Direction] message renders as a 🎬 cue, not a player
// bubble, and the per-generation model picker is hidden (a Direct turn runs on the
// session model).
test('stage: directed authorship — Direct runs a steered turn and renders a cue', async ({ page }) => {
  await stubMedia(page)

  let directed: string | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'direct.turn') {
        directed = (params as { text?: string }).text ?? ''
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  await page.locator('.stage-suggest-btn').click()
  await page.locator('.stage-suggest__target button', { hasText: 'Direct' }).click()
  // The model picker is irrelevant to a Direct turn and is hidden.
  await expect(page.locator('.stage-suggest__model')).toHaveCount(0)

  await page.locator('.stage-suggest__note').fill('Kael storms out')
  await page.locator('.stage-suggest__go', { hasText: 'Direct the story' }).click()
  await expect.poll(() => directed).toBe('Kael storms out')
  await expect(page.locator('.stage-suggest')).toHaveCount(0)

  // A [Direction] message renders as a 🎬 cue, not a player bubble.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'user', content: [{ type: 'text', text: '[Direction] Kael storms out' }] }],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-row--direction')).toContainText('Kael storms out')
  await expect(page.locator('.stage-row--user')).toHaveCount(0)
})

// Worlds W1: the Character target can pick a LIBRARY CARD instead of typing a
// walk-on. Picking one threads target_card + the card's name through
// suggest.reply, and the posted line is attributed to the card's character.
test('stage: directed authorship — voice a library card', async ({ page }) => {
  await stubMedia(page)

  let draft: { target_card?: string; target_name?: string } | null = null
  let posted: { actor?: string } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'ivy-1', name: 'Ivy', greetings: 1 }, { id: 'elira-1', name: 'Mistress Elira', greetings: 1 }] }
      if (method === 'cards.get') return { id: 'ivy-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'ivy-1' } }
      if (method === 'suggest.reply') {
        draft = params as { target_card?: string; target_name?: string }
        return { draft: 'Elira inclines her head. "You kept me waiting."' }
      }
      if (method === 'post.line') {
        posted = params as { actor?: string }
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  await page.locator('.stage-suggest-btn').click()
  await page.locator('.stage-suggest__target button', { hasText: 'Character' }).click()
  // Pick a library card — the walk-on inputs give way to the card note.
  await page.locator('.stage-suggest__card-pick').selectOption({ label: 'Mistress Elira' })
  await expect(page.locator('.stage-suggest__card-note')).toContainText('Mistress Elira')
  await expect(page.locator('.stage-suggest__actor-name')).toHaveCount(0)

  await page.locator('.stage-suggest__go').click()
  await expect(page.locator('.stage-suggest__draft')).toHaveValue(/You kept me waiting/)
  await expect.poll(() => draft).not.toBeNull()
  expect(draft!.target_card).toBe('elira-1')
  expect(draft!.target_name).toBe('Mistress Elira')

  await page.locator('.stage-suggest__use', { hasText: 'Post to scene' }).click()
  await expect.poll(() => posted).not.toBeNull()
  expect(posted!.actor).toBe('Mistress Elira')
})

// Worlds W2: "keep on stage" adds a picked library character to the session
// roster (cast.add); once on stage it's a one-tap quick-pick chip. Zero backend.
test('stage: directed authorship — keep a character on stage', async ({ page }) => {
  await stubMedia(page)
  let added: { name?: string; ref?: string } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'ivy-1', name: 'Ivy', greetings: 1 }, { id: 'elira-1', name: 'Mistress Elira', greetings: 1 }] }
      if (method === 'cards.get') return { id: 'ivy-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'ivy-1' } }
      if (method === 'cast.add') {
        added = params as { name?: string; ref?: string }
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  await page.locator('.stage-suggest-btn').click()
  await page.locator('.stage-suggest__target button', { hasText: 'Character' }).click()
  await page.locator('.stage-suggest__card-pick').selectOption({ label: 'Mistress Elira' })
  // A fresh library pick offers "keep on stage" → cast.add.
  await page.locator('.stage-suggest__keep').click()
  await expect.poll(() => added).not.toBeNull()
  expect(added).toEqual({ name: 'Mistress Elira', ref: 'elira-1' })

  // The daemon folds the new roster into the session snapshot → a quick-pick chip.
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'ivy-1', cast: { 'Mistress Elira': 'elira-1' } }, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-suggest__roster-chip', { hasText: 'Mistress Elira' })).toBeVisible()
})
