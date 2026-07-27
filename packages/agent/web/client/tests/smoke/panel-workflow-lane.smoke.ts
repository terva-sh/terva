import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL, SMOKE_SESSION } from './support'

// The board's workflow lane and the run detail behind it.
//
// Both defects this pins were invisible to the component tests and obvious in a
// screenshot, which is the whole reason this file exists:
//
//   1. CopyButton's default variant is absolutely positioned into a message
//      bubble's corner at opacity 0, revealed by `.msg-wrap:hover`. Used as a
//      control on a modal — over the script, over the resume command — it
//      rendered as NOTHING a user could see or click. happy-dom applies no
//      stylesheet and has no layout, so every assertion about it passed.
//   2. `.board-tile-meta` clips to one nowrap line. A session's meta fits; a
//      run's is three facts joined ("12/12 agents · 4 replayed · $6.8812") and
//      the cost — the number an operator is actually looking for — fell off the
//      end at desktop tile width.
const RUNS = [
  {
    id: 'wf_1a2b3c4d5e6f',
    name: 'exhaustive-review',
    status: 'incomplete',
    started: '2026-07-26T18:04:00Z',
    completed: 1,
    cost_usd: 0.4213,
    script_at: '/Users/x/plans/review.js',
    resumable: true,
  },
  {
    id: 'wf_9f8e7d6c5b4a',
    name: 'migrate-call-sites',
    status: 'done',
    started: '2026-07-26T14:10:00Z',
    ended: '2026-07-26T14:41:00Z',
    completed: 12,
    agents: 12,
    cached: 4,
    cost_usd: 6.8812,
  },
]

const VIEW = {
  run: RUNS[0],
  script: "export const meta = { name: 'exhaustive-review', description: 'x' }\nawait agent('slice')\n",
  results: [{ label: 'review:correctness', agent_id: 'rc-4f21', bytes: 2148, result: { finding: 'one' } }],
}

// reachable reports whether a control is genuinely on screen where it was put.
//
// Neither half is what Playwright's toBeVisible() checks: it ignores opacity
// entirely, and an absolutely-positioned element reports a perfectly healthy
// bounding box wherever it landed. Both halves failed here — the button was at
// opacity 0 AND, having no positioned ancestor inside the modal, had flown to the
// top-right corner of the viewport.
//
// Opacity is walked up the tree because it does not inherit as a computed value:
// a child of an opacity-0 parent computes its own opacity as 1 and is invisible
// anyway. The first version of this probe read `elementFromPoint`'s result, which
// is the button's <svg> child — computed opacity 1 — and so passed against the
// unfixed build. Pinned by neutering the fix and watching this fail.
async function reachable(page: import('@playwright/test').Page, selector: string): Promise<string> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel) as HTMLElement | null
    if (!el) return 'not in the DOM'
    for (let n: HTMLElement | null = el; n; n = n.parentElement) {
      const cs = getComputedStyle(n)
      if (cs.opacity === '0') return `invisible: ${n.className || n.tagName} is at opacity 0`
      if (cs.visibility === 'hidden' || cs.display === 'none') return `hidden: ${n.className || n.tagName}`
      if (n.classList.contains('modal')) break
    }
    const r = el.getBoundingClientRect()
    const parent = el.parentElement!.getBoundingClientRect()
    // Escaped its own row: absolutely positioned with no positioned ancestor
    // nearby, it anchors to whatever is (here, the fixed scrim = the viewport).
    if (r.left < parent.left - 1 || r.right > parent.right + 1 || r.top < parent.top - 1) {
      return `displaced: at ${Math.round(r.left)},${Math.round(r.top)} but its row is ${Math.round(parent.left)},${Math.round(parent.top)}`
    }
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
    return hit?.closest(sel) ? 'ok' : 'covered by something else'
  }, selector)
}

test('panel: the workflow lane shows a run whole, and its copies are reachable', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') {
        return { sessions: [{ id: SMOKE_SESSION, title: 'dashboard', model: 'opus-5', messages: 4 }] }
      }
      if (method === 'workflows.list') return { runs: RUNS }
      if (method === 'workflows.get') return VIEW
      if (method === 'surface.get') return { surface: { id: 'tasks', tasks: { tasks: [] } } }
      return undefined
    },
  })
  await page.addInitScript(() => localStorage.setItem('terva_viewmode', 'board'))
  await page.goto(panelSessionURL)
  await expect(page.locator('.board-view')).toBeVisible()

  const tiles = page.locator('.workflow-tile')
  await expect(tiles).toHaveCount(2)

  // Defect 2: the finished run's cost must be on screen, not clipped off the
  // end of a nowrap line. scrollWidth > clientWidth is the actual test — the
  // text is present in the DOM either way.
  const meta = tiles.nth(1).locator('.board-tile-meta').first()
  await expect(meta).toContainText('$6.8812')
  expect(
    await meta.evaluate((el) => el.scrollWidth <= el.clientWidth),
    'the run meta line is clipped — the cost is in the DOM but off the end of the tile',
  ).toBe(true)

  // Defect 1, on the resume command: an interrupted run's whole value is that
  // command, and the copy beside it has to be a control you can hit.
  await tiles.first().click()
  await expect(page.locator('.wf-modal')).toBeVisible()
  await expect(page.locator('.wf-cmd code')).toHaveText(
    'terva workflow run /Users/x/plans/review.js --resume wf_1a2b3c4d5e6f',
  )
  expect(await reachable(page, '.wf-cmd .copy-btn'), 'the resume command copy').toBe('ok')

  // And on the script, which is the tab this screen exists for.
  await page.getByRole('button', { name: 'Script' }).click()
  await expect(page.locator('.wf-pre')).toContainText('exhaustive-review')
  expect(await reachable(page, '.wf-script-bar .copy-btn'), 'the script copy').toBe('ok')

  // A large report must not paste itself into the page on open.
  await page.getByRole('button', { name: /Results/ }).click()
  await expect(page.locator('.wf-result-head')).toContainText('review:correctness')
  await expect(page.locator('.wf-result-body')).toHaveCount(0)
  await page.locator('.wf-result-head').click()
  await expect(page.locator('.wf-result-body .wf-pre')).toContainText('"finding"')

  // WF_SHOT=<dir> captures the screen, the same escape hatch panel-group-menu
  // uses. Assertions catch a defect you already know about; these are for the
  // ones you don't, and both of the bugs above were found this way and not by
  // any test here.
  if (process.env.WF_SHOT) {
    await page.screenshot({ path: `${process.env.WF_SHOT}/wf-results.png` })
    await page.getByRole('button', { name: 'Overview' }).click()
    await page.screenshot({ path: `${process.env.WF_SHOT}/wf-overview.png` })
    await page.getByRole('button', { name: 'Script' }).click()
    await page.screenshot({ path: `${process.env.WF_SHOT}/wf-script.png` })
    await page.locator('.wf-modal .modal-head button').click()
    await expect(page.locator('.wf-modal')).toHaveCount(0)
    await page.screenshot({ path: `${process.env.WF_SHOT}/wf-lane.png`, fullPage: true })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.screenshot({ path: `${process.env.WF_SHOT}/wf-mobile.png`, fullPage: true })
  }
})
