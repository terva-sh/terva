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

// An enum param is a closed set, so it gets a picker rather than a text box you
// have to type into blind. `defaultReasoning` is the one that made this matter:
// it shipped as free text, labelled with jargon and hinting "inherit ()", which
// is why the per-model thinking default read as a missing feature rather than
// an unusable one.
describe('ModelParamsForm enum params', () => {
  const enumView: ModelParamsView = {
    provider: 'openai-codex',
    model: 'gpt-5.6-luna',
    has_override: false,
    params: [
      {
        key: 'defaultReasoning',
        label: 'default thinking',
        kind: 'enum',
        default: 'high',
        options: ['off', 'low', 'medium', 'high', 'maximum', 'max'],
      },
    ],
  }

  it('renders a picker over the options the daemon sent, not a text box', () => {
    render(<ModelParamsForm view={enumView} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)

    const select = screen.getByRole('combobox') as HTMLSelectElement
    // The options are the MODEL's, not a fixed ladder: gpt-5.6 sends "minimum"
    // and "low" as one effort, so offering both would be two names for one
    // choice. Whatever the daemon listed is what appears, and nothing else.
    expect([...select.options].map((o) => o.value)).toEqual(['', 'off', 'low', 'medium', 'high', 'maximum', 'max'])
    expect(select.value).toBe('')
  })

  // The empty entry has to NAME what would apply. An "inherit" naming nothing is
  // exactly what the terminal shipped, and it left the operator unable to tell
  // whether a global level was quietly deciding the turn.
  it('names the inherited value on the empty entry', () => {
    render(<ModelParamsForm view={enumView} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect((screen.getByRole('combobox') as HTMLSelectElement).options[0].text).toContain('high')
  })

  it('sends the picked level back, and an empty pick clears the override', () => {
    const onSave = vi.fn()
    render(<ModelParamsForm view={enumView} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    const select = screen.getByRole('combobox') as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'maximum' } })
    fireEvent.click(screen.getByText('Save'))
    expect(onSave).toHaveBeenCalledWith({ defaultReasoning: 'maximum' })

    fireEvent.change(select, { target: { value: '' } })
    fireEvent.click(screen.getByText('Save'))
    expect(onSave).toHaveBeenLastCalledWith({ defaultReasoning: '' })
  })
})

// The capability tri-states. These reached the wire late: the TUI hand-wrote
// them as form rows for months while this form, reading the same registry,
// showed nothing — so telling terva that a local model can think meant editing
// models.json by hand, on every machine.
describe('ModelParamsForm capability tri-states', () => {
  const capView: ModelParamsView = {
    provider: 'neot',
    model: 'qwen3.8-27b-abl',
    has_override: false,
    params: [{ key: 'reasoning', label: 'thinking', kind: 'tristate', default: 'off' }],
  }

  // 🪤 A checkbox has two states and this setting has three. "Off" is the
  // operator overruling terva and it keeps outranking discovery; the empty
  // value leaves the catalog deciding. Rendered as a checkbox, every model this
  // form was opened on would come back pinned.
  it('offers inherit, on and off, rather than a checkbox', () => {
    render(<ModelParamsForm view={capView} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)

    expect(screen.queryByRole('checkbox')).toBeNull()
    const select = screen.getByRole('combobox') as HTMLSelectElement
    expect([...select.options].map((o) => o.value)).toEqual(['', 'on', 'off'])
    expect(select.value).toBe('')
    expect(select.options[0].text).toContain('off') // names what inheriting means here
  })

  it('sends the picked state back, and an empty pick clears the override', () => {
    const onSave = vi.fn()
    render(<ModelParamsForm view={capView} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    const select = screen.getByRole('combobox') as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'on' } })
    fireEvent.click(screen.getByText('Save'))
    expect(onSave).toHaveBeenCalledWith({ reasoning: 'on' })

    fireEvent.change(select, { target: { value: '' } })
    fireEvent.click(screen.getByText('Save'))
    expect(onSave).toHaveBeenLastCalledWith({ reasoning: '' })
  })
})

// A list param: which reasoning_effort values a backend accepts. Declaring them
// removes a guess rather than adding a preference — undeclared, terva clamps
// its top two rungs to "high", so a server that takes "xhigh" never sees it.
describe('ModelParamsForm list params', () => {
  const scale = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
  const listView: ModelParamsView = {
    provider: 'neot',
    model: 'qwen3.8-27b-abl',
    has_override: false,
    params: [
      {
        key: 'reasoningEfforts',
        label: 'accepted efforts',
        kind: 'list',
        default: 'undeclared',
        options: scale,
        free_values: true,
      },
    ],
  }

  it('renders one checkbox per value the daemon offers', () => {
    render(<ModelParamsForm view={listView} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect(screen.getAllByRole('checkbox')).toHaveLength(scale.length)
    for (const v of scale) expect(screen.getByText(v)).toBeTruthy()
  })

  // Saved in the daemon's order, not the order they were clicked, so two
  // operators who picked the same set save the same string and the value reads
  // as a scale rather than a history of clicks.
  it('composes the set in the daemon order', () => {
    const onSave = vi.fn()
    render(<ModelParamsForm view={listView} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    const boxes = screen.getAllByRole('checkbox')
    fireEvent.click(boxes[scale.indexOf('xhigh')])
    fireEvent.click(boxes[scale.indexOf('low')])
    fireEvent.click(screen.getByText('Save'))

    expect(onSave).toHaveBeenCalledWith({ reasoningEfforts: 'low, xhigh' })
  })

  it('pre-ticks the values already pinned, and unticking clears them', () => {
    const onSave = vi.fn()
    const pinned: ModelParamsView = {
      ...listView,
      has_override: true,
      params: [{ ...listView.params[0], value: 'none, low, medium, xhigh' }],
    }
    render(<ModelParamsForm view={pinned} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    const boxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(boxes.filter((b) => b.checked)).toHaveLength(4)

    fireEvent.click(boxes[scale.indexOf('none')])
    fireEvent.click(screen.getByText('Save'))
    expect(onSave).toHaveBeenCalledWith({ reasoningEfforts: 'low, medium, xhigh' })
  })

  // 🪤 The escape hatch. clampEffortToDeclared leaves an effort it does not
  // recognize alone, because an unknown effort is the server's own word. A
  // closed picker would make that reachable only by hand-editing the file,
  // which is the failure this whole form exists to end.
  it('keeps a value that is not on terva scale', () => {
    const onSave = vi.fn()
    render(<ModelParamsForm view={listView} busy={false} error="" onSave={onSave} onReset={() => {}} onCancel={() => {}} />)

    fireEvent.click(screen.getAllByRole('checkbox')[scale.indexOf('low')])
    fireEvent.input(screen.getByPlaceholderText(/another value/), { target: { value: 'ludicrous' } })
    fireEvent.click(screen.getByText('Save'))

    expect(onSave).toHaveBeenCalledWith({ reasoningEfforts: 'low, ludicrous' })
  })

  // A closed list gets no free box: offering one would invite a value the
  // daemon is about to refuse.
  it('offers the free box only when the daemon says the set is open', () => {
    const closed: ModelParamsView = {
      ...listView,
      params: [{ ...listView.params[0], free_values: false }],
    }
    render(<ModelParamsForm view={closed} busy={false} error="" onSave={() => {}} onReset={() => {}} onCancel={() => {}} />)
    expect(screen.queryByPlaceholderText(/another value/)).toBeNull()
  })
})
