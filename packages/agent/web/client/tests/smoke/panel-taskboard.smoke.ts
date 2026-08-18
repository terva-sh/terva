import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The task board pane — what the MODEL is tracking, as opposed to the "Agents"
// pane, which is the swarm of background sub-agents.
//
// This is a browser test rather than a component test because the last two panes
// added here shipped layout defects that vitest was green on: happy-dom applies
// no stylesheet and has no layout, so it cannot see a clipped title, a control at
// opacity 0, or text that has fallen out of its box. A task title is a sentence,
// and the failure mode this pins is the one a table-shaped row would produce —
// truncating it to something unreadable.
//
// Set TB_SHOT=<dir> to capture screenshots while iterating on the styling.
const TASKS = [
  { id: 'task-1', status: 'done', title: 'Read the trust store and find the stale verdict' },
  {
    id: 'task-2',
    status: 'active',
    title: 'Re-point the swarm_spawn gate at the live verdict',
    active_form: 'Re-pointing the swarm_spawn gate at the live verdict',
    note: 'applyTrust has to move w.trusted before it fans out to the sessions',
  },
  {
    id: 'task-3',
    status: 'blocked',
    title: 'Re-derive project hooks on a trust flip',
    note: 'needs a re-wire seam on the tool-call ladder',
  },
  { id: 'task-4', status: 'pending', title: 'Add the web pane' },
  { id: 'task-5', status: 'cancelled', title: 'Rename the surface id' },
]

const SURFACES = [
  { id: 'taskboard', title: 'Tasks', icon: '✓', kind: 'taskboard', scope: 'session', live: true },
  { id: 'tasks', title: 'Agents', icon: '🐝', kind: 'tasks', scope: 'workspace', live: true, actions: true },
]

function backend(page: import('@playwright/test').Page) {
  return installMockBackend(page, {
    respond(method, params) {
      if (method === 'surfaces.list') return { surfaces: SURFACES }
      if (method === 'surface.get') {
        const id = (params as { id?: string })?.id
        if (id === 'taskboard') return { surface: { id, title: 'Tasks', kind: 'taskboard', task_board: { tasks: TASKS } } }
        if (id === 'tasks') return { surface: { id, title: 'Agents', kind: 'tasks', tasks: { tasks: [] } } }
      }
      return undefined
    },
  })
}

// The panes live behind the topbar's pane toggle, in a tab rail — the same
// route a user takes, so the tab strip itself is under test.
async function openRail(page: import('@playwright/test').Page) {
  await page.locator('.topbar .dot.open').waitFor()
  await page.locator('button[title="Panes (usage, settings, extensions)"]').click()
  const rail = page.locator('.pane-rail')
  await rail.waitFor()
  return rail
}

async function openBoard(page: import('@playwright/test').Page) {
  const rail = await openRail(page)
  await rail.locator('.pane-tab', { hasText: 'Tasks' }).first().click()
}

// overflows reports text clipped or spilling out of its own row — the class of
// defect that only exists once a stylesheet and a layout engine are involved.
async function overflows(page: import('@playwright/test').Page, selector: string): Promise<string> {
  return page.evaluate((sel) => {
    for (const el of Array.from(document.querySelectorAll(sel)) as HTMLElement[]) {
      const row = el.closest('.tb-row') as HTMLElement | null
      if (!row) return `${sel}: no .tb-row ancestor`
      const e = el.getBoundingClientRect()
      const r = row.getBoundingClientRect()
      // scrollWidth > clientWidth alone does NOT prove clipping (a scrollable
      // box is fine), so compare against the row's painted box too.
      if (el.scrollWidth > el.clientWidth + 1 && getComputedStyle(el).overflow !== 'auto') {
        return `${sel}: "${el.textContent?.slice(0, 40)}" is clipped (${el.scrollWidth} > ${el.clientWidth})`
      }
      if (e.right > r.right + 1 || e.bottom > r.bottom + 1) {
        return `${sel}: "${el.textContent?.slice(0, 40)}" spills outside its row`
      }
    }
    return ''
  }, selector)
}

test('the task board shows what the model is tracking', async ({ page }) => {
  const mock = await backend(page)
  await page.goto(panelSessionURL)
  await mock.subscribed

  await openBoard(page)
  await expect(page.locator('.tb-row')).toHaveCount(TASKS.length)

  // An active task shows what the model says it is DOING, not the bare title.
  await expect(page.locator('.tb-row.active .tb-title')).toHaveText(
    'Re-pointing the swarm_spawn gate at the live verdict',
  )
  // Every task keeps its handle: it is what task_update needs, and what a human
  // reads back to the agent when something is wrong.
  await expect(page.locator('.tb-id').first()).toHaveText('task-1')
  await expect(page.locator('.tb-head')).toContainText('2')

  // The whole point of the pane: the sentences are readable.
  for (const sel of ['.tb-title', '.tb-note']) {
    expect(await overflows(page, sel)).toBe('')
  }

  if (process.env.TB_SHOT) {
    await page.screenshot({ path: `${process.env.TB_SHOT}/taskboard-desktop.png`, fullPage: true })
  }
})

test('the task board stays readable on a phone', async ({ page }) => {
  const mock = await backend(page)
  await page.setViewportSize({ width: 390, height: 780 })
  await page.goto(panelSessionURL)
  await mock.subscribed

  await openBoard(page)
  await expect(page.locator('.tb-row')).toHaveCount(TASKS.length)

  for (const sel of ['.tb-title', '.tb-note']) {
    expect(await overflows(page, sel)).toBe('')
  }
  // The page itself must not scroll sideways — the rule the shared design system
  // asserts, and the one a long unbroken task title would break.
  const overflowX = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  )
  expect(overflowX).toBe(false)

  if (process.env.TB_SHOT) {
    await page.screenshot({ path: `${process.env.TB_SHOT}/taskboard-phone.png`, fullPage: true })
  }
})

// The two panes are titled differently on purpose: they were both "Tasks", one
// showing the swarm and one the model's list, which is indistinguishable in a
// tab strip.
test('the swarm pane and the task board are not both called Tasks', async ({ page }) => {
  const mock = await backend(page)
  await page.goto(panelSessionURL)
  await mock.subscribed

  const rail = await openRail(page)
  await expect(rail.locator('.pane-tab', { hasText: 'Agents' })).toHaveCount(1)
  await expect(rail.locator('.pane-tab-label', { hasText: /^Tasks$/ })).toHaveCount(1)
})
