// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { AskRequest } from './AskRequest'
import { PermissionRequest } from './PermissionRequest'

afterEach(cleanup)

describe('PermissionRequest', () => {
  it('emits each existing permission decision', () => {
    const onDecide = vi.fn()
    render(<PermissionRequest request={{ call_id: 'c1', tool: 'bash', preview: 'pwd' }} onDecide={onDecide} />)

    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    fireEvent.click(screen.getByRole('button', { name: 'Allow & remember' }))
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))

    expect(onDecide.mock.calls).toEqual([
      ['c1', { allow: true }],
      ['c1', { allow: true, remember_tool: true }],
      ['c1', { allow: false, reason: 'denied by user' }],
    ])
  })

  it('preserves the preview truncation limit', () => {
    render(<PermissionRequest request={{ call_id: 'c1', tool: 'read', preview: 'x'.repeat(1600) }} onDecide={() => {}} />)
    expect(screen.getByText(/…$/).textContent).toHaveLength(1501)
  })

  it('offers the scoped grant only when the daemon derived scopes, echoing the patterns', () => {
    const onDecide = vi.fn()
    render(
      <PermissionRequest
        request={{
          call_id: 'c2',
          tool: 'bash',
          preview: 'git status',
          scopes: [{ display: 'git status', pattern: '^git status(?:\\s|$)' }],
        }}
        onDecide={onDecide}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Always allow “git status”' }))
    expect(onDecide).toHaveBeenCalledWith('c2', {
      allow: true,
      persist_tool: true,
      persist_scopes: ['^git status(?:\\s|$)'],
    })
  })

  it('renders no scoped button without scopes', () => {
    render(<PermissionRequest request={{ call_id: 'c3', tool: 'bash', preview: 'ls' }} onDecide={() => {}} />)
    expect(screen.queryByRole('button', { name: /Always allow/ })).toBeNull()
  })
})

describe('AskRequest', () => {
  it('answers with a presented option', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a1', question: 'Choose', options: ['One', 'Two'] }} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: 'Two' }))
    expect(onAnswer).toHaveBeenCalledWith('a1', 'Two')
  })

  it('trims and submits a custom answer', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a2', question: 'Explain', allow_custom: true }} onAnswer={onAnswer} />)
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: '  details  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('a2', 'details')
  })
})
