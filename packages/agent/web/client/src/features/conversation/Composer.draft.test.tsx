// @vitest-environment happy-dom
//
// The persisted composer draft — stage 6 of
// docs/proposals/session-state-sidecar.md.
//
// The draft belongs to the SESSION, not to this tab, so most of what is worth
// guarding here is about not destroying something: not planting the TUI's
// unaccepted suggestion as the user's prose, not clearing a suggestion this
// panel cannot even display, not saving an empty composer over a draft whose
// read has not landed yet, and not overwriting keystrokes typed while it was in
// flight. The happy path is one test; the rest are the ways it could cost
// writing rather than save it.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { Composer } from './Composer'

const draftProps = (
  overrides: Partial<Parameters<typeof Composer>[0]> = {},
): Parameters<typeof Composer>[0] => ({
  busy: false,
  onSend: () => true,
  onToast: () => {},
  commands: [],
  skills: [],
  onCancel: () => {},
  sessionID: 's1',
  ...overrides,
})

// settle flushes the effects and promise callbacks a render queued, without
// advancing far enough to fire the debounce.
const settle = async () => {
  await vi.advanceTimersByTimeAsync(0)
}

// debounce advances past the composer's save delay.
const debounce = async () => {
  await vi.advanceTimersByTimeAsync(900)
}

const composer = () => screen.getByPlaceholderText('Message terva…') as HTMLTextAreaElement

const type = (value: string) => fireEvent.input(composer(), { target: { value } })

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('the persisted composer draft', () => {
  it('restores the session draft into an empty composer', async () => {
    const onLoadDraft = vi.fn().mockResolvedValue({ text: 'half a question about', source: 'user' })
    render(<Composer {...draftProps({ onLoadDraft, onSaveDraft: vi.fn().mockResolvedValue(undefined) })} />)

    await settle()
    expect(composer().value).toBe('half a question about')
    expect(onLoadDraft).toHaveBeenCalledWith('s1')
  })

  // The panel has no ghost affordance and never makes a suggestion, so one in
  // the slot came from the TUI. Restoring it as text would hand the machine's
  // line back as the user's own writing.
  it('never plants a stored suggestion as typed text', async () => {
    const onLoadDraft = vi.fn().mockResolvedValue({ text: 'shall I run the tests?', source: 'suggestion' })
    render(<Composer {...draftProps({ onLoadDraft, onSaveDraft: vi.fn().mockResolvedValue(undefined) })} />)

    await settle()
    expect(composer().value).toBe('')
  })

  // ...and it must not be quietly deleted either. Clearing an offer this front
  // end cannot display would destroy it on the user's behalf without ever
  // telling them it existed.
  //
  // Typing WHITESPACE is what makes this bite, and it took an ablation to find
  // out. An untouched composer is protected by something else entirely — its
  // text already equals the saved value, so no write is due — which made the
  // first version of this test pass with the suggestion guard deleted. A space
  // differs from the empty saved value, so a write IS due, and the daemon reads
  // blank text as "clear it".
  it('leaves a stored suggestion alone when the composer holds only whitespace', async () => {
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    const onLoadDraft = vi.fn().mockResolvedValue({ text: 'shall I run the tests?', source: 'suggestion' })
    render(<Composer {...draftProps({ onLoadDraft, onSaveDraft })} />)
    await settle()

    type('   ')
    await debounce()
    expect(onSaveDraft).not.toHaveBeenCalled()
  })

  // The composer is empty on arrival because the read has not come back. Saving
  // that empty string would delete the draft being fetched.
  //
  // Whitespace again, and for the same reason: an empty composer is already
  // covered by the "nothing has changed" check, so it cannot demonstrate this
  // guard. A space is a real change that trims to nothing — so without the
  // guard it would go out as a blank draft and DELETE the stored one before the
  // read that was fetching it had even landed.
  it('saves nothing while the read is still in flight', async () => {
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    // A load that never resolves: the restore is permanently in flight.
    const onLoadDraft = vi.fn().mockReturnValue(new Promise<never>(() => {}))
    render(<Composer {...draftProps({ onLoadDraft, onSaveDraft })} />)
    await settle()

    type('   ')
    await debounce()
    expect(onSaveDraft).not.toHaveBeenCalled()
  })

  it('saves what was typed, once the composer settles', async () => {
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    render(<Composer {...draftProps({ onLoadDraft: vi.fn().mockResolvedValue(null), onSaveDraft })} />)
    await settle()

    type('a message I have not sent')

    // Well inside the debounce: nothing may have gone out yet.
    await vi.advanceTimersByTimeAsync(400)
    expect(onSaveDraft).not.toHaveBeenCalled()

    await debounce()
    expect(onSaveDraft).toHaveBeenCalledWith('s1', 'a message I have not sent')
  })

  // The read took a round trip. Anything typed since outranks what was typed
  // before — overwriting live keystrokes is the one way this could cost writing.
  it('does not overwrite text typed while the read was in flight', async () => {
    let land!: (d: { text: string; source: string }) => void
    const onLoadDraft = vi.fn().mockReturnValue(
      new Promise<{ text: string; source: string }>((resolve) => {
        land = resolve
      }),
    )
    render(<Composer {...draftProps({ onLoadDraft, onSaveDraft: vi.fn().mockResolvedValue(undefined) })} />)
    await settle()

    type('what I am typing now')
    land({ text: 'the older draft', source: 'user' })
    await settle()

    expect(composer().value).toBe('what I am typing now')
  })

  // The sidecar carries plain text only, so a draft saved while files are
  // staged silently leaves them behind. The user is told once, because a
  // debounced saver that toasts on every write is a stutter rather than a
  // warning.
  it('says so when a saved draft leaves attachments behind', async () => {
    class Reader {
      result: string | null = null
      onload: (() => void) | null = null
      onerror: (() => void) | null = null
      readAsDataURL(file: File) {
        this.result = `data:${file.type};base64,UE5H`
        this.onload?.()
      }
    }
    vi.stubGlobal('FileReader', Reader)
    const onToast = vi.fn()
    render(
      <Composer
        {...draftProps({
          onToast,
          onLoadDraft: vi.fn().mockResolvedValue(null),
          onSaveDraft: vi.fn().mockResolvedValue(undefined),
        })}
      />,
    )
    await settle()

    const png = new File(['png'], 'image.png', { type: 'image/png' })
    fireEvent.paste(composer(), {
      clipboardData: { items: [{ kind: 'file', type: png.type, getAsFile: () => png }] },
    })
    await settle()
    type('a draft with a picture')
    await debounce()

    const said = onToast.mock.calls.map((c) => String(c[0])).join(' | ')
    expect(said).toContain('attachments are not')
    // Once, not once per write.
    type('a draft with a picture, still')
    await debounce()
    expect(onToast.mock.calls.filter((c) => String(c[0]).includes('attachments are not'))).toHaveLength(1)
  })

  // The draft belongs to the conversation being left. Saving it under the new
  // binding would file one conversation's unsent words in another.
  it('saves under the session being left, and does not carry the text over', async () => {
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    const onLoadDraft = vi.fn().mockResolvedValue(null)
    const { rerender } = render(<Composer {...draftProps({ onLoadDraft, onSaveDraft })} />)
    await settle()

    type('unsent, and about s1')
    await settle()

    rerender(<Composer {...draftProps({ sessionID: 's2', onLoadDraft, onSaveDraft })} />)
    await settle()

    expect(onSaveDraft).toHaveBeenCalledWith('s1', 'unsent, and about s1')
    expect(composer().value).toBe('')
  })
})
