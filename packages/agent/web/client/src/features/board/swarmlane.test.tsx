// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { TaskInfo } from '../../platform/ctrlproto/types'
import { SwarmLane } from './SwarmLane'

const task = (overrides: Partial<TaskInfo> = {}): TaskInfo => ({
  id: 'a1', task: 'review the diff', status: 'running', model: 'model-a', ...overrides,
})

const noop = () => {}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('SwarmLane', () => {
  it('renders nothing when there are no agents and no way to spawn one', () => {
    const { container } = render(<SwarmLane tasks={[]} onAction={noop} />)
    expect(container.querySelector('.board-lane')).toBeNull()
  })

  it('keeps the lane (with a spawn affordance) when empty but spawning is possible', () => {
    const { container } = render(<SwarmLane tasks={[]} onAction={noop} onSpawn={noop} backends={[]} />)
    expect(container.querySelector('.board-lane')).not.toBeNull()
    expect(screen.getByRole('button', { name: '+ Spawn' })).toBeTruthy()
    expect(screen.getByText('No swarm agents running.')).toBeTruthy()
  })

  it('spawns a native agent: task text, no backend', () => {
    const onSpawn = vi.fn()
    render(<SwarmLane tasks={[]} onAction={noop} onSpawn={onSpawn} backends={['claude']} workersEnabled />)
    fireEvent.click(screen.getByRole('button', { name: '+ Spawn' }))
    fireEvent.input(screen.getByPlaceholderText(/Task for the new agent/), { target: { value: 'run tests' } })
    fireEvent.click(screen.getByRole('button', { name: 'Spawn' }))
    expect(onSpawn).toHaveBeenCalledWith('run tests', '')
  })

  it('spawns against a chosen backend when workers are enabled', () => {
    const onSpawn = vi.fn()
    render(<SwarmLane tasks={[]} onAction={noop} onSpawn={onSpawn} backends={['claude']} workersEnabled />)
    fireEvent.click(screen.getByRole('button', { name: '+ Spawn' }))
    fireEvent.input(screen.getByPlaceholderText(/Task for the new agent/), { target: { value: 'audit' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'claude' } })
    fireEvent.click(screen.getByRole('button', { name: 'Spawn' }))
    expect(onSpawn).toHaveBeenCalledWith('audit', 'claude')
  })

  it('greys foreign backends with a hint when external workers are off', () => {
    render(<SwarmLane tasks={[]} onAction={noop} onSpawn={noop} backends={['claude', 'terva']} workersEnabled={false} />)
    fireEvent.click(screen.getByRole('button', { name: '+ Spawn' }))
    expect((screen.getByRole('option', { name: 'claude' }) as HTMLOptionElement).disabled).toBe(true)
    expect((screen.getByRole('option', { name: 'native' }) as HTMLOptionElement).disabled).toBe(false)
    expect(screen.getByText(/External workers are off/)).toBeTruthy()
  })

  it('renders a tile per agent with its status and task', () => {
    render(<SwarmLane tasks={[task({ id: 'a1', task: 'First' }), task({ id: 'a2', task: 'Second' })]} onAction={noop} />)
    expect(screen.getByText('First')).toBeTruthy()
    expect(screen.getByText('Second')).toBeTruthy()
    expect(screen.getAllByText('running')).toHaveLength(2)
  })

  it('lights up the worker fields (backend, cost) only when present', () => {
    const { container, rerender } = render(<SwarmLane tasks={[task({ backend: 'claude', cost_usd: 0.0042 })]} onAction={noop} />)
    expect(screen.getByText('claude')).toBeTruthy()
    expect(screen.getByText(/\$0\.0042/)).toBeTruthy()
    // A native child (no backend, no cost) shows neither.
    rerender(<SwarmLane tasks={[task({})]} onAction={noop} />)
    expect(container.querySelector('.swarm-backend')).toBeNull()
    expect(screen.queryByText(/\$/)).toBeNull()
  })

  it('badges a worker parked on approval and opens its session on click', () => {
    const onOpenSession = vi.fn()
    render(
      <SwarmLane
        tasks={[task({ id: 'wk7', status: 'running' })]}
        onAction={noop}
        waiting={{ wk7: 's1' }}
        onOpenSession={onOpenSession}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'needs approval' }))
    expect(onOpenSession).toHaveBeenCalledWith('s1')
  })

  it('shows no approval badge for a worker that is not waiting', () => {
    render(<SwarmLane tasks={[task({ id: 'wk7' })]} onAction={noop} waiting={{ wk9: 's1' }} onOpenSession={noop} />)
    expect(screen.queryByText('needs approval')).toBeNull()
  })

  it('routes stop for a running agent and remove for a finished one', () => {
    const onAction = vi.fn()
    const { rerender } = render(<SwarmLane tasks={[task({ id: 'a1', status: 'running' })]} onAction={onAction} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(onAction).toHaveBeenCalledWith('tasks', 'stop', { id: 'a1' })

    rerender(<SwarmLane tasks={[task({ id: 'a1', status: 'done' })]} onAction={onAction} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(onAction).toHaveBeenCalledWith('tasks', 'remove', { id: 'a1' })
  })
})
