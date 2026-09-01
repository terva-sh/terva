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

  // The streaming branch carries the assistant bubble's padding, background,
  // border and caret from styles.css, so a row with no speech in it is the
  // empty block reported after thinking and before a tool call. applyEvent no
  // longer opens such a row; this is the rendering defense behind that.
  it.each([
    ['an empty streaming row', ''],
    ['a spaces-only streaming row', '   '],
    ['a newline-only streaming row', '\n\n'],
  ])('renders no bubble for %s', (_label, text) => {
    const { container } = show({ kind: 'assistant', id: 'a5', text, streaming: true })
    expect(container.querySelector('.msg.assistant.streaming')).toBeNull()
    expect(container.querySelector('.msg.assistant')).toBeNull()
  })

  it('still renders a streaming row whose text is only just arriving', () => {
    const { container } = show({ kind: 'assistant', id: 'a6', text: 'H', streaming: true })
    expect(container.querySelector('.msg.assistant.streaming')?.textContent).toBe('H')
  })

  // Whitespace AROUND real text is content, not chrome: the guard keys on
  // whether anything survives trimming, never on what is rendered.
  it('keeps the surrounding whitespace of a streaming row that has text', () => {
    const { container } = show({ kind: 'assistant', id: 'a7', text: '  hi  ', streaming: true })
    expect(container.querySelector('.msg.assistant.streaming')?.textContent).toBe('  hi  ')
  })

  // A turn that thinks and then calls a tool carries thinking, a tool_call and
  // no text -- the shape Anthropic emits most. The store keeps that item so the
  // thinking survives, and rendering its empty body put a blank bubble under
  // every such block.
  it('shows the thinking but no empty bubble for a text-less assistant message', () => {
    const { container } = show({
      kind: 'assistant',
      id: 'a3',
      text: '',
      streaming: false,
      reasoning: 'weighing two indexes',
    })
    expect(container.querySelector('.reasoning')).toBeTruthy()
    expect(container.querySelector('.msg.assistant')).toBeNull()
  })

  it('still renders the bubble when a message has only images', () => {
    const { container } = show({
      kind: 'assistant',
      id: 'a4',
      text: '',
      streaming: false,
      images: [{ mime: 'image/png', data: 'cG5n' }],
    })
    expect(container.querySelector('.msg.assistant')).toBeTruthy()
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

// The arrival stamp. It answers "when did this land"; the gap markers in
// itemSequence answer "how long was the silence".
describe('MessageContent timestamps', () => {
  const AT = '2026-08-01T09:04:00Z'
  const clock = new Date(AT).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })

  it('stamps a user message and an assistant reply on their own side', () => {
    const u = show({ kind: 'user', id: 'u1', text: 'hi', time: AT })
    expect(u.container.querySelector('.user-wrap .msg-time')?.textContent).toBe(clock)

    const a = show({ kind: 'assistant', id: 'a1', text: 'hey', streaming: false, time: AT })
    expect(a.container.querySelector('.assistant-wrap .msg-time')?.textContent).toBe(clock)
  })

  // The clock alone cannot settle a session resumed days later, so the full
  // localized instant rides along behind it.
  it('keeps the full instant in a tooltip', () => {
    const { container } = show({ kind: 'user', id: 'u1', text: 'hi', time: AT })
    const title = container.querySelector('.msg-time')?.getAttribute('title') ?? ''
    expect(title).toContain('2026')
    expect(title.length).toBeGreaterThan(clock.length)
  })

  // A reply still streaming has not arrived, and an older daemon sends no time
  // at all. Both must leave the row exactly as it was, not show a blank stamp.
  it('shows no stamp while streaming or when the wire carried no time', () => {
    const streaming = show({ kind: 'assistant', id: 'a1', text: 'work', streaming: true, time: AT })
    expect(streaming.container.querySelector('.msg-time')).toBeNull()

    const untimed = show({ kind: 'user', id: 'u1', text: 'hi' })
    expect(untimed.container.querySelector('.msg-time')).toBeNull()
  })
})
