// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ModelInfo } from '../../platform/ctrlproto/types'
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
    render(<ModelPicker groups={groups} favorites={[models[0]]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onClose={() => {}} />)
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
    render(<ModelPicker groups={groups} favorites={[]} current="model-b" onSwitch={onSwitch} onToggleFavorite={() => {}} onClose={() => {}} />)
    const current = screen.getByText('model-b').closest('.pick-row')
    expect(current?.classList.contains('current')).toBe(true)
    fireEvent.click(current!)
    expect(onSwitch).toHaveBeenCalledWith('model-b', 'beta')
  })

  it('toggles favorites without selecting the model', () => {
    const onSwitch = vi.fn()
    const onToggleFavorite = vi.fn()
    render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={onSwitch} onToggleFavorite={onToggleFavorite} onClose={() => {}} />)
    fireEvent.click(screen.getByTitle('Favorite'))
    expect(onToggleFavorite).toHaveBeenCalledWith('beta', 'model-b', true)
    expect(onSwitch).not.toHaveBeenCalled()
  })

  it('closes from Escape and the backdrop but not picker content', () => {
    const onClose = vi.fn()
    const { container } = render(<ModelPicker groups={groups} favorites={[]} current="" onSwitch={() => {}} onToggleFavorite={() => {}} onClose={onClose} />)
    fireEvent.keyDown(screen.getByPlaceholderText('Search models…'), { key: 'Escape' })
    fireEvent.click(container.querySelector('.picker')!)
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(container.querySelector('.modal-scrim')!)
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})
