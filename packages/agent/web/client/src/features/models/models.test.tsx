// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo, ModelTierRung } from '../../platform/ctrlproto/types'
import { ModelPicker } from './ModelPicker'

const models: ModelInfo[] = [
  { id: 'model-a', provider: 'alpha', context_window: 128_000, favorite: true },
  { id: 'model-b', provider: 'beta' },
]
const groups: [string, ModelInfo[]][] = [
  ['alpha', [models[0]]],
  ['beta', [models[1]]],
]

afterEach(cleanup)

describe('ModelPicker', () => {
  it('filters by model or provider and shows an empty result', () => {
    render(<ModelPicker groups={groups} favorites={[models[0]]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onClose={() => {}} />)
    const search = screen.getByPlaceholderText('Search models…')
    expect(document.activeElement).toBe(search)
    fireEvent.input(search, { target: { value: 'beta' } })
    expect(screen.queryByText('model-a')).toBeNull()
    expect(screen.getByText('model-b')).toBeTruthy()
    fireEvent.input(search, { target: { value: 'missing' } })
    expect(screen.getByText('no models match “missing”')).toBeTruthy()
  })

  it('marks the current model and routes selection with its provider', () => {
    const onSwitch = vi.fn()
    render(<ModelPicker groups={groups} favorites={[]} current="model-b" onSwitch={onSwitch} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onClose={() => {}} />)
    const current = screen.getByText('model-b').closest('.pick-row')
    expect(current?.classList.contains('current')).toBe(true)
    fireEvent.click(current!)
    expect(onSwitch).toHaveBeenCalledWith('model-b', 'beta')
  })

  it('toggles favorites without selecting the model', () => {
    const onSwitch = vi.fn()
    const onToggleFavorite = vi.fn()
    render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={onSwitch} onToggleFavorite={onToggleFavorite} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onClose={() => {}} />)
    fireEvent.click(screen.getByTitle('Favorite'))
    expect(onToggleFavorite).toHaveBeenCalledWith('beta', 'model-b', true)
    expect(onSwitch).not.toHaveBeenCalled()
  })

  // Adopting a default is a persistent, cross-session write, so — like the TUI's
  // ctrl+d → [p]/[g] — it takes a scope choice, never a single tap. And it must
  // not switch this session: trying a model and adopting it are separate acts.
  it('asks for a scope before setting a default, and never switches the session', () => {
    const onSwitch = vi.fn()
    const onSetDefault = vi.fn()
    render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={onSwitch} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={onSetDefault} onEdit={() => {}} onClose={() => {}} />)

    fireEvent.click(screen.getAllByTitle('Set as default for new sessions')[1])
    expect(onSetDefault).not.toHaveBeenCalled() // armed, not fired
    expect(screen.getByText('set “model-b” as the default for new sessions:')).toBeTruthy()

    fireEvent.click(screen.getByText('Everywhere'))
    expect(onSetDefault).toHaveBeenCalledWith('beta', 'model-b', 'global')
    expect(onSwitch).not.toHaveBeenCalled()
    expect(screen.queryByText('set “model-b” as the default for new sessions:')).toBeNull()
  })

  it('offers project scope, and lets the confirm be cancelled', () => {
    const onSetDefault = vi.fn()
    render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={onSetDefault} onEdit={() => {}} onClose={() => {}} />)

    fireEvent.click(screen.getAllByTitle('Set as default for new sessions')[0])
    fireEvent.click(screen.getByText('This project'))
    expect(onSetDefault).toHaveBeenCalledWith('alpha', 'model-a', 'project')

    fireEvent.click(screen.getAllByTitle('Set as default for new sessions')[0])
    fireEvent.click(screen.getByText('Cancel'))
    expect(onSetDefault).toHaveBeenCalledTimes(1)
    expect(screen.queryByText('Everywhere')).toBeNull()
  })

  // The ◉ marks the model NEW sessions start on. That is not the same as the
  // model THIS session is on (.current) — the picker must not conflate them.
  it('marks the default model distinctly from the current one', () => {
    const withDefault: [string, ModelInfo[]][] = [
      ['alpha', [{ id: 'model-a', provider: 'alpha', default: true, default_scope: 'global' }]],
      ['beta', [{ id: 'model-b', provider: 'beta' }]],
    ]
    render(<ModelPicker groups={withDefault} favorites={[]} current="model-b" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onClose={() => {}} />)

    const defaultRow = screen.getByText('model-a').closest('.pick-row')
    const currentRow = screen.getByText('model-b').closest('.pick-row')
    expect(defaultRow?.querySelector('.pick-default.on')).toBeTruthy()
    expect(defaultRow?.classList.contains('current')).toBe(false)
    expect(currentRow?.querySelector('.pick-default.on')).toBeNull()
    expect(currentRow?.classList.contains('current')).toBe(true)
    expect(screen.getByTitle('Default for new sessions (everywhere)')).toBeTruthy()
  })

  // The state of every provider's ladder, without opening any of them. Three
  // answers worth telling apart: you set it, a built-in rule set it, nothing
  // did — and the third is the one that silently spends host-model money.
  it('shows each provider ladder state on its group header', () => {
    const summaries: Record<string, ModelTierRung[]> = {
      alpha: [
        { rung: 'weak', model: 'x', pinned: 'x', source: 'override' },
        { rung: 'medium', model: 'y', source: 'built-in' },
        { rung: 'strong' },
      ],
    }
    const { container } = render(
      <ModelPicker groups={groups} favorites={[]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onTiers={() => {}} tierSummaries={summaries} onClose={() => {}} />,
    )
    const dots = [...container.querySelectorAll('.pick-tier-dot')]
    expect(dots.map((d) => d.getAttribute('data-source'))).toEqual([
      'override',
      'built-in',
      'empty',
    ])
    // The tooltip is the legend, naming every rung rather than only the
    // vocabulary — a thing the TUI has to spend a whole line on.
    expect(container.querySelector('.pick-tiers')?.getAttribute('title')).toBe(
      'weak: set · medium: built-in · strong: empty',
    )
  })

  // beta has no entry: its ladder could not be read. Drawing empty dots there
  // would report a broken ladder where the truth is that nothing was fetched.
  it('draws no dots for a provider whose ladder is unknown', () => {
    const { container } = render(
      <ModelPicker groups={groups} favorites={[]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onTiers={() => {}} tierSummaries={{ alpha: [{ rung: 'weak', model: 'x', source: 'built-in' }] }} onClose={() => {}} />,
    )
    expect(container.querySelectorAll('.pick-tiers')).toHaveLength(1)
  })

  it('closes from Escape and the backdrop but not picker content', () => {
    const onClose = vi.fn()
    const { container } = render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onToggleHidden={() => {}} onSetDefault={() => {}} onEdit={() => {}} onClose={onClose} />)
    fireEvent.keyDown(screen.getByPlaceholderText('Search models…'), { key: 'Escape' })
    fireEvent.click(container.querySelector('.picker')!)
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(container.querySelector('.modal-scrim')!)
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})
