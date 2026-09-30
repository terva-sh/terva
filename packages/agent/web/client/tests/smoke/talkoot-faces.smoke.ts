import { test, expect, type Page } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The Talkoot faces in a real browser: eight members in the sidebar, each in a
// state that draws its own pose, and the idle CPU those eight cost. happy-dom
// runs no CSS animation, so neither can be checked there.
//
// TKT-01M39TTDH3 asks that eight animated marks add no idle CPU anyone can
// measure. The second test measures it: the main thread's task time while the
// sidebar sits idle, in alternating windows at full motion and with motion off.

const member = (id: string, shape: string, color: string, status: Record<string, unknown>) => ({
  id,
  role: 'specialist',
  mark: { shape, color },
  status: { member: id, ...status },
})

const CREW = {
  id: 'crew',
  name: 'crew',
  home: '/w',
  members: [
    { ...member('helm', 'hexagon', '#3E63DD', { presence: 'idle' }), role: 'coordinator' },
    member('jev', 'shield', '#E5484D', { presence: 'working', working: true }),
    member('atlas', 'blob', '#46A758', { presence: 'waiting' }),
    member('rune', 'drop', '#F76B15', { presence: 'paused', paused: 'paused by sothr', pauses: ['person'] }),
    member('kivi', 'tab', '#6E56CF', { presence: 'paused', paused: 'spent $1.00', pauses: ['spend'] }),
    member('sade', 'gem', '#12A594', { presence: 'paused', paused: 'the turn failed', pauses: ['failed'] }),
    member('oras', 'pill', '#AB4ABA', { presence: 'offline' }),
    member('tuli', 'circle', '#FFB224', { presence: 'idle', idle: true }),
  ],
}

const POSES: Record<string, string> = {
  helm: 'open',
  jev: 'focused',
  atlas: 'looking-up',
  rune: 'half-lidded',
  kivi: 'closed-squint',
  sade: 'worried',
  oras: 'closed',
  tuli: 'closed',
}

async function openCrew(page: Page, motion: string) {
  await page.addInitScript((m) => {
    localStorage.setItem('terva_talkoot_open', '1')
    localStorage.setItem('terva_talkoot_motion', m)
  }, motion)
  await installMockBackend(page, {
    groups: ['conversation', 'session', 'control', 'talkoot'],
    respond: (method) => {
      switch (method) {
        case 'talkoot.list':
          return { talkoots: [{ id: 'crew', running: true }] }
        case 'talkoot.get':
          return CREW
        case 'talkoot.room':
          return { total: 0, lines: [] }
        case 'talkoot.inbox':
          return { cards: [] }
      }
      return undefined
    },
  })
  await page.goto(panelSessionURL)
  await expect(page.locator('.talkoot-member-row .talkoot-mark')).toHaveCount(CREW.members.length)
}

test('each member in the sidebar shows the face of its state beside its label', async ({ page }) => {
  await openCrew(page, 'full')
  const rows = page.locator('.talkoot-member-row')
  for (const [i, m] of CREW.members.entries()) {
    const row = rows.nth(i)
    await expect(row.locator('.talkoot-mark')).toHaveAttribute('data-pose', POSES[m.id])
    // The label stays on screen beside the face.
    await expect(row.locator('.talkoot-member-activity')).toBeVisible()
  }
  await expect(rows.nth(6).locator('.talkoot-mark')).toHaveClass(/faded/)
  await expect(rows.nth(7).locator('.talkoot-mark')).not.toHaveClass(/faded/)
  // At 24 pixels the pupil draws.
  const box = await rows.nth(0).locator('.talkoot-mark').boundingBox()
  expect(box?.width).toBe(24)
  const shots = process.env.SMOKE_SHOTS
  if (shots) await page.locator('.talkoot-side').screenshot({ path: `${shots}/talkoot-faces-sidebar.png` })
})

test('eight animated faces add no measurable idle CPU', async ({ page, browserName }) => {
  test.skip(browserName !== 'chromium', 'the measure reads Chromium performance metrics')
  // The windows take about 15 s on a quiet machine, and a busy runner can
  // double that past the 30 s default (TKT-01M3SSWY02).
  test.setTimeout(90_000)
  await openCrew(page, 'full')
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Performance.enable')
  const task = async () => {
    const { metrics } = await cdp.send('Performance.getMetrics')
    return metrics.find((x) => x.name === 'TaskDuration')!.value
  }
  // The page switches motion as the Marks picker does in another tab: the
  // setting in storage, and the event motion.ts listens for.
  const setMotion = (m: string) =>
    page.evaluate((m) => {
      localStorage.setItem('terva_talkoot_motion', m)
      window.dispatchEvent(new CustomEvent('terva-talkoot-motion'))
    }, m)
  const running = () => page.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running').length)

  // 🔑 Two long windows one after the other let a burst of load on a shared
  // runner land on one side only (TKT-01M3S9FQZD). Short windows of each
  // motion alternate in the order off, full, full, off, so load and drift fall
  // on both sides, and the medians drop the window a burst hit.
  const SETTLE = 300
  const WINDOW = 800
  const ROUNDS = 6
  const samples: Record<string, number[]> = { off: [], full: [] }
  // lag is each window's wall time over the time asked for. A starved runner
  // fires its timers late.
  const lag: number[] = []
  // Let the load settle before the first window opens.
  await page.waitForTimeout(1500)
  for (let i = 0; i < ROUNDS; i++) {
    for (const motion of i % 2 ? ['full', 'off'] : ['off', 'full']) {
      await setMotion(motion)
      await page.waitForTimeout(SETTLE)
      const t0 = await task()
      const w0 = Date.now()
      await page.waitForTimeout(WINDOW)
      const ms = ((await task()) - t0) * 1000
      const took = Date.now() - w0
      samples[motion].push((ms / took) * 1000)
      lag.push(took / WINDOW)
      // The positive control, in every window: at full motion the faces run
      // their loops, so a quiet measure means cheap faces and not faces that
      // never moved.
      const n = await running()
      if (motion === 'full') expect(n).toBeGreaterThan(CREW.members.length)
      else expect(n).toBe(0)
    }
  }
  await cdp.detach()
  const median = (xs: number[]) => {
    const s = [...xs].sort((a, b) => a - b)
    return (s[(s.length - 1) >> 1] + s[s.length >> 1]) / 2
  }
  const still = median(samples.off)
  const moving = median(samples.full)
  const show = (xs: number[]) => xs.map((x) => x.toFixed(1)).join(' ')
  const figures = `median off ${still.toFixed(1)}, full ${moving.toFixed(1)}, timer lag ${median(lag).toFixed(2)}; off ${show(samples.off)}; full ${show(samples.full)}`
  test.info().annotations.push({ type: 'idle task ms per second', description: figures })
  // 🔑 A starved runner measures itself, not the page. Still windows cost
  // 0.7 to 1.6 ms/s on a quiet machine, and 3.6 to 5.0 with the browser
  // pinned to one core beside three busy loops or with the whole machine at
  // a load of 12, where the difference still stayed under 1.5. Past
  // STARVED, or with timers firing late, the comparison would say nothing
  // about the faces, so the test records the figures and skips it.
  const STARVED = 8
  test.skip(still > STARVED || median(lag) > 1.25, `the runner is starved, so the comparison is skipped: ${figures}`)
  // A frame budget is 16.7ms. The eight faces may cost a small share of one
  // frame each second, and no more.
  expect(moving - still, figures).toBeLessThan(5)
})
