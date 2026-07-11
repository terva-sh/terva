// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/preact'
import type { Item } from '../../platform/conversation/store'
import { MessageContent, type ToolView } from './MessageContent'

const show = (item: Item, toolView: ToolView = 'full') => render(<MessageContent item={item} toolView={toolView} />)

afterEach(cleanup)

describe('MessageContent', () => {
  it('renders user text, safe images, and copy affordance', () => {
    const { container } = show({
      kind: 'user',
      id: 'u1',
      text: 'hello',
      images: [{ mime: 'image/png', data: 'cG5n' }],
    })
    expect(container.querySelector('.user-wrap .msg.user')?.textContent).toContain('hello')
    expect(screen.getByAltText('attached image')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy()
  })

  it('keeps streaming assistant content raw and renders finalized markdown', () => {
    const streaming = show({ kind: 'assistant', id: 'a1', text: '**working**', streaming: true })
    expect(streaming.container.querySelector('.assistant.streaming')?.textContent).toBe('**working**')
    expect(streaming.container.querySelector('strong')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull()
    cleanup()

    const complete = show({ kind: 'assistant', id: 'a2', text: '**done**', streaming: false })
    expect(complete.container.querySelector('.assistant.md strong')?.textContent).toBe('done')
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy()
  })

  it('renders error, system, and attributed error notices with existing classes', () => {
    const error = show({ kind: 'error', id: 'e1', text: 'failed turn' })
    expect(error.container.querySelector('.msg.err')?.textContent).toBe('failed turn')
    cleanup()

    const system = show({ kind: 'system', id: 's1', text: 'continue' })
    expect(system.container.querySelector('.sys-note .sys-tag')?.textContent).toBe('auto')
    cleanup()

    const notice = show({ kind: 'notice', id: 'n1', level: 'error', ext: 'calendar', text: 'unavailable' })
    expect(notice.container.querySelector('.sys-note.notice.err')?.textContent).toContain('calendar')
    expect(notice.container.querySelector('.sys-note.notice.err')?.textContent).toContain('unavailable')
  })

  it('hides or minimizes tool calls according to the display mode', () => {
    const item: Item = { kind: 'tool', id: 't1', name: 'bash', args: {}, error: true }
    const hidden = show(item, 'hidden')
    expect(hidden.container.firstChild).toBeNull()
    cleanup()

    const minimal = show(item, 'minimal')
    expect(minimal.container.querySelector('.tool-min')?.textContent).toContain('bash · failed')
    expect(minimal.container.querySelector('.tool-min')?.getAttribute('aria-hidden')).toBe('true')
  })

  it('renders full tool arguments, truncated results, errors, and images', () => {
    const result = 'x'.repeat(2001)
    const { container } = show({
      kind: 'tool',
      id: 't1',
      name: 'read',
      args: { path: 'file.txt' },
      result,
      error: true,
      images: [{ mime: 'image/jpeg', data: 'anBn' }],
    })
    expect(container.querySelector('.tool-name')?.textContent).toBe('read')
    expect(container.querySelector('.tool-args')?.textContent).toBe('{"path":"file.txt"}')
    expect(container.querySelector('.tool-result.err')?.textContent).toBe('x'.repeat(2000) + '…')
    expect(screen.getByAltText('attached image')).toBeTruthy()
  })
})
