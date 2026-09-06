// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo, ModelParamsView } from '../../platform/ctrlproto/types'
import { ModelPicker } from './ModelPicker'
import { ModelParamsForm } from './ModelParamsForm'

// The web half of telling a custom model from an edited one. Both arrive with
// source 'user', so `custom` is the only thing that separates them, and the
// separation matters because the ⚙ form's reset button does two different
// things to them.

const models: ModelInfo[] = [
  // Nothing underneath it: the entry IS the model.
  { id: 'gpt-6-astra', provider: 'openai', source: 'user', custom: true },
  // A tweak over a catalog row that survives the reset.
  { id: 'gpt-5.5', provider: 'openai', source: 'user' },
  // Untouched by models.json.
  { id: 'gpt-5', provider: 'openai', source: 'catalog' },
]

const picker = (over: Partial<Parameters<typeof ModelPicker>[0]> = {}) => (
  <ModelPicker
    groups={[['openai', models]]}
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

const form = (view: ModelParamsView, onReset = () => {}) => (
  <ModelParamsForm view={view} busy={false} error="" onSave={() => {}} onReset={onReset} onCancel={() => {}} />
)

const paramsView: ModelParamsView = {
  provider: 'openai',
  model: 'gpt-6-astra',
  has_override: true,
  params: [{ key: 'contextWindow', label: 'context window', kind: 'int', default: '0' }],
}

afterEach(cleanup)

describe('ModelPicker models.json tags', () => {
  it('tags a model that exists only in models.json as custom', () => {
    render(picker())
    const row = screen.getByText('gpt-6-astra').closest('.pick-row')
    expect(row?.querySelector('.pick-tag')?.textContent).toBe('custom')
  })

  it('tags a tweaked catalog row as edited, not custom', () => {
    render(picker())
    const row = screen.getByText('gpt-5.5').closest('.pick-row')
    // "custom" here would promise that resetting removes the model, when
    // resetting actually restores the catalog values.
    expect(row?.querySelector('.pick-tag')?.textContent).toBe('edited')
  })

  it('leaves an untouched model unmarked', () => {
    render(picker())
    const row = screen.getByText('gpt-5').closest('.pick-row')
    // A tag on every row is a tag that says nothing.
    expect(row?.querySelector('.pick-tag')).toBeNull()
  })
})

describe('ModelParamsForm reset wording', () => {
  it('offers to delete a custom model rather than reset it', () => {
    render(form({ ...paramsView, custom: true }))
    // "Reset to defaults" over a model with no defaults offers something that
    // cannot happen.
    expect(screen.queryByText('Delete this model')).not.toBeNull()
    expect(screen.queryByText('Reset to defaults')).toBeNull()
  })

  it('says what the delete costs before doing it', () => {
    const onReset = vi.fn()
    render(form({ ...paramsView, custom: true }, onReset))
    fireEvent.click(screen.getByText('Delete this model'))

    expect(screen.queryByText(/leaves the picker/)).not.toBeNull()
    // Confirmation is still required, and the action itself is unchanged.
    expect(onReset).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('Delete'))
    expect(onReset).toHaveBeenCalled()
  })

  it('keeps the reset wording for a tweaked catalog row', () => {
    render(form({ ...paramsView, custom: false }))
    // The half a careless branch breaks while the custom case looks right.
    expect(screen.queryByText('Reset to defaults')).not.toBeNull()
    expect(screen.queryByText('Delete this model')).toBeNull()

    fireEvent.click(screen.getByText('Reset to defaults'))
    expect(screen.queryByText(/Drop every override/)).not.toBeNull()
  })
})
