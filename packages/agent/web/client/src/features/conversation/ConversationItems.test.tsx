// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { Item } from '../../platform/conversation/store'
import { ConversationItems } from './ConversationItems'

const items: Item[] = [
  { kind: 'user', id: 'u1', text: 'run checks' },
  { kind: 'tool', id: 't1', name: 'bash', args: { command: 'test' }, result: 'ok' },
  { kind: 'tool', id: 't2', name: 'read', args: { path: 'result.txt' }, result: 'done' },
  { kind: 'assistant', id: 'a1', text: 'finished', streaming: false },
]

afterEach(cleanup)

describe('ConversationItems', () => {
  it('composes grouped tool runs between ordinary message content', () => {
    const { container } = render(<ConversationItems items={items} toolView="grouped" />)
    expect(screen.getByText('run checks')).toBeTruthy()
    expect(screen.getByText('finished')).toBeTruthy()
    expect(screen.getByText('2 tool calls')).toBeTruthy()
    expect(container.querySelector('.tool-group-body')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /2 tool calls/ }))
    expect(container.querySelectorAll('.tool-group-body .tool')).toHaveLength(2)
    expect(screen.getByText('bash')).toBeTruthy()
    expect(screen.getByText('read')).toBeTruthy()
  })
})
