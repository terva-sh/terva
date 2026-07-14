// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelParamsView } from '../../platform/ctrlproto/types'
import { ModelParamsForm } from './ModelParamsForm'

afterEach(cleanup)

// What the daemon actually sends: a label, a kind, the default this model would
// take, and the override currently pinned (here, none).
const view: ModelParamsView = {
  provider: 'anthropic',
  model: 'claude-opus-4-8',
  has_override: false,
  params: [
    { key: 'contextWindow', label: 'context window', kind: 'int', default: '200000' },
    { key: 'desiredContextWindow', label: 'desired context window', kind: 'int', default: '0', help: 'drives auto-condensing' },
    { key: 'maxTokens', label: 'max tokens', kind: 'int', default: '64000' },
    { key: 'temperature', label: 'temperature', kind: 'float', default: '1', min: 0, max: 2 },
  ],
}

const withOverride: ModelParamsView = {
  ...view,
  has_override: true,
  params: view.params.map((p) => (p.key === 'maxTokens' ? { ...p, value: '8192' } : p)),
}

describe('ModelParamsForm', () => {
  // The defect this guards: a box pre-filled with the DEFAULT looks like an
  // override, and saving would pin a value the operator never chose — which then
  // stops tracking the catalog when terva learns the model's real window.
  it('shows a default as a placeholder, never as a value', () => {
    render(<ModelParamsForm view={view} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)

    const ctx = screen.getByPlaceholderText('200000') as HTMLInputElement
    expect(ctx.value).toBe('')

    // An override, by contrast, IS the value — it is what the operator chose.
    cleanup()
    render(<ModelParamsForm view={withOverride} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect((screen.getByPlaceholderText('64000') as HTMLInputElement).value).toBe('8192')
  })

  // Every field the descriptor listed goes back, including the ones left empty:
  // an empty value CLEARS an override, and omitting an untouched field would make
  // "cleared" and "untouched" indistinguishable to the daemon.
  it('submits every described field, so a blank can clear an override', () => {
    const onSave = vi.fn()
    render(<ModelParamsForm view={withOverride} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    fireEvent.input(screen.getByPlaceholderText('64000'), { target: { value: '' } })
    fireEvent.click(screen.getByText('Save'))

    expect(onSave).toHaveBeenCalledWith({
      contextWindow: '',
      desiredContextWindow: '',
      maxTokens: '',
      temperature: '',
    })
  })

  // Offering "reset" against a model with no entry would promise an undo for a
  // change nobody made.
  it('offers a reset only when there is something to reset', () => {
    render(<ModelParamsForm view={view} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect(screen.queryByText('Reset to defaults')).toBeNull()

    cleanup()
    render(<ModelParamsForm view={withOverride} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect(screen.getByText('Reset to defaults')).toBeTruthy()
  })

  // Dropping every override is not a click-once act.
  it('asks before dropping every override', () => {
    const onReset = vi.fn()
    render(<ModelParamsForm view={withOverride} busy={false} error="" onSave={() => {}} onReset={onReset} onCancel={() => {}} />)

    fireEvent.click(screen.getByText('Reset to defaults'))
    expect(onReset).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('Reset'))
    expect(onReset).toHaveBeenCalledOnce()
  })

  // The daemon's refusal names the setting that was wrong. Showing it is the whole
  // point of keeping the form open on failure.
  it('shows the refusal the daemon sent back', () => {
    render(
      <ModelParamsForm
        view={view}
        busy={false}
        error="context window: must be a whole number"
        onSave={() => {}}
        onReset={() => {}}
        onCancel={() => {}}
      />,
    )
    expect(screen.getByText(/must be a whole number/)).toBeTruthy()
  })
})
