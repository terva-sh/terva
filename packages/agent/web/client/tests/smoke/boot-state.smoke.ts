import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL, SMOKE_SESSION } from './support'

// The boot state: what the panel and Stage say between painting and knowing
// anything about the workspace.
//
// The bug these guard is not that the wait was unsignalled — it is that the wait
// was signalled WRONGLY. Both surfaces render their lists from a useState
// default, and both treated "the array is empty" as "there is nothing here", so
// a page that had merely finished painting told you:
//
//   "No sessions in this workspace yet."   (features/board/SessionsBoard.tsx)
//   "No personas available."               (features/landing/PanelLanding.tsx)
//   "No characters yet — drop a card PNG…" (apps/stage/Library.tsx)
//
// while the socket was still connecting. The connection dot in the corner did
// say `connecting`, in nine colour-only pixels, against a sentence.
//
// These run in a real browser rather than happy-dom because the thing under
// test is what a person SEES at a moment in time, and because the two states
// this distinguishes render identical DOM shape by design — a placeholder in
// place of an empty state, in the same slot.
//
// The window is normally too short to catch, which is why it went untested: a
// loopback daemon answers in tens of milliseconds. deferHello holds it open.

test('panel: does not claim an empty workspace before the daemon has answered', async ({ page }) => {
  const mock = await installMockBackend(page, { deferHello: true })

  // No `?session=` — a fresh tab lands on the session-focused landing, which is
  // exactly where both false claims lived.
  await page.goto('/')

  // Painted, connecting, and knowing nothing. It must say so, and it must not
  // say the other thing.
  await expect(page.getByText('Loading sessions…')).toBeVisible()
  await expect(page.getByText('Loading personas…')).toBeVisible()
  await expect(page.getByText('No sessions in this workspace yet.')).toHaveCount(0)
  await expect(page.getByText('No personas available.')).toHaveCount(0)

  // The banner is on a grace period so a fast local connect never flashes it.
  // Past that, silence is not an option.
  await expect(page.locator('.ui-conn-banner')).toBeVisible({ timeout: 5_000 })
  await expect(page.locator('.ui-conn-banner')).toContainText('Connecting to terva')

  // The daemon answers. Every placeholder resolves into a real answer, and the
  // banner leaves without being dismissed.
  mock.sendHello()
  await expect(page.locator('.ui-conn-banner')).toHaveCount(0)
  await expect(page.getByText('Loading sessions…')).toHaveCount(0)
  await expect(page.getByText('smoke')).toBeVisible()
  // The default mock serves no personas, so THIS empty state is now the truth —
  // the placeholder must not have become a permanent shimmer.
  await expect(page.getByText('No personas available.')).toBeVisible()
})

test('panel: says so loudly when a live connection drops', async ({ page }) => {
  const mock = await installMockBackend(page)
  await page.goto('/')
  await expect(page.getByText('smoke')).toBeVisible()
  await expect(page.locator('.ui-conn-banner')).toHaveCount(0)

  // The socket dies under us — a daemon restart, a laptop sleep, a radio
  // handoff. Before this change the only sign was the corner dot turning red.
  mock.drop()

  // No grace period here: we HAD data and it has stopped being current, so the
  // banner is immediate and reads differently from a slow boot.
  await expect(page.locator('.ui-conn-banner')).toBeVisible()
  await expect(page.locator('.ui-conn-banner')).toContainText('Lost the connection')

  // The screen behind it stays readable — the banner does not block, because a
  // 1.5s reconnect blip must not feel like a fault.
  await expect(page.getByText('smoke')).toBeVisible()
})

// The board's swarm lane. Its window is not the one deferHello holds open: the
// lane only renders once a session is picked, and picking one needs the hello.
// So hold back exactly the verb that fills it and let everything else boot.
test('panel: does not report a quiet swarm before the tasks surface has answered', async ({ page }) => {
  const mock = await installMockBackend(page, { holdMethods: ['surface.get'] })

  // Board mode is persisted, so a panel can boot straight into it — which is
  // the case where the lane paints before its fetch resolves.
  await page.addInitScript(() => localStorage.setItem('terva_viewmode', 'board'))
  await page.goto(panelSessionURL)

  await expect(page.getByText('Loading swarm agents…')).toBeVisible()
  await expect(page.getByText('No swarm agents running.')).toHaveCount(0)
  // The lane is not a read-only readout: spawning needs no data, so the
  // affordance must not be hidden behind the wait.
  await expect(page.getByRole('button', { name: '+ Spawn' })).toBeVisible()

  mock.release('surface.get', { surface: { id: 'tasks', kind: 'tasks', tasks: { tasks: [] } } })
  await expect(page.getByText('No swarm agents running.')).toBeVisible()
  await expect(page.getByText('Loading swarm agents…')).toHaveCount(0)
})

// The defect the test above uncovered, which is worse than the empty-state one:
// the board effect fired on the FIRST render, while the socket was still
// connecting. Client.send rejects "not connected" immediately, fetchBoardTasks
// catches it and leaves the lane empty — and with viewMode as its only real
// dependency the effect never ran again. The swarm lane sat empty for the life
// of the page, whatever was actually running.
//
// The panel's own comments already name this trap on the workflow lane ("a send
// on a still-connecting socket rejects, and with viewMode as the only
// dependency nothing would ever change again — same shape that once broke the
// persona shelves"). This effect had it too, unnoticed, because sessions.list
// happens to be re-issued by a 4s poll and the tasks surface has no such poll.
// dropFirstConnection is what makes this deterministic rather than a coin flip:
// see its comment in support.ts. Written first without it, this test PASSED
// against the broken code — against a mock on loopback the mount-flush fetch
// usually wins the race with the handshake. A regression test that can go green
// with the bug present is worse than none.
test('panel: the swarm lane fills once the connection is actually up', async ({ page }) => {
  await installMockBackend(page, {
    dropFirstConnection: true,
    respond: (method, params) => {
      if (method === 'surface.get' && (params as { id?: string })?.id === 'tasks') {
        return {
          surface: {
            id: 'tasks',
            kind: 'tasks',
            tasks: { tasks: [{ id: 'a1', task: 'review the diff', status: 'running', model: 'model-a' }] },
          },
        }
      }
      return undefined
    },
  })

  await page.addInitScript(() => localStorage.setItem('terva_viewmode', 'board'))
  await page.goto(panelSessionURL)

  // Under the ungated effect this never arrived: the effect's one and only run
  // happened on the socket that just died, and nothing re-ran it. The reconnect
  // takes ~1.5s, so this needs more than the default assertion budget.
  await expect(page.getByText('review the diff')).toBeVisible({ timeout: 15_000 })
})

// Stage's transcript — the sharpest case of the whole class. The subscribe is
// fire-and-forget and the snapshot arrives later as an EVENT, so this window is
// wide open by design and needs no mock trickery to observe: just don't push a
// snapshot.
test('stage: does not tell you to start a scene that already has history', async ({ page }) => {
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create')
        return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Subscribed, no snapshot yet. "Say something to begin." here is a claim about
  // a conversation nobody has looked at.
  await expect(page.getByText('Loading this scene…')).toBeVisible()
  await expect(page.getByText('Say something to begin.')).toHaveCount(0)

  // The snapshot lands and says the scene really is empty — now the invitation
  // is the truth, and the placeholder must give way to it.
  mock.pushEvent({
    type: 'snapshot',
    snapshot: {
      session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' },
      epoch: 1,
      base: 0,
      total: 0,
      messages: [],
    },
  })
  await expect(page.getByText('Say something to begin.')).toBeVisible()
  await expect(page.getByText('Loading this scene…')).toHaveCount(0)
})

test('stage: does not claim an empty library before cards.list has answered', async ({ page }) => {
  const mock = await installMockBackend(page, {
    deferHello: true,
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })

  await page.goto('/stage.html')

  await expect(page.getByText('Loading your characters…')).toBeVisible()
  await expect(page.getByText('No characters yet — drop a card PNG here, paste a URL, or use Import.')).toHaveCount(0)
  await expect(page.locator('.ui-conn-banner')).toBeVisible({ timeout: 5_000 })

  mock.sendHello()
  await expect(page.locator('.stage-card')).toHaveCount(1)
  await expect(page.getByText('Loading your characters…')).toHaveCount(0)
  await expect(page.locator('.ui-conn-banner')).toHaveCount(0)
})
