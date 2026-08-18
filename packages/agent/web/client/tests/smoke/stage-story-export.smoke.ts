import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Export the scene as a story: the Steering drawer's Session tab downloads the
// markdown the daemon renders. Zero backend — this asserts the verb fires with
// the session in the frame, and that the browser download lands with the
// daemon's suggested name. What the markdown CONTAINS is the renderer's
// business and is pinned by Go tests; base64 here is just "some bytes".
test('stage: export a session as a story downloads markdown', async ({ page }) => {
  await stubMedia(page)

  const SESSION = { id: SMOKE_SESSION, title: 'The Lowtown Job', experience: 'chat', card: 'elira-1', world_lore: [] }
  let exportSess = ''
  let exportFormat = ''
  const mock = await installStageBackend(page, {
    cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }],
    respond: (method, params, sess) => {
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'sessions.export') {
        exportSess = sess ?? ''
        exportFormat = (params as { format?: string })?.format ?? ''
        // "---\ntitle: x\n---\n" — the shape, not the substance.
        return { filename: 'The Lowtown Job-abc12345.md', mime_type: 'text/markdown; charset=utf-8', bytes: 'LS0tCnRpdGxlOiB4Ci0tLQo=' }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // The Session tab is the drawer's landing tab, so the action is one click in.
  await page.locator('.stage-steer-btn').click()
  const downloadPromise = page.waitForEvent('download')
  await page.locator('.stage-export').click()
  const download = await downloadPromise

  expect(download.suggestedFilename()).toBe('The Lowtown Job-abc12345.md')
  // The verb is session-scoped: the id rides the frame, not the params.
  expect(exportSess).toBe(SMOKE_SESSION)
  expect(exportFormat).toBe('markdown')
  // The button settles back rather than staying stuck in its pending label.
  await expect(page.locator('.stage-export')).toBeEnabled()
})
