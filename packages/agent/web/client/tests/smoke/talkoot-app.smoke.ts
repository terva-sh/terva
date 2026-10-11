import { test, expect, type Page } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The Talkoot app at /talkoot/, served here as talkoot.html by vite preview,
// against a mock daemon that spends nothing. It checks what only a browser
// shows: the three columns, a card in the conversation, the narrow drawer, and
// the posts and answers that reach the daemon.

const SHOTS = process.env.TALKOOT_SMOKE_SHOTS

const at = (s: number) => `2026-10-10T10:00:${String(s).padStart(2, '0')}Z`
const env = (id: string, from: string, to: string[], s: number, body: string, root = 'post', refs?: string[]) => ({
  type: 'envelope',
  at: at(s),
  envelope: { id, talkoot: 'crew', from, to, kind: 'message', body, at: at(s), chain: { root, hops: 1 }, refs },
})

async function openApp(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('terva_talkoot_person', 'Drew')
    localStorage.setItem('terva_talkoot_motion', 'off')
  })
  const sent: Array<{ method: string; params: unknown; sess?: string }> = []
  await installMockBackend(page, {
    groups: ['conversation', 'session', 'control', 'talkoot'],
    features: ['workspace-events'],
    respond: (method, params, sess) => {
      sent.push({ method, params, sess })
      switch (method) {
        case 'talkoot.list':
          return { talkoots: [{ id: 'crew', name: 'Docs crew', running: true, color: '#3E63DD', state: 'busy' }] }
        case 'talkoot.get':
          return {
            id: 'crew', name: 'Docs crew', home: '/w', budget_usd_per_day: 5,
            members: [
              { id: 'lead', role: 'coordinator', title: 'Lead', driver: 'native', mark: { shape: 'hexagon', color: '#3E63DD' }, status: { member: 'lead', presence: 'idle', spend_usd: 0.12 } },
              { id: 'writer', role: 'specialist', title: 'Writer', driver: 'native', session: 's-writer', mark: { shape: 'circle', color: '#46A758' }, status: { member: 'writer', presence: 'waiting', spend_usd: 0.31, turns: 4 } },
              { id: 'checker', role: 'specialist', title: 'Checker', driver: 'native', mark: { shape: 'triangle', color: '#E5484D' }, status: { member: 'checker', presence: 'working', working: true, tool: 'grep' } },
            ],
          }
        case 'talkoot.room':
          return { total: 5, lines: [
            env('p1', 'human:Drew', ['lead'], 1, 'Can we refresh the install page?'),
            env('m1', 'lead', ['writer'], 2, 'Handing this to you.'),
            env('m2', 'writer', ['checker'], 3, 'Please check the branch.'),
            env('m3', 'checker', ['lead'], 4, 'Looks right to me.'),
            env('r1', 'lead', ['human:Drew'], 9, 'The page is refreshed. One question is waiting for you.', 'post', ['path:docs/install.md']),
          ] }
        case 'talkoot.inbox':
          return { cards: [{
            session: 's-writer', member: 'writer', kind: 'ask', id: 'ask1', at: at(5),
            ask: { ask_id: 'ask1', question: 'Keep the old URL as a redirect?', options: ['Yes', 'No'] },
          }] }
        case 'talkoot.notes':
          return { notes: [
            { ref: 'note:writer/outline.md', member: 'writer', name: 'outline.md', size: 1240, at: at(8) },
            { ref: 'note:lead/plan.md', member: 'lead', name: 'plan.md', size: 610, at: at(2) },
          ] }
        case 'talkoot.ref': {
          const ref = (params as { ref: string }).ref
          return { ref, text: `# ${ref}\n\n1. Install with the script.\n2. Verify the checksum.\n`, size: 64 }
        }
      }
      return undefined
    },
  })
  await page.goto('/talkoot.html')
  await expect(page.getByText('Can we refresh the install page?')).toBeVisible()
  return sent
}

test('the Talkoot app shows the room as a conversation, with a card where it opened', async ({ page }) => {
  await page.setViewportSize({ width: 1200, height: 760 })
  const sent = await openApp(page)

  await expect(page.locator('.tka-rail')).toBeVisible()
  await expect(page.locator('.tka-thread summary')).toContainText('3 messages')
  const card = page.locator('.tka-card')
  await expect(card).toContainText('Keep the old URL as a redirect?')
  // The card sits between the thread and the reply that came after it.
  const order = await page.locator('.tka-log > *').evaluateAll((els) =>
    els.map((el) => (el.matches('.tka-card') ? 'card' : el.matches('.tka-thread') ? 'thread' : 'line')),
  )
  expect(order).toEqual(['line', 'thread', 'card', 'line'])
  await expect(page.locator('.tka-strip')).toContainText('1 card waits for you')
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-wide.png` })

  await card.getByRole('button', { name: 'Yes' }).click()
  await expect
    .poll(() => sent.filter((c) => c.method === 'answer').map((c) => ({ sess: c.sess, ask: (c.params as { ask_id: string }).ask_id })))
    .toEqual([{ sess: 's-writer', ask: 'ask1' }])

  await page.locator('.tka-thread summary').click()
  await expect(page.getByText('Please check the branch.')).toBeVisible()

  const box = page.getByLabel('Message to the team')
  await box.fill('')
  await box.pressSequentially('@che')
  await expect(page.getByRole('option', { name: '@checker' })).toBeVisible()
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-mention.png` })
  await box.press('Enter')
  await expect(box).toHaveValue('@checker ')
  await box.pressSequentially('is the branch green?')
  await box.press('Enter')
  await expect.poll(() => sent.filter((c) => c.method === 'talkoot.post').map((c) => c.params)).toEqual([
    { id: 'crew', by: 'Drew', body: '@checker is the branch green?', to: ['checker'] },
  ])
})

test('the Talkoot app opens a member, its work, and the notes in the side panel', async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 760 })
  await openApp(page)

  await page.locator('.tka-rail').getByRole('button', { name: /^Writer/ }).click()
  await page.getByRole('button', { name: 'Details' }).click()
  const side = page.locator('.tka-side')
  await expect(side.locator('.talkoot-member-card')).toBeVisible()
  // Wide enough for three columns: the panel sits beside the conversation.
  const [main, aside] = await Promise.all([page.locator('.tka-main').boundingBox(), side.boundingBox()])
  expect(aside!.x).toBeGreaterThanOrEqual(main!.x + main!.width - 1)

  await side.getByRole('tab', { name: 'Work' }).click()
  await expect(side).toContainText('$0.31')
  await expect(side.getByRole('link', { name: 'Open the session in the control panel' })).toHaveAttribute('href', /session=s-writer/)
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-side-work.png` })

  await side.getByRole('tab', { name: 'Notes' }).click()
  await side.getByRole('button', { name: /outline\.md/ }).click()
  await expect(side.locator('pre')).toContainText('Verify the checksum.')
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-side-note.png` })

  // A reference in the room opens in the same panel.
  await page.locator('.tka-rail').getByRole('button', { name: /# room/ }).click()
  await page.getByRole('button', { name: 'path:docs/install.md' }).click()
  await expect(side.locator('.tka-file-ref')).toHaveText('path:docs/install.md')
  await side.getByRole('button', { name: 'Close the side panel' }).click()
  await expect(side).toHaveCount(0)
})

test('the Talkoot app turns the rail into a drawer on a narrow screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 760 })
  await openApp(page)
  await expect(page.locator('.tka-rail')).not.toBeInViewport()
  await page.getByRole('button', { name: 'Teams and members' }).click()
  await expect(page.locator('.tka-rail')).toBeInViewport({ ratio: 1 })
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-narrow-drawer.png` })
  await page.locator('.tka-rail').getByRole('button', { name: /^Writer/ }).click()
  await expect(page.locator('.tka-rail')).not.toBeInViewport()
  await expect(page.locator('.tka-main-head strong')).toHaveText('Writer')
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/talkoot-app-narrow-member.png` })
})

test("the control panel's ⁂ button opens the Talkoot app", async ({ page }) => {
  // A person who left the old view open has this flag stored. It no longer
  // mounts anything.
  await page.addInitScript(() => localStorage.setItem('terva_talkoot_open', '1'))
  await installMockBackend(page, { groups: ['conversation', 'session', 'control', 'talkoot'] })
  await page.goto(panelSessionURL)
  const open = page.getByRole('link', { name: 'Open the Talkoot app' })
  await expect(open).toHaveAttribute('href', '/talkoot/')
  // The panel no longer mounts a Talkoot view of its own.
  await expect(page.locator('.talkoot-bar, .talkoot-team')).toHaveCount(0)
})
