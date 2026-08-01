// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo } from '../../platform/ctrlproto/types'
import { ModelPicker } from './ModelPicker'
import { modelLabel, renamedTo } from './label'

const LONG_ID = 'hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL'

const renamed: ModelInfo = {
  id: LONG_ID,
  provider: 'ollama',
  display_name: 'Qwen Coder',
  renamed: true,
}
// The daemon sends a display name for EVERY model, so the catalog case is the
// one that proves `renamed` is doing the work rather than mere presence.
const catalog: ModelInfo = {
  id: 'claude-sonnet-4-5',
  provider: 'anthropic',
  display_name: 'Claude Sonnet 4.5 (latest)',
}
const groups: [string, ModelInfo[]][] = [
  ['ollama', [renamed]],
  ['anthropic', [catalog]],
]

const picker = (props: Partial<Parameters<typeof ModelPicker>[0]> = {}) => (
  <ModelPicker
    groups={groups}
    favorites={[]}
    current=""
    onSwitch={() => {}}
    onToggleFavorite={() => {}}
    onSetDefault={() => {}}
    onEdit={() => {}}
    onClose={() => {}}
    {...props}
  />
)

afterEach(cleanup)

describe('model display names', () => {
  it('substitutes only an operator name for the id', () => {
    expect(modelLabel(renamed)).toBe('Qwen Coder')
    expect(renamedTo(renamed)).toBe('Qwen Coder')
    // A catalog name is LONGER than the id it would replace, so it must not win.
    expect(modelLabel(catalog)).toBe('claude-sonnet-4-5')
    expect(renamedTo(catalog)).toBeUndefined()
    // A peer that predates the fields keeps rendering ids.
    expect(modelLabel({ id: 'llama3', provider: 'ollama' })).toBe('llama3')
  })

  it('leads a renamed row with the name and keeps the id beside it', () => {
    render(picker())
    const row = screen.getByText('Qwen Coder').closest('.pick-row')
    expect(row).toBeTruthy()
    // The id is still on the row: it is the only spelling in logs and --model.
    expect(row!.textContent).toContain(LONG_ID)
    // And it is the NAME that occupies the prominent slot.
    expect(row!.querySelector('.pick-id')?.textContent).toBe('Qwen Coder')
  })

  it('leaves an un-renamed row exactly as it was', () => {
    render(picker())
    const row = screen.getByText('claude-sonnet-4-5').closest('.pick-row')
    expect(row!.querySelector('.pick-id')?.textContent).toBe('claude-sonnet-4-5')
    expect(row!.textContent).not.toContain('Claude Sonnet 4.5 (latest)')
  })

  it('finds a renamed model by either spelling', () => {
    render(picker())
    const search = screen.getByPlaceholderText('Search models…')

    fireEvent.input(search, { target: { value: 'qwen coder' } })
    expect(screen.queryByText('Qwen Coder')).toBeTruthy()
    expect(screen.queryByText('claude-sonnet-4-5')).toBeNull()

    fireEvent.input(search, { target: { value: 'unsloth' } })
    expect(screen.queryByText('Qwen Coder')).toBeTruthy()

    // A catalog display name is not searchable, because it is not shown.
    fireEvent.input(search, { target: { value: 'latest' } })
    expect(screen.queryByText('claude-sonnet-4-5')).toBeNull()
  })
})
