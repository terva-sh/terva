// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { Composer } from './Composer'

let textareaScrollHeight = 40
let originalScrollHeight: PropertyDescriptor | undefined
let originalInnerHeight: PropertyDescriptor | undefined

const props = (overrides: Partial<Parameters<typeof Composer>[0]> = {}): Parameters<typeof Composer>[0] => ({
  busy: false,
  onSend: () => true,
  onToast: () => {},
  commands: [],
  skills: [],
  onCancel: () => {},
  ...overrides,
})

beforeEach(() => {
  textareaScrollHeight = 40
  originalScrollHeight = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'scrollHeight')
  originalInnerHeight = Object.getOwnPropertyDescriptor(window, 'innerHeight')
  Object.defineProperty(HTMLTextAreaElement.prototype, 'scrollHeight', {
    configurable: true,
    get() {
      return textareaScrollHeight
    },
  })
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: 1000 })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  if (originalScrollHeight) Object.defineProperty(HTMLTextAreaElement.prototype, 'scrollHeight', originalScrollHeight)
  else delete (HTMLTextAreaElement.prototype as unknown as Record<string, unknown>).scrollHeight
  if (originalInnerHeight) Object.defineProperty(window, 'innerHeight', originalInnerHeight)
  else delete (window as unknown as Record<string, unknown>).innerHeight
})

class SuccessfulFileReader {
  result: string | null = null
  onload: (() => void) | null = null
  onerror: (() => void) | null = null

  readAsDataURL(file: File) {
    this.result = `data:${file.type};base64,UE5H`
    this.onload?.()
  }
}

const imageFile = () => new File(['png'], 'image.png', { type: 'image/png' })

const pasteImage = (textarea: HTMLElement, file = imageFile()) =>
  fireEvent.paste(textarea, {
    clipboardData: {
      items: [{ kind: 'file', type: file.type, getAsFile: () => file }],
    },
  })

describe('Composer attachments', () => {
  it('converts pasted images into removable data-URL chips', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    render(<Composer {...props()} />)
    const textarea = screen.getByPlaceholderText('Message terva…')

    pasteImage(textarea)
    const image = await screen.findByAltText('attached image') as HTMLImageElement
    expect(image.getAttribute('src')).toBe('data:image/png;base64,UE5H')

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(screen.queryByAltText('attached image')).toBeNull()
  })

  it('accepts image drops, ignores non-images, and toasts conversion errors', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onToast = vi.fn()
    const { container } = render(<Composer {...props({ onToast })} />)
    const footer = container.querySelector('.composer') as HTMLElement
    const oversized = { type: 'image/png', size: 11 * 1024 * 1024, name: 'large.png' } as File
    const textFile = new File(['text'], 'notes.txt', { type: 'text/plain' })

    fireEvent.drop(footer, { dataTransfer: { files: [textFile, imageFile(), oversized] } })
    expect(await screen.findByAltText('attached image')).toBeTruthy()
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('large.png is too large (max 10 MB)'))
    expect(container.querySelectorAll('.composer-chip')).toHaveLength(1)
  })

  it('sends attachments without text, retaining rejected sends and clearing accepted sends', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onSend = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true)
    render(<Composer {...props({ onSend })} />)
    pasteImage(screen.getByPlaceholderText('Message terva…'))
    await screen.findByAltText('attached image')

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenLastCalledWith('', [{ mime: 'image/png', data: 'UE5H' }])
    expect(screen.getByAltText('attached image')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledTimes(2)
    await waitFor(() => expect(screen.queryByAltText('attached image')).toBeNull())
  })
})

describe('Composer autocomplete', () => {
  const command = (name: string, arg?: string) => ({ name, arg, desc: `${name} description`, run: vi.fn() })

  it('filters commands, navigates selected rows, dismisses with Escape, and reopens on input', () => {
    render(<Composer {...props({ commands: [command('compact'), command('continue'), command('skill', '<name>')] })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '/c' } })
    const options = screen.getAllByRole('option')
    expect(options.map((option) => option.textContent)).toEqual([
      '/compactcompact description',
      '/continuecontinue description',
    ])
    expect(options[0].getAttribute('aria-selected')).toBe('true')

    fireEvent.keyDown(textarea, { key: 'ArrowDown' })
    expect(screen.getAllByRole('option')[1].getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(textarea, { key: 'ArrowUp' })
    expect(screen.getAllByRole('option')[0].getAttribute('aria-selected')).toBe('true')

    fireEvent.keyDown(textarea, { key: 'Escape' })
    expect(screen.queryByRole('listbox')).toBeNull()
    fireEvent.input(textarea, { target: { value: '/comp' } })
    expect(screen.getByRole('option').textContent).toContain('/compact')
  })

  it('runs argumentless commands immediately with Enter', () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ commands: [command('compact')], onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '/comp' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(onSend).toHaveBeenCalledWith('/compact', [])
    expect(textarea.value).toBe('')
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('primes argument-taking commands with Tab and retains textarea focus', () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ commands: [command('skill', '<name>')], onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '/sk' } })
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('/skill ')
    expect(document.activeElement).toBe(textarea)
    expect(onSend).not.toHaveBeenCalled()
  })

  it('filters skill names case-insensitively and completes the selected skill', () => {
    render(
      <Composer
        {...props({
          commands: [command('skill', '<name>')],
          skills: [
            { name: 'Party-Planner', description: 'Plan a party' },
            { name: 'pair-review', description: 'Review together' },
          ],
        })}
      />,
    )
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '/skill PA' } })
    expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Party-PlannerPlan a party',
      'pair-reviewReview together',
    ])
    expect(screen.getAllByRole('option')[0].getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(textarea.value).toBe('/skill Party-Planner ')
    expect(document.activeElement).toBe(textarea)
    expect(screen.queryByRole('listbox')).toBeNull()
  })
})

describe('Composer core interaction', () => {
  it('grows with content, caps at 40vh, and shrinks after an accepted send', async () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement
    await waitFor(() => expect(textarea.style.height).toBe('40px'))

    textareaScrollHeight = 700
    fireEvent.input(textarea, { target: { value: 'multiple\nlines' } })
    await waitFor(() => expect(textarea.style.height).toBe('400px'))

    textareaScrollHeight = 30
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledWith('multiple\nlines', [])
    await waitFor(() => expect(textarea.value).toBe(''))
    expect(textarea.style.height).toBe('30px')
  })

  it('submits Enter without Shift and clears accepted text', () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: 'hello' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })

    expect(onSend).toHaveBeenCalledWith('hello', [])
    expect(textarea.value).toBe('')
  })

  it('keeps text for Shift+Enter, empty submissions, and rejected sends', () => {
    const onSend = vi.fn(() => false)
    render(<Composer {...props({ onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: 'line' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true })
    expect(onSend).not.toHaveBeenCalled()
    expect(textarea.value).toBe('line')

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledWith('line', [])
    expect(textarea.value).toBe('line')

    onSend.mockClear()
    fireEvent.input(textarea, { target: { value: '   ' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(onSend).not.toHaveBeenCalled()
    expect(textarea.value).toBe('   ')
  })

  it('shows Stop while busy and keeps keyboard submission delegated to the parent', () => {
    const onSend = vi.fn(() => true)
    const onCancel = vi.fn()
    render(<Composer {...props({ busy: true, onSend, onCancel })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(onCancel).toHaveBeenCalledOnce()

    fireEvent.input(textarea, { target: { value: 'queue this' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(onSend).toHaveBeenCalledWith('queue this', [])
    expect(textarea.value).toBe('')
  })
})
