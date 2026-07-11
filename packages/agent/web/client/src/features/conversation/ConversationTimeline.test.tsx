// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { ConversationTimeline } from './ConversationTimeline'
import type { Item } from '../../platform/conversation/store'

let scrollHeight = 600
let clientHeight = 300
let originalScrollHeight: PropertyDescriptor | undefined
let originalClientHeight: PropertyDescriptor | undefined
let originalExecCommand: PropertyDescriptor | undefined

const props = (overrides: Partial<Parameters<typeof ConversationTimeline>[0]> = {}): Parameters<typeof ConversationTimeline>[0] => ({
  items: [],
  busy: false,
  toolView: 'full',
  queued: [],
  onEditQueued: () => {},
  onCancelQueued: () => {},
  ...overrides,
})

const message = (id: string): Item => ({ kind: 'user', id, text: id })

beforeEach(() => {
  scrollHeight = 600
  clientHeight = 300
  originalScrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
  originalClientHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight')
  originalExecCommand = Object.getOwnPropertyDescriptor(document, 'execCommand')
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
    configurable: true,
    get() {
      return scrollHeight
    },
  })
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', {
    configurable: true,
    get() {
      return clientHeight
    },
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  if (originalScrollHeight) Object.defineProperty(HTMLElement.prototype, 'scrollHeight', originalScrollHeight)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollHeight
  if (originalClientHeight) Object.defineProperty(HTMLElement.prototype, 'clientHeight', originalClientHeight)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientHeight
  if (originalExecCommand) Object.defineProperty(document, 'execCommand', originalExecCommand)
  else delete (document as unknown as Record<string, unknown>).execCommand
})

describe('ConversationTimeline queued messages', () => {
  it('routes removal with the queued message index', () => {
    const onCancelQueued = vi.fn()
    render(<ConversationTimeline {...props({ queued: ['first', 'second'], onCancelQueued })} />)

    fireEvent.click(screen.getAllByTitle('Remove from queue')[1])
    expect(onCancelQueued).toHaveBeenCalledWith(1)
  })

  it('focuses editing and submits Enter without Shift', () => {
    const onEditQueued = vi.fn()
    render(<ConversationTimeline {...props({ queued: ['original'], onEditQueued })} />)

    fireEvent.click(screen.getByTitle('Edit queued message'))
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(document.activeElement).toBe(textarea)
    fireEvent.input(textarea, { target: { value: 'updated' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })

    expect(onEditQueued).toHaveBeenCalledWith(0, 'updated')
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('keeps Shift+Enter in the editor and Escape restores the original text', () => {
    const onEditQueued = vi.fn()
    render(<ConversationTimeline {...props({ queued: ['original'], onEditQueued })} />)

    fireEvent.click(screen.getByTitle('Edit queued message'))
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.input(textarea, { target: { value: 'draft' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true })
    expect(onEditQueued).not.toHaveBeenCalled()
    expect(screen.getByRole('textbox')).toBe(textarea)

    fireEvent.keyDown(textarea, { key: 'Escape' })
    expect(screen.queryByRole('textbox')).toBeNull()
    fireEvent.click(screen.getByTitle('Edit queued message'))
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('original')
  })

  it('ignores whitespace-only changes and Cancel discards edited drafts', () => {
    const onEditQueued = vi.fn()
    render(<ConversationTimeline {...props({ queued: ['original'], onEditQueued })} />)

    fireEvent.click(screen.getByTitle('Edit queued message'))
    fireEvent.input(screen.getByRole('textbox'), { target: { value: '  original  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onEditQueued).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTitle('Edit queued message'))
    fireEvent.input(screen.getByRole('textbox'), { target: { value: 'discard me' } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onEditQueued).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTitle('Edit queued message'))
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('original')
  })
})

describe('ConversationTimeline delegated code copying', () => {
  const codeMessage = (): Item => ({
    kind: 'assistant',
    id: 'code',
    text: '```ts\nconst value = 1\n```',
    streaming: false,
  })

  it('copies code from nested button content and clears feedback after 1200 ms', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const timeout = vi.spyOn(globalThis, 'setTimeout')
    const view = render(<ConversationTimeline {...props({ items: [codeMessage()] })} />)
    const button = view.container.querySelector('.code-copy') as HTMLButtonElement

    fireEvent.click(button.querySelector('svg')!)
    await waitFor(() => expect(button.classList.contains('copied')).toBe(true))
    expect(writeText).toHaveBeenCalledWith('const value = 1\n')

    const reset = timeout.mock.calls.find(([, delay]) => delay === 1200)?.[0]
    expect(reset).toBeTypeOf('function')
    ;(reset as () => void)()
    expect(button.classList.contains('copied')).toBe(false)
  })

  it('ignores unrelated clicks and code wrappers without text', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const view = render(<ConversationTimeline {...props({ items: [message('ordinary')] })} />)
    const log = view.container.querySelector('.log') as HTMLDivElement

    fireEvent.click(screen.getByText('ordinary'))
    const wrapper = document.createElement('div')
    wrapper.innerHTML = '<button class="code-copy"><svg><path /></svg></button><pre></pre>'
    log.appendChild(wrapper)
    fireEvent.click(wrapper.querySelector('path')!)

    expect(writeText).not.toHaveBeenCalled()
  })

  it('does not show copied feedback when clipboard handling fails', async () => {
    const writeText = vi.fn(async () => {
      throw new Error('denied')
    })
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn(() => false) })
    const view = render(<ConversationTimeline {...props({ items: [codeMessage()] })} />)
    const button = view.container.querySelector('.code-copy') as HTMLButtonElement

    fireEvent.click(button)
    await waitFor(() => expect(writeText).toHaveBeenCalledOnce())
    expect(button.classList.contains('copied')).toBe(false)
  })
})

describe('ConversationTimeline pinned scrolling', () => {
  it('pins on mount and follows item, busy, and queue updates', async () => {
    const view = render(<ConversationTimeline {...props()} />)
    const log = view.container.querySelector('.log') as HTMLDivElement
    await waitFor(() => expect(log.scrollTop).toBe(600))

    scrollHeight = 700
    view.rerender(<ConversationTimeline {...props({ items: [message('one')] })} />)
    await waitFor(() => expect(log.scrollTop).toBe(700))

    scrollHeight = 800
    view.rerender(<ConversationTimeline {...props({ items: [message('one')], busy: true })} />)
    await waitFor(() => expect(log.scrollTop).toBe(800))

    scrollHeight = 900
    view.rerender(<ConversationTimeline {...props({ items: [message('one')], busy: true, queued: ['later'] })} />)
    await waitFor(() => expect(log.scrollTop).toBe(900))
  })

  it('unpins when the user scrolls away and resumes only after jumping to latest', async () => {
    scrollHeight = 1000
    const view = render(<ConversationTimeline {...props({ items: [message('one')] })} />)
    const log = view.container.querySelector('.log') as HTMLDivElement
    await waitFor(() => expect(log.scrollTop).toBe(1000))

    log.scrollTop = 100
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: '↓ jump to latest' })).toBeTruthy()

    scrollHeight = 1200
    view.rerender(<ConversationTimeline {...props({ items: [message('one'), message('two')] })} />)
    expect(log.scrollTop).toBe(100)

    fireEvent.click(screen.getByRole('button', { name: '↓ jump to latest' }))
    expect(log.scrollTop).toBe(1200)
    expect(screen.queryByRole('button', { name: '↓ jump to latest' })).toBeNull()

    scrollHeight = 1400
    view.rerender(<ConversationTimeline {...props({ items: [message('one'), message('two'), message('three')] })} />)
    await waitFor(() => expect(log.scrollTop).toBe(1400))
  })

  it('treats less than 80px from the bottom as pinned and exactly 80px as unpinned', async () => {
    scrollHeight = 1000
    const view = render(<ConversationTimeline {...props({ items: [message('one')] })} />)
    const log = view.container.querySelector('.log') as HTMLDivElement
    await waitFor(() => expect(log.scrollTop).toBe(1000))

    log.scrollTop = 621
    fireEvent.scroll(log)
    expect(screen.queryByRole('button', { name: '↓ jump to latest' })).toBeNull()

    log.scrollTop = 620
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: '↓ jump to latest' })).toBeTruthy()
  })
})
