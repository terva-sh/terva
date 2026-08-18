import { test, expect } from '@playwright/test'
import { installStageBackend } from './support'

// Card editor (S7.1): the writable sibling of the detail sheet. Open a card's ⋯
// detail → Edit → fix a field → Save, which round-trips untouched fields
// (extensions) through cards.edit and re-runs the lint so a fix visibly clears.
// The card here carries a MALFORMED macro ({{user)} — the kobeni tolerance
// probe) that the deterministic lint flags until it's corrected.
test('stage: edit a card, fix a finding, and save', async ({ page }) => {
  // A holder rather than a bare `let`. The assignment happens inside the mock's
  // respond callback, which the checker cannot order against the reads at the
  // end of the test, so a bare `let edited = null` narrows to its initializer
  // there and every property read types as `never`. A property on an object is
  // not narrowed that way, and the runtime behaviour is identical.
  const captured: { edit?: { id?: string; card?: { data?: Record<string, unknown> } } } = {}

  await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'cards.get')
        return {
          id: 'card-1',
          name: 'Ivy',
          greetings: 1,
          raw: {
            spec: 'chara_card_v2',
            spec_version: '2.0',
            data: {
              name: 'Ivy',
              description: 'A florist with a sharp tongue.',
              personality: '',
              first_mes: 'Hey {{user)}, welcome in.',
              extensions: { depth_prompt: { depth: 4 } },
            },
          },
        }
      // Before an edit the malformed macro is flagged; a saved edit clears it.
      if (method === 'cards.lint')
        return captured.edit
          ? { findings: [] }
          : { findings: [{ rule: 'malformed-macro', severity: 'warn', field: 'first_mes', message: 'Malformed macro', detail: '{{user)}' }] }
      if (method === 'cards.edit') {
        captured.edit = params as typeof captured.edit
        return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(1)

  // ⋯ opens the detail sheet; its Edit button swaps to the editor.
  await page.locator('.stage-card__more').first().click()
  await expect(page.locator('.stage-sheet--detail')).toBeVisible()
  await page.locator('.stage-cardsheet__edit').click()
  await expect(page.locator('.stage-cardeditor')).toBeVisible()

  // Fields load from the card; the malformed-macro finding is shown.
  // ⚠️ Scoped to the card editor. The studio keeps BOTH tabs mounted — switching
  // to "You" must not cost an unsaved draft — so the persona pane's "Your name"
  // is in the DOM at the same time and a bare .stage-editfield/'Name' matches it
  // too.
  const editor = page.locator('.stage-cardeditor')
  await expect(editor.locator('.stage-editfield', { hasText: 'Name' }).locator('input')).toHaveValue('Ivy')
  await expect(page.locator('.stage-lint__item--warn')).toContainText('Malformed macro')

  // Fix the greeting's macro and save.
  const firstMes = editor.locator('.stage-editfield', { hasText: 'First message' }).locator('textarea')
  await firstMes.fill('Hey {{user}}, welcome in.')
  await page.locator('.stage-cardeditor__save').click()

  // cards.edit carries the fixed field AND round-trips the untouched extensions.
  await expect.poll(() => captured.edit?.card?.data?.first_mes).toBe('Hey {{user}}, welcome in.')
  expect(captured.edit?.id).toBe('card-1')
  expect(captured.edit?.card?.data?.extensions).toEqual({ depth_prompt: { depth: 4 } })

  // The re-lint after save reports clean, and the "Saved ✓" marker shows.
  await expect(page.locator('.stage-lint__clean')).toBeVisible()
  await expect(page.locator('.stage-cardeditor__saved')).toBeVisible()
  if (process.env.EDITOR_SHOT) await page.screenshot({ path: `${process.env.EDITOR_SHOT}.png` })
})
