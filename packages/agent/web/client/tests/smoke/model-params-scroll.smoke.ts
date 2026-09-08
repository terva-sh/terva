import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The model settings form is the modal's whole body, and .modal caps at 82vh
// with overflow:hidden. A real params view is nine fields with help text under
// each, which is taller than that on any laptop, so the tail of the form, the
// row holding Save, was clipped away with nothing scrolling to reach it. The
// only way out was to make the window taller, which on a laptop at its native
// size is not a way out at all.
//
// happy-dom cannot see this: it renders the button, and every unit assertion
// about Save passes while Save is unreachable. It takes a real layout at a real
// viewport, which is why this is a smoke.

const models = [
  { id: 'gpt-5.6-luna', provider: 'openai-codex', context_window: 1050000, current: true },
]

// The params the daemon actually sends for a model like this one. Kept long on
// purpose: the bug is a function of form height, so a short fixture tests
// nothing.
const params = [
  { key: 'displayName', label: 'display name', kind: 'text', value: 'GPT-5.6 Luna', help: 'What to call this model on screen, in place of its id. Worth setting for local models whose ids run long. Empty uses the id.' },
  { key: 'baseURL', label: 'base url', kind: 'text', default: 'provider default', help: "Send this model's requests somewhere else. Changing it rebuilds the client." },
  { key: 'contextWindow', label: 'context window', kind: 'int', value: '1050000', help: "The model's hard ceiling. Leave empty to use what terva knows about this model." },
  { key: 'desiredContextWindow', label: 'desired context window', kind: 'int', value: '272000', help: "The working window that drives auto-condensing, not the model's ceiling. Keep a large-window model but condense earlier." },
  { key: 'maxTokens', label: 'max tokens', kind: 'int', value: '128000', help: "Cap on a single reply. Empty means the model's own maximum." },
  { key: 'temperature', label: 'temperature', kind: 'float', help: 'Sampling temperature.' },
  { key: 'defaultReasoning', label: 'default thinking', kind: 'enum', default: 'maximum', options: ['minimal', 'low', 'medium', 'high', 'maximum'], help: 'The level a new session starts this model at.' },
  { key: 'reasoning', label: 'thinking', kind: 'tristate', default: 'on', help: 'Whether this model thinks at all. A local endpoint lists its models without saying, so one that reasons usually arrives with this off.' },
  { key: 'images', label: 'image input', kind: 'tristate', default: 'on', help: 'Whether this model accepts images.' },
]

const view = { provider: 'openai-codex', model: 'gpt-5.6-luna', has_override: true, params }

test('the model settings form scrolls and keeps Save on screen in a short window', async ({
  page,
}, testInfo) => {
  // 900×620: a laptop with the browser chrome taking its share. Nothing
  // pathological, and the form does not fit.
  await page.setViewportSize({ width: 900, height: 620 })

  const saved: unknown[] = []
  await installMockBackend(page, {
    respond: (method, p) => {
      if (method === 'models.list') return { models }
      if (method === 'models.params') return view
      if (method === 'models.params.set') {
        saved.push(p)
        return {}
      }
      return undefined
    },
  })
  await page.goto(panelSessionURL)

  await page.locator('button.model-btn').click()
  await page.locator('.pick-row', { hasText: 'gpt-5.6-luna' }).locator('button', { hasText: '⚙' }).click()

  const form = page.locator('.modal > .prov-flow')
  await expect(form).toBeVisible()

  // Guard the fixture before asserting anything about the tail. An absence or a
  // reachability claim over a form that never rendered is a test that passes on
  // a broken fixture.
  await expect(form.locator('.prov-flow-title')).toHaveText('openai-codex/gpt-5.6-luna')
  // Preact assigns value as a PROPERTY, so an [value=…] attribute selector
  // matches nothing here however the box renders.
  await expect(form.locator('.prov-field', { hasText: 'display name' }).locator('input')).toHaveValue(
    'GPT-5.6 Luna',
  )

  // Guard that the arm under test BINDS: the form has to be taller than the
  // modal that holds it at this viewport, or the whole test is about a form
  // that fits. Measured against the MODAL, not against the form's own
  // scrollHeight, because an unfixed form does not scroll at all: its
  // scrollHeight equals its clientHeight while it overflows the modal, so that
  // reading is 0 in exactly the case this test exists for.
  const modal = page.locator('.modal')
  const formHeight = await form.evaluate((el) => el.scrollHeight)
  const modalHeight = await modal.evaluate((el) => el.clientHeight)
  expect(formHeight).toBeGreaterThan(modalHeight + 80)

  // The point. Save is on screen without touching the window, and a click on it
  // reaches the daemon.
  const save = form.locator('button', { hasText: /^Save$/ })
  await expect(save).toBeInViewport()

  const box = await save.boundingBox()
  const modalBox = await modal.boundingBox()
  expect(box).not.toBeNull()
  expect(modalBox).not.toBeNull()
  expect(box!.y + box!.height).toBeLessThanOrEqual(modalBox!.y + modalBox!.height + 1)

  // Shot before the click, because a successful save closes the modal and
  // there would be nothing left to photograph.
  await testInfo.attach('model-params-short-viewport.png', {
    body: await modal.screenshot(),
    contentType: 'image/png',
  })

  await save.click({ timeout: 3000 })
  await expect.poll(() => saved.length).toBe(1)
})
