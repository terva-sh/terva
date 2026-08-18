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

  // The gap this whole feature closes: a dropped .csv used to be filtered out at
  // the drop handler, with no chip, no toast, and nothing sent. It must now be
  // staged and named on the send.
  it('stages a dropped non-image instead of discarding it', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onUpload = vi.fn().mockResolvedValue({
      id: 'att_1', name: 'notes.txt', mime: 'text/plain', kind: 'document', size: 4,
    })
    const { container } = render(<Composer {...props({ onUpload, canAttachFiles: true })} />)
    const footer = container.querySelector('.composer') as HTMLElement
    const textFile = new File(['text'], 'notes.txt', { type: 'text/plain' })

    fireEvent.drop(footer, { dataTransfer: { files: [textFile] } })

    await waitFor(() => expect(onUpload).toHaveBeenCalledWith(textFile))
    expect(await screen.findByText('notes.txt')).toBeTruthy()
  })

  // An image goes inline (vision needs the bytes in the turn); everything beside
  // it is staged. One drop, two destinations.
  it('routes an image inline and a document to the upload route in one drop', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onUpload = vi.fn().mockResolvedValue({
      id: 'att_1', name: 'notes.txt', mime: 'text/plain', kind: 'document', size: 4,
    })
    const { container } = render(<Composer {...props({ onUpload, canAttachFiles: true })} />)
    const footer = container.querySelector('.composer') as HTMLElement
    const textFile = new File(['text'], 'notes.txt', { type: 'text/plain' })

    fireEvent.drop(footer, { dataTransfer: { files: [textFile, imageFile()] } })

    expect(await screen.findByAltText('attached image')).toBeTruthy()
    await waitFor(() => expect(container.querySelectorAll('.composer-chip')).toHaveLength(2))
    expect(onUpload).toHaveBeenCalledTimes(1)
  })

  // An image too big to inline is not an error — the upload route is exactly
  // where it belongs.
  it('stages an image that is too large to ride the frame', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onUpload = vi.fn().mockResolvedValue({
      id: 'att_1', name: 'large.png', mime: 'image/png', kind: 'image', size: 11 * 1024 * 1024,
    })
    const { container } = render(<Composer {...props({ onUpload, canAttachFiles: true })} />)
    const footer = container.querySelector('.composer') as HTMLElement
    const oversized = { type: 'image/png', size: 11 * 1024 * 1024, name: 'large.png' } as File

    fireEvent.drop(footer, { dataTransfer: { files: [oversized] } })

    await waitFor(() => expect(onUpload).toHaveBeenCalledWith(oversized))
  })

  // Refused out loud. Silently dropping the file is the behavior being fixed,
  // so a carrier with no upload route must not reintroduce it.
  it('toasts rather than discards when the daemon cannot take files', async () => {
    const onToast = vi.fn()
    const { container } = render(<Composer {...props({ onToast, canAttachFiles: false })} />)
    const footer = container.querySelector('.composer') as HTMLElement

    fireEvent.drop(footer, { dataTransfer: { files: [new File(['x'], 'notes.txt', { type: 'text/plain' })] } })

    await waitFor(() => expect(onToast).toHaveBeenCalledWith('This daemon cannot take file attachments'))
  })

  // Over the daemon's advertised ceiling, refuse before spending the upload.
  it('refuses a file past the advertised limit without uploading it', async () => {
    const onToast = vi.fn()
    const onUpload = vi.fn()
    const { container } = render(
      <Composer {...props({ onToast, onUpload, canAttachFiles: true, maxAttachmentBytes: 1024 })} />,
    )
    const footer = container.querySelector('.composer') as HTMLElement
    const big = { type: 'text/plain', size: 4096, name: 'huge.log' } as File

    fireEvent.drop(footer, { dataTransfer: { files: [big] } })

    await waitFor(() => expect(onToast).toHaveBeenCalledWith('huge.log is too large (max 1.0 KB)'))
    expect(onUpload).not.toHaveBeenCalled()
  })

  it('surfaces an upload failure and stages no chip for it', async () => {
    const onToast = vi.fn()
    const onUpload = vi.fn().mockResolvedValue({ error: 'notes.txt could not be uploaded' })
    const { container } = render(<Composer {...props({ onToast, onUpload, canAttachFiles: true })} />)
    const footer = container.querySelector('.composer') as HTMLElement

    fireEvent.drop(footer, { dataTransfer: { files: [new File(['x'], 'notes.txt', { type: 'text/plain' })] } })

    await waitFor(() => expect(onToast).toHaveBeenCalledWith('notes.txt could not be uploaded'))
    expect(container.querySelectorAll('.composer-chip')).toHaveLength(0)
  })

  it('sends attachments without text, retaining rejected sends and clearing accepted sends', async () => {
    vi.stubGlobal('FileReader', SuccessfulFileReader)
    const onSend = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true)
    render(<Composer {...props({ onSend })} />)
    pasteImage(screen.getByPlaceholderText('Message terva…'))
    await screen.findByAltText('attached image')

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenLastCalledWith('', [{ mime: 'image/png', data: 'UE5H' }], [])
    expect(screen.getByAltText('attached image')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledTimes(2)
    await waitFor(() => expect(screen.queryByAltText('attached image')).toBeNull())
  })

  // The send names the staged file by id, and a refused send keeps the chip so
  // the upload isn't spent for nothing.
  it('names staged files on the send and keeps them when it is refused', async () => {
    const staged = { id: 'att_1', name: 'notes.txt', mime: 'text/plain', kind: 'document', size: 4 }
    const onSend = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true)
    const { container } = render(
      <Composer {...props({ onSend, onUpload: vi.fn().mockResolvedValue(staged), canAttachFiles: true })} />,
    )
    const footer = container.querySelector('.composer') as HTMLElement
    fireEvent.drop(footer, { dataTransfer: { files: [new File(['x'], 'notes.txt', { type: 'text/plain' })] } })
    await screen.findByText('notes.txt')

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenLastCalledWith('', [], [staged])
    expect(screen.getByText('notes.txt')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.queryByText('notes.txt')).toBeNull())
  })
})

// A staged id is only meaningful against the session directory it was written
// to — the daemon resolves ids against the session it is prompted on. The
// composer keeps its identity across a session switch (no key on the element,
// and the focus view renders it either way), so without this the chips ride
// along and every one of them reports as expired on the far side.
describe('Composer attachments are session-scoped', () => {
  const staged = { id: 'att_1', name: 'notes.txt', mime: 'text/plain', kind: 'document', size: 4 }
  const drop = (container: Element, name = 'notes.txt') =>
    fireEvent.drop(container.querySelector('.composer') as HTMLElement, {
      dataTransfer: { files: [new File(['x'], name, { type: 'text/plain' })] },
    })

  it('drops staged chips when the session changes', async () => {
    const onUpload = vi.fn().mockResolvedValue(staged)
    const { container, rerender } = render(
      <Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_a' })} />,
    )
    drop(container)
    await screen.findByText('notes.txt')

    rerender(<Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_b' })} />)

    await waitFor(() => expect(screen.queryByText('notes.txt')).toBeNull())
  })

  // The text goes with the attachments now, and this test used to assert the
  // opposite: a half-written message rode along into the next session, on the
  // reasoning that throwing it away would be the worse bug.
  //
  // It is not thrown away. It is HANDED to the session it was written for
  // (docs/proposals/session-state-sidecar.md), which is what makes keeping it
  // safe: a draft that travelled would be saved into the slot of a session it
  // was not written for, overwriting that session's own unsent message.
  it('hands the typed draft to the session being left, and clears it', async () => {
    const onUpload = vi.fn().mockResolvedValue(staged)
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    const { container, rerender } = render(
      <Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_a', onSaveDraft })} />,
    )
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement
    fireEvent.input(textarea, { target: { value: 'half a thought' } })
    drop(container)
    await screen.findByText('notes.txt')

    rerender(<Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_b', onSaveDraft })} />)

    await waitFor(() => expect(screen.queryByText('notes.txt')).toBeNull())
    expect((screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement).value).toBe('')
    expect(onSaveDraft).toHaveBeenCalledWith('ses_a', 'half a thought')
  })

  // The case clearing state alone does NOT fix: an upload still in flight when
  // the session changes. Its promise resolves afterwards and appends a chip for
  // a file staged under the session the user left, putting back exactly what
  // the switch cleared.
  it('discards an upload that lands after the session changed', async () => {
    let land: (v: typeof staged) => void = () => {}
    const onUpload = vi.fn().mockReturnValue(new Promise<typeof staged>((res) => { land = res }))
    const { container, rerender } = render(
      <Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_a' })} />,
    )
    drop(container)
    await waitFor(() => expect(onUpload).toHaveBeenCalled())

    rerender(<Composer {...props({ onUpload, canAttachFiles: true, sessionID: 'ses_b' })} />)
    land(staged)

    await waitFor(() => expect(container.querySelectorAll('.composer-chip--file')).toHaveLength(0))
    expect(screen.queryByText('notes.txt')).toBeNull()
  })

  // …and its failure is not announced either: a toast about a session the user
  // has left names a file they can no longer see and cannot re-drop from here.
  it('swallows an upload error that lands after the session changed', async () => {
    const onToast = vi.fn()
    let land: (v: { error: string }) => void = () => {}
    const onUpload = vi.fn().mockReturnValue(new Promise<{ error: string }>((res) => { land = res }))
    const { container, rerender } = render(
      <Composer {...props({ onToast, onUpload, canAttachFiles: true, sessionID: 'ses_a' })} />,
    )
    drop(container)
    await waitFor(() => expect(onUpload).toHaveBeenCalled())

    rerender(<Composer {...props({ onToast, onUpload, canAttachFiles: true, sessionID: 'ses_b' })} />)
    land({ error: 'notes.txt could not be uploaded' })

    await waitFor(() => expect(container.querySelectorAll('.composer-chip')).toHaveLength(0))
    expect(onToast).not.toHaveBeenCalled()
  })

  // A carrier that never says which session it is on (no sessionID at all) must
  // not have its chips cleared on every render by an effect comparing undefined
  // to undefined — that would make the feature unusable rather than wrong.
  it('leaves chips alone when the session never changes', async () => {
    const onUpload = vi.fn().mockResolvedValue(staged)
    const { container, rerender } = render(<Composer {...props({ onUpload, canAttachFiles: true })} />)
    drop(container)
    await screen.findByText('notes.txt')

    rerender(<Composer {...props({ onUpload, canAttachFiles: true })} />)

    expect(screen.getByText('notes.txt')).toBeTruthy()
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
    expect(onSend).toHaveBeenCalledWith('/compact', [], [])
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

describe('Composer @-file stage', () => {
  const files = [
    { path: 'docs', dir: true },
    { path: 'src/main.go' },
    { path: 'src/util/parse.go' },
    { path: 'README.md' },
  ]

  it('requests the listing lazily and completes a file, replacing the @-token', () => {
    const onFilesNeeded = vi.fn()
    render(<Composer {...props({ files, onFilesNeeded })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: 'look at @main' } })
    expect(onFilesNeeded).toHaveBeenCalled()
    const options = screen.getAllByRole('option')
    expect(options[0].textContent).toBe('src/main.go')

    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(textarea.value).toBe('look at src/main.go ')
    expect(document.activeElement).toBe(textarea)
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('ranks substring hits above subsequence hits and matches case-insensitively', () => {
    render(<Composer {...props({ files, onFilesNeeded: () => {} })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    // "READ" substring-matches README.md; "sup" only subsequence-matches
    // src/util/parse.go.
    fireEvent.input(textarea, { target: { value: '@READ' } })
    expect(screen.getAllByRole('option')[0].textContent).toBe('README.md')
    fireEvent.input(textarea, { target: { value: '@sup' } })
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toContain('src/util/parse.go')
  })

  it('keeps the stage live when a directory is completed, narrowing into it', () => {
    render(<Composer {...props({ files, onFilesNeeded: () => {} })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '@docs' } })
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('@docs/')
    // The token is still live: the menu re-filters against the new query.
    expect(screen.queryByRole('listbox')).toBeNull() // nothing under docs/ in the fixture
  })

  it('Tab shell-completes the token — extend, never commit; Enter commits', () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ files, onFilesNeeded: () => {}, onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    // Unique file: Tab completes the text fully but sends nothing.
    fireEvent.input(textarea, { target: { value: 'see @REA' } })
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('see @README.md')
    expect(onSend).not.toHaveBeenCalled()
    // A second Tab is a consumed no-op at the deepest prefix.
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('see @README.md')
    // Enter applies the (single) highlighted row — the commit.
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })
    expect(textarea.value).toBe('see README.md ')

    // Segment-wise: completion happens within the token's parent.
    fireEvent.input(textarea, { target: { value: '@src/m' } })
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('@src/main.go')
  })

  it('Tab still completes after Escape dismissed the menu', () => {
    render(<Composer {...props({ files, onFilesNeeded: () => {} })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: '@REA' } })
    fireEvent.keyDown(textarea, { key: 'Escape' })
    expect(screen.queryByRole('listbox')).toBeNull()
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('@README.md')
  })

  it('only triggers on an @ at start or after whitespace, and never mid-word', () => {
    render(<Composer {...props({ files, onFilesNeeded: () => {} })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: 'user@host' } })
    expect(screen.queryByRole('listbox')).toBeNull()
    fireEvent.input(textarea, { target: { value: 'see @src' } })
    expect(screen.getAllByRole('option').length).toBeGreaterThan(0)
    // A completed token (whitespace after) closes the stage.
    fireEvent.input(textarea, { target: { value: 'see @src/main.go done' } })
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('shows no stage when the daemon serves no listing', () => {
    render(<Composer {...props({ files: null, onFilesNeeded: () => {} })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement
    fireEvent.input(textarea, { target: { value: '@src' } })
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
    expect(onSend).toHaveBeenCalledWith('multiple\nlines', [], [])
    await waitFor(() => expect(textarea.value).toBe(''))
    expect(textarea.style.height).toBe('30px')
  })

  // iOS keeps window.innerHeight at the full screen height while the software
  // keyboard covers the bottom half of it, so a cap measured against innerHeight
  // lets the box grow under the keyboard and swallow the transcript. Only
  // visualViewport shrinks with the keyboard — so that is what the cap reads,
  // and a resize (the keyboard opening under an already-grown box) re-tightens it.
  it('caps growth against the visible viewport, not the keyboard-blind one', async () => {
    const listeners: Record<string, () => void> = {}
    const viewport = {
      height: 400, // a phone with the keyboard up; innerHeight is still 1000
      addEventListener: (event: string, fn: () => void) => {
        listeners[event] = fn
      },
      removeEventListener: (event: string) => {
        delete listeners[event]
      },
    }
    vi.stubGlobal('visualViewport', viewport)

    render(<Composer {...props()} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    textareaScrollHeight = 700
    fireEvent.input(textarea, { target: { value: 'a\nvery\ntall\nmessage' } })
    // 40% of the VISIBLE 400px — not the 400px that 40%-of-innerHeight would give.
    await waitFor(() => expect(textarea.style.height).toBe('160px'))

    // The keyboard dismisses: the cap loosens on the resize, with no keystroke.
    viewport.height = 900
    listeners.resize?.()
    await waitFor(() => expect(textarea.style.height).toBe('360px'))
  })

  it('submits Enter without Shift and clears accepted text', () => {
    const onSend = vi.fn(() => true)
    render(<Composer {...props({ onSend })} />)
    const textarea = screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

    fireEvent.input(textarea, { target: { value: 'hello' } })
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false })

    expect(onSend).toHaveBeenCalledWith('hello', [], [])
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
    expect(onSend).toHaveBeenCalledWith('line', [], [])
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
    expect(onSend).toHaveBeenCalledWith('queue this', [], [])
    expect(textarea.value).toBe('')
  })
})
