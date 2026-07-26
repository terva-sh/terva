import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The studio's You tab (plan part C): who you play AS, given the same room as
// who you play WITH. Two doors lead here — "playing as …" in the scene header,
// and 🎭 You in the Library topbar — and they arrive in different modes: with a
// scene the editor steers THAT session's persona live, without one it edits the
// saved library and saves explicitly.
test('stage: the You tab steers the scene it was opened from', async ({ page }) => {
  const PERSONAS = [
    { ref: 'kira', name: 'Kira', description: 'A wary courier who trusts no one.', pronouns: 'she/her', default: true },
    { ref: 'rook', name: 'Rook', description: 'A blunt merc with a debt.' },
  ]
  const binds: { ref?: string; name?: string; description?: string }[] = []
  let saved: { ref?: string; name?: string } | null = null

  const session = {
    id: SMOKE_SESSION,
    title: 'Ivy',
    experience: 'chat',
    card: 'card-1',
    user_name: 'Kira',
    user_description: 'A wary courier who trusts no one.',
    user_pronouns: 'she/her',
  }

  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: { spec: 'chara_card_v2', data: { name: 'Ivy' } } }
      if (method === 'cards.lint') return { findings: [] }
      if (method === 'sessions.create') return { session }
      if (method === 'userpersonas.list') return { personas: PERSONAS }
      if (method === 'user.bind') {
        binds.push(params as { ref?: string })
        return {}
      }
      if (method === 'userpersonas.save') {
        saved = params as { ref?: string; name?: string }
        return { ref: 'kira', name: (params as { name: string }).name }
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
      snapshot: { session, epoch: 1, base: 0, total: 1, messages: [{ role: 'assistant', content: [{ type: 'text', text: 'Hi.' }] }], busy: false },
    },
    SMOKE_SESSION,
  )

  // ⚠️ Wait for the header to finish assembling BEFORE clicking into it.
  //
  // `.stage-chat__who` renders a bare title span until cards.get resolves, then
  // swaps it for a button carrying the portrait — a much wider element — so the
  // chip beside it jumps right at some point after the snapshot lands. Clicking
  // the chip while that is still pending raced the shift and the navigation was
  // simply lost: 1 run in 40 under contention sat on the chat screen forever.
  // Waiting for the card button is waiting for the header's final layout.
  await expect(page.locator('.stage-chat__cardbtn')).toBeVisible()

  // The scene header says who you are, beside who you are playing with.
  const chip = page.locator('.stage-chat__playingas')
  await expect(chip).toContainText('Kira')
  await chip.click()

  // ...and lands on the studio's You tab, with the character on the other one.
  await expect(page.locator('.stage-you')).toBeVisible()
  await expect(page.locator('.stage-studio__tab--on')).toHaveText('You')
  await expect(page.locator('.stage-studio__tabs')).toContainText('Ivy')
  await expect(page.locator('.stage-you__name')).toHaveValue('Kira')
  await expect(page.locator('.stage-you__row--playing')).toContainText('Kira')

  // An edit commits to the session on blur — a scene is live, and steering it
  // should not need a save.
  await page.locator('.stage-you__desc').fill('A wary courier, wearier now.')
  await page.locator('.stage-you__name').click()
  await expect.poll(() => binds.at(-1)).toMatchObject({ name: 'Kira', description: 'A wary courier, wearier now.' })

  if (process.env.YOU_SHOT) await page.screenshot({ path: `${process.env.YOU_SHOT}-scene.png`, fullPage: true })

  // Keeping it in the library is name-keyed: the scene is what is being edited.
  await page.locator('.stage-you__save', { hasText: 'Keep in your personas' }).click()
  await expect.poll(() => saved).toMatchObject({ ref: '', name: 'Kira' })

  // Playing as another saved identity binds it BY REF and re-seeds the form.
  await page.locator('.stage-you__pick', { hasText: 'Rook' }).click()
  await expect.poll(() => binds.at(-1)).toEqual({ ref: 'rook' })
  await expect(page.locator('.stage-you__name')).toHaveValue('Rook')

  // The other tab is still the character, and switching to it costs nothing.
  await page.locator('.stage-studio__tab', { hasText: 'Ivy' }).click()
  await expect(page.locator('.stage-cardeditor')).toBeVisible()

  // Back returns to the scene it came from, not the Library.
  await page.locator('.stage-back').click()
  await expect(page.locator('.stage-composer')).toBeVisible()
})

test('stage: the Library door opens the You tab in library mode', async ({ page }) => {
  const PERSONAS = [{ ref: 'kira', name: 'Kira', description: 'A wary courier who trusts no one.', default: true }]
  let saved: { ref?: string; name?: string } | null = null

  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'userpersonas.list') return { personas: PERSONAS }
      if (method === 'userpersonas.save') {
        saved = params as { ref?: string; name?: string }
        return { ref: 'kiran', name: (params as { name: string }).name }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-you-link').click()
  await expect(page.locator('.stage-you')).toBeVisible()

  // No scene behind it, so nothing is live: open a saved persona, rename it, and
  // the save carries its ref — which is what makes it a rename rather than a
  // second Kira.
  await page.locator('.stage-you__pick', { hasText: 'Kira' }).click()
  await expect(page.locator('.stage-you__editor h2')).toContainText('Editing Kira')
  await page.locator('.stage-you__name').fill('Kiran')

  if (process.env.YOU_SHOT) await page.screenshot({ path: `${process.env.YOU_SHOT}-library.png`, fullPage: true })

  await page.locator('.stage-you__save', { hasText: 'Save' }).first().click()
  await expect.poll(() => saved).toMatchObject({ ref: 'kira', name: 'Kiran' })
  await expect(page.locator('.stage-you__saved')).toBeVisible()
})
