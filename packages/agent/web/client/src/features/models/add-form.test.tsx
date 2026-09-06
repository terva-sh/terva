// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo, ModelParamsView } from '../../platform/ctrlproto/types'
import { ModelPicker } from './ModelPicker'
import { ModelParamsForm } from './ModelParamsForm'

// The web half of creating a model terva does not ship. The daemon owns the two
// refusals this cannot make (a duplicate id, an unreachable provider); the form
// owns the two it can see the point of, and the seed that makes clone-from work.

// A clone source shaped like almost every clone source: real effective values,
// and NO models.json entry, so every `value` is empty.
const cloneSource: ModelParamsView = {
  provider: 'anthropic',
  model: 'claude-x',
  params: [
    { key: 'contextWindow', label: 'context window', kind: 'int', default: '200000' },
    { key: 'maxTokens', label: 'max tokens', kind: 'int', default: '64000' },
  ],
}

const addForm = (over: Partial<Parameters<typeof ModelParamsForm>[0]> = {}) => (
  <ModelParamsForm
    view={cloneSource}
    busy={false}
    error=""
    onSave={() => {}}
    onReset={() => {}}
    onCancel={() => {}}
    adding
    providers={['anthropic', 'empty-shop']}
    onAdd={() => {}}
    {...over}
  />
)

const box = (label: string) =>
  screen.getByText(label).parentElement!.querySelector('input,select') as HTMLInputElement

afterEach(cleanup)

describe('the add form', () => {
  // 🪤 The seed is `default`, never `value`. A clone source pins nothing, so
  // seeding from `value` hands back empty boxes and creates a model with no
  // context window: no gauge, and auto-condensing never fires.
  it('seeds every box from the effective value, not the models.json pin', () => {
    render(addForm())
    expect(box('context window').value).toBe('200000')
    expect(box('max tokens').value).toBe('64000')
  })

  // The same descriptor in EDIT mode must keep the opposite rule: a box holding
  // a default looks like an override and would be saved as one.
  it('leaves the boxes empty when editing the same model', () => {
    render(addForm({ adding: false }))
    expect(box('context window').value).toBe('')
  })

  it('offers every reachable provider, including one with no models yet', () => {
    render(addForm())
    const options = Array.from(box('provider').querySelectorAll('option')).map((o) => o.textContent)
    expect(options).toContain('empty-shop')
    expect(box('provider').value).toBe('anthropic')
  })

  it('refuses an empty id and does not call the daemon', () => {
    const onAdd = vi.fn()
    render(addForm({ onAdd }))
    fireEvent.click(screen.getByText('Add model'))
    expect(onAdd).not.toHaveBeenCalled()
    expect(screen.getByText(/model id is required/i)).toBeTruthy()
  })

  it('refuses a cleared context window, naming what it costs', () => {
    const onAdd = vi.fn()
    render(addForm({ onAdd }))
    fireEvent.input(box('id'), { target: { value: 'invented-local' } })
    fireEvent.input(box('context window'), { target: { value: '' } })
    fireEvent.click(screen.getByText('Add model'))
    expect(onAdd).not.toHaveBeenCalled()
    expect(screen.getByText(/never auto-condenses/i)).toBeTruthy()
  })

  it('files the model under the chosen provider, not the clone source', () => {
    const onAdd = vi.fn()
    render(addForm({ onAdd }))
    fireEvent.input(box('id'), { target: { value: 'invented-local' } })
    fireEvent.change(box('provider'), { target: { value: 'empty-shop' } })
    fireEvent.click(screen.getByText('Add model'))
    expect(onAdd).toHaveBeenCalledTimes(1)
    const [prov, id, values] = onAdd.mock.calls[0]
    expect(prov).toBe('empty-shop')
    expect(id).toBe('invented-local')
    expect(values.contextWindow).toBe('200000')
  })

  // Nothing exists yet, so there is nothing to reset and nothing to delete.
  // Offering either would promise an undo for a change nobody has made.
  it('offers no reset or delete control', () => {
    render(addForm({ view: { ...cloneSource, has_override: true } }))
    expect(screen.queryByText(/Reset to defaults|Delete this model/)).toBeNull()
  })
})

describe('the picker row', () => {
  const models: ModelInfo[] = [{ id: 'claude-x', provider: 'anthropic', source: 'catalog' }]
  const picker = (over: Partial<Parameters<typeof ModelPicker>[0]> = {}) => (
    <ModelPicker
      groups={[['anthropic', models]]}
      favorites={[]}
      current=""
      onSwitch={() => {}}
      onToggleFavorite={() => {}}
      onToggleHidden={() => {}}
      onSetDefault={() => {}}
      onEdit={() => {}}
      onClose={() => {}}
      {...over}
    />
  )

  // Clone-from, so the entry point hangs off a row: the row IS the source.
  it('clones from the row it sits on', () => {
    const onAdd = vi.fn()
    render(picker({ onAdd }))
    fireEvent.click(screen.getByTitle(/Add a model like this one/))
    expect(onAdd).toHaveBeenCalledWith('anthropic', 'claude-x')
  })

  // A daemon that does not serve models.add offers nothing, rather than a
  // control whose save can only fail.
  it('shows no add control when the host serves none', () => {
    render(picker())
    expect(screen.queryByTitle(/Add a model like this one/)).toBeNull()
  })

  // Switching is what a row click means. The add button must not also do it.
  it('does not switch the session when adding', () => {
    const onSwitch = vi.fn()
    render(picker({ onAdd: () => {}, onSwitch }))
    fireEvent.click(screen.getByTitle(/Add a model like this one/))
    expect(onSwitch).not.toHaveBeenCalled()
  })
})
