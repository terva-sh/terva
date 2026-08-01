import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The Agents pane's archive affordance: the button beside Remove, and the
// absence of anything reporting on what has been archived.
//
// A smoke because the pair is a VISUAL contract. Archive and Remove sit next to
// each other and do opposite things with the transcript, so which one wears the
// destructive styling is the entire signal — and no unit test sees colour.

const task = (over: Record<string, unknown> = {}) => ({
  id: 'inspect-the-repo-191000',
  task: 'Inspect the current repository and report the build layout',
  status: 'done',
  activity: 'done (offline)',
  model: 'k3-256k',
  provider: 'kimi',
  dir: '/Users/x/src/dol-clone',
  started: '2026-07-30T15:52:09Z',
  finished: '2026-07-30T16:04:41Z',
  cost_usd: 0.42,
  lines: 128,
  ...over,
})

function mount(list: unknown) {
  return {
    respond: (method: string, params: unknown) => {
      if (method === 'surfaces.list') return { surfaces: [{ id: 'tasks', title: 'Agents', kind: 'tasks' }] }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'tasks')
        return { surface: { id: 'tasks', title: 'Agents', kind: 'tasks', tasks: list } }
      return undefined
    },
  }
}

async function openTasks(page: import('@playwright/test').Page) {
  await page.goto(panelSessionURL)
  await page.locator('.topbar .dot.open').waitFor()
  await page.locator('.topbar button[title="Panes (usage, settings, extensions)"]').click()
  await page.locator('.pane-tab[title="Agents"]').click()
}

test('a finished agent offers Archive beside Remove', async ({ page }) => {
  await installMockBackend(page, mount({ tasks: [task()] }))
  await openTasks(page)

  const row = page.locator('.task-actions').first()
  await expect(row.getByRole('button', { name: 'Archive' })).toBeVisible()
  await expect(row.getByRole('button', { name: 'Remove' })).toBeVisible()

  // Archive must not wear the destructive styling — it is the SAFE half of the
  // pair, and a red button next to a red button teaches nothing.
  const archive = row.getByRole('button', { name: 'Archive' })
  await expect(archive).not.toHaveClass(/danger/)
  await expect(row.getByRole('button', { name: 'Remove' })).toHaveClass(/danger/)

  if (process.env.TASKS_SHOT) await page.screenshot({ path: `${process.env.TASKS_SHOT}.png`, fullPage: true })
})

// Nothing reports on the archive, by design: an archived agent is unreachable
// from terva, and a count is the read path that grows into a list. A server
// that sends the field anyway (an older or newer daemon) must not resurrect a
// display for it.
test('the pane never reports on the archive', async ({ page }) => {
  await installMockBackend(page, mount({ tasks: [task()], archived: 11 }))
  await openTasks(page)

  await expect(page.locator('.task-actions').first()).toBeVisible()
  await expect(page.locator('.tasks-archived')).toHaveCount(0)
  await expect(page.locator('.tasks-body')).not.toContainText('archived')

  if (process.env.TASKS_SHOT) await page.screenshot({ path: `${process.env.TASKS_SHOT}-noreport.png`, fullPage: true })
})

// An empty list stays empty. It does not grow a consolation line about work
// that has left the program.
test('an emptied list says only that it is empty', async ({ page }) => {
  await installMockBackend(page, mount({ tasks: [], archived: 11 }))
  await openTasks(page)

  await expect(page.locator('.pick-empty')).toContainText('no background agents')
  await expect(page.locator('.tasks-archived')).toHaveCount(0)
})

// Clicking Archive dispatches the verb — the wiring the unit tests cannot see.
test('Archive dispatches the archive action', async ({ page }) => {
  const seen: string[] = []
  await installMockBackend(page, {
    respond: (method: string, params: unknown) => {
      if (method === 'surfaces.list') return { surfaces: [{ id: 'tasks', title: 'Agents', kind: 'tasks' }] }
      if (method === 'surface.get' && (params as { id?: string })?.id === 'tasks')
        return { surface: { id: 'tasks', title: 'Agents', kind: 'tasks', tasks: { tasks: [task()] } } }
      if (method === 'surface.action') {
        const p = params as { action?: string; args?: Record<string, string> }
        seen.push(`${p.action}:${p.args?.id ?? ''}`)
        return {}
      }
      return undefined
    },
  })
  await openTasks(page)

  await page.locator('.task-actions').first().getByRole('button', { name: 'Archive' }).click()
  await expect.poll(() => seen).toContain('archive:inspect-the-repo-191000')
})
