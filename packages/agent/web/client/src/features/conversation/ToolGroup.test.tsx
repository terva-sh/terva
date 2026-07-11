// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { summarizeTools, ToolGroup, type ToolItem } from './ToolGroup'

const tool = (id: string, name: string, error = false): ToolItem => ({
  kind: 'tool',
  id,
  name,
  args: {},
  error,
})

afterEach(cleanup)

describe('summarizeTools', () => {
  it('counts repeated tools in first-seen order and truncates after four names', () => {
    const tools = [
      tool('1', 'bash'),
      tool('2', 'read'),
      tool('3', 'bash'),
      tool('4', 'edit'),
      tool('5', 'grep'),
      tool('6', 'glob'),
    ]
    expect(summarizeTools(tools)).toBe('bash ×2, read, edit, grep, …')
  })

  it('keeps exactly four distinct names without truncating', () => {
    const tools = [tool('1', 'a'), tool('2', 'b'), tool('3', 'c'), tool('4', 'd')]
    expect(summarizeTools(tools)).toBe('a, b, c, d')
  })
})

describe('ToolGroup', () => {
  it('renders a collapsed count, summary, and failure count', () => {
    render(
      <ToolGroup
        tools={[tool('1', 'bash'), tool('2', 'read', true)]}
        renderTool={(item) => <div key={item.id}>{item.name}</div>}
      />,
    )
    expect(screen.getByText('2 tool calls')).toBeTruthy()
    expect(screen.getByText('bash, read')).toBeTruthy()
    expect(screen.getByText('· 1 failed')).toBeTruthy()
    expect(document.querySelector('.tool-group-head')?.classList.contains('open')).toBe(false)
    expect(document.querySelector('.tool-group-body')).toBeNull()
  })

  it('omits the failure marker when no tool failed', () => {
    render(
      <ToolGroup
        tools={[tool('1', 'bash'), tool('2', 'read')]}
        renderTool={(item) => <div key={item.id}>{item.name}</div>}
      />,
    )
    expect(document.querySelector('.tg-failed')).toBeNull()
  })

  it('expands and collapses caller-rendered tool details', () => {
    const renderTool = vi.fn((item: ToolItem) => <div key={item.id}>detail: {item.name}</div>)
    render(<ToolGroup tools={[tool('1', 'bash')]} renderTool={renderTool} />)

    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByText('detail: bash')).toBeTruthy()
    expect(document.querySelector('.tool-group-head')?.classList.contains('open')).toBe(true)
    expect(document.querySelector('.tg-summary')).toBeNull()
    expect(renderTool).toHaveBeenCalledWith(expect.objectContaining({ id: '1', name: 'bash' }))

    fireEvent.click(screen.getByRole('button'))
    expect(screen.queryByText('detail: bash')).toBeNull()
  })
})
