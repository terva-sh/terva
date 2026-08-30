// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo } from '../../platform/ctrlproto/types'
import { ModelPicker } from './ModelPicker'

// The web half of model visibility. Hidden models arrive over the wire FLAGGED
// rather than filtered, so the picker owns the decision to show them — these
// pin that it gets it right in both directions: out of the way by default, and
// always reachable again.

const models: ModelInfo[] = [
  { id: 'anthropic/claude-sonnet-4.5', provider: 'openrouter' },
  { id: 'deepseek/deepseek-r1', provider: 'openrouter', hidden: true, hidden_by: 'openrouter/*' },
]
const groups: [string, ModelInfo[]][] = [['openrouter', models]]

const picker = (over: Partial<Parameters<typeof ModelPicker>[0]> = {}) => (
  <ModelPicker
    groups={groups}
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

afterEach(cleanup)

describe('ModelPicker hidden models', () => {
  it('keeps hidden models out of the list until asked', () => {
    render(picker())
    expect(screen.queryByText('anthropic/claude-sonnet-4.5')).not.toBeNull()
    expect(screen.queryByText('deepseek/deepseek-r1')).toBeNull()
  })

  it('offers a switch that counts what it is holding back', () => {
    render(picker())
    // The count is the affordance: without it the switch is an invitation to
    // look at nothing, and a user with no hidden models sees no switch at all.
    const toggle = screen.getByText('Show hidden (1)')
    fireEvent.click(toggle)
    expect(screen.queryByText('deepseek/deepseek-r1')).not.toBeNull()
  })

  it('hides the switch entirely when nothing is hidden', () => {
    render(picker({ groups: [['openrouter', [models[0]]]] }))
    expect(screen.queryByText(/Show hidden/)).toBeNull()
  })

  it('dims a revealed row so it reads as struck off rather than on offer', () => {
    render(picker())
    fireEvent.click(screen.getByText('Show hidden (1)'))
    const row = screen.getByText('deepseek/deepseek-r1').closest('.pick-row')
    expect(row?.classList.contains('is-hidden')).toBe(true)
  })

  it('toggles hidden without switching the session to that model', () => {
    const onToggleHidden = vi.fn()
    const onSwitch = vi.fn()
    render(picker({ onToggleHidden, onSwitch }))

    fireEvent.click(screen.getByTitle('Hide this model from the pickers'))
    expect(onToggleHidden).toHaveBeenCalledWith('openrouter', 'anthropic/claude-sonnet-4.5', true)
    // Clicking a row-level control must not be read as picking the model:
    // tidying the menu and changing model are different intentions.
    expect(onSwitch).not.toHaveBeenCalled()
  })

  it('names the rule when a pattern rather than the row itself did the hiding', () => {
    const onToggleHidden = vi.fn()
    render(picker({ onToggleHidden }))
    fireEvent.click(screen.getByText('Show hidden (1)'))

    // A pattern-hidden model must say so: restoring it writes an exception and
    // leaves the operator's rule governing everything else it covers.
    const restore = screen.getByTitle('Hidden by the rule “openrouter/*” — show this one anyway')
    fireEvent.click(restore)
    expect(onToggleHidden).toHaveBeenCalledWith('openrouter', 'deepseek/deepseek-r1', false)
  })

  it('says how many are hidden when the search finds nothing visible', () => {
    render(picker())
    fireEvent.input(screen.getByPlaceholderText('Search models…'), {
      target: { value: 'deepseek' },
    })
    // The one moment the user most needs telling that hidden models exist:
    // reporting a bare "no models match" would be actively misleading here.
    expect(screen.queryByText(/1 hidden/)).not.toBeNull()
  })
})
