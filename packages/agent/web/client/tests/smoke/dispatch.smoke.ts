import { test, expect } from '@playwright/test'
import { installMockBackend, installStageBackend, panelSessionURL, SMOKE_SESSION } from './support'

for (const kind of ['panel', 'stage']) {
  test(`${kind}: acceptance preserves later writing and tools keep Stop available`, async ({ page }) => {
    const install = kind === 'panel' ? installMockBackend : installStageBackend
    const backend = await install(page, { holdMethods: ['prompt'] })
    await page.goto(kind === 'panel' ? panelSessionURL : `/stage.html?session=${SMOKE_SESSION}`)
    await backend.subscribed
    const composer = page.locator(kind === 'panel' ? 'footer.composer' : '.stage-composer')
    const input = composer.locator('textarea')
    await input.fill('first message')
    await input.press('Enter')
    await expect(input).toHaveValue('first message')
    await expect(composer.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
    await input.fill('later writing')
    backend.release('prompt')
    const stop = composer.getByRole('button', { name: /Stop/ })
    await expect(stop).toBeVisible()
    await expect(input).toHaveValue('later writing')
    backend.pushEvent({ type: 'turn_start' })
    backend.pushEvent({ type: 'turn_end' })
    backend.pushEvent({ type: 'tool_start', name: 'bash', call_id: 'held-tool' })
    await expect(stop).toBeVisible()
    await input.press('Enter')
    await expect(input).toHaveValue('')
    await stop.click()
    backend.pushEvent({ type: 'done' })
    await expect(stop).toHaveCount(0)
  })

  test(`${kind}: a lost acknowledgment retains the draft after reconnect`, async ({ page }) => {
    const install = kind === 'panel' ? installMockBackend : installStageBackend
    const backend = await install(page, { holdMethods: ['prompt'] })
    await page.goto(kind === 'panel' ? panelSessionURL : `/stage.html?session=${SMOKE_SESSION}`)
    await backend.subscribed
    const input = page.locator(kind === 'panel' ? 'footer.composer textarea' : '.stage-composer textarea')
    await input.fill('check whether this arrived')
    await input.press('Enter')
    await expect(input).toHaveValue('check whether this arrived')
    backend.drop()
    await expect(page.getByText(/Check the transcript after reconnecting/)).toBeVisible()
    await expect.poll(backend.subscribeCount).toBeGreaterThan(1)
    await expect(input).toHaveValue('check whether this arrived')
  })

  test(`${kind}: composition confirmation does not submit`, async ({ page }) => {
    const install = kind === 'panel' ? installMockBackend : installStageBackend
    let prompts = 0
    const backend = await install(page, { respond: (method) => { if (method === 'prompt') prompts++; return undefined } })
    await page.goto(kind === 'panel' ? panelSessionURL : `/stage.html?session=${SMOKE_SESSION}`)
    await backend.subscribed
    const input = page.locator(kind === 'panel' ? 'footer.composer textarea' : '.stage-composer textarea')
    await input.fill('未確定')
    await input.dispatchEvent('compositionstart')
    // A synthetic composition event does not activate the operating system's
    // IME. Dispatch its key event too, without a normal Enter's newline action.
    await input.dispatchEvent('keydown', { key: 'Enter', isComposing: false })
    await expect(input).toHaveValue('未確定')
    expect(prompts).toBe(0)
    await input.dispatchEvent('compositionend')
    await input.press('Enter')
    await expect.poll(() => prompts).toBe(1)
  })
}
