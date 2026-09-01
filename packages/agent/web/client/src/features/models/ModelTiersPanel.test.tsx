// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo, ModelTiersView, ReasoningRungInfo } from '../../platform/ctrlproto/types'
import { ModelTiersPanel } from './ModelTiersPanel'

afterEach(cleanup)

const models = [
  { id: 'gemini-3.1-flash-lite', provider: 'google', ladder: 'g' },
  { id: 'gemini-3.7-flash', provider: 'google', ladder: 'g' },
  { id: 'gemini-3.1-pro-preview', provider: 'google', ladder: 'g' },
] as ModelInfo[]

// Gemini lands several rungs on one value, which is what same_as records.
const ladders: Record<string, ReasoningRungInfo[]> = {
  g: [
    { level: 'off' },
    { level: 'low' },
    { level: 'medium' },
    { level: 'high', same_as: 'medium' },
    { level: 'maximum', same_as: 'medium' },
  ],
}

const view: ModelTiersView = {
  provider: 'google',
  has_override: true,
  rungs: [
    { rung: 'weak', model: 'gemini-3.1-flash-lite', label: 'Flash Lite', source: 'built-in' },
    { rung: 'medium', model: 'gemini-3.7-flash', label: 'Flash', source: 'built-in' },
    {
      rung: 'strong',
      model: 'gemini-3.1-pro-preview',
      pinned: 'gemini-3.1-pro-preview',
      reasoning: 'low',
      source: 'override',
    },
  ],
}

const noop = () => {}

describe('ModelTiersPanel', () => {
  // The whole reason this panel exists: config was EMPTY while google's medium
  // and strong rungs resolved to image-generation models. A panel that rendered
  // overrides would have shown three blank rows the entire time.
  it('shows what each rung resolves to and where it came from', () => {
    const { container } = render(<ModelTiersPanel view={view} models={models} ladders={ladders} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    // The resolved model and its source are separate elements now, but both
    // still have to be on the row: "nobody pinned this" and "this is wrong"
    // are different answers and only the source tells them apart.
    // Scoped to the resolved line: every model id is also an <option> in each
    // rung's picker, so a document-wide text query would match those instead
    // and pass whether or not the row says anything.
    const resolved = [...container.querySelectorAll('.tier-resolved')].map((n) => n.textContent)
    expect(resolved).toEqual(['Flash Lite', 'Flash', 'gemini-3.1-pro-preview'])
    const rows = [...container.querySelectorAll('.tier-row')]
    expect(rows.map((r) => r.getAttribute('data-source'))).toEqual([
      'built-in',
      'built-in',
      'override',
    ])
  })

  // The three states drive the dot, the badge and the row's left border from
  // one value, so they cannot disagree. A rung that resolves to nothing is the
  // one worth seeing: it is not "off", it silently spends host-model money.
  it('marks an unresolved rung as empty rather than built-in', () => {
    const mixed: ModelTiersView = {
      provider: 'ollama',
      rungs: [
        { rung: 'weak', model: 'a', source: 'built-in' },
        { rung: 'medium' },
        { rung: 'strong', model: 'b', pinned: 'b', source: 'override' },
      ],
    }
    const { container } = render(<ModelTiersPanel view={mixed} models={[]} ladders={{}} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    const rows = [...container.querySelectorAll('.tier-row')]
    expect(rows.map((r) => r.getAttribute('data-source'))).toEqual([
      'built-in',
      'empty',
      'override',
    ])
    // The badge says it in words too — a dot alone is not a label.
    expect(screen.getByText('empty')).toBeTruthy()
  })

  // A dash reads as "off". The truth is the sub-agent runs on the host model,
  // at the host's cost, which is worth saying in words.
  it('says a rung that resolves to nothing falls back to the host model', () => {
    const bare: ModelTiersView = { provider: 'ollama', rungs: [{ rung: 'weak' }] }
    render(<ModelTiersPanel view={bare} models={[]} ladders={{}} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    expect(screen.getByText(/falls back to the host model/)).toBeTruthy()
  })

  // The model picker carries the PIN, not the resolved id. Showing the resolved
  // model would make an untouched rung look pinned, and saving it would freeze a
  // rung that had been tracking its family rule.
  it('shows the pin in the picker and the built-in pick as the empty option', () => {
    render(<ModelTiersPanel view={view} models={models} ladders={ladders} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]
    // Two selects per rung: model, then thinking.
    expect(selects[0].value).toBe('') // weak is built-in: nothing pinned
    expect(selects[0].options[0].text).toContain('gemini-3.1-flash-lite')
    expect(selects[4].value).toBe('gemini-3.1-pro-preview') // strong is pinned
  })

  // Changing only the level must carry the pin back, or the rung's model
  // silently changes meaning.
  it('carries the pin back when only the thinking level changes', () => {
    const onSet = vi.fn()
    render(<ModelTiersPanel view={view} models={models} ladders={ladders} busy={false} error="" onSet={onSet} onReset={noop} onClose={noop} />)
    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]

    fireEvent.change(selects[5], { target: { value: 'medium' } }) // strong's thinking
    expect(onSet).toHaveBeenCalledWith('strong', 'gemini-3.1-pro-preview', 'medium')

    // And a built-in rung sends an EMPTY model, so it keeps following the rule.
    fireEvent.change(selects[1], { target: { value: 'low' } }) // weak's thinking
    expect(onSet).toHaveBeenLastCalledWith('weak', '', 'low')
  })

  // Only canonical rungs are offered: Gemini lands high/maximum on medium's
  // value, and offering all three asks for a choice between three spellings.
  it('offers only the levels this model can tell apart', () => {
    render(<ModelTiersPanel view={view} models={models} ladders={ladders} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]
    const levels = [...selects[1].options].map((o) => o.value)
    expect(levels).toEqual(['', 'off', 'low', 'medium'])
  })

  it('offers a reset only on a rung that is actually pinned', () => {
    render(<ModelTiersPanel view={view} models={models} ladders={ladders} busy={false} error="" onSet={noop} onReset={noop} onClose={noop} />)
    expect(screen.getAllByText('Reset rung')).toHaveLength(1)
  })
})
